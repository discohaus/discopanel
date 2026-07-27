package proxy

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Handles proxy lifecycle and manages routes
type Manager struct {
	proxies       map[int]Proxier // Map of port -> Proxy instance (TCP or UDP)
	listenerPorts map[int]bool    // Ports serving hostname-routed server listeners
	statsBase     map[string]*v1.ProxyRoute
	statsLast     map[string]*v1.ProxyRoute
	store         *db.Store
	docker        *docker.Client
	config        *config.ProxyConfig
	logger        *logger.Logger
	mu            sync.Mutex
	gate          ServerGate
}

// Registers the wake gate, must be called before Start
func (m *Manager) SetServerGate(gate ServerGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gate = gate
	for _, p := range m.proxies {
		if mp, ok := p.(*MinecraftProxy); ok {
			mp.SetGate(gate)
		}
	}
}

// Creates a new proxy manager
func NewManager(store *db.Store, dockerClient *docker.Client, cfg *config.Config, logger *logger.Logger) *Manager {
	return &Manager{
		proxies:       make(map[int]Proxier),
		listenerPorts: make(map[int]bool),
		statsBase:     make(map[string]*v1.ProxyRoute),
		statsLast:     make(map[string]*v1.ProxyRoute),
		store:         store,
		docker:        dockerClient,
		config:        &cfg.Proxy,
		logger:        logger,
	}
}

// Resolves a container IP on the panel network
func (m *Manager) containerIP(containerID string) (string, error) {
	return m.docker.ContainerIP(context.Background(), containerID)
}

// Initializes and starts the proxy if enabled
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		m.logger.Info("Proxy is disabled in configuration")
		return nil
	}

	// Ensure a default listener exists when proxy is enabled
	if _, err := m.ensureDefaultListenerLocked(); err != nil {
		m.logger.Error("Failed to ensure default listener: %v", err)
	}

	// Get all proxy listeners from database
	listeners, err := m.store.ListProxyListeners(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load proxy listeners: %w", err)
	}

	// Create a proxy instance for each enabled listener
	for _, listener := range listeners {
		if !listener.Enabled {
			continue
		}

		listenAddr := fmt.Sprintf(":%d", listener.Port)
		proxy := NewMinecraftProxy(&Config{
			ListenAddr: listenAddr,
			Logger:     m.logger,
			Gate:       m.gate,
		})

		m.proxies[int(listener.Port)] = proxy
		m.listenerPorts[int(listener.Port)] = true
		m.logger.Info("Created Minecraft proxy for listener %s on port %d", listener.Name, listener.Port)
	}

	// Load existing server routes
	servers, err := m.store.ListServers(context.Background())
	if err != nil {
		return fmt.Errorf("failed to load servers: %w", err)
	}

	// Map to track which listener each server uses
	listenerMap := make(map[string]*v1.ProxyListener)
	for _, listener := range listeners {
		listenerMap[listener.Id] = listener
	}

	for _, server := range servers {
		// Registers routes even for stopped wakeable servers
		if server.ProxyHostname == "" || server.ProxyListenerId == "" {
			continue
		}

		listener, ok := listenerMap[server.ProxyListenerId]
		if !ok || !listener.Enabled {
			m.logger.Error("Server %s has invalid or disabled listener %s", server.Name, server.ProxyListenerId)
			continue
		}

		mp, ok := m.proxies[int(listener.Port)].(*MinecraftProxy)
		if !ok {
			m.logger.Error("No proxy instance for port %d", listener.Port)
			continue
		}

		route, want, err := m.desiredRoute(server, m.generateHostname(server))
		if err != nil {
			m.logger.Error("Failed to build route for server %s: %v", server.Name, err)
			continue
		}
		if want {
			mp.UpsertServerRoute(route)
		}
	}

	// Start all proxy instances
	for port, proxy := range m.proxies {
		if err := proxy.Start(); err != nil {
			return fmt.Errorf("failed to start proxy on port %d: %w", port, err)
		}
	}

	m.logger.Info("Proxy manager started")
	return nil
}

// Stops all proxy instances
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.proxies) == 0 {
		return nil
	}

	var lastErr error
	for port, proxy := range m.proxies {
		if err := proxy.Stop(); err != nil {
			lastErr = fmt.Errorf("failed to stop proxy on port %d: %w", port, err)
			m.logger.Error("Failed to stop proxy on port %d: %v", port, err)
		}
	}

	m.proxies = make(map[int]Proxier)
	m.listenerPorts = make(map[int]bool)
	m.logger.Info("Proxy manager stopped")
	return lastErr
}

