package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/module"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Compile-time check that ProxyService implements the interface
var _ discopanelv1connect.ProxyServiceHandler = (*ProxyService)(nil)

// Implements the Proxy service
type ProxyService struct {
	store         *storage.Store
	docker        *docker.Client
	proxyManager  *proxy.Manager
	moduleManager *module.Manager
	config        *config.Config
	rec           *metrics.Recorder
	log           *logger.Logger
}

// Creates a new proxy service
func NewProxyService(store *storage.Store, dockerClient *docker.Client, proxyManager *proxy.Manager, moduleManager *module.Manager, cfg *config.Config, rec *metrics.Recorder, log *logger.Logger) *ProxyService {
	return &ProxyService{
		store:         store,
		docker:        dockerClient,
		proxyManager:  proxyManager,
		moduleManager: moduleManager,
		config:        cfg,
		rec:           rec,
		log:           log,
	}
}

// Gets proxy routes
func (s *ProxyService) GetProxyRoutes(ctx context.Context, req *connect.Request[v1.GetProxyRoutesRequest]) (*connect.Response[v1.GetProxyRoutesResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy not enabled"))
	}

	return connect.NewResponse(&v1.GetProxyRoutesResponse{
		Routes: s.buildProxyRoutes(),
	}), nil
}

// Live routes with stats merged and socket attribution
func (s *ProxyService) buildProxyRoutes() []*v1.ProxyRoute {
	entries := s.proxyManager.RouteEntries()
	stats := s.proxyManager.GetRouteStats()

	// Stats snapshots become the rows, route facts fill the rest
	protoRoutes := make([]*v1.ProxyRoute, 0, len(entries))
	for _, entry := range entries {
		route := entry.Route
		pr := &v1.ProxyRoute{}
		// Rows copy shared counters before mutation
		if counters := stats[route.ServerID]; counters != nil {
			pr, _ = proto.Clone(counters).(*v1.ProxyRoute)
		}
		pr.ServerId = ""
		if route.OwnerKind == proxy.OwnerServer {
			pr.ServerId = route.OwnerID
		}
		pr.Hostname = route.Hostname
		pr.BackendHost = route.BackendHost
		pr.BackendPort = int32(route.BackendPort)
		pr.State = route.State
		pr.Wakeable = route.Wakeable
		pr.ProxyProtocol = route.ProxyProtocol
		pr.PreserveHostname = route.PreserveHost
		pr.ListenPort = int32(entry.Port)
		pr.ListenerId = entry.ListenerID
		pr.OwnerKind = proxy.OwnerKindProto(route.OwnerKind)
		pr.OwnerId = route.OwnerID
		pr.PortName = route.PortName
		pr.Protocol = route.Protocol
		protoRoutes = append(protoRoutes, pr)
	}
	return protoRoutes
}

