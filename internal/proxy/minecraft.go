package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Counts per-route proxy activity, keyed by server ID
type RouteStats struct {
	ActiveConns    atomic.Int64
	TotalConns     atomic.Int64
	StatusPings    atomic.Int64
	Logins         atomic.Int64
	Wakes          atomic.Int64
	BytesToBackend atomic.Int64
	BytesToClient  atomic.Int64
	LastProtocol   atomic.Int32
}

// Relays and folds moved bytes into the counters
func (st *RouteStats) countRelay(client, backend net.Conn) {
	toBackend, toClient := relay(client, backend)
	st.BytesToBackend.Add(toBackend)
	st.BytesToClient.Add(toClient)
}

// Copies the live counters onto a fresh route message
func (st *RouteStats) Snapshot() *v1.ProxyRoute {
	return &v1.ProxyRoute{
		ActiveConnections:   st.ActiveConns.Load(),
		TotalConnections:    st.TotalConns.Load(),
		StatusPings:         st.StatusPings.Load(),
		Logins:              st.Logins.Load(),
		Wakes:               st.Wakes.Load(),
		BytesToBackend:      st.BytesToBackend.Load(),
		BytesToClient:       st.BytesToClient.Load(),
		LastProtocolVersion: st.LastProtocol.Load(),
	}
}

// Returns a route's counters, creating them on first use
func (s *ListenerSocket) statsFor(serverID string) *RouteStats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	st, ok := s.stats[serverID]
	if !ok {
		st = &RouteStats{}
		s.stats[serverID] = st
	}
	return st
}

// Forgets counters matching the predicate
func (s *ListenerSocket) DropStats(match func(string) bool) {
	s.statsMu.Lock()
	for id := range s.stats {
		if match(id) {
			delete(s.stats, id)
		}
	}
	s.statsMu.Unlock()
}

// Copies every route's counters for the API
func (s *ListenerSocket) StatsSnapshots() map[string]*v1.ProxyRoute {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	out := make(map[string]*v1.ProxyRoute, len(s.stats))
	for id, st := range s.stats {
		out[id] = st.Snapshot()
	}
	return out
}

// Wire side normalizer, config names use NormalizeHostname
func normalizeWireHostname(hostname string) string {
	if idx := strings.IndexByte(hostname, 0); idx != -1 {
		hostname = hostname[:idx]
	}
	hostname, _, _ = strings.Cut(hostname, ":")
	return strings.ToLower(strings.TrimSuffix(hostname, "."))
}

// Installs or replaces an mc route, silent when unchanged
func (s *ListenerSocket) UpsertServerRoute(route Route) {
	route.Protocol = v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT
	if route.State == v1.ProxyRouteState_PROXY_ROUTE_STATE_UNSPECIFIED {
		route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE
	}
	route.Hostname = normalizeWireHostname(route.Hostname)

	s.routesMu.Lock()
	old, exists := s.mcRoutes[route.Hostname]
	changed := !exists || *old != route
	if changed {
		s.mcRoutes[route.Hostname] = &route
	}
	s.routesMu.Unlock()

	if changed {
		s.logger.Info("Route %s is %s (backend=%s:%d wakeable=%v)",
			route.Hostname, protometa.Name(route.State), route.BackendHost, route.BackendPort, route.Wakeable)
	}
}

// Removes an mc route and its counters
func (s *ListenerSocket) removeMCRoute(hostname string) {
	hostname = normalizeWireHostname(hostname)

	s.routesMu.Lock()
	route, exists := s.mcRoutes[hostname]
	delete(s.mcRoutes, hostname)
	s.routesMu.Unlock()

	if !exists {
		return
	}

	s.statsMu.Lock()
	delete(s.stats, route.ServerID)
	s.statsMu.Unlock()

	s.logger.Info("Removed route: hostname=%s", hostname)
}

// Returns a snapshot of the mc route for hostname
func (s *ListenerSocket) lookupMCRoute(hostname string) (Route, bool) {
	s.routesMu.RLock()
	defer s.routesMu.RUnlock()
	if route, exists := s.mcRoutes[hostname]; exists {
		return *route, true
	}
	return s.soleMCRouteLocked()
}

// Raw ip and typo joins land on the sole service
func (s *ListenerSocket) soleMCRouteLocked() (Route, bool) {
	var sole *Route
	for _, route := range s.mcRoutes {
		if sole != nil && route.ServerID != sole.ServerID {
			return Route{}, false
		}
		sole = route
	}
	if sole == nil {
		return Route{}, false
	}
	return *sole, true
}