// Reconciles a server route with its current status
func (m *Manager) UpdateServerRoute(server *v1.Server) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.proxies) == 0 || !m.config.Enabled {
		return nil
	}

	// Get the listener for this server
	if server.ProxyListenerId == "" {
		return nil // No listener assigned
	}

	listener, err := m.store.GetProxyListener(context.Background(), server.ProxyListenerId)
	if err != nil {
		return fmt.Errorf("failed to get proxy listener: %w", err)
	}

	if !listener.Enabled {
		return nil // Listener is disabled
	}

	// Get the proxy instance for this listener's port
	mp, ok := m.proxies[int(listener.Port)].(*MinecraftProxy)
	if !ok {
		return fmt.Errorf("no proxy instance for port %d", listener.Port)
	}

	hostname := m.generateHostname(server)
	route, want, err := m.desiredRoute(server, hostname)
	if err != nil {
		return err
	}
	if !want {
		mp.RemoveRoute(hostname)
		return nil
	}
	mp.UpsertServerRoute(route)
	return nil
}

// Derives the route a server should serve right now
func (m *Manager) desiredRoute(server *v1.Server, hostname string) (route Route, want bool, err error) {
	ctx := context.Background()
	cfg, cfgErr := m.store.GetServerProperties(ctx, server.Id)
	if cfgErr != nil {
		cfg = nil
	}

	route = Route{
		ServerID:      server.Id,
		Hostname:      hostname,
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
		ip, ipErr := m.containerIP(server.ContainerId)
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
			if ip, ipErr := m.containerIP(server.ContainerId); ipErr == nil {
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

// Removes a route for a server
func (m *Manager) RemoveServerRoute(serverID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.proxies) == 0 || !m.config.Enabled {
		return nil
	}

	server, err := m.store.GetServer(context.Background(), serverID)
	if err != nil {
		return err
	}

	hostname := m.generateHostname(server)

	// Remove from all proxies since listener may have changed
	for _, proxy := range m.proxies {
		proxy.RemoveRoute(hostname)
	}

	return nil
}

// Removes a route using the hostname
func (m *Manager) RemoveRouteByHostname(hostname string, listenerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.proxies) == 0 || !m.config.Enabled || hostname == "" {
		return nil
	}

	// If listenerID provided, only remove from that specific listener's proxy
	if listenerID != "" {
		listener, err := m.store.GetProxyListener(context.Background(), listenerID)
		if err == nil && listener != nil {
			if proxy, ok := m.proxies[int(listener.Port)]; ok {
				proxy.RemoveRoute(hostname)
				m.logger.Info("Removed route %s from listener port %d", hostname, listener.Port)
				return nil
			}
		}
	}

	// Otherwise remove from all proxies
	for port, proxy := range m.proxies {
		proxy.RemoveRoute(hostname)
		m.logger.Debug("Removed route %s from port %d", hostname, port)
	}

	return nil
}

// Generates the hostname for a server
func (m *Manager) generateHostname(server *v1.Server) string {
	// Use custom hostname if set
	if server.ProxyHostname != "" {
		return server.ProxyHostname
	}

	// Otherwise use default pattern
	if m.config.BaseUrl != "" {
		// Use server name as subdomain
		return fmt.Sprintf("%s.%s", strings.ToLower(strings.ReplaceAll(server.Name, " ", "-")), m.config.BaseUrl)
	}
	// Fallback to using server ID
	return fmt.Sprintf("server-%s.minecraft.mc", server.Id)
}

// Returns all current routes from all proxies
func (m *Manager) GetRoutes() map[string]*Route {
	m.mu.Lock()
	defer m.mu.Unlock()

	allRoutes := make(map[string]*Route)
	for _, proxy := range m.proxies {
		maps.Copy(allRoutes, proxy.GetRoutes())
	}

	return allRoutes
}

// Returns whether any proxy is running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, proxy := range m.proxies {
		if proxy.IsRunning() {
			return true
		}
	}

	return false
}

// Creates and starts a proxy for a new listener
func (m *Manager) AddListener(listener *v1.ProxyListener) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled || !listener.Enabled {
		return nil
	}

	// Check if proxy already exists for this port
	if _, exists := m.proxies[int(listener.Port)]; exists {
		return fmt.Errorf("proxy already exists for port %d", listener.Port)
	}

	// Create new proxy instance
	listenAddr := fmt.Sprintf(":%d", listener.Port)
	proxy := NewMinecraftProxy(&Config{
		ListenAddr: listenAddr,
		Logger:     m.logger,
		Gate:       m.gate,
	})

	// Start the proxy
	if err := proxy.Start(); err != nil {
		return fmt.Errorf("failed to start proxy on port %d: %w", listener.Port, err)
	}

	m.proxies[int(listener.Port)] = proxy
	m.listenerPorts[int(listener.Port)] = true
	m.logger.Info("Added and started Minecraft proxy for listener %s on port %d", listener.Name, listener.Port)

	return nil
}

