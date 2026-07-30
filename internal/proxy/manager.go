package proxy

import (
	"context"
	"fmt"
	"sync"
	"time"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Handles proxy lifecycle and manages routes
type Manager struct {
	tcpSockets  map[int]*ListenerSocket
	udpSockets  map[int]*UDPProxy
	listenerIDs map[int]string
	statsBase   map[string]*v1.ProxyRoute
	statsLast   map[string]*v1.ProxyRoute
	store       *db.Store
	docker      *docker.Client
	config      *config.ProxyConfig
	appCfg      *config.Config
	logger      *logger.Logger
	mu          sync.Mutex
	gate        ServerGate

	// Runtime toggle state owned by the manager
	enabled bool
	baseURL string

	// Cached outbound address for instant domains
	detectedIP string
	detectedAt time.Time

	// Cached router address from internet echo services
	publicIP    string
	publicAt    time.Time
	publicTried time.Time
	refreshOnce sync.Once

	// Granted checkouts awaiting their callers' persists
	pendingClaims map[uint64]pendingClaim
	claimSeq      uint64
}

// Registers the wake gate, must be called before Start
func (m *Manager) SetServerGate(gate ServerGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gate = gate
	for _, sock := range m.tcpSockets {
		sock.SetGate(gate)
	}
}

// Creates a new proxy manager
func NewManager(store *db.Store, dockerClient *docker.Client, cfg *config.Config, logger *logger.Logger) *Manager {
	return &Manager{
		tcpSockets:    make(map[int]*ListenerSocket),
		udpSockets:    make(map[int]*UDPProxy),
		listenerIDs:   make(map[int]string),
		statsBase:     make(map[string]*v1.ProxyRoute),
		statsLast:     make(map[string]*v1.ProxyRoute),
		store:         store,
		docker:        dockerClient,
		config:        &cfg.Proxy,
		appCfg:        cfg,
		logger:        logger,
		enabled:       cfg.Proxy.Enabled,
		baseURL:       cfg.Proxy.BaseUrl,
		pendingClaims: make(map[uint64]pendingClaim),
	}
}

// Reports the runtime proxy toggle
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// Reports the configured custom base domain
func (m *Manager) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// Applies a config change and reconciles running sockets
func (m *Manager) ApplyConfig(ctx context.Context, enabled bool, baseURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.enabled = enabled
	m.baseURL = baseURL

	if !enabled {
		return m.stopAllLocked()
	}
	return m.syncListenersLocked(ctx)
}

// Bounds docker inspects so a hung daemon cannot wedge syncs
const containerInspectTimeout = 5 * time.Second

// Resolves a container IP on the panel network
func (m *Manager) containerIP(ctx context.Context, containerID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	inspectCtx, cancel := context.WithTimeout(ctx, containerInspectTimeout)
	defer cancel()
	return m.docker.ContainerIP(inspectCtx, containerID)
}

// Initializes and starts the proxy if enabled
func (m *Manager) Start() error {
	// Address cache warms even while the proxy is off
	m.startAddressRefresh()

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		m.logger.Info("Proxy is disabled in configuration")
		return nil
	}

	if err := m.syncListenersLocked(context.Background()); err != nil {
		return err
	}
	m.logger.Info("Proxy manager started")
	return nil
}

// Reconciles sockets and routes against database state
func (m *Manager) SyncListeners(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncListenersLocked(ctx)
}

