package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/nickheyer/discopanel/pkg/logger"
)

// RakNet sends connection traffic in bursts, so repeated packets from one join
// must not queue duplicate server lifecycle calls.
const lazyWakeRetryInterval = 5 * time.Second

// UDPProxy handles UDP forwarding for modules like Geyser
// Implements the Proxier interface
type UDPProxy struct {
	listenAddr      string
	backendHost     string
	backendPort     int
	serverID        string
	conn            *net.UDPConn
	logger          *logger.Logger
	running         bool
	mu              sync.RWMutex
	ctx             context.Context
	cancel          context.CancelFunc
	lazyServerID    string
	isLazyServer    func(string) bool
	wakeServer      func(context.Context, string) error
	wakeInFlight    bool
	lastWakeAttempt time.Time

	// Client session tracking - maintains backend connection per client
	sessions   map[string]*udpSession
	sessionsMu sync.RWMutex
}

type udpSession struct {
	clientAddr  *net.UDPAddr
	backendConn *net.UDPConn
	backendAddr *net.UDPAddr
	lastActive  time.Time
}

// NewUDPProxy creates a new UDP proxy
func NewUDPProxy(cfg *Config) *UDPProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &UDPProxy{
		listenAddr: cfg.ListenAddr,
		logger:     cfg.Logger,
		ctx:        ctx,
		cancel:     cancel,
		sessions:   make(map[string]*udpSession),
	}
}

func (p *UDPProxy) setLazyWakeHandler(serverID string, isLazyServer func(string) bool, wakeServer func(context.Context, string) error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.lazyServerID = serverID
	p.isLazyServer = isLazyServer
	p.wakeServer = wakeServer
}

// Start starts the UDP proxy
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

	go p.proxyLoop()
	go p.cleanupLoop()

	p.logger.Info("UDP proxy started on %s", p.listenAddr)
	return nil
}

// Stop stops the UDP proxy
func (p *UDPProxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	p.cancel()
	p.running = false

	// Close all backend connections
	p.sessionsMu.Lock()
	for _, session := range p.sessions {
		session.backendConn.Close()
	}
	p.sessions = make(map[string]*udpSession)
	p.sessionsMu.Unlock()

	if p.conn != nil {
		p.conn.Close()
	}

	p.logger.Info("UDP proxy stopped: %s", p.listenAddr)
	return nil
}

// AddRoute sets the backend for UDP proxy (only one route supported)
func (p *UDPProxy) AddRoute(serverID, hostname, backendHost string, backendPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serverID = serverID
	p.backendHost = backendHost
	p.backendPort = backendPort
	p.logger.Info("UDP proxy route set: %s -> %s:%d", p.listenAddr, backendHost, backendPort)
}

// RemoveRoute removes the backend route
func (p *UDPProxy) RemoveRoute(hostname string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serverID = ""
	p.backendHost = ""
	p.backendPort = 0
	p.logger.Info("UDP proxy route removed: %s", p.listenAddr)
}

// UpdateRoute updates the backend address
func (p *UDPProxy) UpdateRoute(hostname, backendHost string, backendPort int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backendHost = backendHost
	p.backendPort = backendPort
	p.logger.Info("UDP proxy route updated: %s -> %s:%d", p.listenAddr, backendHost, backendPort)
}

// GetRoutes returns the current route (UDP only has one)
func (p *UDPProxy) GetRoutes() map[string]*Route {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.backendHost == "" {
		return make(map[string]*Route)
	}

	return map[string]*Route{
		"udp": {
			ServerID:    p.serverID,
			Hostname:    "udp",
			BackendHost: p.backendHost,
			BackendPort: p.backendPort,
			Active:      true,
		},
	}
}

// proxyLoop handles incoming UDP packets from clients
func (p *UDPProxy) proxyLoop() {
	buf := make([]byte, 65535)

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		p.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, clientAddr, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			select {
			case <-p.ctx.Done():
				return
			default:
				p.logger.Error("UDP read error: %v", err)
				continue
			}
		}

		// RakNet discovery is forwarded to Geyser without waking Java. Connection
		// traffic means a Bedrock client is actively trying to join.
		p.wakeOnBedrockConnect(buf[:n])

		// Get or create session for this client
		session, err := p.getOrCreateSession(clientAddr)
		if err != nil {
			p.logger.Error("Failed to create session for %s: %v", clientAddr, err)
			continue
		}

		// Update last active time
		session.lastActive = time.Now()

		// Forward packet to backend
		_, err = session.backendConn.WriteToUDP(buf[:n], session.backendAddr)
		if err != nil {
			p.logger.Error("Failed to forward to backend: %v", err)
			p.removeSession(clientAddr.String())
		}
	}
}