// Gets proxy status
func (s *ProxyService) GetProxyStatus(ctx context.Context, req *connect.Request[v1.GetProxyStatusRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	// Load proxy config from database
	proxyConfig, _, err := s.store.GetProxyConfig(ctx)
	if err != nil {
		s.log.Error("Failed to load proxy configuration: %v", err)
		proxyConfig = &v1.ProxyConfig{
			Enabled:     s.proxyManager.Enabled(),
			Hostnames:   s.proxyManager.PanelHostnames(),
			CatchAll:    s.proxyManager.PanelCatchAll(),
			Lobby:       s.proxyManager.LobbyEnabled(),
			LobbyOnline: s.proxyManager.LobbyOnline(),
		}
	}

	// Get listeners
	listeners, err := s.store.ListProxyListeners(ctx)
	if err != nil {
		s.log.Error("Failed to load proxy listeners: %v", err)
		listeners = []*v1.ProxyListener{}
	}

	listenPorts := make([]int32, len(listeners))
	for i, l := range listeners {
		listenPorts[i] = l.Port
	}

	// Default listener carries the primary port
	var primaryPort int32
	for _, l := range listeners {
		if l.IsDefault {
			primaryPort = l.Port
			break
		}
	}
	if primaryPort == 0 && len(listenPorts) > 0 {
		primaryPort = listenPorts[0]
	}

	// Get running status and active routes count
	running := false
	activeRoutes := int32(0)
	if s.proxyManager != nil {
		running = s.proxyManager.IsRunning()
		activeRoutes = int32(len(s.proxyManager.RouteEntries()))
	}

	resp := &v1.GetProxyStatusResponse{
		Enabled:      proxyConfig.Enabled,
		Hostnames:    proxyConfig.Hostnames,
		ListenPorts:  listenPorts,
		Listeners:    listeners,
		ListenPort:   primaryPort,
		Running:      running,
		ActiveRoutes: activeRoutes,
		CatchAll:     proxyConfig.CatchAll,
		Lobby:        proxyConfig.Lobby,
		LobbyOnline:  proxyConfig.LobbyOnline,
		BaseUrl:      proxyConfig.BaseUrl,
	}
	if s.proxyManager != nil {
		resp.LobbyMembers = s.proxyManager.LobbyMembers()
		resp.LobbyWaiting = s.proxyManager.LobbyWaiting()
		resp.EffectiveBaseUrl = s.proxyManager.EffectiveBaseURL()
		resp.LanIp, resp.PublicIp, _ = s.proxyManager.NetworkAddresses()
	}
	return connect.NewResponse(resp), nil
}

// Updates proxy configuration
func (s *ProxyService) UpdateProxyConfig(ctx context.Context, req *connect.Request[v1.UpdateProxyConfigRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	msg := req.Msg
	hostnames, err := proxy.NormalizeHostnames(msg.Hostnames)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Registry claim holds the names through the persist
	var claim *proxy.NetClaim
	if s.proxyManager != nil {
		claim, err = s.proxyManager.CheckoutPanelHostnames(ctx, hostnames)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		defer claim.Release()
	}

	// Disable needs an approved conversion plan before persisting
	disabling := !msg.Enabled && s.proxyManager.Enabled()
	if disabling {
		if err := s.requireDisableConvert(ctx, msg); err != nil {
			return nil, err
		}
	}

	// Old row comes back if the runtime apply fails
	prevConfig, _, prevErr := s.store.GetProxyConfig(ctx)

	// Absent lobby flags keep their saved values
	lobby, lobbyOnline := false, true
	baseURL := ""
	if prevConfig != nil {
		lobby, lobbyOnline = prevConfig.Lobby, prevConfig.LobbyOnline
		baseURL = prevConfig.BaseUrl
	}
	if msg.Lobby != nil {
		lobby = *msg.Lobby
	}
	if msg.LobbyOnline != nil {
		lobbyOnline = *msg.LobbyOnline
	}
	if msg.BaseUrl != nil {
		baseURL = proxy.NormalizeHostname(*msg.BaseUrl)
		if baseURL != "" && !proxy.ValidHostname(baseURL) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("base domain %q is not a valid hostname", baseURL))
		}
	}

	// Save to database
	proxyConfig := &v1.ProxyConfig{
		Id:          "default",
		Enabled:     msg.Enabled,
		Hostnames:   hostnames,
		CatchAll:    msg.CatchAll,
		Lobby:       lobby,
		LobbyOnline: lobbyOnline,
		BaseUrl:     baseURL,
	}

	if err := s.store.SaveProxyConfig(ctx, proxyConfig); err != nil {
		s.log.Error("Failed to save proxy configuration: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save proxy configuration"))
	}
	claim.Confirm()

	s.log.Info("Proxy configuration saved to database: enabled=%v, hostnames=%v", msg.Enabled, hostnames)

	// Old row comes back if apply or validation fails
	restorePrev := func() {
		if prevErr != nil || prevConfig == nil {
			return
		}
		if rerr := s.store.SaveProxyConfig(ctx, prevConfig); rerr != nil {
			s.log.Error("Failed to restore previous proxy configuration: %v", rerr)
		} else {
			s.proxyManager.ApplyConfig(ctx, prevConfig)
		}
	}

	// Manager owns runtime state and reconciles sockets
	if s.proxyManager != nil {
		if err := s.proxyManager.ApplyConfig(ctx, proxyConfig); err != nil {
			s.log.Error("Failed to apply proxy configuration: %v", err)
			restorePrev()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to apply proxy configuration: %w", err))
		}
	}

	// Workloads convert only after the new config holds
	if disabling {
		recreateServers, recreateModules, convErr := s.convertForDisable(ctx, msg)
		if convErr != nil {
			s.log.Error("Failed to convert proxied workloads after disable: %v", convErr)
		}

		// Converted containers rebind once sockets are gone
		for _, id := range recreateServers {
			server, err := s.store.GetServer(ctx, id)
			if err != nil {
				s.log.Error("Failed to load server %s for rebind: %v", id, err)
				continue
			}
			s.recreateForConvert(ctx, server)
		}
		for _, id := range recreateModules {
			if err := s.moduleManager.RecreateModule(ctx, id); err != nil {
				s.log.Error("Failed to recreate module %s after convert: %v", id, err)
			}
		}
		if convErr != nil {
			return nil, convErr
		}
	}

	// Return updated status, callers read it like GetProxyStatus
	return s.GetProxyStatus(ctx, connect.NewRequest(&v1.GetProxyStatusRequest{}))
}

// Rejects a disable without an approved conversion plan
func (s *ProxyService) requireDisableConvert(ctx context.Context, msg *v1.UpdateProxyConfigRequest) error {
	overrides := make(map[string]int32, len(msg.Assignments))
	for _, a := range msg.Assignments {
		if a != nil {
			overrides[a.ServerId] = a.ProposedPort
		}
	}
	impact, err := s.computeDisableImpact(ctx, overrides)
	if err != nil {
		var conflict *proxy.NetConflictError
		if errors.As(err, &conflict) {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to compute disable impact: %v", err)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to compute disable impact"))
	}
	if len(impact.Servers) == 0 && len(impact.ModulePorts) == 0 && len(impact.ServerPorts) == 0 {
		return nil
	}
	if !msg.ConvertToDirect {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%d proxied servers and %d proxied ports need direct ports first",
				len(impact.Servers), len(impact.ModulePorts)+len(impact.ServerPorts)))
	}
	return nil
}