// Full reconcile pass, caller must hold the lock
func (m *Manager) syncListenersLocked(ctx context.Context) error {
	if !m.enabled {
		return nil
	}

	if err := m.ensureListenerInvariantsLocked(ctx); err != nil {
		m.logger.Error("Failed to reconcile listener rows: %v", err)
	}

	listeners, err := m.store.ListProxyListeners(ctx)
	if err != nil {
		return fmt.Errorf("failed to load proxy listeners: %w", err)
	}

	desired := make(map[int]*v1.ProxyListener, len(listeners))
	byID := make(map[string]*v1.ProxyListener, len(listeners))
	for _, l := range listeners {
		if l.Enabled {
			desired[int(l.Port)] = l
			byID[l.Id] = l
		}
	}

	// Sockets for removed or disabled listeners stop first
	for port, sock := range m.tcpSockets {
		if desired[port] != nil {
			continue
		}
		if err := sock.Stop(); err != nil {
			m.logger.Error("Failed to stop listener socket on port %d: %v", port, err)
		}
		delete(m.tcpSockets, port)
		delete(m.listenerIDs, port)
		m.logger.Info("Stopped listener socket on port %d", port)
	}
	for port, up := range m.udpSockets {
		if desired[port] != nil {
			continue
		}
		up.Stop()
		delete(m.udpSockets, port)
	}

	// Missing sockets start, running ones stay untouched
	for port, listener := range desired {
		m.listenerIDs[port] = listener.Id
		if _, ok := m.tcpSockets[port]; ok {
			continue
		}
		sock := NewListenerSocket(&Config{
			ListenAddr: fmt.Sprintf(":%d", port),
			Logger:     m.logger,
			Gate:       m.gate,
		})
		if err := sock.Start(); err != nil {
			m.logger.Error("Failed to start listener %s on port %d: %v", listener.Name, port, err)
			continue
		}
		m.tcpSockets[port] = sock
		m.logger.Info("Started listener %s on port %d", listener.Name, port)
	}

	tcpRoutes, udpRoutes := m.desiredRoutesLocked(ctx, byID)

	// Route tables replace wholesale, stale entries die here
	for port, sock := range m.tcpSockets {
		sock.SetRoutes(tcpRoutes[port])
	}

	// UDP relay sockets follow their routes
	for port, route := range udpRoutes {
		if desired[port] == nil {
			continue
		}
		up, ok := m.udpSockets[port]
		if !ok {
			up = NewUDPProxy(&Config{ListenAddr: fmt.Sprintf(":%d", port), Logger: m.logger})
			if err := up.Start(); err != nil {
				m.logger.Error("Failed to start udp relay on port %d: %v", port, err)
				continue
			}
			m.udpSockets[port] = up
		}
		up.SetRoute(route)
	}
	for port, up := range m.udpSockets {
		if _, ok := udpRoutes[port]; ok && desired[port] != nil {
			continue
		}
		up.Stop()
		delete(m.udpSockets, port)
	}

	return nil
}

// Desired route tables derived from rows and containers
func (m *Manager) desiredRoutesLocked(ctx context.Context, listenersByID map[string]*v1.ProxyListener) (map[int][]Route, map[int]Route) {
	tcpRoutes := make(map[int][]Route)
	udpRoutes := make(map[int]Route)

	servers, err := m.store.ListServers(ctx)
	if err != nil {
		m.logger.Error("Failed to load servers for route sync: %v", err)
		return tcpRoutes, udpRoutes
	}
	serversByID := make(map[string]*v1.Server, len(servers))
	for _, server := range servers {
		serversByID[server.Id] = server
	}

	for _, server := range servers {
		// Game routes register even for stopped wakeable servers
		if server.ProxyHostname != "" {
			if listener := listenersByID[server.ProxyListenerId]; listener != nil {
				route, want, err := m.desiredRoute(ctx, server, server.ProxyHostname)
				if err != nil {
					m.logger.Error("Failed to build route for server %s: %v", server.Name, err)
				} else if want {
					port := int(listener.Port)
					tcpRoutes[port] = append(tcpRoutes[port], route)
				}
			}
		}

		// Extra proxied ports need a live container backend
		if !HasProxyPorts(server.AdditionalPorts) || server.ContainerId == "" {
			continue
		}
		switch server.Status {
		case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_PAUSED, v1.ServerStatus_SERVER_STATUS_UNHEALTHY:
		default:
			continue
		}
		ip, err := m.containerIP(ctx, server.ContainerId)
		if err != nil {
			m.logger.Debug("No container IP for server %s: %v", server.Name, err)
			continue
		}
		appendPortRoutes(tcpRoutes, udpRoutes, server.AdditionalPorts, server.ProxyHostname,
			OwnerServer, server.Id, ip, func(p *v1.NetworkPort) int { return int(p.ContainerPort) })
	}

	modules, err := m.store.ListModules(ctx)
	if err != nil {
		m.logger.Error("Failed to load modules for route sync: %v", err)
		return tcpRoutes, udpRoutes
	}
	for _, mod := range modules {
		if mod.ContainerId == "" || mod.Status != v1.ModuleStatus_MODULE_STATUS_RUNNING {
			continue
		}
		if !HasProxyPorts(mod.Ports) {
			continue
		}
		ip, err := m.containerIP(ctx, mod.ContainerId)
		if err != nil {
			m.logger.Debug("No container IP for module %s: %v", mod.Name, err)
			continue
		}
		hostname := ""
		if srv := serversByID[mod.ServerId]; srv != nil {
			hostname = srv.ProxyHostname
		}
		module := mod
		appendPortRoutes(tcpRoutes, udpRoutes, mod.Ports, hostname,
			OwnerModule, mod.Id, ip, func(p *v1.NetworkPort) int { return m.moduleContainerPort(module, p) })
	}

	return tcpRoutes, udpRoutes
}

