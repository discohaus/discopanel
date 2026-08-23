package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/discopanel/pkg/logger"
)

// MinecraftProxy handles Minecraft protocol proxying with handshake parsing for hostname-based routing
type MinecraftProxy struct {
	listener            net.Listener
	routes              map[string]*Route
	routesMutex         sync.RWMutex
	logger              *logger.Logger
	listenAddr          string
	running             bool
	runningMutex        sync.RWMutex
	ctx                 context.Context
	cancel              context.CancelFunc
	wakeServer          func(context.Context, string) error
	getLazyServerConfig func(context.Context, string) LazyServerConfig
}

// NewMinecraftProxy creates a new Minecraft proxy instance
func NewMinecraftProxy(cfg *Config) *MinecraftProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &MinecraftProxy{
		routes:              make(map[string]*Route),
		logger:              cfg.Logger,
		listenAddr:          cfg.ListenAddr,
		ctx:                 ctx,
		cancel:              cancel,
		wakeServer:          cfg.WakeServer,
		getLazyServerConfig: cfg.GetLazyServerConfig,
	}
}

// AddRoute adds a new routing rule
func (p *MinecraftProxy) AddRoute(serverID, hostname, backendHost string, backendPort int) {
	p.routesMutex.Lock()
	defer p.routesMutex.Unlock()

	hostname = strings.ToLower(strings.Split(hostname, ":")[0])

	p.routes[hostname] = &Route{
		ServerID:    serverID,
		Hostname:    hostname,
		BackendHost: backendHost,
		BackendPort: backendPort,
		Active:      true,
	}

	p.logger.Info("Added route: hostname=%s backend=%s:%d", hostname, backendHost, backendPort)
}

// RemoveRoute removes a routing rule
func (p *MinecraftProxy) RemoveRoute(hostname string) {
	p.routesMutex.Lock()
	defer p.routesMutex.Unlock()

	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	delete(p.routes, hostname)

	p.logger.Info("Removed route: hostname=%s", hostname)
}

// UpdateRoute updates the backend for a route
func (p *MinecraftProxy) UpdateRoute(hostname, backendHost string, backendPort int) {
	p.routesMutex.Lock()
	defer p.routesMutex.Unlock()

	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	if route, exists := p.routes[hostname]; exists {
		route.BackendHost = backendHost
		route.BackendPort = backendPort
		p.logger.Info("Updated route: hostname=%s backend=%s:%d", hostname, backendHost, backendPort)
	}
}

// SetRouteActive enables or disables a route
func (p *MinecraftProxy) SetRouteActive(hostname string, active bool) {
	p.routesMutex.Lock()
	defer p.routesMutex.Unlock()

	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	if route, exists := p.routes[hostname]; exists {
		route.Active = active
		p.logger.Info("Set route active: hostname=%s active=%v", hostname, active)
	}
}

// Start starts the proxy server
func (p *MinecraftProxy) Start() error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

	if p.running {
		return fmt.Errorf("proxy already running")
	}

	listener, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
	}

	p.listener = listener
	p.running = true

	go p.acceptLoop()

	p.logger.Info("Minecraft proxy started on %s", p.listenAddr)
	return nil
}

// Stop stops the proxy server
func (p *MinecraftProxy) Stop() error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

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

// acceptLoop accepts incoming connections
func (p *MinecraftProxy) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return
			default:
				p.logger.Error("Failed to accept connection: %v", err)
				continue
			}
		}

		go p.handleConnection(conn)
	}
}