// Preview what disabling the proxy converts
func (s *ProxyService) GetProxyDisableImpact(ctx context.Context, req *connect.Request[v1.GetProxyDisableImpactRequest]) (*connect.Response[v1.GetProxyDisableImpactResponse], error) {
	impact, err := s.computeDisableImpact(ctx, nil)
	if err != nil {
		var conflict *proxy.NetConflictError
		if errors.As(err, &conflict) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to compute disable impact: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to compute disable impact"))
	}
	return connect.NewResponse(impact), nil
}

// Ports that stay busy once the proxy turns off
func (s *ProxyService) postDisableBusy(ctx context.Context) (map[int]bool, map[int]bool, error) {
	reservations, err := s.proxyManager.Reservations(ctx)
	if err != nil {
		return nil, nil, err
	}
	listeners, err := s.store.ListProxyListeners(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Manual listener rows keep their ports across a disable
	manualPorts := make(map[int32]bool)
	for _, l := range listeners {
		if !l.AutoCreated {
			manualPorts[l.Port] = true
		}
	}

	busyTCP := make(map[int]bool)
	busyUDP := make(map[int]bool)
	for _, r := range reservations {
		pb := r.Proto()
		switch pb.Kind {
		case v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_EXCLUSIVE:
			if pb.Transport == v1.NetworkTransport_NETWORK_TRANSPORT_UDP {
				busyUDP[int(pb.Port)] = true
			} else {
				busyTCP[int(pb.Port)] = true
			}
		case v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_SOCKET:
			if manualPorts[pb.Port] {
				busyTCP[int(pb.Port)] = true
			}
		}
	}
	return busyTCP, busyUDP, nil
}

// Plans direct ports for everything the proxy serves
func (s *ProxyService) computeDisableImpact(ctx context.Context, overrides map[string]int32) (*v1.GetProxyDisableImpactResponse, error) {
	impact := &v1.GetProxyDisableImpactResponse{}
	exclude := make(map[int]bool)

	// Routed and relay claims vanish on disable, plan without them
	busyTCP, busyUDP, err := s.postDisableBusy(ctx)
	if err != nil {
		return nil, err
	}
	tcpFree := func(port int) bool {
		return port > 0 && port <= 65535 && !busyTCP[port] && !exclude[port]
	}

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		if len(server.ProxyHostnames) == 0 {
			continue
		}
		port := int(overrides[server.Id])
		if port > 0 {
			// User picks fail fast with a concrete reason
			if !tcpFree(port) || !tcpFree(port+docker.RCONPortOffset) {
				return nil, &proxy.NetConflictError{Port: port, Reason: fmt.Sprintf("port %d or its rcon shadow is not free after disable", port)}
			}
		} else {
			for p := s.config.Proxy.PortRangeMin; ; p++ {
				if p > 65535 {
					return nil, &proxy.NetConflictError{Port: 0, Reason: "no free port for a converted server"}
				}
				if tcpFree(p) && tcpFree(p+docker.RCONPortOffset) {
					port = p
					break
				}
			}
		}
		exclude[port] = true
		exclude[port+docker.RCONPortOffset] = true
		impact.Servers = append(impact.Servers, &v1.ProxiedServerImpact{
			ServerId:     server.Id,
			Hostnames:    server.ProxyHostnames,
			ProposedPort: int32(port),
		})
	}

	// Server extra ports convert no matter the routing mode
	for _, server := range servers {
		for _, port := range server.AdditionalPorts {
			if port == nil || !port.ProxyEnabled || port.HostPort <= 0 {
				continue
			}
			busy := busyTCP
			if storage.TransportOf(port.Protocol) == v1.NetworkTransport_NETWORK_TRANSPORT_UDP {
				busy = busyUDP
			}
			proposed := int(port.HostPort)
			// Ports keep their number when it stays free
			if busy[proposed] || exclude[proposed] {
				found := 0
				for p := s.config.Proxy.PortRangeMin; p <= 65535; p++ {
					if !busy[p] && !exclude[p] {
						found = p
						break
					}
				}
				if found == 0 {
					return nil, &proxy.NetConflictError{Port: proposed, Reason: fmt.Sprintf("no free port for server port %s", port.Name)}
				}
				proposed = found
			}
			exclude[proposed] = true
			impact.ServerPorts = append(impact.ServerPorts, &v1.ProxiedServerPortImpact{
				ServerId:         server.Id,
				PortName:         port.Name,
				CurrentHostPort:  port.HostPort,
				ProposedHostPort: int32(proposed),
			})
		}
	}

	modules, err := s.store.ListModules(ctx)
	if err != nil {
		return nil, err
	}
	for _, mod := range modules {
		for _, port := range mod.Ports {
			if port == nil || !port.ProxyEnabled || port.HostPort <= 0 {
				continue
			}
			busy := busyTCP
			if storage.TransportOf(port.Protocol) == v1.NetworkTransport_NETWORK_TRANSPORT_UDP {
				busy = busyUDP
			}
			proposed := int(port.HostPort)
			// Ports keep their number when it stays free
			if busy[proposed] || exclude[proposed] {
				found := 0
				for p := s.config.Module.PortRangeMin; p <= s.config.Module.PortRangeMax; p++ {
					if !busy[p] && !exclude[p] {
						found = p
						break
					}
				}
				if found == 0 {
					return nil, &proxy.NetConflictError{Port: proposed, Reason: fmt.Sprintf("no free port for module port %s", port.Name)}
				}
				proposed = found
			}
			exclude[proposed] = true
			impact.ModulePorts = append(impact.ModulePorts, &v1.ProxiedModulePortImpact{
				ModuleId:         mod.Id,
				PortName:         port.Name,
				CurrentHostPort:  port.HostPort,
				ProposedHostPort: int32(proposed),
			})
		}
	}

	return impact, nil
}