// Adds one port list's routes onto the desired tables
func appendPortRoutes(tcpRoutes map[int][]Route, udpRoutes map[int]Route, ports []*v1.NetworkPort, fallbackHostname, ownerKind, ownerID, backendHost string, containerPort func(*v1.NetworkPort) int) {
	for _, port := range ports {
		if port == nil || !port.ProxyEnabled || port.HostPort <= 0 {
			continue
		}
		backendPort := containerPort(port)
		if backendPort == 0 {
			continue
		}
		route := Route{
			ServerID:    fmt.Sprintf("%s-port-%d", ownerID, port.HostPort),
			OwnerKind:   ownerKind,
			OwnerID:     ownerID,
			PortName:    port.Name,
			BackendHost: backendHost,
			BackendPort: backendPort,
			Protocol:    port.Protocol,
		}
		hostPort := int(port.HostPort)
		switch port.Protocol {
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
			hostname := NormalizeHostname(port.Hostname)
			if hostname == "" {
				hostname = fallbackHostname
			}
			// Handshake routing cannot match without a hostname
			if hostname == "" && port.Protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT {
				continue
			}
			route.Hostname = hostname
			tcpRoutes[hostPort] = append(tcpRoutes[hostPort], route)
		case v1.ModuleProtocol_MODULE_PROTOCOL_UDP:
			udpRoutes[hostPort] = route
		default:
			route.Protocol = v1.ModuleProtocol_MODULE_PROTOCOL_TCP
			tcpRoutes[hostPort] = append(tcpRoutes[hostPort], route)
		}
	}
}

// True when any port wants proxy routing
func HasProxyPorts(ports []*v1.NetworkPort) bool {
	for _, port := range ports {
		if port != nil && port.ProxyEnabled && port.HostPort != 0 {
			return true
		}
	}
	return false
}