// Stops and removes a proxy instance for a listener
func (m *Manager) RemoveListener(port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	proxy, exists := m.proxies[port]
	if !exists {
		return nil // Already removed or doesn't exist
	}

	// Stop the proxy
	if err := proxy.Stop(); err != nil {
		m.logger.Error("Failed to stop proxy on port %d: %v", port, err)
	}

	delete(m.proxies, port)
	delete(m.listenerPorts, port)
	m.logger.Info("Removed proxy for port %d", port)

	return nil
}

// Adds proxy routes for a module's ports
func (m *Manager) AddModuleRoute(module *v1.Module, server *v1.Server) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled || !hasProxyPorts(module) {
		return nil
	}

	// Get the container IP first
	if module.ContainerId == "" {
		return fmt.Errorf("module has no container ID")
	}

	containerIP, err := m.containerIP(module.ContainerId)
	if err != nil {
		return fmt.Errorf("failed to get module container IP: %w", err)
	}

	// Hostless modules route every hostname on their port
	hostname := ""
	if server != nil {
		hostname = server.ProxyHostname
	}

	// Add routes for all proxy-enabled ports
	for _, port := range module.Ports {
		if port == nil || !port.ProxyEnabled || port.HostPort == 0 {
			continue
		}

		protocol := port.Protocol

		// Handshake routing cannot match without a hostname
		if hostname == "" && protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT {
			continue
		}

		routeID := fmt.Sprintf("%s-port-%d", module.Id, port.HostPort)
		if err := m.addPortRouteUnlocked(routeID, hostname, containerIP,
			int(port.HostPort), m.moduleContainerPort(module, port), protocol, module.Name, port.Name); err != nil {
			m.logger.Error("Failed to add port route for %s: %v", port.Name, err)
		}
	}

	return nil
}

// True when any port wants proxy routing
func hasProxyPorts(module *v1.Module) bool {
	for _, port := range module.Ports {
		if port != nil && port.ProxyEnabled && port.HostPort != 0 {
			return true
		}
	}
	return false
}

