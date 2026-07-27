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
	"sync"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Routes Minecraft connections to backends by hostname
type MinecraftProxy struct {
	listenAddr string
	logger     *logger.Logger

	routes   map[string]*Route
	routesMu sync.RWMutex

	stats   map[string]*RouteStats
	statsMu sync.Mutex

	gate   ServerGate
	gateMu sync.RWMutex

	listener net.Listener
	running  bool
	stateMu  sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

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

// Creates a new Minecraft proxy instance
func NewMinecraftProxy(cfg *Config) *MinecraftProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &MinecraftProxy{
		routes:     make(map[string]*Route),
		stats:      make(map[string]*RouteStats),
		logger:     cfg.Logger,
		listenAddr: cfg.ListenAddr,
		ctx:        ctx,
		cancel:     cancel,
		gate:       cfg.Gate,
	}
}

// Returns a server's counters, creating them on first use
func (p *MinecraftProxy) statsFor(serverID string) *RouteStats {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	st, ok := p.stats[serverID]
	if !ok {
		st = &RouteStats{}
		p.stats[serverID] = st
	}
	return st
}

// Copies every route's counters for the API
func (p *MinecraftProxy) StatsSnapshots() map[string]*v1.ProxyRoute {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	out := make(map[string]*v1.ProxyRoute, len(p.stats))
	for id, st := range p.stats {
		out[id] = st.Snapshot()
	}
	return out
}

// Registers the wake gate for paused servers
func (p *MinecraftProxy) SetGate(gate ServerGate) {
	p.gateMu.Lock()
	p.gate = gate
	p.gateMu.Unlock()
}

func (p *MinecraftProxy) getGate() ServerGate {
	p.gateMu.RLock()
	defer p.gateMu.RUnlock()
	return p.gate
}

// Lowercases hostname, strips port, FML markers, and trailing dot
func normalizeHostname(hostname string) string {
	if idx := strings.IndexByte(hostname, 0); idx != -1 {
		hostname = hostname[:idx]
	}
	hostname, _, _ = strings.Cut(hostname, ":")
	return strings.ToLower(strings.TrimSuffix(hostname, "."))
}

// Adds a new online routing rule
func (p *MinecraftProxy) AddRoute(serverID, hostname, backendHost string, backendPort int) {
	p.UpsertServerRoute(Route{
		ServerID:    serverID,
		Hostname:    hostname,
		BackendHost: backendHost,
		BackendPort: backendPort,
		State:       v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
	})
}

// Installs or replaces a route, silent when unchanged
func (p *MinecraftProxy) UpsertServerRoute(route Route) {
	route.Hostname = normalizeHostname(route.Hostname)

	p.routesMu.Lock()
	old, exists := p.routes[route.Hostname]
	changed := !exists || *old != route
	if changed {
		p.routes[route.Hostname] = &route
	}
	p.routesMu.Unlock()

	if changed {
		p.logger.Info("Route %s is %s (backend=%s:%d wakeable=%v)",
			route.Hostname, protometa.Name(route.State), route.BackendHost, route.BackendPort, route.Wakeable)
	}
}

// Removes a routing rule and its counters
func (p *MinecraftProxy) RemoveRoute(hostname string) {
	hostname = normalizeHostname(hostname)

	p.routesMu.Lock()
	route, exists := p.routes[hostname]
	delete(p.routes, hostname)
	p.routesMu.Unlock()

	if !exists {
		return
	}

	p.statsMu.Lock()
	delete(p.stats, route.ServerID)
	p.statsMu.Unlock()

	p.logger.Info("Removed route: hostname=%s", hostname)
}

// Updates the backend for a route
func (p *MinecraftProxy) UpdateRoute(hostname, backendHost string, backendPort int) {
	hostname = normalizeHostname(hostname)

	p.routesMu.Lock()
	if route, exists := p.routes[hostname]; exists {
		route.BackendHost = backendHost
		route.BackendPort = backendPort
		p.logger.Info("Updated route: hostname=%s backend=%s:%d", hostname, backendHost, backendPort)
	}
	p.routesMu.Unlock()
}

// Returns a snapshot of the route for hostname
func (p *MinecraftProxy) lookupRoute(hostname string) (Route, bool) {
	p.routesMu.RLock()
	defer p.routesMu.RUnlock()
	route, exists := p.routes[hostname]
	if !exists {
		return Route{}, false
	}
	return *route, true
}

// Returns a copy of all current routes
func (p *MinecraftProxy) GetRoutes() map[string]*Route {
	p.routesMu.RLock()
	defer p.routesMu.RUnlock()

	routes := make(map[string]*Route, len(p.routes))
	for k, v := range p.routes {
		routeCopy := *v
		routes[k] = &routeCopy
	}
	return routes
}

// Starts the proxy server
func (p *MinecraftProxy) Start() error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	if p.running {
		return fmt.Errorf("proxy already running")
	}

	listener, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
	}

	p.listener = listener
	p.running = true

	go acceptLoop(p.ctx, listener, p.logger, p.handleConnection)

	p.logger.Info("Minecraft proxy started on %s", p.listenAddr)
	return nil
}

// Stops the proxy, established connections drain on their own
func (p *MinecraftProxy) Stop() error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	if !p.running {
		return nil
	}

	p.cancel()
	p.running = false

	if p.listener != nil {
		if err := p.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	p.logger.Info("Minecraft proxy stopped")
	return nil
}

// Returns whether the proxy is running
func (p *MinecraftProxy) IsRunning() bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.running
}