// Converts proxied servers and module ports to direct binds
func (s *ProxyService) convertForDisable(ctx context.Context, msg *v1.UpdateProxyConfigRequest) ([]string, []string, error) {
	overrides := make(map[string]int32, len(msg.Assignments))
	for _, a := range msg.Assignments {
		if a != nil {
			overrides[a.ServerId] = a.ProposedPort
		}
	}

	impact, err := s.computeDisableImpact(ctx, overrides)
	if err != nil {
		var conflict *proxy.NetConflictError
		if errors.As(err, &conflict) {
			return nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to compute disable impact: %v", err)
		return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to compute disable impact"))
	}
	if len(impact.Servers) == 0 && len(impact.ModulePorts) == 0 && len(impact.ServerPorts) == 0 {
		return nil, nil, nil
	}
	if !msg.ConvertToDirect {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%d proxied servers and %d proxied ports need direct ports first",
				len(impact.Servers), len(impact.ModulePorts)+len(impact.ServerPorts)))
	}

	// Landing ports queue up in plan iteration order
	serverPortPlan := make(map[string][]int32)
	for _, sp := range impact.ServerPorts {
		serverPortPlan[sp.ServerId] = append(serverPortPlan[sp.ServerId], sp.ProposedHostPort)
	}

	// Rows all load before the first persist
	portsByModule := make(map[string]map[string]int32)
	for _, mp := range impact.ModulePorts {
		if portsByModule[mp.ModuleId] == nil {
			portsByModule[mp.ModuleId] = make(map[string]int32)
		}
		portsByModule[mp.ModuleId][mp.PortName] = mp.ProposedHostPort
	}
	// Routing conversions carry a proposed direct port
	proposed := make(map[string]int32, len(impact.Servers))
	for _, sv := range impact.Servers {
		proposed[sv.ServerId] = sv.ProposedPort
	}
	serversByID := make(map[string]*v1.Server)
	for serverID := range proposed {
		serversByID[serverID] = nil
	}
	for serverID := range serverPortPlan {
		serversByID[serverID] = nil
	}
	for serverID := range serversByID {
		server, err := s.store.GetServer(ctx, serverID)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server %s not found", serverID))
		}
		serversByID[serverID] = server
	}
	modulesByID := make(map[string]*v1.Module)
	for moduleID := range portsByModule {
		mod, err := s.store.GetModule(ctx, moduleID)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("module %s not found", moduleID))
		}
		modulesByID[moduleID] = mod
	}

	// Persist pass runs every row, errors pool up
	var errs []error
	var recreateServers []string
	for serverID, server := range serversByID {
		if err := s.flipServerPorts(ctx, server, serverPortPlan[serverID]); err != nil {
			errs = append(errs, err)
			continue
		}
		if port, ok := proposed[serverID]; ok {
			if err := s.applyServerRouting(ctx, server, nil, "", port, false, true); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if server.ContainerId != "" {
			recreateServers = append(recreateServers, server.Id)
		}
	}

	// Module rows flip to direct binds on their landing ports
	var recreate []string
	for moduleID, landing := range portsByModule {
		mod := modulesByID[moduleID]
		for _, port := range mod.Ports {
			if port == nil || !port.ProxyEnabled {
				continue
			}
			if proposed, ok := landing[port.Name]; ok {
				port.HostPort = proposed
			}
			port.ProxyEnabled = false
		}

		if err := s.store.UpdateModule(ctx, mod); err != nil {
			errs = append(errs, fmt.Errorf("failed to update module %s", mod.Name))
			continue
		}

		if mod.ContainerId != "" {
			recreate = append(recreate, mod.Id)
		}
	}

	// Auto listener rows retire with their routed demand
	if listeners, lerr := s.store.ListProxyListeners(ctx); lerr == nil {
		referenced := make(map[string]bool)
		if servers, serr := s.store.ListServers(ctx); serr == nil {
			for _, server := range servers {
				if server.ProxyListenerId != "" {
					referenced[server.ProxyListenerId] = true
				}
			}
		}
		for _, l := range listeners {
			if l.AutoCreated && !l.IsDefault && !referenced[l.Id] {
				if err := s.store.DeleteProxyListener(ctx, l.Id); err != nil {
					s.log.Error("Failed to retire auto listener on port %d: %v", l.Port, err)
				}
			}
		}
	}

	// Survivors still rebind, the pooled error reports the rest
	if len(errs) > 0 {
		return recreateServers, recreate, connect.NewError(connect.CodeInternal, errors.Join(errs...))
	}
	return recreateServers, recreate, nil
}