// Keeps listener rows matching demand and default rules
func (m *Manager) ensureListenerInvariantsLocked(ctx context.Context) error {
	// Routed and relay demand keyed by port
	all, err := m.reservationsLocked(ctx)
	if err != nil {
		return err
	}
	demand := make(map[int]bool)
	for _, r := range all {
		if r.Kind == kindRouted || r.Kind == kindRelay {
			demand[r.Port] = true
		}
	}
	// Unsettled checkouts count as demand too
	m.sweepClaimsLocked()
	for _, claim := range m.pendingClaims {
		for _, r := range claim.held {
			if r.Kind == kindRouted || r.Kind == kindRelay {
				demand[r.Port] = true
			}
		}
	}

	listeners, err := m.store.ListProxyListeners(ctx)
	if err != nil {
		return err
	}

	// First run bootstraps the primary listener
	if len(listeners) == 0 {
		port, err := m.findFreePortLocked(ctx, FreePortOpts{
			Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_TCP,
			Start:    m.config.PortRangeMin,
			End:      65535,
		})
		if err != nil {
			return fmt.Errorf("failed to find a port for the default listener: %w", err)
		}
		listener := &v1.ProxyListener{
			Id:        "default",
			Port:      int32(port),
			Name:      "Primary",
			IsDefault: true,
			Enabled:   true,
		}
		if err := m.store.CreateProxyListener(ctx, listener); err != nil {
			return fmt.Errorf("failed to create default listener: %w", err)
		}
		m.logger.Info("Created default proxy listener on port %d", port)
		listeners = []*v1.ProxyListener{listener}
	}

	servers, err := m.store.ListServers(ctx)
	if err != nil {
		return err
	}
	referenced := make(map[string]bool)
	for _, server := range servers {
		if server.ProxyListenerId != "" {
			referenced[server.ProxyListenerId] = true
		}
	}

	// Idle auto rows leave, demand recreates them later
	kept := listeners[:0]
	for _, l := range listeners {
		if l.AutoCreated && !l.IsDefault && !demand[int(l.Port)] && !referenced[l.Id] {
			if err := m.store.DeleteProxyListener(ctx, l.Id); err == nil {
				m.logger.Info("Removed idle auto listener on port %d", l.Port)
				continue
			}
		}
		kept = append(kept, l)
	}
	listeners = kept

	// Missing rows appear for routed and relay ports
	have := make(map[int]bool, len(listeners))
	for _, l := range listeners {
		have[int(l.Port)] = true
	}
	for port := range demand {
		if have[port] {
			continue
		}
		listener, err := m.createListenerRowLocked(ctx, port)
		if err != nil {
			m.logger.Error("Failed to auto create listener for port %d: %v", port, err)
			continue
		}
		listeners = append(listeners, listener)
	}

	// Exactly one default listener at all times
	var defaults []*v1.ProxyListener
	for _, l := range listeners {
		if l.IsDefault {
			defaults = append(defaults, l)
		}
	}
	if len(defaults) == 0 && len(listeners) > 0 {
		promote := listeners[0]
		for _, l := range listeners {
			if !l.AutoCreated {
				promote = l
				break
			}
		}
		promote.IsDefault = true
		if err := m.store.UpdateProxyListener(ctx, promote); err == nil {
			m.logger.Info("Promoted listener %s to default", promote.Name)
		}
	} else if len(defaults) > 1 {
		for _, l := range defaults[1:] {
			l.IsDefault = false
			if err := m.store.UpdateProxyListener(ctx, l); err == nil {
				m.logger.Info("Demoted extra default listener %s", l.Name)
			}
		}
	}

	return nil
}

// Stops all proxy instances
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopAllLocked()
}

// Stops every socket, caller must hold the lock
func (m *Manager) stopAllLocked() error {
	if len(m.tcpSockets) == 0 && len(m.udpSockets) == 0 {
		return nil
	}

	var lastErr error
	for port, sock := range m.tcpSockets {
		if err := sock.Stop(); err != nil {
			lastErr = fmt.Errorf("failed to stop listener on port %d: %w", port, err)
			m.logger.Error("Failed to stop listener on port %d: %v", port, err)
		}
	}
	for _, up := range m.udpSockets {
		up.Stop()
	}

	m.tcpSockets = make(map[int]*ListenerSocket)
	m.udpSockets = make(map[int]*UDPProxy)
	m.listenerIDs = make(map[int]string)
	m.logger.Info("Proxy manager stopped")
	return lastErr
}