func (p *UDPProxy) wakeOnBedrockConnect(packet []byte) {
	if !isRakNetConnectionTraffic(packet) {
		return
	}

	p.mu.Lock()
	serverID := p.lazyServerID
	isLazyServer := p.isLazyServer
	wakeServer := p.wakeServer
	if serverID == "" || isLazyServer == nil || wakeServer == nil || p.wakeInFlight || time.Since(p.lastWakeAttempt) < lazyWakeRetryInterval {
		p.mu.Unlock()
		return
	}
	p.wakeInFlight = true
	p.lastWakeAttempt = time.Now()
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.wakeInFlight = false
			p.mu.Unlock()
		}()

		if !isLazyServer(serverID) {
			return
		}
		if err := wakeServer(p.ctx, serverID); err != nil {
			p.logger.Error("Failed to wake lazy server %s from Bedrock connection: %v", serverID, err)
		}
	}()
}

func isRakNetConnectionTraffic(packet []byte) bool {
	if len(packet) == 0 {
		return false
	}

	packetID := packet[0]
	return packetID == 0x05 || packetID == 0x07 || packetID&0xf0 == 0x80
}

// getOrCreateSession gets an existing session or creates a new one
func (p *UDPProxy) getOrCreateSession(clientAddr *net.UDPAddr) (*udpSession, error) {
	clientKey := clientAddr.String()

	p.sessionsMu.RLock()
	session, exists := p.sessions[clientKey]
	p.sessionsMu.RUnlock()

	if exists {
		return session, nil
	}

	// Create new session
	p.mu.RLock()
	backendHost := p.backendHost
	backendPort := p.backendPort
	p.mu.RUnlock()

	if backendHost == "" || backendPort == 0 {
		return nil, fmt.Errorf("no backend configured")
	}

	backendAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", backendHost, backendPort))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve backend: %w", err)
	}

	// Use unconnected socket to debug response routing
	backendConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend socket: %w", err)
	}

	session = &udpSession{
		clientAddr:  clientAddr,
		backendConn: backendConn,
		backendAddr: backendAddr,
		lastActive:  time.Now(),
	}

	p.sessionsMu.Lock()
	p.sessions[clientKey] = session
	p.sessionsMu.Unlock()

	// Start goroutine to handle responses from backend
	go p.handleBackendResponses(session, clientKey)

	p.logger.Debug("UDP session created: %s -> %s:%d", clientKey, backendHost, backendPort)
	return session, nil
}

// handleBackendResponses forwards responses from backend back to client
func (p *UDPProxy) handleBackendResponses(session *udpSession, clientKey string) {
	buf := make([]byte, 65535)
	p.logger.Debug("UDP response handler started for %s", clientKey)

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}

		session.backendConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, _, err := session.backendConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if session is stale
				if time.Since(session.lastActive) > 5*time.Minute {
					p.removeSession(clientKey)
					return
				}
				continue
			}
			select {
			case <-p.ctx.Done():
				return
			default:
				p.logger.Error("Backend read error for %s: %v", clientKey, err)
				p.removeSession(clientKey)
				return
			}
		}

		// Forward response to client
		_, err = p.conn.WriteToUDP(buf[:n], session.clientAddr)
		if err != nil {
			p.logger.Error("Failed to send to client %s: %v", clientKey, err)
		}

		session.lastActive = time.Now()
	}
}

// removeSession removes and cleans up a session
func (p *UDPProxy) removeSession(clientKey string) {
	p.sessionsMu.Lock()
	defer p.sessionsMu.Unlock()

	if session, exists := p.sessions[clientKey]; exists {
		session.backendConn.Close()
		delete(p.sessions, clientKey)
		p.logger.Debug("Removed UDP session for %s", clientKey)
	}
}

// cleanupLoop removes stale sessions
func (p *UDPProxy) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.sessionsMu.Lock()
			now := time.Now()
			for key, session := range p.sessions {
				if now.Sub(session.lastActive) > 5*time.Minute {
					session.backendConn.Close()
					delete(p.sessions, key)
					p.logger.Debug("Cleaned up stale UDP session for %s", key)
				}
			}
			p.sessionsMu.Unlock()
		}
	}
}

// IsRunning returns whether the proxy is running
func (p *UDPProxy) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}