// Flips a server's proxied ports onto their planned numbers
func (s *ProxyService) flipServerPorts(ctx context.Context, server *v1.Server, landing []int32) error {
	if !proxy.HasProxyPorts(server.AdditionalPorts) {
		return nil
	}
	next := 0
	for _, port := range server.AdditionalPorts {
		if port == nil || !port.ProxyEnabled || port.HostPort <= 0 {
			continue
		}
		if next < len(landing) {
			port.HostPort = landing[next]
			next++
		}
		port.ProxyEnabled = false
	}
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to persist converted ports for %s: %v", server.Name, err)
		return fmt.Errorf("failed to convert ports for server %s", server.Name)
	}
	return nil
}

// Rebinds a converted server's container onto direct ports
func (s *ProxyService) recreateForConvert(ctx context.Context, server *v1.Server) {
	if server.ContainerId == "" || s.docker == nil {
		return
	}
	serverConfig, err := s.store.GetServerProperties(ctx, server.Id)
	if err != nil {
		s.log.Error("Failed to get server config for %s: %v", server.Name, err)
		return
	}
	result, err := s.docker.RecreateContainer(ctx, server.ContainerId, server, serverConfig, nil)
	if err != nil {
		s.log.Error("Failed to recreate container for %s after convert: %v", server.Name, err)
		server.Status = v1.ServerStatus_SERVER_STATUS_ERROR
		if result != nil && result.NewContainerID != "" {
			server.ContainerId = result.NewContainerID
		} else {
			server.ContainerId = ""
		}
	} else {
		server.ContainerId = result.NewContainerID
		if result.WasRunning {
			server.Status = v1.ServerStatus_SERVER_STATUS_RUNNING
		} else {
			server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
		}
	}
	fields := map[string]any{"container_id": server.ContainerId, "status": server.Status}
	if err := s.store.UpdateServerFields(ctx, server.Id, fields); err != nil {
		s.log.Error("Failed to persist container for %s: %v", server.Name, err)
	}
}

// Gets proxy listeners
func (s *ProxyService) GetProxyListeners(ctx context.Context, req *connect.Request[v1.GetProxyListenersRequest]) (*connect.Response[v1.GetProxyListenersResponse], error) {
	listeners, err := s.store.ListProxyListeners(ctx)
	if err != nil {
		s.log.Error("Failed to get proxy listeners: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get proxy listeners"))
	}

	// Get all servers to count usage
	servers, _ := s.store.ListServers(ctx)
	serverCounts := make(map[string]int32, len(listeners))
	for _, server := range servers {
		serverCounts[server.ProxyListenerId]++
	}

	// One registry derivation serves every listener row
	demand := s.listenerDemandByPort(ctx)

	protoListeners := make([]*v1.ProxyListenerWithCount, len(listeners))
	for i, listener := range listeners {
		protoListeners[i] = &v1.ProxyListenerWithCount{
			Listener:      listener,
			ServerCount:   serverCounts[listener.Id],
			WorkloadCount: int32(demand[listener.Port]),
		}
	}

	return connect.NewResponse(&v1.GetProxyListenersResponse{
		Listeners: protoListeners,
	}), nil
}

// Creates a proxy listener
func (s *ProxyService) CreateProxyListener(ctx context.Context, req *connect.Request[v1.CreateProxyListenerRequest]) (*connect.Response[v1.CreateProxyListenerResponse], error) {
	msg := req.Msg

	// Concurrent creates must see each other, so id comes first
	listenerID := uuid.New().String()

	// Registry checkout guards the socket until the row persists
	netClaim, err := s.proxyManager.CheckoutNetwork(ctx, proxy.NetOwner{Kind: proxy.OwnerListener, ID: listenerID},
		[]proxy.NetRequest{{
			Port:   int(msg.Port),
			Socket: true,
			Detail: msg.Name,
		}})
	if err != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, err)
	}
	defer netClaim.Release()

	listener := &v1.ProxyListener{
		Id:          listenerID,
		Name:        msg.Name,
		Description: msg.Description,
		Port:        msg.Port,
		Enabled:     msg.Enabled,
		IsDefault:   msg.IsDefault,
	}

	if err := s.store.CreateProxyListener(ctx, listener); err != nil {
		s.log.Error("Failed to create proxy listener: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create proxy listener"))
	}
	netClaim.Confirm()

	// A new default demotes every other default
	if listener.IsDefault {
		if listeners, lerr := s.store.ListProxyListeners(ctx); lerr == nil {
			for _, l := range listeners {
				if l.Id != listener.Id && l.IsDefault {
					l.IsDefault = false
					s.store.UpdateProxyListener(ctx, l)
				}
			}
		}
	}

	// Reconcile starts the socket when the proxy is on
	syncRoutes(ctx, s.proxyManager, s.log, "after listener create")

	return connect.NewResponse(&v1.CreateProxyListenerResponse{Listener: listener}), nil
}

// Reconciles proxy sockets and routes, logging any failure
func syncRoutes(ctx context.Context, pm *proxy.Manager, log *logger.Logger, why string) {
	if pm == nil {
		return
	}
	if err := pm.SyncListeners(ctx); err != nil {
		log.Error("Failed to sync listeners %s: %v", why, err)
	}
}

