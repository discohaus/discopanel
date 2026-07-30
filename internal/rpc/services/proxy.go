package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/module"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
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
		pr := stats[route.ServerID]
		if pr == nil {
			pr = &v1.ProxyRoute{}
		}
		pr.ServerId = ""
		if route.OwnerKind == proxy.OwnerServer {
			pr.ServerId = route.OwnerID
		}
		pr.Hostname = route.Hostname
		pr.BackendHost = route.BackendHost
		pr.BackendPort = int32(route.BackendPort)
		pr.Active = true
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
			Enabled: s.proxyManager.Enabled(),
			BaseUrl: s.proxyManager.BaseURL(),
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

	effectiveBaseURL, baseURLSource := s.proxyManager.EffectiveBaseDomain()

	return connect.NewResponse(&v1.GetProxyStatusResponse{
		Enabled:          proxyConfig.Enabled,
		BaseUrl:          proxyConfig.BaseUrl,
		ListenPorts:      listenPorts,
		Listeners:        listeners,
		ListenPort:       primaryPort,
		Running:          running,
		ActiveRoutes:     activeRoutes,
		EffectiveBaseUrl: effectiveBaseURL,
		BaseUrlSource:    baseURLSource,
	}), nil
}

// Updates proxy configuration
func (s *ProxyService) UpdateProxyConfig(ctx context.Context, req *connect.Request[v1.UpdateProxyConfigRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	msg := req.Msg
	baseURL := proxy.NormalizeHostname(msg.BaseUrl)

	// Disable converts proxied workloads to direct binds first
	var recreateModules []string
	if !msg.Enabled && s.proxyManager.Enabled() {
		ids, err := s.convertForDisable(ctx, msg)
		if err != nil {
			return nil, err
		}
		recreateModules = ids
	}

	// Save to database
	proxyConfig := &v1.ProxyConfig{
		Id:      "default",
		Enabled: msg.Enabled,
		BaseUrl: baseURL,
	}

	if err := s.store.SaveProxyConfig(ctx, proxyConfig); err != nil {
		s.log.Error("Failed to save proxy configuration: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save proxy configuration"))
	}

	s.log.Info("Proxy configuration saved to database: enabled=%v, base_url=%v", msg.Enabled, baseURL)

	// Manager owns runtime state and reconciles sockets
	if s.proxyManager != nil {
		if err := s.proxyManager.ApplyConfig(ctx, msg.Enabled, baseURL); err != nil {
			s.log.Error("Failed to apply proxy configuration: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to apply proxy configuration: %w", err))
		}
	}

	// Converted module ports rebind once sockets are gone
	for _, id := range recreateModules {
		if err := s.moduleManager.RecreateModule(ctx, id); err != nil {
			s.log.Error("Failed to recreate module %s after convert: %v", id, err)
		}
	}

	// Return updated status, callers read it like GetProxyStatus
	return s.GetProxyStatus(ctx, connect.NewRequest(&v1.GetProxyStatusRequest{}))
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
		if server.ProxyHostname == "" {
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
			Hostname:     server.ProxyHostname,
			ProposedPort: int32(port),
		})
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
func (s *ProxyService) convertForDisable(ctx context.Context, msg *v1.UpdateProxyConfigRequest) ([]string, error) {
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
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to compute disable impact: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to compute disable impact"))
	}
	if len(impact.Servers) == 0 && len(impact.ModulePorts) == 0 {
		return nil, nil
	}
	if !msg.ConvertToDirect {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%d proxied servers and %d proxied module ports need direct ports first", len(impact.Servers), len(impact.ModulePorts)))
	}

	// Servers convert one at a time with container recreates
	for _, sv := range impact.Servers {
		server, err := s.store.GetServer(ctx, sv.ServerId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server %s not found", sv.ServerId))
		}
		if err := s.applyServerRouting(ctx, server, "", "", sv.ProposedPort); err != nil {
			return nil, err
		}
	}

	// Module rows flip to direct binds on their landing ports
	portsByModule := make(map[string]map[string]int32)
	for _, mp := range impact.ModulePorts {
		if portsByModule[mp.ModuleId] == nil {
			portsByModule[mp.ModuleId] = make(map[string]int32)
		}
		portsByModule[mp.ModuleId][mp.PortName] = mp.ProposedHostPort
	}
	var recreate []string
	for moduleID, landing := range portsByModule {
		module, err := s.store.GetModule(ctx, moduleID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("module %s not found", moduleID))
		}
		for _, port := range module.Ports {
			if port == nil || !port.ProxyEnabled {
				continue
			}
			if proposed, ok := landing[port.Name]; ok {
				port.HostPort = proposed
			}
			port.ProxyEnabled = false
		}

		claim, err := s.proxyManager.CheckoutNetwork(ctx,
			proxy.NetOwner{Kind: proxy.OwnerModule, ID: module.Id},
			s.proxyManager.ModuleNetRequests(module, ""))
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}

		if err := s.store.UpdateModule(ctx, module); err != nil {
			claim.Release()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update module %s", module.Name))
		}
		claim.Confirm()

		if module.ContainerId != "" {
			recreate = append(recreate, module.Id)
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

	return recreate, nil
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

	// Convert to proto format with usage counts
	protoListeners := make([]*v1.ProxyListenerWithCount, len(listeners))
	for i, listener := range listeners {
		// Count servers using this listener
		count := int32(0)
		for _, server := range servers {
			if server.ProxyListenerId == listener.Id {
				count++
			}
		}

		protoListeners[i] = &v1.ProxyListenerWithCount{
			Listener:      listener,
			ServerCount:   count,
			WorkloadCount: int32(s.listenerDemand(ctx, listener.Port)),
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
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync listeners: %v", err)
	}

	return connect.NewResponse(&v1.CreateProxyListenerResponse{Listener: listener}), nil
}

// Routed and relay reservations riding a listener port
func (s *ProxyService) listenerDemand(ctx context.Context, port int32) int {
	reservations, err := s.proxyManager.Reservations(ctx)
	if err != nil {
		return 0
	}
	count := 0
	for _, r := range reservations {
		pb := r.Proto()
		if pb.Port != port {
			continue
		}
		if pb.Kind == v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_ROUTED ||
			pb.Kind == v1.NetworkReservationKind_NETWORK_RESERVATION_KIND_RELAY {
			count++
		}
	}
	return count
}

// Updates a proxy listener
func (s *ProxyService) UpdateProxyListener(ctx context.Context, req *connect.Request[v1.UpdateProxyListenerRequest]) (*connect.Response[v1.UpdateProxyListenerResponse], error) {
	msg := req.Msg

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
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync listeners: %v", err)
	}

	return connect.NewResponse(&v1.UpdateProxyListenerResponse{Listener: listener}), nil
}