// Finds backend for a parsed handshake, wakes sleepers, relays
func (s *ListenerSocket) serveMinecraft(clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket) {
	hostname := normalizeWireHostname(handshake.ServerAddress)
	route, ok := s.lookupMCRoute(hostname)
	if !ok {
		s.logger.Debug("No active route for hostname %q from %s", hostname, clientConn.RemoteAddr())
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake, statusUnknownHost(hostname, int(handshake.ProtocolVersion)))
			return
		}
		s.kick(clientConn, handshake, kickUnknownHost(hostname))
		return
	}

	stats := s.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.LastProtocol.Store(int32(handshake.ProtocolVersion))
	if handshake.NextState == mcproto.NextStateStatus {
		stats.StatusPings.Add(1)
	} else {
		stats.Logins.Add(1)
	}

	// Paused servers answer status pings without waking, wake on login
	if gate := s.getGate(); gate != nil {
		if info, sleeping := gate.SleepingInfo(route.ServerID); sleeping {
			if handshake.NextState == mcproto.NextStateStatus {
				s.serveSyntheticStatus(clientConn, br, handshake, statusSleeping(info.Motd, info.MaxPlayers, route.Favicon))
				return
			}
			s.logger.Info("Waking sleeping server %s for incoming login", route.ServerID)
			stats.Wakes.Add(1)
			wakeCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			err := gate.WakeServer(wakeCtx, route.ServerID)
			cancel()
			if err != nil {
				s.logger.Error("Failed to wake server %s: %v", route.ServerID, err)
				s.kick(clientConn, handshake, kickWakeFailed())
				return
			}
		}
	}

	// Stopped and booting servers answer synthetically instead of dialing
	switch route.State {
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE:
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake, statusOffline(route))
			return
		}
		if !route.Wakeable {
			s.kick(clientConn, handshake, kickOffline())
			return
		}
		gate := s.getGate()
		if gate == nil {
			s.kick(clientConn, handshake, kickOffline())
			return
		}
		s.logger.Info("Starting stopped server %s for incoming login", route.ServerID)
		stats.Wakes.Add(1)
		if err := gate.StartServer(route.ServerID); err != nil {
			s.logger.Error("Failed to start server %s for login: %v", route.ServerID, err)
			s.kick(clientConn, handshake, kickStartFailed())
			return
		}
		s.kick(clientConn, handshake, kickStarted())
		return

	case v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING:
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake, statusStarting(route))
			return
		}
		// No backend yet, container isn't up, tell client
		if route.BackendHost == "" {
			s.kick(clientConn, handshake, kickStillStarting())
			return
		}
		// Backend exists, let dial retry ride out the boot
	}

	if route.BackendHost == "" {
		s.logger.Error("Route %s has no backend address", hostname)
		if handshake.NextState == mcproto.NextStateLogin {
			s.kick(clientConn, handshake, kickUnreachable())
		}
		return
	}

	backendAddr := route.BackendAddr()
	backendConn, err := dialBackendWithRetry(s.ctx, backendAddr, 10*time.Second)
	if err != nil {
		s.logger.Error("Failed to connect to backend %s for %s: %v", backendAddr, hostname, err)
		if handshake.NextState == mcproto.NextStateLogin {
			s.kick(clientConn, handshake, kickNotAccepting())
		}
		return
	}
	defer backendConn.Close()

	backendConn.SetWriteDeadline(time.Now().Add(handshakeTimeout))

	// Real client address rides ahead of the handshake when enabled
	if route.ProxyProtocol {
		if err := WriteProxyV2Header(backendConn, clientConn.RemoteAddr(), clientConn.LocalAddr()); err != nil {
			s.logger.Error("Failed to write PROXY header to backend %s: %v", backendAddr, err)
			return
		}
	}

	rewriteHandshakeAddress(handshake, route.BackendPort, route.PreserveHost)

	if err := mcproto.WriteHandshakePacket(backendConn, handshake); err != nil {
		s.logger.Error("Failed to write handshake to backend %s: %v", backendAddr, err)
		return
	}

	// Flushes client bytes already buffered before relay handoff
	if buffered := br.Buffered(); buffered > 0 {
		pending, _ := br.Peek(buffered)
		if _, err := backendConn.Write(pending); err != nil {
			s.logger.Error("Failed to flush buffered client data to backend %s: %v", backendAddr, err)
			return
		}
		br.Discard(buffered)
	}

	// Clears deadlines, relays raw sockets via splice fast path
	clientConn.SetDeadline(time.Time{})
	backendConn.SetDeadline(time.Time{})
	stats.ActiveConns.Add(1)
	stats.countRelay(clientConn, backendConn)
	stats.ActiveConns.Add(-1)
}