// Claims network slices, failed checkouts retire stray rows
func checkoutNetwork(ctx context.Context, pm *proxy.Manager, log *logger.Logger, owner proxy.NetOwner, netReqs []proxy.NetRequest) (*proxy.NetClaim, error) {
	if err := pm.EnsureListenersFor(ctx, netReqs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	claim, err := pm.CheckoutNetwork(ctx, owner, netReqs)
	if err != nil {
		// Reconcile retires any listener row made just above
		syncRoutes(ctx, pm, log, "after checkout failure")
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return claim, nil
}

// Routed and relay reservation counts bucketed by port
func (s *ProxyService) listenerDemandByPort(ctx context.Context) map[int32]int {
	counts := make(map[int32]int)
	reservations, err := s.proxyManager.Reservations(ctx)
	if err != nil {
		return counts
	}
	for _, r := range reservations {
		pb := r.Proto()
		if pb.Kind == v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_ROUTED ||
			pb.Kind == v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_RELAY {
			counts[pb.Port]++
		}
	}
	return counts
}

// Routed and relay reservations riding a listener port
func (s *ProxyService) listenerDemand(ctx context.Context, port int32) int {
	return s.listenerDemandByPort(ctx)[port]
}

// Updates a proxy listener
func (s *ProxyService) UpdateProxyListener(ctx context.Context, req *connect.Request[v1.UpdateProxyListenerRequest]) (*connect.Response[v1.UpdateProxyListenerResponse], error) {
	msg := req.Msg

	// Panel listener follows the server config, never edits
	if msg.Id == proxy.PanelListenerID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the panel listener follows the server config"))
	}

	listener, err := s.store.GetProxyListener(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("listener not found"))
	}

	// Ports pin at creation, routed workloads name them
	if msg.Port != 0 && msg.Port != listener.Port {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("listener ports cannot change, create a new listener instead"))
	}

	// The sole default stays default until another takes over
	if listener.IsDefault && !msg.IsDefault {
		hasOther := false
		if listeners, lerr := s.store.ListProxyListeners(ctx); lerr == nil {
			for _, l := range listeners {
				if l.Id != listener.Id && l.IsDefault {
					hasOther = true
					break
				}
			}
		}
		if !hasOther {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("make another listener the default first"))
		}
	}

	// Disable needs the routed workloads moved first
	if listener.Enabled && !msg.Enabled {
		if demand := s.listenerDemand(ctx, listener.Port); demand > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("%d routed workloads use port %d, move them first", demand, listener.Port))
		}
	}

	// Update fields
	listener.Name = msg.Name
	listener.Description = msg.Description
	listener.Enabled = msg.Enabled
	listener.IsDefault = msg.IsDefault

	// If setting as default, unset other defaults
	if msg.IsDefault {
		listeners, _ := s.store.ListProxyListeners(ctx)
		for _, l := range listeners {
			if l.Id != msg.Id && l.IsDefault {
				l.IsDefault = false
				s.store.UpdateProxyListener(ctx, l)
			}
		}
	}

	if err := s.store.UpdateProxyListener(ctx, listener); err != nil {
		s.log.Error("Failed to update proxy listener: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update proxy listener"))
	}

	// Reconcile moves sockets and keeps routes registered
	syncRoutes(ctx, s.proxyManager, s.log, "after listener update")

	return connect.NewResponse(&v1.UpdateProxyListenerResponse{Listener: listener}), nil
}

// Deletes a proxy listener
func (s *ProxyService) DeleteProxyListener(ctx context.Context, req *connect.Request[v1.DeleteProxyListenerRequest]) (*connect.Response[v1.DeleteProxyListenerResponse], error) {
	// Panel listener exists as long as the panel does
	if req.Msg.Id == proxy.PanelListenerID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the panel listener cannot be removed"))
	}

	listener, err := s.store.GetProxyListener(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("listener not found"))
	}

	// The proxy keeps one listener minimum while enabled
	if s.proxyManager.Enabled() {
		if listeners, lerr := s.store.ListProxyListeners(ctx); lerr == nil {
			// Panel row does not count toward the minimum
			real := 0
			for _, l := range listeners {
				if l.Id != proxy.PanelListenerID {
					real++
				}
			}
			if real <= 1 {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("the proxy needs at least one listener"))
			}
		}
	}

	if demand := s.listenerDemand(ctx, listener.Port); demand > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%d routed workloads use port %d, move them first", demand, listener.Port))
	}

	if err := s.store.DeleteProxyListener(ctx, req.Msg.Id); err != nil {
		if strings.Contains(err.Error(), "still referenced by") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		} else {
			s.log.Error("Failed to delete proxy listener: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete proxy listener"))
		}
	}

	// Reconcile stops the socket and promotes a new default
	syncRoutes(ctx, s.proxyManager, s.log, "after listener delete")

	return connect.NewResponse(&v1.DeleteProxyListenerResponse{}), nil
}