// Deletes a proxy listener
func (s *ProxyService) DeleteProxyListener(ctx context.Context, req *connect.Request[v1.DeleteProxyListenerRequest]) (*connect.Response[v1.DeleteProxyListenerResponse], error) {
	listener, err := s.store.GetProxyListener(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("listener not found"))
	}

	// The proxy keeps one listener minimum while enabled
	if s.proxyManager.Enabled() {
		if listeners, lerr := s.store.ListProxyListeners(ctx); lerr == nil && len(listeners) <= 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("the proxy needs at least one listener"))
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
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync listeners: %v", err)
	}

	return connect.NewResponse(&v1.DeleteProxyListenerResponse{}), nil
}

// Gets server routing configuration
func (s *ProxyService) GetServerRouting(ctx context.Context, req *connect.Request[v1.GetServerRoutingRequest]) (*connect.Response[v1.GetServerRoutingResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	// Get suggested hostname if not set
	effectiveBaseURL, baseURLSource := s.proxyManager.EffectiveBaseDomain()
	suggestedHostname := ""
	if server.ProxyHostname == "" && effectiveBaseURL != "" {
		candidate := proxy.NormalizeHostname(strings.ReplaceAll(server.Name, " ", "-") + "." + effectiveBaseURL)
		if proxy.ValidHostname(candidate) {
			suggestedHostname = candidate
		}
	}

	// Check if proxy is enabled and get current route
	var currentRoute *v1.ServerRoute
	if s.proxyManager != nil {
		for _, entry := range s.proxyManager.RouteEntries() {
			if entry.Route.OwnerKind == proxy.OwnerServer && entry.Route.OwnerID == server.Id {
				currentRoute = &v1.ServerRoute{
					Hostname: entry.Route.Hostname,
					Active:   true,
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
		ProxyEnabled:      s.proxyManager.Enabled(),
		ProxyHostname:     server.ProxyHostname,
		ProxyListenerId:   server.ProxyListenerId,
		SuggestedHostname: suggestedHostname,
		BaseUrl:           s.proxyManager.BaseURL(),
		ListenPort:        listenPort,
		CurrentRoute:      currentRoute,
		EffectiveBaseUrl:  effectiveBaseURL,
		BaseUrlSource:     baseURLSource,
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

	effectiveBaseURL, baseURLSource := s.proxyManager.EffectiveBaseDomain()

	return connect.NewResponse(&v1.GetNetworkTopologyResponse{
		ProxyEnabled:     s.proxyManager.Enabled(),
		ProxyRunning:     s.proxyManager.IsRunning(),
		EffectiveBaseUrl: effectiveBaseURL,
		BaseUrlSource:    baseURLSource,
		PanelPort:        int32(panelPort),
		Reservations:     protoReservations,
		Routes:           s.buildProxyRoutes(),
	}), nil
}

// Updates server routing configuration
func (s *ProxyService) UpdateServerRouting(ctx context.Context, req *connect.Request[v1.UpdateServerRoutingRequest]) (*connect.Response[v1.UpdateServerRoutingResponse], error) {
	msg := req.Msg

	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	oldProxyListenerID := server.ProxyListenerId

	// Normalize hostname, registry checkout validates it later
	hostname := proxy.NormalizeHostname(msg.ProxyHostname)

	// Routing needs the proxy on
	if hostname != "" && !s.proxyManager.Enabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proxy is disabled"))
	}

	// Determine new listener ID
	listenerID := msg.ProxyListenerId
	if listenerID == "" && hostname != "" {
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
		if listenerID == "" && hostname != "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no proxy listener available"))
		}
	}

	// Clear listener if disabling proxy
	if hostname == "" {
		listenerID = ""
	}

	requestedPort := int32(0)
	if msg.Port != nil {
		requestedPort = *msg.Port
	}
	if err := s.applyServerRouting(ctx, server, hostname, listenerID, requestedPort); err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.UpdateServerRoutingResponse{
		Hostname:        hostname,
		ProxyListenerId: listenerID,
	}), nil
}

