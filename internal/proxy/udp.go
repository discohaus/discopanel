package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

const (
	udpSessionIdleTimeout = 5 * time.Minute
	maxUDPSessions        = 1024
)

var errUDPSessionsFull = errors.New("udp session table full")

// Forwards UDP per client, tracking one backend socket each
type UDPProxy struct {
	listenAddr string
	logger     *logger.Logger

	backendHost string
	backendPort int
	route       Route
	stats       *RouteStats

	conn    *net.UDPConn
	running bool
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc

	sessions   map[string]*udpSession
	sessionsMu sync.Mutex
}

type udpSession struct {
	clientAddr  *net.UDPAddr
	backendConn *net.UDPConn
	backendAddr *net.UDPAddr
	lastActive  atomic.Int64 // Unix nanos
}

func (s *udpSession) touch() {
	s.lastActive.Store(time.Now().UnixNano())
}

func (s *udpSession) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.lastActive.Load()))
}

// Creates a new UDP proxy
func NewUDPProxy(cfg *Config) *UDPProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &UDPProxy{
		listenAddr: cfg.ListenAddr,
		logger:     cfg.Logger,
		stats:      &RouteStats{},
		ctx:        ctx,
		cancel:     cancel,
		sessions:   make(map[string]*udpSession),
	}
}

// Returns the current counters under the config lock
func (p *UDPProxy) statsRef() *RouteStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stats
}

// Copies the relay's counters, live sessions gauge included
func (p *UDPProxy) StatsSnapshot() *v1.ProxyRoute {
	snap := p.statsRef().Snapshot()
	p.sessionsMu.Lock()
	snap.ActiveConnections = int64(len(p.sessions))
	p.sessionsMu.Unlock()
	return snap
}

// Starts the UDP proxy
func (p *UDPProxy) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("UDP proxy already running")
	}

	addr, err := net.ResolveUDPAddr("udp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve listen address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
	}

	p.conn = conn
	p.running = true

	go p.proxyLoop(conn)
	go p.cleanupLoop()

	p.logger.Info("UDP proxy started on %s", p.listenAddr)
	return nil
}

// Stops the UDP proxy
func (p *UDPProxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	p.cancel()
	p.running = false

	p.flushSessions()

	if p.conn != nil {
		p.conn.Close()
	}

	p.logger.Info("UDP proxy stopped: %s", p.listenAddr)
	return nil
}

// Sets the relay backend, only one route supported
func (p *UDPProxy) SetRoute(route Route) {
	route.Protocol = v1.ModuleProtocol_MODULE_PROTOCOL_UDP
	p.mu.Lock()
	changed := p.backendHost != route.BackendHost || p.backendPort != route.BackendPort
	ownerChanged := p.route.ServerID != route.ServerID
	p.route = route
	p.backendHost = route.BackendHost
	p.backendPort = route.BackendPort
	// Fresh counters keep an old owner's totals out
	if ownerChanged {
		p.stats = &RouteStats{}
	}
	p.mu.Unlock()
	if changed {
		p.flushSessions()
		p.logger.Info("UDP relay route set: %s -> %s:%d", p.listenAddr, route.BackendHost, route.BackendPort)
	}
}

// Returns the relay route, UDP only has one
func (p *UDPProxy) Route() (Route, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.backendHost == "" {
		return Route{}, false
	}
	return p.route, true
}

// Returns whether the proxy is running
func (p *UDPProxy) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// Forwards client packets to per-client backend sockets
func (p *UDPProxy) proxyLoop(conn *net.UDPConn) {
	buf := make([]byte, 65535)

	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				p.logger.Error("UDP read error: %v", err)
				continue
			}
		}

		session, err := p.getOrCreateSession(clientAddr)
		if err != nil {
			if errors.Is(err, errUDPSessionsFull) {
				p.logger.Debug("Dropping packet from %s: %v", clientAddr, err)
			} else {
				p.logger.Error("Failed to create session for %s: %v", clientAddr, err)
			}
			continue
		}

		session.touch()

		if _, err := session.backendConn.WriteToUDP(buf[:n], session.backendAddr); err != nil {
			p.logger.Error("Failed to forward to backend: %v", err)
			p.removeSession(clientAddr.String())
			continue
		}
		p.statsRef().BytesToBackend.Add(int64(n))
	}
}