// Gets server routing configuration
func (s *ProxyService) GetServerRouting(ctx context.Context, req *connect.Request[v1.GetServerRoutingRequest]) (*connect.Response[v1.GetServerRoutingResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}

	// Check if proxy is enabled and get current route
	var currentRoute *v1.ServerRoute
	if s.proxyManager != nil {
		for _, entry := range s.proxyManager.RouteEntries() {
			if entry.Route.OwnerKind == proxy.OwnerServer && entry.Route.OwnerID == server.Id {
				currentRoute = &v1.ServerRoute{
					Hostname: entry.Route.Hostname,
				}
				break
			}
		}
	}

	// Get listen port from the listener if assigned
	var listenPort int32
	if server.ProxyListenerId != "" {
		if listener, err := s.store.GetProxyListener(ctx, server.ProxyListenerId); err == nil {
			listenPort = listener.Port
		}
	}
	if listenPort == 0 {
		// Default listener row carries the fallback port
		if listeners, err := s.store.ListProxyListeners(ctx); err == nil {
			for _, l := range listeners {
				if l.IsDefault {
					listenPort = l.Port
					break
				}
			}
			if listenPort == 0 && len(listeners) > 0 {
				listenPort = listeners[0].Port
			}
		}
	}

	return connect.NewResponse(&v1.GetServerRoutingResponse{
		ProxyEnabled:    s.proxyManager.Enabled(),
		ProxyHostnames:  server.ProxyHostnames,
		ProxyListenerId: server.ProxyListenerId,
		ListenPort:      listenPort,
		CurrentRoute:    currentRoute,
		ProxyCatchAll:   server.ProxyCatchAll,
	}), nil
}

// Full network snapshot for the topology view
func (s *ProxyService) GetNetworkTopology(ctx context.Context, req *connect.Request[v1.GetNetworkTopologyRequest]) (*connect.Response[v1.GetNetworkTopologyResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy manager not available"))
	}

	reservations, err := s.proxyManager.Reservations(ctx)
	if err != nil {
		s.log.Error("Failed to derive reservations: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to derive reservations"))
	}
	protoReservations := make([]*v1.NetworkReservation, len(reservations))
	for i, r := range reservations {
		protoReservations[i] = r.Proto()
	}

	panelPort := 0
	if p, err := strconv.Atoi(s.config.Server.Port); err == nil {
		panelPort = p
	}

	lanIP, publicIP, gatewayIP := s.proxyManager.NetworkAddresses()

	return connect.NewResponse(&v1.GetNetworkTopologyResponse{
		ProxyEnabled: s.proxyManager.Enabled(),
		ProxyRunning: s.proxyManager.IsRunning(),
		PanelPort:    int32(panelPort),
		Reservations: protoReservations,
		Routes:       s.buildProxyRoutes(),
		PublicIp:     publicIP,
		LanIp:        lanIP,
		GatewayIp:    gatewayIP,
	}), nil
}

// Updates server routing configuration
func (s *ProxyService) UpdateServerRouting(ctx context.Context, req *connect.Request[v1.UpdateServerRoutingRequest]) (*connect.Response[v1.UpdateServerRoutingResponse], error) {
	msg := req.Msg

	server, err := getServer(ctx, s.store, msg.ServerId)
	if err != nil {
		return nil, err
	}

	oldProxyListenerID := server.ProxyListenerId

	hostnames, err := proxy.NormalizeHostnames(msg.ProxyHostnames)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Routing needs the proxy on
	if len(hostnames) > 0 && !s.proxyManager.Enabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proxy is disabled"))
	}

	// Determine new listener ID
	listenerID := msg.ProxyListenerId
	if listenerID == "" && len(hostnames) > 0 {
		// Uses existing or default listener when enabling without one
		if oldProxyListenerID != "" {
			listenerID = oldProxyListenerID
		} else {
			// Find default listener
			listeners, err := s.store.ListProxyListeners(ctx)
			if err == nil {
				for _, l := range listeners {
					if l.IsDefault && l.Enabled {
						listenerID = l.Id
						break
					}
				}
				// If no default, use first enabled listener
				if listenerID == "" {
					for _, l := range listeners {
						if l.Enabled {
							listenerID = l.Id
							break
						}
					}
				}
			}
		}
		if listenerID == "" && len(hostnames) > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no proxy listener available"))
		}
	}

	// Clear listener if disabling proxy
	if len(hostnames) == 0 {
		listenerID = ""
	}

	requestedPort := int32(0)
	if msg.Port != nil {
		requestedPort = *msg.Port
	}
	// Catch all only means something in proxied mode
	catchAll := msg.ProxyCatchAll && len(hostnames) > 0
	if err := s.applyServerRouting(ctx, server, hostnames, listenerID, requestedPort, catchAll, false); err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.UpdateServerRoutingResponse{
		Hostnames:       hostnames,
		ProxyListenerId: listenerID,
	}), nil
}

