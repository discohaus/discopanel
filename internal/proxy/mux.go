package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// One tcp socket dispatching minecraft, http, and relay lanes
type ListenerSocket struct {
	listenAddr string
	logger     *logger.Logger

	mcRoutes map[string]*Route
	tcpRelay *Route
	routesMu sync.RWMutex

	httpLane *httpLane

	stats   map[string]*RouteStats
	statsMu sync.Mutex

	gate   ServerGate
	gateMu sync.RWMutex

	listener net.Listener
	feed     *connFeed
	running  bool
	stateMu  sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

// Creates a listener socket for one port
func NewListenerSocket(cfg *Config) *ListenerSocket {
	ctx, cancel := context.WithCancel(context.Background())
	return &ListenerSocket{
		mcRoutes:   make(map[string]*Route),
		stats:      make(map[string]*RouteStats),
		httpLane:   newHTTPLane(cfg.Logger),
		logger:     cfg.Logger,
		listenAddr: cfg.ListenAddr,
		ctx:        ctx,
		cancel:     cancel,
		gate:       cfg.Gate,
	}
}

// Starts the socket and its http lane server
func (s *ListenerSocket) Start() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if s.running {
		return fmt.Errorf("listener socket already running")
	}

	listener, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.listenAddr, err)
	}

	s.listener = listener
	s.feed = newConnFeed(listener.Addr())
	s.running = true

	s.httpLane.start(s.feed)
	go acceptLoop(s.ctx, listener, s.logger, s.handleConnection)

	s.logger.Info("Listener socket started on %s", s.listenAddr)
	return nil
}

// Stops the socket, established relays drain on their own
func (s *ListenerSocket) Stop() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	if !s.running {
		return nil
	}

	s.cancel()
	s.running = false
	s.httpLane.stop()
	if s.feed != nil {
		s.feed.Close()
	}

	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	s.logger.Info("Listener socket stopped on %s", s.listenAddr)
	return nil
}

// Returns whether the socket is accepting
func (s *ListenerSocket) IsRunning() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.running
}

// Registers the wake gate for paused servers
func (s *ListenerSocket) SetGate(gate ServerGate) {
	s.gateMu.Lock()
	s.gate = gate
	s.gateMu.Unlock()
}

func (s *ListenerSocket) getGate() ServerGate {
	s.gateMu.RLock()
	defer s.gateMu.RUnlock()
	return s.gate
}

// Replaces every lane's routes in one pass
func (s *ListenerSocket) SetRoutes(routes []Route) {
	newMC := make(map[string]*Route)
	newHTTP := make(map[string]*Route)
	var relay *Route
	keepStats := make(map[string]bool, len(routes))

	for i := range routes {
		route := routes[i]
		keepStats[route.ServerID] = true
		switch route.Protocol {
		case v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
			if route.State == v1.ProxyRouteState_PROXY_ROUTE_STATE_UNSPECIFIED {
				route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE
			}
			route.Hostname = normalizeHostname(route.Hostname)
			newMC[route.Hostname] = &route
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
			route.Hostname = normalizeHostname(route.Hostname)
			newHTTP[route.Hostname] = &route
		default:
			relay = &route
		}
	}

	s.routesMu.Lock()
	s.mcRoutes = newMC
	s.tcpRelay = relay
	s.routesMu.Unlock()
	s.httpLane.setRoutes(newHTTP)

	// Counters for dropped routes go away with them
	s.statsMu.Lock()
	for id := range s.stats {
		if !keepStats[id] {
			delete(s.stats, id)
		}
	}
	s.statsMu.Unlock()
}

// Installs or replaces one route in its lane
func (s *ListenerSocket) UpsertRoute(route Route) {
	switch route.Protocol {
	case v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
		s.UpsertServerRoute(route)
	case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
		route.Hostname = normalizeHostname(route.Hostname)
		s.httpLane.upsert(&route)
	default:
		s.routesMu.Lock()
		s.tcpRelay = &route
		s.routesMu.Unlock()
	}
}

// Removes one route from its lane
func (s *ListenerSocket) RemoveRoute(protocol v1.ModuleProtocol, hostname string) {
	switch protocol {
	case v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
		s.removeMCRoute(hostname)
	case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
		s.httpLane.remove(normalizeHostname(hostname))
	default:
		s.routesMu.Lock()
		s.tcpRelay = nil
		s.routesMu.Unlock()
	}
}

// Snapshot of every lane's routes
func (s *ListenerSocket) Routes() []Route {
	var out []Route
	s.routesMu.RLock()
	for _, r := range s.mcRoutes {
		out = append(out, *r)
	}
	if s.tcpRelay != nil {
		out = append(out, *s.tcpRelay)
	}
	s.routesMu.RUnlock()
	out = append(out, s.httpLane.routes()...)
	return out
}

// Relay backend if one is configured
func (s *ListenerSocket) relayRoute() (Route, bool) {
	s.routesMu.RLock()
	defer s.routesMu.RUnlock()
	if s.tcpRelay == nil {
		return Route{}, false
	}
	return *s.tcpRelay, true
}

// True when only the relay lane is populated
func (s *ListenerSocket) relayOnly() bool {
	s.routesMu.RLock()
	pure := s.tcpRelay != nil && len(s.mcRoutes) == 0
	s.routesMu.RUnlock()
	return pure && s.httpLane.empty()
}

