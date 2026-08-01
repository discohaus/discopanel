package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
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

// Lowercases hostname, strips port, FML markers, and trailing dot
func normalizeHostname(hostname string) string {
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
	route.Hostname = normalizeHostname(route.Hostname)

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
	hostname = normalizeHostname(hostname)

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
	// Raw ip and typo joins land on an only route
	if len(s.mcRoutes) == 1 {
		for _, route := range s.mcRoutes {
			return *route, true
		}
	}
	return Route{}, false
}

// Finds backend for a parsed handshake, wakes sleepers, relays
func (s *ListenerSocket) serveMinecraft(clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket) {
	hostname := normalizeHostname(handshake.ServerAddress)
	route, ok := s.lookupMCRoute(hostname)
	if !ok {
		s.logger.Debug("No active route for hostname %q from %s", hostname, clientConn.RemoteAddr())
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake,
				fmt.Sprintf("Powered by DiscoPanel - nothing is running at %s", hostname), 0, "DiscoPanel")
			return
		}
		clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		mcproto.WriteLoginDisconnect(clientConn,
			fmt.Sprintf("No server answers at %s. Join with the address shown in the panel.", hostname))
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
				s.serveSyntheticStatus(clientConn, br, handshake, info.Motd, info.MaxPlayers, "Sleeping")
				return
			}
			s.logger.Info("Waking sleeping server %s for incoming login", route.ServerID)
			stats.Wakes.Add(1)
			wakeCtx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			err := gate.WakeServer(wakeCtx, route.ServerID)
			cancel()
			if err != nil {
				s.logger.Error("Failed to wake server %s: %v", route.ServerID, err)
				clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				mcproto.WriteLoginDisconnect(clientConn, "The server is waking up, try again in a moment")
				return
			}
		}
	}

	// Stopped and booting servers answer synthetically instead of dialing
	switch route.State {
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE:
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake, route.Motd, route.MaxPlayers, "Offline")
			return
		}
		clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if !route.Wakeable {
			mcproto.WriteLoginDisconnect(clientConn, "The server is offline")
			return
		}
		gate := s.getGate()
		if gate == nil {
			mcproto.WriteLoginDisconnect(clientConn, "The server is offline")
			return
		}
		s.logger.Info("Starting stopped server %s for incoming login", route.ServerID)
		stats.Wakes.Add(1)
		if err := gate.StartServer(route.ServerID); err != nil {
			s.logger.Error("Failed to start server %s for login: %v", route.ServerID, err)
			mcproto.WriteLoginDisconnect(clientConn, "The server could not be started, check the panel")
			return
		}
		mcproto.WriteLoginDisconnect(clientConn, "The server is starting up, join again in a minute")
		return

	case v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING:
		if handshake.NextState == mcproto.NextStateStatus {
			s.serveSyntheticStatus(clientConn, br, handshake, route.Motd, route.MaxPlayers, "Starting")
			return
		}
		// No backend yet, container isn't up, tell client
		if route.BackendHost == "" {
			clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			mcproto.WriteLoginDisconnect(clientConn, "The server is still starting, join again in a moment")
			return
		}
		// Backend exists, let dial retry ride out the boot
	}

	if route.BackendHost == "" {
		s.logger.Error("Route %s has no backend address", hostname)
		if handshake.NextState == mcproto.NextStateLogin {
			clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			mcproto.WriteLoginDisconnect(clientConn, "The server is not reachable right now")
		}
		return
	}

	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	backendConn, err := dialBackendWithRetry(s.ctx, backendAddr, 10*time.Second)
	if err != nil {
		s.logger.Error("Failed to connect to backend %s for %s: %v", backendAddr, hostname, err)
		if handshake.NextState == mcproto.NextStateLogin {
			clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			mcproto.WriteLoginDisconnect(clientConn, "The server is not accepting connections yet, try again in a moment")
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
	toBackend, toClient := relay(clientConn, backendConn)
	stats.ActiveConns.Add(-1)
	stats.BytesToBackend.Add(toBackend)
	stats.BytesToClient.Add(toClient)
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

// Synthesizes a status reply so server lists never wake backends
func (s *ListenerSocket) serveSyntheticStatus(conn net.Conn, r io.Reader, handshake *mcproto.HandshakePacket, motd string, maxPlayers int, versionName string) {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))

	statusJSON, err := json.Marshal(map[string]any{
		"version": map[string]any{
			// Echo the client protocol so the entry renders as compatible
			"name":     versionName,
			"protocol": int(handshake.ProtocolVersion),
		},
		"players": map[string]any{
			"max":    maxPlayers,
			"online": 0,
		},
		"description": map[string]any{
			"text": motd,
		},
	})
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
		return "Powered by DiscoPanel", "DiscoPanel", 0
	}

	stats := s.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.StatusPings.Add(1)

	if gate := s.getGate(); gate != nil {
		if info, sleeping := gate.SleepingInfo(route.ServerID); sleeping {
			return info.Motd, "Sleeping", info.MaxPlayers
		}
	}

	motd := route.Motd
	if motd == "" {
		motd = route.Hostname
	}
	version := "Online"
	switch route.State {
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING:
		version = "Starting"
	case v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE:
		version = "Offline"
	}
	return motd, version, route.MaxPlayers
}

// Resolves the route a legacy ping is asking about
func (s *ListenerSocket) legacyPingRoute(raw []byte) (Route, bool) {
	if hostname, ok := mcproto.LegacyPingHostname(raw); ok {
		return s.lookupMCRoute(normalizeHostname(hostname))
	}

	s.routesMu.RLock()
	defer s.routesMu.RUnlock()
	if len(s.mcRoutes) == 1 {
		for _, route := range s.mcRoutes {
			return *route, true
		}
	}
	return Route{}, false
}
