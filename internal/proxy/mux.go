package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"golang.org/x/crypto/cryptobyte"
)

// One tcp socket dispatching minecraft, http, and relay lanes
type ListenerSocket struct {
	listenAddr string
	logger     *logger.Logger

	mcRoutes map[string]*Route
	tcpRelay *Route
	routesMu sync.RWMutex

	httpLane *httpLane
	certs    *certIndex

	stats   map[string]*RouteStats
	statsMu sync.Mutex

	intents *IntentTable
	hub     *HubRuntime

	gate   ServerGate
	gateMu sync.RWMutex

	ingressProxyProtocol bool
	trustedProxies       []string

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
	s := &ListenerSocket{
		mcRoutes:             make(map[string]*Route),
		stats:                make(map[string]*RouteStats),
		logger:               cfg.Logger,
		listenAddr:           cfg.ListenAddr,
		ctx:                  ctx,
		cancel:               cancel,
		gate:                 cfg.Gate,
		certs:                cfg.Certs,
		intents:              cfg.Intents,
		hub:                  cfg.Hub,
		ingressProxyProtocol: cfg.IngressProxyProtocol,
		trustedProxies:       cfg.TrustedProxies,
	}
	s.httpLane = newHTTPLane(cfg.Logger, cfg.TrustedEdge, s.statsFor)
	return s
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

	if s.ingressProxyProtocol {
		wrapped, err := WrapIngressListener(listener, s.trustedProxies)
		if err != nil {
			listener.Close()
			return fmt.Errorf("failed to wrap ingress listener with PROXY protocol: %w", err)
		}
		listener = wrapped
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

// Reports whether socket configuration has drifted from desired listener settings
func (s *ListenerSocket) NeedsRebind(ingressProxyProtocol bool, trustedProxies []string) bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.ingressProxyProtocol != ingressProxyProtocol {
		return true
	}
	if len(s.trustedProxies) != len(trustedProxies) {
		return true
	}
	for i := range s.trustedProxies {
		if s.trustedProxies[i] != trustedProxies[i] {
			return true
		}
	}
	return false
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
			route.Hostname = normalizeWireHostname(route.Hostname)
			newMC[route.Hostname] = &route
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
			route.Hostname = normalizeWireHostname(route.Hostname)
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

// Removes one route from its lane
func (s *ListenerSocket) RemoveRoute(protocol v1.ModuleProtocol, hostname string) {
	switch protocol {
	case v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
		s.removeMCRoute(hostname)
	case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
		s.httpLane.remove(normalizeWireHostname(hostname))
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

// Snapshots recorded bytes and stops recording
func (c *recordedConn) take() []byte {
	pending := append([]byte(nil), c.buf.Bytes()...)
	c.stopRecording()
	return pending
}

// Forwards half-close so relay drains work through the wrapper
func (c *recordedConn) CloseWrite() error {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// Serves buffered sniff bytes ahead of the live socket
type replayConn struct {
	net.Conn
	pending []byte
	// Marks conns that arrived through tls termination
	secure bool
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// Forwards half-close so relay drains work through the wrapper
func (c *replayConn) CloseWrite() error {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
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
	s.dispatch(raw, false)
}

// Peek and descend dispatch, tls unwraps at most once
func (s *ListenerSocket) dispatch(conn net.Conn, terminated bool) {
	rec := &recordedConn{Conn: conn}
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReaderSize(rec, mcproto.MaxHandshakeLength)

	// Pure relay ports skip the sniff, hello peek excepted
	if s.relayOnly() {
		if !terminated && s.certsAvailable() {
			// Short grace keeps server first protocols flowing
			conn.SetReadDeadline(time.Now().Add(relaySniffGrace))
			if hdr, err := br.Peek(3); err == nil && hdr[0] == tlsRecordHandshake && hdr[1] == 0x03 {
				conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
				s.serveTLS(conn, br, rec)
				return
			}
			conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
		}
		s.serveRelay(conn, rec.take())
		return
	}

	first, err := br.Peek(1)
	if err != nil {
		// Silent clients still reach a configured relay
		if _, ok := s.relayRoute(); ok {
			s.serveRelay(conn, rec.take())
			return
		}
		conn.Close()
		return
	}

	// Hello detection, mc handshakes never carry 0x03 second
	if !terminated && first[0] == tlsRecordHandshake {
		if hdr, herr := br.Peek(3); herr == nil && hdr[1] == 0x03 {
			s.serveHello(conn, br, rec)
			return
		}
	}

	// Legacy ping detection, big handshakes also start 0xfe
	if first[0] == mcproto.LegacyPingByte {
		peeked, _ := br.Peek(br.Buffered())
		if len(peeked) < 3 {
			// Grace peek separates split handshakes from bare pings
			conn.SetReadDeadline(time.Now().Add(legacyPeekGrace))
			if more, err := br.Peek(3); err == nil {
				peeked = more
			}
			conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
		}
		if len(peeked) < 3 || peeked[2] != 0x00 {
			defer conn.Close()
			s.serveLegacyPing(conn, peeked)
			return
		}
	} else if sniffHTTP(br) {
		s.serveHTTPConn(rec, conn, terminated)
		return
	}

	handshake, err := mcproto.ReadHandshakePacket(br)
	if err != nil {
		if _, ok := s.relayRoute(); ok {
			s.serveRelay(conn, rec.take())
			return
		}
		s.logger.Debug("Unrecognized protocol from %s on %s: %v", conn.RemoteAddr(), s.listenAddr, err)
		conn.Close()
		return
	}

	rec.stopRecording()
	defer conn.Close()
	s.serveMinecraft(conn, br, handshake)
}

// First byte of a tls handshake record
const tlsRecordHandshake = 0x16

// Hello wait budget on otherwise sniffless relay ports
const relaySniffGrace = 250 * time.Millisecond

// Client hello size cap across records
const maxClientHello = 64 << 10

// True when config file certificates cover this socket
func (s *ListenerSocket) certsAvailable() bool {
	return s.certs != nil && len(s.certs.entries) > 0
}

// Routes a hello to termination, relay, or refusal
func (s *ListenerSocket) serveHello(conn net.Conn, br *bufio.Reader, rec *recordedConn) {
	if s.certsAvailable() {
		s.serveTLS(conn, br, rec)
		return
	}
	// Relay backends terminate their own traffic
	if _, ok := s.relayRoute(); ok {
		s.serveRelay(conn, rec.take())
		return
	}
	s.logger.Debug("Refused TLS client %s on %s, no certificates configured", conn.RemoteAddr(), s.listenAddr)
	conn.Close()
}

// Terminates tls when a certificate matches the hello
func (s *ListenerSocket) serveTLS(conn net.Conn, br *bufio.Reader, rec *recordedConn) {
	sni, ok := readClientHelloSNI(br)
	pending := rec.take()

	var cert *tls.Certificate
	if ok {
		if matched, found := s.certs.match(sni); found {
			cert = matched
		}
	}
	if cert == nil {
		// Unknown names pass through to a relay backend
		if _, hasRelay := s.relayRoute(); hasRelay {
			s.serveRelay(conn, pending)
			return
		}
		s.logger.Debug("No certificate for %q from %s on %s", sni, conn.RemoteAddr(), s.listenAddr)
		conn.Close()
		return
	}

	tlsConn := tls.Server(&replayConn{Conn: conn, pending: pending}, terminationConfig(cert))
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if err := tlsConn.HandshakeContext(s.ctx); err != nil {
		s.logger.Debug("TLS handshake failed from %s on %s: %v", conn.RemoteAddr(), s.listenAddr, err)
		conn.Close()
		return
	}

	// Plaintext re-enters the sniff exactly once
	s.dispatch(tlsConn, true)
}

// Server tls config for one matched certificate
func terminationConfig(cert *tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}
}

// Reads the hello for sni, consumed bytes stay recorded
func readClientHelloSNI(br *bufio.Reader) (string, bool) {
	msg, ok := readHandshakeMessage(br)
	if !ok {
		return "", false
	}
	return parseHelloSNI(msg)
}

// Reassembles one handshake message across records
func readHandshakeMessage(br *bufio.Reader) ([]byte, bool) {
	var msg []byte
	need := -1
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil, false
		}
		if hdr[0] != tlsRecordHandshake || hdr[1] != 0x03 {
			return nil, false
		}
		recLen := int(hdr[3])<<8 | int(hdr[4])
		if recLen == 0 || recLen > (1<<14)+256 {
			return nil, false
		}
		frag := make([]byte, recLen)
		if _, err := io.ReadFull(br, frag); err != nil {
			return nil, false
		}
		msg = append(msg, frag...)
		if need < 0 && len(msg) >= 4 {
			if msg[0] != 0x01 {
				return nil, false
			}
			need = (int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3])) + 4
			if need > maxClientHello {
				return nil, false
			}
		}
		if need >= 0 && len(msg) >= need {
			return msg[:need], true
		}
		if len(msg) > maxClientHello {
			return nil, false
		}
	}
}

// Pulls the server name extension from a hello
func parseHelloSNI(msg []byte) (string, bool) {
	s := cryptobyte.String(msg)
	var msgType uint8
	var body cryptobyte.String
	if !s.ReadUint8(&msgType) || msgType != 0x01 || !s.ReadUint24LengthPrefixed(&body) {
		return "", false
	}
	var legacyVersion uint16
	var random []byte
	if !body.ReadUint16(&legacyVersion) || !body.ReadBytes(&random, 32) {
		return "", false
	}
	var sessionID, ciphers, compression cryptobyte.String
	if !body.ReadUint8LengthPrefixed(&sessionID) ||
		!body.ReadUint16LengthPrefixed(&ciphers) ||
		!body.ReadUint8LengthPrefixed(&compression) {
		return "", false
	}
	if body.Empty() {
		return "", false
	}
	var extensions cryptobyte.String
	if !body.ReadUint16LengthPrefixed(&extensions) {
		return "", false
	}
	for !extensions.Empty() {
		var extType uint16
		var extData cryptobyte.String
		if !extensions.ReadUint16(&extType) || !extensions.ReadUint16LengthPrefixed(&extData) {
			return "", false
		}
		if extType != 0 {
			continue
		}
		var names cryptobyte.String
		if !extData.ReadUint16LengthPrefixed(&names) {
			return "", false
		}
		for !names.Empty() {
			var nameType uint8
			var name cryptobyte.String
			if !names.ReadUint8(&nameType) || !names.ReadUint16LengthPrefixed(&name) {
				return "", false
			}
			if nameType == 0 {
				return string(name), true
			}
		}
		return "", false
	}
	return "", false
}

// Forwards sniffed bytes then splices raw sockets
func (s *ListenerSocket) serveRelay(raw net.Conn, pending []byte) {
	defer raw.Close()

	route, ok := s.relayRoute()
	if !ok {
		return
	}

	backendAddr := route.BackendAddr()
	backendConn, err := dialBackend(s.ctx, backendAddr)
	if err != nil {
		s.logger.Error("Relay dial failed for %s: %v", backendAddr, err)
		return
	}
	defer backendConn.Close()

	stats := s.statsFor(route.ServerID)
	stats.TotalConns.Add(1)
	stats.ActiveConns.Add(1)
	defer stats.ActiveConns.Add(-1)

	if len(pending) > 0 {
		if _, err := backendConn.Write(pending); err != nil {
			return
		}
		stats.BytesToBackend.Add(int64(len(pending)))
	}

	raw.SetDeadline(time.Time{})
	stats.countRelay(raw, backendConn)
}

// Hands an http connection to the lane server
func (s *ListenerSocket) serveHTTPConn(rec *recordedConn, conn net.Conn, secure bool) {
	conn.SetReadDeadline(time.Time{})
	if !s.feed.Push(&replayConn{Conn: conn, pending: rec.take(), secure: secure}) {
		conn.Close()
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