// Buffers read bytes so a failed sniff can replay them
type recordedConn struct {
	net.Conn
	buf  bytes.Buffer
	done bool
}

func (c *recordedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if !c.done && n > 0 {
		c.buf.Write(p[:n])
	}
	return n, err
}

func (c *recordedConn) stopRecording() {
	c.done = true
	c.buf.Reset()
}

// Serves buffered sniff bytes ahead of the live socket
type replayConn struct {
	net.Conn
	pending []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// Methods every http verb can open with
var httpMethods = []string{
	"GET ", "HEAD ", "POST ", "PUT ", "DELETE ",
	"OPTIONS ", "PATCH ", "TRACE ", "CONNECT ", "PRI ",
}

// Reports whether buffered bytes open an http request
func sniffHTTP(br *bufio.Reader) bool {
	for {
		avail := br.Buffered()
		if avail > 8 {
			avail = 8
		}
		peeked, err := br.Peek(avail)
		if err != nil {
			return false
		}
		prefix := false
		for _, method := range httpMethods {
			if len(peeked) >= len(method) {
				if string(peeked[:len(method)]) == method {
					return true
				}
				continue
			}
			if string(peeked) == method[:len(peeked)] {
				prefix = true
			}
		}
		if !prefix || avail >= 8 {
			return false
		}
		// Prefix of a method, wait for one more byte
		if _, err := br.Peek(avail + 1); err != nil {
			return false
		}
	}
}

// Sniffs the first bytes and dispatches to a lane
func (s *ListenerSocket) handleConnection(raw net.Conn) {
	rec := &recordedConn{Conn: raw}
	raw.SetReadDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReaderSize(rec, mcproto.MaxHandshakeLength)

	// Pure relay ports skip the sniff entirely
	if s.relayOnly() {
		s.serveRelay(raw, rec)
		return
	}

	first, err := br.Peek(1)
	if err != nil {
		// Silent clients still reach a configured relay
		if _, ok := s.relayRoute(); ok {
			s.serveRelay(raw, rec)
			return
		}
		raw.Close()
		return
	}

	// Legacy ping detection, big handshakes also start 0xfe
	if first[0] == mcproto.LegacyPingByte {
		peeked, _ := br.Peek(br.Buffered())
		if len(peeked) < 3 {
			// Grace peek separates split handshakes from bare pings
			raw.SetReadDeadline(time.Now().Add(legacyPeekGrace))
			if more, err := br.Peek(3); err == nil {
				peeked = more
			}
			raw.SetReadDeadline(time.Now().Add(handshakeTimeout))
		}
		if len(peeked) < 3 || peeked[2] != 0x00 {
			defer raw.Close()
			s.serveLegacyPing(raw, peeked)
			return
		}
	} else if sniffHTTP(br) {
		s.serveHTTPConn(rec, raw)
		return
	}

	handshake, err := mcproto.ReadHandshakePacket(br)
	if err != nil {
		if _, ok := s.relayRoute(); ok {
			s.serveRelay(raw, rec)
			return
		}
		s.logger.Debug("Unrecognized protocol from %s on %s: %v", raw.RemoteAddr(), s.listenAddr, err)
		raw.Close()
		return
	}

	rec.stopRecording()
	defer raw.Close()
	s.serveMinecraft(raw, br, handshake)
}

// Forwards recorded bytes then splices raw sockets
func (s *ListenerSocket) serveRelay(raw net.Conn, rec *recordedConn) {
	defer raw.Close()

	route, ok := s.relayRoute()
	if !ok {
		return
	}

	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	backendConn, err := dialBackend(s.ctx, backendAddr)
	if err != nil {
		s.logger.Error("Relay dial failed for %s: %v", backendAddr, err)
		return
	}
	defer backendConn.Close()

	if rec.buf.Len() > 0 {
		if _, err := backendConn.Write(rec.buf.Bytes()); err != nil {
			return
		}
	}
	rec.stopRecording()

	raw.SetDeadline(time.Time{})
	relay(raw, backendConn)
}

// Hands an http connection to the lane server
func (s *ListenerSocket) serveHTTPConn(rec *recordedConn, raw net.Conn) {
	raw.SetReadDeadline(time.Time{})
	pending := append([]byte(nil), rec.buf.Bytes()...)
	rec.stopRecording()
	if !s.feed.Push(&replayConn{Conn: raw, pending: pending}) {
		raw.Close()
	}
}

// Feeds sniffed connections to the http lane server
type connFeed struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
	addr net.Addr
}

func newConnFeed(addr net.Addr) *connFeed {
	return &connFeed{
		ch:   make(chan net.Conn),
		done: make(chan struct{}),
		addr: addr,
	}
}

func (f *connFeed) Accept() (net.Conn, error) {
	select {
	case conn := <-f.ch:
		return conn, nil
	case <-f.done:
		return nil, net.ErrClosed
	}
}

func (f *connFeed) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

func (f *connFeed) Addr() net.Addr {
	return f.addr
}

// Reports whether the conn was handed off
func (f *connFeed) Push(conn net.Conn) bool {
	select {
	case f.ch <- conn:
		return true
	case <-f.done:
		return false
	}
}
