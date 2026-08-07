package proxy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
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

	// Hostnames the panel answers on
	panelNames []string

	// Cached outbound address for suggestions
	detectedIP string
	detectedAt time.Time

	// Cached router address from internet echo services
	publicIP    string
	publicAt    time.Time
	publicTried time.Time

	// Cached default gateway address
	gatewayIP string
	gatewayAt time.Time

	// Granted checkouts awaiting their callers' persists
	pendingClaims map[uint64]pendingClaim
	claimSeq      uint64

	// Loopback port the panel http server answers on
	panelBackend int

	// Config file certificates every socket terminates with
	certs *certIndex

	// Panel also answers unmatched hostnames
	panelCatchAll bool

	// Cached agent reachability names for panel routes
	infraNames   []string
	infraNamesAt time.Time

	// Container addresses cached so inspects stay off the lock
	ipCache   map[string]ipEntry
	ipCacheMu sync.Mutex

	// Encoded server icons cached by file identity
	favicons minecraft.FaviconCache
}

// One cached container address with its inspect time
type ipEntry struct {
	ip string
	at time.Time
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
	m := &Manager{
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
		pendingClaims: make(map[uint64]pendingClaim),
		panelCatchAll: true,
		certs:         LoadTLSCertificates(cfg.Proxy.TLS.Certificates, logger),
		ipCache:       make(map[string]ipEntry),
	}
	return m
}

// Panel http backend target, must precede Start
func (m *Manager) SetPanelBackend(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.panelBackend = port
}

// Panel web port from the server config
func (m *Manager) panelWebPort() int {
	if m.appCfg == nil {
		return 0
	}
	p, err := strconv.Atoi(m.appCfg.Server.Port)
	if err != nil || p < 1 {
		return 0
	}
	return p
}

// How long resolved agent names stay cached
const infraNameTTL = 5 * time.Minute