// handleConnection handles a single client connection with Minecraft protocol parsing
func (p *MinecraftProxy) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	p.logger.Debug("Attempting to route incoming Minecraft connection!")

	// Set initial timeout for handshake
	clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Read the handshake packet
	handshake, err := ReadHandshakePacket(clientConn)
	if err != nil {
		p.logger.Debug("Failed to read handshake from %s: %v", clientConn.RemoteAddr(), err)
		return
	}

	// Extract hostname from the handshake
	p.logger.Debug("Extracting hostname from: %s", handshake.ServerAddress)
	hostname := strings.ToLower(strings.Split(handshake.ServerAddress, ":")[0])
	if idx := strings.IndexByte(hostname, 0); idx != -1 {
		hostname = hostname[:idx]
		p.logger.Debug("Null byte(s) detected, trimmed suffix null termination: %s", hostname)
	}

	// Copy the route while holding the lock so backend updates can happen
	// concurrently without changing this connection's routing decision.
	p.routesMutex.RLock()
	routePtr, exists := p.routes[hostname]
	var route Route
	if exists {
		route = *routePtr
	}
	p.routesMutex.RUnlock()

	if !exists || !route.Active {
		p.logger.Debug("No active route found for hostname: %s", hostname)
		p.routesMutex.RLock()
		p.logger.Debug("Available routes:")
		for r := range p.routes {
			p.logger.Debug("%s", r)
		}
		p.routesMutex.RUnlock()
		return
	}

	// Sleeping lazy servers keep their route but have no backend until a login
	// attempt wakes the container. Server list pings must not trigger a wake-up.
	if route.BackendHost == "" {
		lazyConfig := p.lazyServerConfig(route.ServerID)
		if !lazyConfig.Enabled {
			return
		}

		switch handshake.NextState {
		case 1:
			if err := p.serveSleepingStatus(clientConn, handshake, lazyConfig.MOTD); err != nil {
				p.logger.Debug("Failed to serve sleeping status for %s: %v", route.ServerID, err)
			}
		case 2:
			p.wakeOnLogin(clientConn, &route, lazyConfig.StartingMessage)
		}
		return
	}

	// Connect to backend
	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	backendConn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
	if err != nil {
		lazyConfig := p.lazyServerConfig(route.ServerID)
		if lazyConfig.Enabled {
			p.logger.Debug("Backend %s unavailable for lazy server %s: %v", backendAddr, route.ServerID, err)
			switch handshake.NextState {
			case 1:
				if err := p.serveSleepingStatus(clientConn, handshake, lazyConfig.MOTD); err != nil {
					p.logger.Debug("Failed to serve sleeping status for %s: %v", route.ServerID, err)
				}
			case 2:
				p.wakeOnLogin(clientConn, &route, lazyConfig.StartingMessage)
			}
			return
		}

		p.logger.Error("Failed to connect to backend %s: %v", backendAddr, err)
		return
	}
	defer backendConn.Close()

	// Modify handshake packet to use backend's expected hostname
	// For Forge servers, we need to preserve any FML data in the address field
	addressParts := strings.Split(handshake.ServerAddress, "\x00")
	if len(addressParts) > 1 {
		// Forge client detected - preserve all FML protocol data
		originalHost := addressParts[0]
		addressParts[0] = "localhost"

		if len(addressParts) >= 2 {
			fmlVersion := addressParts[1]
			p.logger.Debug("Forge handshake detected - FML version: %s, original host: %s", fmlVersion, originalHost)

			if len(addressParts) > 2 {
				p.logger.Debug("Additional FML data segments: %d", len(addressParts)-2)
			}
		}

		handshake.ServerAddress = strings.Join(addressParts, "\x00")
	} else {
		handshake.ServerAddress = "localhost"
	}
	handshake.ServerPort = uint16(route.BackendPort)

	// Forward the modified handshake to the backend
	if err := WriteHandshakePacket(backendConn, handshake); err != nil {
		p.logger.Error("Failed to write handshake to backend: %v", err)
		return
	}

	// Clear timeouts for proxying
	clientConn.SetReadDeadline(time.Time{})
	backendConn.SetReadDeadline(time.Time{})

	// Start bidirectional proxying
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(backendConn, clientConn)
		backendConn.Close()
	}()

	go func() {
		defer wg.Done()
		io.Copy(clientConn, backendConn)
		clientConn.Close()
	}()

	wg.Wait()
}