// Reconciles a server's game route with its current status
func (m *Manager) UpdateServerRoute(server *v1.Server) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled || server.ProxyHostname == "" || server.ProxyListenerId == "" {
		return nil
	}

	ctx := context.Background()
	listener, err := m.store.GetProxyListener(ctx, server.ProxyListenerId)
	if err != nil {
		return fmt.Errorf("failed to get proxy listener: %w", err)
	}
	if !listener.Enabled {
		return nil
	}

	sock, ok := m.tcpSockets[int(listener.Port)]
	if !ok {
		return fmt.Errorf("no listener socket for port %d", listener.Port)
	}

	route, want, err := m.desiredRoute(ctx, server, server.ProxyHostname)
	if err != nil {
		return err
	}
	if !want {
		sock.RemoveRoute(v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT, server.ProxyHostname)
		return nil
	}
	sock.UpsertServerRoute(route)
	return nil
}

// Reconciles every route a server owns after status changes
func (m *Manager) SyncServerRoutes(ctx context.Context, server *v1.Server) error {
	var firstErr error
	if server.ProxyHostname != "" {
		firstErr = m.UpdateServerRoute(server)
	}
	// Extra proxied ports only reconcile in a full pass
	if HasProxyPorts(server.AdditionalPorts) {
		if err := m.SyncListeners(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Derives the route a server should serve right now
func (m *Manager) desiredRoute(ctx context.Context, server *v1.Server, hostname string) (route Route, want bool, err error) {
	cfg, cfgErr := m.store.GetServerProperties(ctx, server.Id)
	if cfgErr != nil {
		cfg = nil
	}

	route = Route{
		ServerID:      server.Id,
		OwnerKind:     OwnerServer,
		OwnerID:       server.Id,
		Hostname:      hostname,
		Protocol:      v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		BackendPort:   docker.DefaultMinecraftPort,
		ProxyProtocol: propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.EnableProxyProtocol }),
		PreserveHost:  propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.ProxyPreserveHostname }),
		MaxPlayers:    int(server.MaxPlayers),
	}
	wakeable := propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.EnableWakeOnConnect })

	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_PAUSED, v1.ServerStatus_SERVER_STATUS_UNHEALTHY:
		if server.ContainerId == "" {
			return Route{}, false, fmt.Errorf("server %s has no container", server.Name)
		}
		ip, ipErr := m.containerIP(ctx, server.ContainerId)
		if ipErr != nil {
			return Route{}, false, fmt.Errorf("failed to get container IP: %w", ipErr)
		}
		route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE
		route.BackendHost = ip
		return route, true, nil

	case v1.ServerStatus_SERVER_STATUS_PROVISIONING, v1.ServerStatus_SERVER_STATUS_CREATING, v1.ServerStatus_SERVER_STATUS_STARTING:
		route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING
		route.Motd = bootMOTD(server, cfg)
		if server.ContainerId != "" {
			if ip, ipErr := m.containerIP(ctx, server.ContainerId); ipErr == nil {
				route.BackendHost = ip
			}
		}
		return route, true, nil

	case v1.ServerStatus_SERVER_STATUS_STOPPED, v1.ServerStatus_SERVER_STATUS_STOPPING, v1.ServerStatus_SERVER_STATUS_ERROR:
		if !wakeable {
			return Route{}, false, nil
		}
		route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE
		route.Wakeable = true
		route.Motd = offlineMOTD(server, cfg)
		return route, true, nil

	default:
		return Route{}, false, nil
	}
}

// Reads an optional bool off possibly-nil properties
func propEnabled(cfg *v1.ServerProperties, field func(*v1.ServerProperties) *bool) bool {
	if cfg == nil {
		return false
	}
	v := field(cfg)
	return v != nil && *v
}

// Builds the joinable-while-stopped status line
func offlineMOTD(server *v1.Server, cfg *v1.ServerProperties) string {
	if cfg != nil && cfg.Motd != nil && *cfg.Motd != "" {
		return *cfg.Motd + " (offline - join to start it up)"
	}
	return server.Name + " is offline - join to start it up"
}