// Agent reachability names the panel always answers
func (m *Manager) infraNamesLocked(ctx context.Context) []string {
	if m.infraNames != nil && time.Since(m.infraNamesAt) < infraNameTTL {
		return m.infraNames
	}
	seen := make(map[string]bool)
	var out []string
	add := func(raw string) {
		name := NormalizeHostname(raw)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	// Loopback access works no matter the catch all
	add("localhost")
	add("127.0.0.1")
	add(docker.PanelNetworkAlias)
	add("host.docker.internal")
	if m.appCfg != nil && m.appCfg.Docker.AgentURL != "" {
		if u, err := url.Parse(m.appCfg.Docker.AgentURL); err == nil {
			add(u.Hostname())
		}
	}
	if m.docker != nil && m.appCfg != nil {
		inspectCtx, cancel := context.WithTimeout(ctx, containerInspectTimeout)
		agentURL, err := m.docker.PanelAgentURL(inspectCtx, m.appCfg.Server.Port)
		cancel()
		if err != nil && m.infraNames != nil {
			// Inspect hiccup keeps the last good set
			return m.infraNames
		}
		if err == nil {
			if u, uerr := url.Parse(agentURL); uerr == nil {
				add(u.Hostname())
			}
		}
	}
	// Ip keeps the host hostname alias reachable
	if len(m.panelNames) == 0 {
		if ip, ok := m.lanIPLocked(); ok {
			add(ip)
		}
	}
	m.infraNames = out
	m.infraNamesAt = time.Now()
	return out
}

// Reservations reuse the sync time name snapshot
func (m *Manager) infraNamesSnapshotLocked(ctx context.Context) []string {
	if m.infraNames != nil {
		return m.infraNames
	}
	return m.infraNamesLocked(ctx)
}

// User names plus agent names the panel serves
func (m *Manager) panelServedNamesLocked(ctx context.Context) []string {
	seen := make(map[string]bool)
	var out []string
	for _, name := range m.panelNames {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, name := range m.infraNamesLocked(ctx) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// Panel routes, named always plus an optional catch all
func (m *Manager) panelRoutesLocked(ctx context.Context) []Route {
	if m.panelBackend == 0 {
		return nil
	}
	base := Route{
		ServerID:    PanelListenerID,
		OwnerKind:   OwnerPanel,
		OwnerID:     OwnerPanel,
		BackendHost: "127.0.0.1",
		BackendPort: m.panelBackend,
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_HTTP,
	}
	var routes []Route
	if m.panelCatchAll {
		routes = append(routes, base)
	}
	for _, name := range m.panelServedNamesLocked(ctx) {
		variant := base
		variant.Hostname = name
		routes = append(routes, variant)
	}
	return routes
}

// Keeps the panel listener row present and current
func (m *Manager) ensurePanelListenerLocked(ctx context.Context) error {
	port := m.panelWebPort()
	if port == 0 {
		return nil
	}
	row, err := m.store.GetProxyListener(ctx, PanelListenerID)
	if err != nil {
		row = &v1.ProxyListener{
			Id:          PanelListenerID,
			Port:        int32(port),
			Name:        "Panel",
			Description: "DiscoPanel web interface",
			Enabled:     true,
		}
		return m.store.CreateProxyListener(ctx, row)
	}
	if row.Port == int32(port) && row.Enabled && !row.IsDefault {
		return nil
	}
	row.Port = int32(port)
	row.Enabled = true
	row.IsDefault = false
	return m.store.UpdateProxyListener(ctx, row)
}

// Starts the always on panel socket when missing
func (m *Manager) ensurePanelSocketLocked() error {
	port := m.panelWebPort()
	if port == 0 || m.panelBackend == 0 {
		return nil
	}
	m.listenerIDs[port] = PanelListenerID
	if _, ok := m.tcpSockets[port]; ok {
		return nil
	}
	sock := NewListenerSocket(&Config{
		ListenAddr:  net.JoinHostPort(m.appCfg.Server.Host, strconv.Itoa(port)),
		Logger:      m.logger,
		Gate:        m.gate,
		Certs:       m.certs,
		TrustedEdge: m.config.TrustedEdge,
	})
	if err := sock.Start(); err != nil {
		return fmt.Errorf("panel socket failed on port %d: %w", port, err)
	}
	m.tcpSockets[port] = sock
	m.logger.Info("Panel socket started on port %d", port)
	return nil
}

// Reports the runtime proxy toggle
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// Reports the panel catch all state
func (m *Manager) PanelCatchAll() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.panelCatchAll
}

// Applies a config change and reconciles running sockets
func (m *Manager) ApplyConfig(ctx context.Context, cfg *v1.ProxyConfig) error {
	m.warmContainerIPs(ctx)
	m.mu.Lock()
	m.enabled = cfg.Enabled
	m.panelNames = cfg.Hostnames
	m.panelCatchAll = cfg.CatchAll
	// Panel names gate the lan alias, recompute
	m.infraNames = nil
	err := m.syncListenersLocked(ctx)
	m.mu.Unlock()
	return err
}

// Bounds docker inspects so a hung daemon cannot wedge syncs
const containerInspectTimeout = 5 * time.Second

// How long a cached container address stays trusted
const containerIPTTL = 15 * time.Second

// Resolves a container IP, fresh cache entries skip docker
func (m *Manager) containerIP(ctx context.Context, containerID string) (string, error) {
	m.ipCacheMu.Lock()
	entry, ok := m.ipCache[containerID]
	m.ipCacheMu.Unlock()
	if ok && time.Since(entry.at) < containerIPTTL {
		return entry.ip, nil
	}
	return m.refreshContainerIP(ctx, containerID)
}

// Inspects one container and recaches its address
func (m *Manager) refreshContainerIP(ctx context.Context, containerID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	inspectCtx, cancel := context.WithTimeout(ctx, containerInspectTimeout)
	defer cancel()
	ip, err := m.docker.ContainerIP(inspectCtx, containerID)
	if err != nil {
		return "", err
	}
	m.ipCacheMu.Lock()
	m.ipCache[containerID] = ipEntry{ip: ip, at: time.Now()}
	m.ipCacheMu.Unlock()
	return ip, nil
}

// True while a server container can hold an address
func containerAddressable(status v1.ServerStatus) bool {
	switch status {
	case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_PAUSED,
		v1.ServerStatus_SERVER_STATUS_UNHEALTHY, v1.ServerStatus_SERVER_STATUS_PROVISIONING,
		v1.ServerStatus_SERVER_STATUS_CREATING, v1.ServerStatus_SERVER_STATUS_STARTING:
		return true
	}
	return false
}

// Warms eligible container addresses ahead of a locked pass
func (m *Manager) warmContainerIPs(ctx context.Context) {
	if m.docker == nil {
		return
	}
	eligible := make(map[string]bool)
	if servers, err := m.store.ListServers(ctx); err == nil {
		for _, server := range servers {
			if server.ContainerId != "" && containerAddressable(server.Status) {
				eligible[server.ContainerId] = true
			}
		}
	}
	if modules, err := m.store.ListModules(ctx); err == nil {
		for _, mod := range modules {
			if mod.ContainerId != "" && mod.Status == v1.ModuleStatus_MODULE_STATUS_RUNNING {
				eligible[mod.ContainerId] = true
			}
		}
	}
	for id := range eligible {
		if _, err := m.containerIP(ctx, id); err != nil {
			m.logger.Debug("Warm inspect failed for container %s: %v", id, err)
		}
	}
	// Dead ids fall out so the cache cannot grow
	m.ipCacheMu.Lock()
	for id := range m.ipCache {
		if !eligible[id] {
			delete(m.ipCache, id)
		}
	}
	m.ipCacheMu.Unlock()
}

// Initializes and starts the proxy if enabled
func (m *Manager) Start() error {
	// Public address cache warms even while the proxy is off
	m.mu.Lock()
	m.publicTried = time.Now()
	m.mu.Unlock()
	go m.refreshPublicIP()

	// Panel hostnames live on the config row
	var names []string
	catchAll := true
	if cfg, _, err := m.store.GetProxyConfig(context.Background()); err == nil && cfg != nil {
		names = cfg.Hostnames
		catchAll = cfg.CatchAll
	}

	m.warmContainerIPs(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	m.panelNames = names
	m.panelCatchAll = catchAll

	if err := m.syncListenersLocked(context.Background()); err != nil {
		return err
	}
	m.logger.Info("Proxy manager started")
	return nil
}

// Reconciles sockets and routes against database state
func (m *Manager) SyncListeners(ctx context.Context) error {
	// Inspects land in cache before the lock is held
	m.warmContainerIPs(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.syncListenersLocked(ctx)
}

// Full reconcile pass, caller must hold the lock
func (m *Manager) syncListenersLocked(ctx context.Context) error {
	if err := m.ensurePanelListenerLocked(ctx); err != nil {
		m.logger.Error("Failed to reconcile the panel listener row: %v", err)
	}
	if err := m.ensurePanelSocketLocked(); err != nil {
		return err
	}

	if !m.enabled {
		return m.syncDisabledLocked(ctx)
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
		sock, ok := m.tcpSockets[port]
		if !ok {
			sock = NewListenerSocket(&Config{
				ListenAddr:  fmt.Sprintf(":%d", port),
				Logger:      m.logger,
				Gate:        m.gate,
				Certs:       m.certs,
				TrustedEdge: m.config.TrustedEdge,
			})
			if err := sock.Start(); err != nil {
				m.logger.Error("Failed to start listener %s on port %d: %v", listener.Name, port, err)
				continue
			}
			m.tcpSockets[port] = sock
			m.logger.Info("Started listener %s on port %d", listener.Name, port)
		}
	}

	tcpRoutes, udpRoutes := m.desiredRoutesLocked(ctx, byID)
	if panelRoutes := m.panelRoutesLocked(ctx); len(panelRoutes) > 0 {
		tcpRoutes[m.panelWebPort()] = append(tcpRoutes[m.panelWebPort()], panelRoutes...)
	}

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

// Disabled proxy keeps only the panel socket serving itself
func (m *Manager) syncDisabledLocked(ctx context.Context) error {
	panelPort := m.panelWebPort()
	for port, sock := range m.tcpSockets {
		if port == panelPort {
			continue
		}
		if err := sock.Stop(); err != nil {
			m.logger.Error("Failed to stop listener socket on port %d: %v", port, err)
		}
		delete(m.tcpSockets, port)
		delete(m.listenerIDs, port)
	}
	for port, up := range m.udpSockets {
		up.Stop()
		delete(m.udpSockets, port)
	}
	if sock := m.tcpSockets[panelPort]; sock != nil {
		sock.SetRoutes(m.panelRoutesLocked(ctx))
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
		if len(server.ProxyHostnames) > 0 {
			if listener := listenersByID[server.ProxyListenerId]; listener != nil {
				route, want, err := m.desiredRoute(ctx, server)
				if err != nil {
					m.logger.Error("Failed to build route for server %s: %v", server.Name, err)
				} else if want {
					// Every hostname relays to the same backend
					port := int(listener.Port)
					for _, name := range server.ProxyHostnames {
						variant := route
						variant.Hostname = name
						tcpRoutes[port] = append(tcpRoutes[port], variant)
					}
					// Empty hostname is the port's mc catch all
					if server.ProxyCatchAll {
						variant := route
						variant.Hostname = ""
						tcpRoutes[port] = append(tcpRoutes[port], variant)
					}
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
		appendPortRoutes(tcpRoutes, udpRoutes, server.AdditionalPorts, server.ProxyHostnames,
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
		var hostnames []string
		if srv := serversByID[mod.ServerId]; srv != nil {
			hostnames = srv.ProxyHostnames
		}
		module := mod
		appendPortRoutes(tcpRoutes, udpRoutes, mod.Ports, hostnames,
			OwnerModule, mod.Id, ip, func(p *v1.NetworkPort) int { return m.moduleContainerPort(module, p) })
	}

	m.pruneStatsLocked(servers, modules, tcpRoutes, udpRoutes)

	return tcpRoutes, udpRoutes
}

// Drops stat baselines for routes whose owner or key vanished
func (m *Manager) pruneStatsLocked(servers []*v1.Server, modules []*v1.Module, tcpRoutes map[int][]Route, udpRoutes map[int]Route) {
	// Owner ids keep service counters across stop and start
	owners := make(map[string]bool, len(servers)+len(modules)+1)
	owners[PanelListenerID] = true
	for _, s := range servers {
		owners[s.Id] = true
	}
	for _, mod := range modules {
		owners[mod.Id] = true
	}
	live := make(map[string]bool)
	for _, routes := range tcpRoutes {
		for _, r := range routes {
			live[r.ServerID] = true
		}
	}
	for _, r := range udpRoutes {
		live[r.ServerID] = true
	}
	prune := func(stats map[string]*v1.ProxyRoute) {
		for id := range stats {
			if !live[id] && !owners[id] {
				delete(stats, id)
			}
		}
	}
	prune(m.statsBase)
	prune(m.statsLast)
}

// Adds one port list's routes onto the desired tables
func appendPortRoutes(tcpRoutes map[int][]Route, udpRoutes map[int]Route, ports []*v1.NetworkPort, fallbackHostnames []string, ownerKind, ownerID, backendHost string, containerPort func(*v1.NetworkPort) int) {
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
			for _, hostname := range routedHostnames(port, fallbackHostnames) {
				variant := route
				variant.Hostname = hostname
				tcpRoutes[hostPort] = append(tcpRoutes[hostPort], variant)
			}
		case v1.ModuleProtocol_MODULE_PROTOCOL_UDP:
			// Udp counters never share a tcp stream id
			route.ServerID += "-udp"
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
		if port != nil && port.ProxyEnabled && port.HostPort > 0 {
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

	// Panel row never counts toward listener bootstrap
	nonPanel := 0
	for _, l := range listeners {
		if l.Id != PanelListenerID {
			nonPanel++
		}
	}

	// First run bootstraps the primary listener
	if nonPanel == 0 {
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
		listeners = append(listeners, listener)
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
		// Panel socket serves its own port, never a row here
		if have[port] || port == m.panelWebPort() {
			continue
		}
		listener, err := m.createListenerRowLocked(ctx, port)
		if err != nil {
			m.logger.Error("Failed to auto create listener for port %d: %v", port, err)
			continue
		}
		listeners = append(listeners, listener)
	}

	// Exactly one default listener, never the panel row
	var defaults []*v1.ProxyListener
	var candidates []*v1.ProxyListener
	for _, l := range listeners {
		if l.Id == PanelListenerID {
			continue
		}
		candidates = append(candidates, l)
		if l.IsDefault {
			defaults = append(defaults, l)
		}
	}
	if len(defaults) == 0 && len(candidates) > 0 {
		promote := candidates[0]
		for _, l := range candidates {
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
	// Status changes refresh the address before the lock
	if server.ContainerId != "" && m.docker != nil && containerAddressable(server.Status) {
		if _, err := m.refreshContainerIP(context.Background(), server.ContainerId); err != nil {
			m.logger.Debug("Refresh inspect failed for server %s: %v", server.Name, err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled || len(server.ProxyHostnames) == 0 || server.ProxyListenerId == "" {
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

	route, want, err := m.desiredRoute(ctx, server)
	if err != nil {
		return err
	}
	if !want {
		for _, name := range server.ProxyHostnames {
			sock.RemoveRoute(v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT, name)
		}
		if server.ProxyCatchAll {
			sock.RemoveRoute(v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT, "")
		}
		return nil
	}
	for _, name := range server.ProxyHostnames {
		variant := route
		variant.Hostname = name
		sock.UpsertServerRoute(variant)
	}
	if server.ProxyCatchAll {
		variant := route
		variant.Hostname = ""
		sock.UpsertServerRoute(variant)
	}
	return nil
}

// Reconciles every route a server owns after status changes
func (m *Manager) SyncServerRoutes(ctx context.Context, server *v1.Server) error {
	var firstErr error
	if len(server.ProxyHostnames) > 0 {
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

// Hostname free route template, callers stamp each name on
func (m *Manager) desiredRoute(ctx context.Context, server *v1.Server) (route Route, want bool, err error) {
	cfg, cfgErr := m.store.GetServerProperties(ctx, server.Id)
	if cfgErr != nil {
		cfg = nil
	}

	route = Route{
		ServerID:      server.Id,
		OwnerKind:     OwnerServer,
		OwnerID:       server.Id,
		Protocol:      v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		BackendPort:   docker.DefaultMinecraftPort,
		ProxyProtocol: propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.EnableProxyProtocol }),
		PreserveHost:  propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.ProxyPreserveHostname }),
		MaxPlayers:    int(server.MaxPlayers),
		Favicon:       m.routeFavicon(server),
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
		offlineStatus := propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.EnableOfflineStatus })
		if !wakeable && !offlineStatus {
			return Route{}, false, nil
		}
		route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE
		route.Wakeable = wakeable
		route.Motd = offlineMOTD(cfg, wakeable)
		return route, true, nil

	default:
		return Route{}, false, nil
	}
}

// Server icon else the discopanel avatar fallback
func (m *Manager) routeFavicon(server *v1.Server) string {
	if uri := m.favicons.Get(server.Id, server.DataPath); uri != "" {
		return uri
	}
	return minecraft.DefaultFavicon()
}

// Reads an optional bool off possibly-nil properties
func propEnabled(cfg *v1.ServerProperties, field func(*v1.ServerProperties) *bool) bool {
	if cfg == nil {
		return false
	}
	v := field(cfg)
	return v != nil && *v
}

// Builds both motd lines, custom offline motd replaces head
func SyntheticMOTD(cfg *v1.ServerProperties, head, tail string) string {
	if cfg != nil && cfg.OfflineMotd != nil && *cfg.OfflineMotd != "" {
		head = *cfg.OfflineMotd
	}
	return head + "\n§r" + tail
}

// Builds the status lines stopped servers answer with
func offlineMOTD(cfg *v1.ServerProperties, wakeable bool) string {
	if wakeable {
		return SyntheticMOTD(cfg, "§7offline", "§ajoin to press play")
	}
	return SyntheticMOTD(cfg, "§7offline", "§7the server is paused")
}

// Builds the status lines shown while a server boots
func bootMOTD(server *v1.Server, cfg *v1.ServerProperties) string {
	head, tail := "§estarting up...", "§fjoin in a moment"
	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_PROVISIONING:
		head, tail = "§einstalling server files...", "§7hang tight"
	case v1.ServerStatus_SERVER_STATUS_CREATING:
		head, tail = "§esetting up the stage...", "§7almost ready"
	}
	return SyntheticMOTD(cfg, head, tail)
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

	if !m.enabled {
		return false
	}
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

// Forgets counters owned by a deleted workload
func (m *Manager) DropOwnerStats(ownerID string) {
	if ownerID == "" {
		return
	}
	prefix := ownerID + "-port-"
	match := func(id string) bool { return id == ownerID || strings.HasPrefix(id, prefix) }

	m.mu.Lock()
	for id := range m.statsBase {
		if match(id) {
			delete(m.statsBase, id)
		}
	}
	for id := range m.statsLast {
		if match(id) {
			delete(m.statsLast, id)
		}
	}
	socks := make([]*ListenerSocket, 0, len(m.tcpSockets))
	for _, sock := range m.tcpSockets {
		socks = append(socks, sock)
	}
	m.mu.Unlock()

	for _, sock := range socks {
		sock.DropStats(match)
	}
}

// Aggregates per-route counters from every listener socket
func (m *Manager) GetRouteStats() map[string]*v1.ProxyRoute {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := make(map[string]*v1.ProxyRoute)
	merge := func(id string, raw *v1.ProxyRoute) {
		if countersReset(m.statsLast[id], raw) {
			m.statsBase[id] = addCounters(m.statsBase[id], m.statsLast[id])
		}
		m.statsLast[id] = raw
		stats[id] = addCounters(m.statsBase[id], raw)
	}
	for _, sock := range m.tcpSockets {
		for id, raw := range sock.StatsSnapshots() {
			merge(id, raw)
		}
	}
	for _, up := range m.udpSockets {
		route, ok := up.Route()
		if !ok {
			continue
		}
		merge(route.ServerID, up.StatsSnapshot())
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