// Gets or creates a session, avoids lock order with Stop
func (p *UDPProxy) getOrCreateSession(clientAddr *net.UDPAddr) (*udpSession, error) {
	clientKey := clientAddr.String()

	p.sessionsMu.Lock()
	session, exists := p.sessions[clientKey]
	full := len(p.sessions) >= maxUDPSessions
	p.sessionsMu.Unlock()
	if exists {
		return session, nil
	}
	if full {
		return nil, errUDPSessionsFull
	}

	p.mu.RLock()
	backendHost := p.backendHost
	backendPort := p.backendPort
	p.mu.RUnlock()

	if backendHost == "" || backendPort == 0 {
		return nil, fmt.Errorf("no backend configured")
	}

	backendAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(backendHost, strconv.Itoa(backendPort)))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve backend: %w", err)
	}

	backendConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend socket: %w", err)
	}

	session = &udpSession{
		clientAddr:  clientAddr,
		backendConn: backendConn,
		backendAddr: backendAddr,
	}
	session.touch()

	p.sessionsMu.Lock()
	if existing, ok := p.sessions[clientKey]; ok {
		// Lost a creation race - keep the established session
		p.sessionsMu.Unlock()
		backendConn.Close()
		return existing, nil
	}
	if len(p.sessions) >= maxUDPSessions {
		p.sessionsMu.Unlock()
		backendConn.Close()
		return nil, errUDPSessionsFull
	}
	p.sessions[clientKey] = session
	p.sessionsMu.Unlock()
	p.statsRef().TotalConns.Add(1)

	go p.handleBackendResponses(session, clientKey)

	p.logger.Debug("UDP session created: %s -> %s:%d", clientKey, backendHost, backendPort)
	return session, nil
}

// Forwards backend responses to client until closed or idle
func (p *UDPProxy) handleBackendResponses(session *udpSession, clientKey string) {
	buf := make([]byte, 65535)

	for {
		session.backendConn.SetReadDeadline(time.Now().Add(udpSessionIdleTimeout))
		n, _, err := session.backendConn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && session.idleFor() < udpSessionIdleTimeout {
				continue
			}
			p.removeSession(clientKey)
			return
		}

		session.touch()

		p.mu.RLock()
		conn := p.conn
		p.mu.RUnlock()
		if conn == nil {
			return
		}
		if _, err := conn.WriteToUDP(buf[:n], session.clientAddr); err != nil {
			p.logger.Error("Failed to send to client %s: %v", clientKey, err)
			continue
		}
		p.statsRef().BytesToClient.Add(int64(n))
	}
}

// Removes and cleans up a session
func (p *UDPProxy) removeSession(clientKey string) {
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()

	if session, exists := p.sessions[clientKey]; exists {
		session.backendConn.Close()
		delete(p.sessions, clientKey)
		p.logger.Debug("Removed UDP session for %s", clientKey)
	}
}

// Closes every session, used on stop and backend change
func (p *UDPProxy) flushSessions() {
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()

	for key, session := range p.sessions {
		session.backendConn.Close()
		delete(p.sessions, key)
	}
}

// Removes stale sessions
func (p *UDPProxy) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.sessionsMu.Lock()
			for key, session := range p.sessions {
				if session.idleFor() > udpSessionIdleTimeout {
					session.backendConn.Close()
					delete(p.sessions, key)
					p.logger.Debug("Cleaned up stale UDP session for %s", key)
				}
			}
			p.sessionsMu.Unlock()
		}
	}
}