// Points handshake at backend, optionally preserving client hostname
func rewriteHandshakeAddress(handshake *mcproto.HandshakePacket, backendPort int, preserveHost bool) {
	if !preserveHost {
		addressParts := strings.Split(handshake.ServerAddress, "\x00")
		addressParts[0] = "localhost"
		handshake.ServerAddress = strings.Join(addressParts, "\x00")
	}
	handshake.ServerPort = uint16(backendPort)
}

// Sends a styled kick screen to the client
func (s *ListenerSocket) kick(conn net.Conn, handshake *mcproto.HandshakePacket, screen minecraft.Text) {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	reason, err := json.Marshal(screen.Render(int(handshake.ProtocolVersion)))
	if err != nil {
		return
	}
	mcproto.WriteLoginDisconnectJSON(conn, reason)
}

// Synthesizes a status reply so server lists never wake backends
func (s *ListenerSocket) serveSyntheticStatus(conn net.Conn, r io.Reader, handshake *mcproto.HandshakePacket, card synthStatus) {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))

	desc := card.desc
	if motd, ok := desc.(string); ok {
		desc = map[string]any{"text": motd}
	}
	sample := make([]map[string]any, 0, len(card.sample))
	for _, line := range card.sample {
		sample = append(sample, map[string]any{"name": line, "id": sampleUUID})
	}
	status := map[string]any{
		"version": map[string]any{
			// Echo the client protocol so the entry renders as compatible
			"name":     card.version,
			"protocol": int(handshake.ProtocolVersion),
		},
		"players": map[string]any{
			"max":    card.maxPlayers,
			"online": 0,
			"sample": sample,
		},
		"description": desc,
	}
	if card.favicon != "" {
		status["favicon"] = card.favicon
	}
	statusJSON, err := json.Marshal(status)
	if err != nil {
		return
	}

	for {
		// Reads next packet, status request or ping
		length, err := mcproto.ReadVarInt(r)
		if err != nil || length < 1 || length > 1024 {
			return
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			return
		}
		reader := bytes.NewReader(data)
		packetID, err := mcproto.ReadVarInt(reader)
		if err != nil {
			return
		}

		switch packetID {
		case 0x00: // Status request -> status response
			var payload bytes.Buffer
			mcproto.WriteVarInt(&payload, 0x00)
			mcproto.WriteVarInt(&payload, mcproto.VarInt(len(statusJSON)))
			payload.Write(statusJSON)
			if err := mcproto.WriteFramed(conn, payload.Bytes()); err != nil {
				return
			}
		case 0x01: // Ping -> pong, echoes the 8-byte payload
			var payload bytes.Buffer
			mcproto.WriteVarInt(&payload, 0x01)
			pingData := make([]byte, 8)
			if _, err := io.ReadFull(reader, pingData); err != nil {
				return
			}
			payload.Write(pingData)
			mcproto.WriteFramed(conn, payload.Bytes())
			return
		default:
			return
		}
	}
}

// Answers a pre-1.7 ping with a kick style status
func (s *ListenerSocket) serveLegacyPing(conn net.Conn, raw []byte) {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	motd, version, maxPlayers := s.legacyStatus(raw)
	mcproto.WriteLegacyKick(conn, len(raw) > 1, motd, version, maxPlayers)
}

// Derives legacy status fields from the routed server
func (s *ListenerSocket) legacyStatus(raw []byte) (string, string, int) {
	route, ok := s.legacyPingRoute(raw)
	if !ok {
		return "powered by §fdisco§apanel", "discopanel", 0
	}

	stats := s.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.StatusPings.Add(1)

	if gate := s.getGate(); gate != nil {
		if info, sleeping := gate.SleepingInfo(route.ServerID); sleeping {
			return info.Motd, "sleeping", info.MaxPlayers
		}
	}

	motd := route.Motd
	if motd == "" {
		motd = route.Hostname
	}
	version := "online"
	switch route.State {
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING:
		version = "starting"
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE:
		version = "offline"
	}
	return motd, version, route.MaxPlayers
}

// Resolves the route a legacy ping is asking about
func (s *ListenerSocket) legacyPingRoute(raw []byte) (Route, bool) {
	if hostname, ok := mcproto.LegacyPingHostname(raw); ok {
		return s.lookupMCRoute(normalizeWireHostname(hostname))
	}

	s.routesMu.RLock()
	defer s.routesMu.RUnlock()
	return s.soleMCRouteLocked()
}