// Builds the status line shown while a server boots
func bootMOTD(server *v1.Server, cfg *v1.ServerProperties) string {
	phase := "starting up"
	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_PROVISIONING:
		phase = "installing server files"
	case v1.ServerStatus_SERVER_STATUS_CREATING:
		phase = "preparing the container"
	}
	if cfg != nil && cfg.Motd != nil && *cfg.Motd != "" {
		return fmt.Sprintf("%s (%s - join in a moment)", *cfg.Motd, phase)
	}
	return fmt.Sprintf("%s is %s - join in a moment", server.Name, phase)
}

// One live route with its socket attribution
type RouteEntry struct {
	Port       int
	ListenerID string
	Route      *Route
}

// Returns every live route across all sockets
func (m *Manager) RouteEntries() []RouteEntry {
	m.mu.Lock()
	defer m.mu.Unlock()

	var entries []RouteEntry
	for port, sock := range m.tcpSockets {
		listenerID := m.listenerIDs[port]
		for _, route := range sock.Routes() {
			r := route
			entries = append(entries, RouteEntry{Port: port, ListenerID: listenerID, Route: &r})
		}
	}
	for port, up := range m.udpSockets {
		if route, ok := up.Route(); ok {
			entries = append(entries, RouteEntry{Port: port, ListenerID: m.listenerIDs[port], Route: &route})
		}
	}
	return entries
}

// Returns whether any proxy is running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sock := range m.tcpSockets {
		if sock.IsRunning() {
			return true
		}
	}
	return false
}

// Resolves a container port from the template when unset
func (m *Manager) moduleContainerPort(module *v1.Module, port *v1.NetworkPort) int {
	if port.ContainerPort != 0 {
		return int(port.ContainerPort)
	}

	template, err := m.store.GetModuleTemplate(context.Background(), module.TemplateId)
	if err != nil {
		return 0
	}
	for _, tp := range template.Ports {
		if tp != nil && tp.Name == port.Name {
			return int(tp.ContainerPort)
		}
	}
	return 0
}

// Aggregates per-route counters from every listener socket
func (m *Manager) GetRouteStats() map[string]*v1.ProxyRoute {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := make(map[string]*v1.ProxyRoute)
	for _, sock := range m.tcpSockets {
		for id, raw := range sock.StatsSnapshots() {
			if countersReset(m.statsLast[id], raw) {
				m.statsBase[id] = addCounters(m.statsBase[id], m.statsLast[id])
			}
			m.statsLast[id] = raw
			stats[id] = addCounters(m.statsBase[id], raw)
		}
	}
	return stats
}

// Detects a counter restart after route removal
func countersReset(last, cur *v1.ProxyRoute) bool {
	if last == nil || cur == nil {
		return false
	}
	return cur.TotalConnections < last.TotalConnections ||
		cur.StatusPings < last.StatusPings ||
		cur.Logins < last.Logins ||
		cur.Wakes < last.Wakes ||
		cur.BytesToBackend < last.BytesToBackend ||
		cur.BytesToClient < last.BytesToClient
}

// Adds monotonic counters onto a base, gauges pass through
func addCounters(base, cur *v1.ProxyRoute) *v1.ProxyRoute {
	if base == nil {
		base = &v1.ProxyRoute{}
	}
	if cur == nil {
		cur = &v1.ProxyRoute{}
	}
	return &v1.ProxyRoute{
		ActiveConnections:   cur.ActiveConnections,
		TotalConnections:    base.TotalConnections + cur.TotalConnections,
		StatusPings:         base.StatusPings + cur.StatusPings,
		Logins:              base.Logins + cur.Logins,
		Wakes:               base.Wakes + cur.Wakes,
		BytesToBackend:      base.BytesToBackend + cur.BytesToBackend,
		BytesToClient:       base.BytesToClient + cur.BytesToClient,
		LastProtocolVersion: cur.LastProtocolVersion,
	}
}