func (p *MinecraftProxy) lazyServerConfig(serverID string) LazyServerConfig {
	lazyConfig := LazyServerConfig{}
	if p.getLazyServerConfig != nil {
		lazyConfig = p.getLazyServerConfig(p.ctx, serverID)
	}
	if lazyConfig.MOTD == "" {
		lazyConfig.MOTD = defaultLazyServerMOTD
	}
	if lazyConfig.StartingMessage == "" {
		lazyConfig.StartingMessage = defaultLazyServerStartingMessage
	}
	return lazyConfig
}

func (p *MinecraftProxy) wakeOnLogin(clientConn net.Conn, route *Route, startingMessage string) {
	if p.wakeServer == nil {
		return
	}

	if err := p.wakeServer(p.ctx, route.ServerID); err != nil {
		p.logger.Error("Failed to wake server %s: %v", route.ServerID, err)
		if err := writeLoginDisconnect(clientConn, "Unable to start the server. Please try again later."); err != nil {
			p.logger.Debug("Failed to send wake error to client: %v", err)
		}
		return
	}

	p.logger.Info("Wake requested for lazy server %s", route.ServerID)
	if err := writeLoginDisconnect(clientConn, startingMessage); err != nil {
		p.logger.Debug("Failed to send starting message to client: %v", err)
	}
}

func (p *MinecraftProxy) serveSleepingStatus(clientConn net.Conn, handshake *HandshakePacket, motd string) error {
	packetID, payload, err := readPacket(clientConn)
	if err != nil {
		return err
	}
	if packetID != 0 || len(payload) != 0 {
		return fmt.Errorf("expected status request packet, got id=%d payload=%d bytes", packetID, len(payload))
	}

	status := struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int32  `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
		} `json:"players"`
		Description struct {
			Text  string `json:"text"`
			Color string `json:"color"`
		} `json:"description"`
	}{}
	status.Version.Name = "Sleeping"
	status.Version.Protocol = int32(handshake.ProtocolVersion)
	status.Description.Text = motd
	status.Description.Color = "gray"

	statusJSON, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("failed to encode sleeping status: %w", err)
	}

	var response bytes.Buffer
	if err := writeString(&response, string(statusJSON)); err != nil {
		return err
	}
	if err := writePacket(clientConn, 0, response.Bytes()); err != nil {
		return fmt.Errorf("failed to write sleeping status: %w", err)
	}

	packetID, payload, err = readPacket(clientConn)
	if err != nil {
		return err
	}
	if packetID != 1 || len(payload) != 8 {
		return fmt.Errorf("expected status ping packet, got id=%d payload=%d bytes", packetID, len(payload))
	}

	if err := writePacket(clientConn, 1, payload); err != nil {
		return fmt.Errorf("failed to write status pong: %w", err)
	}
	return nil
}

func writeLoginDisconnect(w io.Writer, message string) error {
	reason, err := json.Marshal(struct {
		Text  string `json:"text"`
		Color string `json:"color"`
	}{
		Text:  message,
		Color: "yellow",
	})
	if err != nil {
		return err
	}

	var payload bytes.Buffer
	if err := writeString(&payload, string(reason)); err != nil {
		return err
	}
	return writePacket(w, 0, payload.Bytes())
}

// GetRoutes returns a copy of all current routes
func (p *MinecraftProxy) GetRoutes() map[string]*Route {
	p.routesMutex.RLock()
	defer p.routesMutex.RUnlock()

	routes := make(map[string]*Route)
	for k, v := range p.routes {
		routeCopy := *v
		routes[k] = &routeCopy
	}
	return routes
}

// IsRunning returns whether the proxy is running
func (p *MinecraftProxy) IsRunning() bool {
	p.runningMutex.RLock()
	defer p.runningMutex.RUnlock()
	return p.running
}