// Applies a routing shape to one server end to end
func (s *ProxyService) applyServerRouting(ctx context.Context, server *v1.Server, hostname, listenerID string, requestedPort int32) error {
	oldProxyHostname := server.ProxyHostname
	oldProxyListenerID := server.ProxyListenerId

	// Detect what changed
	hostnameChanged := oldProxyHostname != hostname
	listenerChanged := oldProxyListenerID != listenerID
	proxyModeChanged := (oldProxyHostname == "") != (hostname == "")

	// Direct mode binds the requested host port
	oldPort := server.Port
	newPort := oldPort
	if hostname == "" && requestedPort > 0 {
		newPort = requestedPort
	}
	// Conversions without a pick get a registry port
	if hostname == "" && oldProxyHostname != "" && requestedPort <= 0 {
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
	if hostname != "" {
		// Proxied containers always listen on the default inside
		newPort = storage.MinecraftDefaultPort
	}
	portChanged := newPort != oldPort

	// Registry checkout guards the new network shape until persist
	proxyOn := s.proxyManager.Enabled()
	var netReqs []proxy.NetRequest
	if hostname != "" {
		listener, lerr := s.store.GetProxyListener(ctx, listenerID)
		if lerr != nil || listener == nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proxy listener not found"))
		}
		netReqs = proxy.ServerProxiedNetRequests(hostname, int(listener.Port), server.AdditionalPorts, proxyOn)
	} else {
		netReqs = proxy.ServerDirectNetRequests(int(newPort), server.AdditionalPorts, proxyOn)
	}
	if err := s.proxyManager.EnsureListenersFor(ctx, netReqs); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	netClaim, err := s.proxyManager.CheckoutNetwork(ctx, proxy.NetOwner{Kind: proxy.OwnerServer, ID: server.Id}, netReqs)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	defer netClaim.Release()

	// Recreate container if proxy mode, listener, or port changes
	needsRecreation := proxyModeChanged || (listenerChanged && hostname != "" && oldProxyHostname != "") || (portChanged && hostname == "")

	// Update server fields
	server.ProxyHostname = hostname
	server.ProxyListenerId = listenerID
	server.Port = newPort
	fields := map[string]any{
		"proxy_hostname":    hostname,
		"proxy_listener_id": listenerID,
		"port":              newPort,
	}

	// Handle container recreation if needed
	if needsRecreation && server.ContainerId != "" && s.docker != nil {
		serverConfig, err := s.store.GetServerProperties(ctx, server.Id)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
		}

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

		fields["container_id"] = server.ContainerId
		fields["status"] = server.Status

		s.log.Info("Container recreated for server %s (proxy: %q -> %q, listener: %s -> %s)",
			server.Name, oldProxyHostname, hostname, oldProxyListenerID, listenerID)
	}

	if hostnameChanged || listenerChanged {
		msgText := "routing disabled"
		if hostname != "" {
			msgText = "routed hostname " + hostname
		}
		s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_ROUTING_UPDATE, metrics.Attrs{"hostname": hostname, "listener": listenerID}, "%s", msgText)
	}

	// Save only the columns this request owns
	if err := s.store.UpdateServerFields(ctx, server.Id, fields); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}
	netClaim.Confirm()

	// Reconcile drops stale routes and registers new ones
	if s.proxyManager != nil {
		if err := s.proxyManager.SyncListeners(ctx); err != nil {
			s.log.Error("Failed to sync routes after routing change: %v", err)
		}
	}

	return nil
}