// Applies a routing shape to one server end to end
func (s *ProxyService) applyServerRouting(ctx context.Context, server *v1.Server, hostnames []string, listenerID string, requestedPort int32, catchAll bool, planned bool) error {
	oldProxyHostnames := server.ProxyHostnames
	oldProxyListenerID := server.ProxyListenerId

	// Detect what changed
	hostnamesChanged := !slices.Equal(oldProxyHostnames, hostnames)
	listenerChanged := oldProxyListenerID != listenerID
	proxyModeChanged := (len(oldProxyHostnames) == 0) != (len(hostnames) == 0)

	// Direct mode binds the requested host port
	oldPort := server.Port
	newPort := oldPort
	if len(hostnames) == 0 && requestedPort > 0 {
		newPort = requestedPort
	}
	// Conversions without a pick get a registry port
	if len(hostnames) == 0 && len(oldProxyHostnames) > 0 && requestedPort <= 0 {
		free, ferr := s.proxyManager.FindFreePort(ctx, proxy.FreePortOpts{
			Protocol:   v1.ModuleProtocol_MODULE_PROTOCOL_TCP,
			Start:      s.config.Proxy.PortRangeMin,
			End:        65535,
			RconShadow: true,
		})
		if ferr != nil {
			return connect.NewError(connect.CodeResourceExhausted, ferr)
		}
		newPort = int32(free)
	}
	if len(hostnames) > 0 {
		// Proxied containers always listen on the default inside
		newPort = storage.MinecraftDefaultPort
	}
	portChanged := newPort != oldPort

	// Registry checkout guards the new network shape until persist
	var netClaim *proxy.NetClaim
	if !planned {
		proxyOn := s.proxyManager.Enabled()
		var netReqs []proxy.NetRequest
		if len(hostnames) > 0 {
			listener, lerr := s.store.GetProxyListener(ctx, listenerID)
			if lerr != nil || listener == nil {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proxy listener not found"))
			}
			netReqs = proxy.ServerProxiedNetRequests(hostnames, int(listener.Port), server.AdditionalPorts, proxyOn, catchAll)
		} else {
			netReqs = proxy.ServerDirectNetRequests(int(newPort), server.AdditionalPorts, proxyOn)
		}
		claim, err := checkoutNetwork(ctx, s.proxyManager, s.log, proxy.NetOwner{Kind: proxy.OwnerServer, ID: server.Id}, netReqs)
		if err != nil {
			return err
		}
		netClaim = claim
		defer netClaim.Release()
	}

	// Planned conversions rebind after the sockets release
	needsRecreation := !planned &&
		(proxyModeChanged || (listenerChanged && len(hostnames) > 0 && len(oldProxyHostnames) > 0) || (portChanged && len(hostnames) == 0))

	// Update server fields
	server.ProxyHostnames = hostnames
	server.ProxyListenerId = listenerID
	server.ProxyCatchAll = catchAll
	server.Port = newPort
	// Map updates bypass serializers, store json text
	hostnamesJSON, _ := json.Marshal(hostnames)
	fields := map[string]any{
		"proxy_hostnames":   string(hostnamesJSON),
		"proxy_listener_id": listenerID,
		"proxy_catch_all":   catchAll,
		"port":              newPort,
	}

	// Recreate inputs load before anything commits
	recreate := needsRecreation && server.ContainerId != "" && s.docker != nil
	var serverConfig *v1.ServerProperties
	if recreate {
		cfg, err := s.store.GetServerProperties(ctx, server.Id)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
		}
		serverConfig = cfg
	}

	if hostnamesChanged || listenerChanged {
		msgText := "routing disabled"
		if len(hostnames) > 0 {
			msgText = "routed hostnames " + strings.Join(hostnames, ", ")
		}
		s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_ROUTING_UPDATE, metrics.Attrs{"hostnames": strings.Join(hostnames, ","), "listener": listenerID}, "%s", msgText)
	}

	// Routing lands durably before the container is touched
	if err := s.store.UpdateServerFields(ctx, server.Id, fields); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}
	netClaim.Confirm()

	// Handle container recreation if needed
	if recreate {
		result, err := s.docker.RecreateContainer(ctx, server.ContainerId, server, serverConfig, nil)
		if err != nil {
			s.log.Error("Failed to recreate container for proxy change: %v", err)
			if result != nil && result.NewContainerID != "" {
				server.ContainerId = result.NewContainerID
				server.Status = v1.ServerStatus_SERVER_STATUS_ERROR
			} else {
				server.Status = v1.ServerStatus_SERVER_STATUS_ERROR
				server.ContainerId = ""
			}
		} else {
			server.ContainerId = result.NewContainerID
			if result.WasRunning {
				server.Status = v1.ServerStatus_SERVER_STATUS_RUNNING
			} else {
				server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
			}
		}

		// New identity persists right after the destructive swap
		if perr := s.store.UpdateServerFields(ctx, server.Id, map[string]any{
			"container_id": server.ContainerId,
			"status":       server.Status,
		}); perr != nil {
			s.log.Error("Server %s swapped to container %q but persist failed: %v", server.Id, server.ContainerId, perr)
			return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
		}

		s.log.Info("Container recreated for server %s (proxy: %v -> %v, listener: %s -> %s)",
			server.Name, oldProxyHostnames, hostnames, oldProxyListenerID, listenerID)
	}

	// Reconcile drops stale routes and registers new ones
	syncRoutes(ctx, s.proxyManager, s.log, "after routing change")

	return nil
}