// Resolves a container port from the template when unset
func (m *Manager) moduleContainerPort(module *v1.Module, port *v1.ModulePort) int {
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

// Adds a single port route, caller must hold lock
func (m *Manager) addPortRouteUnlocked(routeID, hostname, containerIP string, hostPort, containerPort int, protocol v1.ModuleProtocol, moduleName, portName string) error {
	if containerPort == 0 {
		return fmt.Errorf("no container port declared for %s", portName)
	}

	// Check if a proxy already exists for this port
	proxy, exists := m.proxies[hostPort]
	if !exists {
		listenAddr := fmt.Sprintf(":%d", hostPort)
		cfg := &Config{
			ListenAddr: listenAddr,
			Logger:     m.logger,
		}

		// Create appropriate proxy type based on protocol
		switch protocol {
		case v1.ModuleProtocol_MODULE_PROTOCOL_UDP:
			proxy = NewUDPProxy(cfg)
			m.logger.Info("Created UDP proxy for port %d", hostPort)
		case v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
			proxy = NewMinecraftProxy(cfg)
			m.logger.Info("Created Minecraft proxy for port %d", hostPort)
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP:
			proxy = NewHTTPProxy(cfg)
			m.logger.Info("Created HTTP proxy for port %d", hostPort)
		default:
			// Raw TCP forwarding covers tcp and unspecified
			proxy = NewTCPProxy(cfg)
			m.logger.Info("Created TCP proxy for port %d", hostPort)
		}

		// Start the proxy
		if err := proxy.Start(); err != nil {
			return fmt.Errorf("failed to start module proxy on port %d: %w", hostPort, err)
		}

		m.proxies[hostPort] = proxy
	}

	// Add route
	proxy.AddRoute(routeID, hostname, containerIP, containerPort)
	m.logger.Info("Added module route: %s:%d -> %s:%d (module: %s, port: %s, protocol: %s)",
		hostname, hostPort, containerIP, containerPort, moduleName, portName, protocol)

	return nil
}

// Removes proxy routes for a module's ports
func (m *Manager) RemoveModuleRoute(moduleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.config.Enabled {
		return nil
	}

	// Find the module to get its ports
	module, err := m.store.GetModule(context.Background(), moduleID)
	if err != nil {
		return err
	}

	// Hostless modules registered under the catch all key
	hostname := ""
	if module.ServerId != "" {
		server, err := m.store.GetServer(context.Background(), module.ServerId)
		if err != nil {
			return err
		}
		hostname = server.ProxyHostname
	}

	// Remove all port routes
	for _, port := range module.Ports {
		if port == nil || port.HostPort == 0 {
			continue
		}
		m.removePortRouteUnlocked(int(port.HostPort), hostname, module.Name, port.Name)
	}

	return nil
}

// Removes a port route, prunes empty proxies, lock held
func (m *Manager) removePortRouteUnlocked(hostPort int, hostname, moduleName, portName string) {
	proxy, exists := m.proxies[hostPort]
	if !exists {
		return // No proxy for this port
	}

	proxy.RemoveRoute(hostname)
	m.logger.Info("Removed module route: %s:%d (module: %s, port: %s)", hostname, hostPort, moduleName, portName)

	// Check if this proxy has any remaining routes
	routes := proxy.GetRoutes()
	if len(routes) == 0 {
		// Stop and remove the proxy since it's no longer needed
		if err := proxy.Stop(); err != nil {
			m.logger.Error("Failed to stop module proxy on port %d: %v", hostPort, err)
		}
		delete(m.proxies, hostPort)
		m.logger.Info("Removed unused module proxy for port %d", hostPort)
	}
}

// Updates proxy routes for a module's ports
func (m *Manager) UpdateModuleRoute(module *v1.Module, server *v1.Server) error {
	if !m.config.Enabled || !hasProxyPorts(module) {
		return nil
	}

	if module.ContainerId == "" {
		return nil
	}

	// Get the container IP
	containerIP, err := m.containerIP(module.ContainerId)
	if err != nil {
		return fmt.Errorf("failed to get module container IP: %w", err)
	}

	// Hostless modules route every hostname on their port
	hostname := ""
	if server != nil {
		hostname = server.ProxyHostname
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update routes for all proxy-enabled ports
	for _, port := range module.Ports {
		if port == nil || !port.ProxyEnabled || port.HostPort == 0 {
			continue
		}

		protocol := port.Protocol

		// Handshake routing cannot match without a hostname
		if hostname == "" && protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT {
			continue
		}

		containerPort := m.moduleContainerPort(module, port)
		proxy, exists := m.proxies[int(port.HostPort)]
		if !exists {
			// Need to add it
			routeID := fmt.Sprintf("%s-port-%d", module.Id, port.HostPort)
			if err := m.addPortRouteUnlocked(routeID, hostname, containerIP,
				int(port.HostPort), containerPort, protocol, module.Name, port.Name); err != nil {
				m.logger.Error("Failed to add port route for %s: %v", port.Name, err)
			}
			continue
		}

		if containerPort == 0 {
			m.logger.Error("No container port declared for %s", port.Name)
			continue
		}
		proxy.UpdateRoute(hostname, containerIP, containerPort)
		m.logger.Info("Updated module route: %s:%d -> %s:%d (module: %s, port: %s)",
			hostname, port.HostPort, containerIP, containerPort, module.Name, port.Name)
	}

	return nil
}

// Returns all module proxy routes
func (m *Manager) GetModuleRoutes() map[int]map[string]*Route {
	m.mu.Lock()
	defer m.mu.Unlock()

	moduleRoutes := make(map[int]map[string]*Route)

	// Non-listener ports all belong to modules
	for port, proxy := range m.proxies {
		if m.listenerPorts[port] {
			continue
		}
		moduleRoutes[port] = proxy.GetRoutes()
	}

	return moduleRoutes
}

// Aggregates per-server route counters from every listener
func (m *Manager) GetRouteStats() map[string]*v1.ProxyRoute {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := make(map[string]*v1.ProxyRoute)
	for _, proxy := range m.proxies {
		mp, ok := proxy.(*MinecraftProxy)
		if !ok {
			continue
		}
		for id, raw := range mp.StatsSnapshots() {
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

// Creates default proxy listener if proxy is enabled
func (m *Manager) EnsureDefaultListener() (*v1.ProxyListener, error) {
	if !m.config.Enabled {
		return nil, nil
	}
	return m.ensureDefaultListenerLocked()
}

// Assumes caller has already verified proxy is enabled
func (m *Manager) ensureDefaultListenerLocked() (*v1.ProxyListener, error) {
	ctx := context.Background()

	// Check if any listeners exist
	listeners, err := m.store.ListProxyListeners(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get proxy listeners: %w", err)
	}

	if len(listeners) > 0 {
		return nil, nil
	}

	// Find an available port using the store function
	port, err := m.store.FindAvailableListenerPort(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find available port: %w", err)
	}

	// Create default listener
	defaultListener := &v1.ProxyListener{
		Id:        "default",
		Port:      int32(port),
		Name:      "Primary",
		IsDefault: true,
		Enabled:   true,
	}

	if err := m.store.CreateProxyListener(ctx, defaultListener); err != nil {
		return nil, fmt.Errorf("failed to create default listener: %w", err)
	}

	m.logger.Info("Created default proxy listener on port %d", port)
	return defaultListener, nil
}