// Parses handshake, finds backend, wakes sleepers, then relays
func (p *MinecraftProxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	clientConn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReaderSize(clientConn, mcproto.MaxHandshakeLength)

	// Checks third byte to tell legacy ping from big handshake
	if first, err := br.Peek(1); err != nil {
		return
	} else if first[0] == mcproto.LegacyPingByte {
		raw, _ := br.Peek(br.Buffered())
		if len(raw) < 3 || raw[2] != 0x00 {
			p.serveLegacyPing(clientConn, raw)
			return
		}
	}

	handshake, err := mcproto.ReadHandshakePacket(br)
	if err != nil {
		p.logger.Debug("Failed to read handshake from %s: %v", clientConn.RemoteAddr(), err)
		return
	}

	hostname := normalizeHostname(handshake.ServerAddress)
	route, ok := p.lookupRoute(hostname)
	if !ok {
		p.logger.Debug("No active route for hostname %q from %s", hostname, clientConn.RemoteAddr())
		if handshake.NextState == mcproto.NextStateStatus {
			p.serveSyntheticStatus(clientConn, br, handshake,
				fmt.Sprintf("Powered by DiscoPanel - nothing is running at %s", hostname), 0, "DiscoPanel")
			return
		}
		clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		mcproto.WriteLoginDisconnect(clientConn, fmt.Sprintf("No server is available at %s", hostname))
		return
	}

	stats := p.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.LastProtocol.Store(int32(handshake.ProtocolVersion))
	if handshake.NextState == mcproto.NextStateStatus {
		stats.StatusPings.Add(1)
	} else {
		stats.Logins.Add(1)
	}

	// Paused servers answer status pings without waking, wake on login
	if gate := p.getGate(); gate != nil {
		if info, sleeping := gate.SleepingInfo(route.ServerID); sleeping {
			if handshake.NextState == mcproto.NextStateStatus {
				p.serveSyntheticStatus(clientConn, br, handshake, info.Motd, info.MaxPlayers, "Sleeping")
				return
			}
			p.logger.Info("Waking sleeping server %s for incoming login", route.ServerID)
			stats.Wakes.Add(1)
			wakeCtx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
			err := gate.WakeServer(wakeCtx, route.ServerID)
			cancel()
			if err != nil {
				p.logger.Error("Failed to wake server %s: %v", route.ServerID, err)
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
			p.serveSyntheticStatus(clientConn, br, handshake, route.Motd, route.MaxPlayers, "Offline")
			return
		}
		clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if !route.Wakeable {
			mcproto.WriteLoginDisconnect(clientConn, "The server is offline")
			return
		}
		gate := p.getGate()
		if gate == nil {
			mcproto.WriteLoginDisconnect(clientConn, "The server is offline")
			return
		}
		p.logger.Info("Starting stopped server %s for incoming login", route.ServerID)
		stats.Wakes.Add(1)
		if err := gate.StartServer(route.ServerID); err != nil {
			p.logger.Error("Failed to start server %s for login: %v", route.ServerID, err)
			mcproto.WriteLoginDisconnect(clientConn, "The server could not be started, check the panel")
			return
		}
		mcproto.WriteLoginDisconnect(clientConn, "The server is starting up, join again in a minute")
		return

	case v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING:
		if handshake.NextState == mcproto.NextStateStatus {
			p.serveSyntheticStatus(clientConn, br, handshake, route.Motd, route.MaxPlayers, "Starting")
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
		p.logger.Error("Route %s has no backend address", hostname)
		if handshake.NextState == mcproto.NextStateLogin {
			clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			mcproto.WriteLoginDisconnect(clientConn, "The server is not reachable right now")
		}
		return
	}

	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	backendConn, err := dialBackendWithRetry(p.ctx, backendAddr, 10*time.Second)
	if err != nil {
		p.logger.Error("Failed to connect to backend %s for %s: %v", backendAddr, hostname, err)
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
			p.logger.Error("Failed to write PROXY header to backend %s: %v", backendAddr, err)
			return
		}
	}

	rewriteHandshakeAddress(handshake, route.BackendPort, route.PreserveHost)

	if err := mcproto.WriteHandshakePacket(backendConn, handshake); err != nil {
		p.logger.Error("Failed to write handshake to backend %s: %v", backendAddr, err)
		return
	}

	// Flushes client bytes already buffered before relay handoff
	if buffered := br.Buffered(); buffered > 0 {
		pending, _ := br.Peek(buffered)
		if _, err := backendConn.Write(pending); err != nil {
			p.logger.Error("Failed to flush buffered client data to backend %s: %v", backendAddr, err)
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
func (p *MinecraftProxy) serveSyntheticStatus(conn net.Conn, r io.Reader, handshake *mcproto.HandshakePacket, motd string, maxPlayers int, versionName string) {
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
func (p *MinecraftProxy) serveLegacyPing(conn net.Conn, raw []byte) {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	motd, version, maxPlayers := p.legacyStatus(raw)
	mcproto.WriteLegacyKick(conn, len(raw) > 1, motd, version, maxPlayers)
}

// Derives legacy status fields from the routed server
func (p *MinecraftProxy) legacyStatus(raw []byte) (string, string, int) {
	route, ok := p.legacyPingRoute(raw)
	if !ok {
		return "Powered by DiscoPanel", "DiscoPanel", 0
	}

	stats := p.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.StatusPings.Add(1)

	if gate := p.getGate(); gate != nil {
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
func (p *MinecraftProxy) legacyPingRoute(raw []byte) (Route, bool) {
	if hostname, ok := mcproto.LegacyPingHostname(raw); ok {
		return p.lookupRoute(normalizeHostname(hostname))
	}

	p.routesMu.RLock()
	defer p.routesMu.RUnlock()
	if len(p.routes) == 1 {
		for _, route := range p.routes {
			return *route, true
		}
	}
	return Route{}, false
}
