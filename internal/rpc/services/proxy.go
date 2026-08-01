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
	"google.golang.org/protobuf/types/known/timestamppb"
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
		StrictHttps:      proxyConfig.StrictHttps,
	}), nil
}

// Updates proxy configuration
func (s *ProxyService) UpdateProxyConfig(ctx context.Context, req *connect.Request[v1.UpdateProxyConfigRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	msg := req.Msg
	baseURL := proxy.NormalizeHostname(msg.BaseUrl)

	// Instant sslip names follow the network, never persist
	if strings.HasSuffix(baseURL, ".sslip.io") {
		baseURL = ""
	}

	// Strict https without a panel cert locks the ui out
	if msg.StrictHttps {
		target := baseURL
		if target == "" {
			target, _ = s.proxyManager.AutoDomain()
		}
		if target == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("no panel domain detected yet, HTTPS only cannot turn on"))
		}
		covered := false
		if rows, err := s.store.ListProxyCertificates(ctx); err == nil {
			if row, _, _ := proxy.MatchCertificateRow(rows, target); row != nil {
				covered = true
			}
		}
		if !covered {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("no certificate covers %s, issue or upload one before requiring HTTPS", target))
		}
	}

	// Disable converts proxied workloads to direct binds first
	var recreateServers, recreateModules []string
	if !msg.Enabled && s.proxyManager.Enabled() {
		servers, modules, err := s.convertForDisable(ctx, msg)
		if err != nil {
			return nil, err
		}
		recreateServers = servers
		recreateModules = modules
	}

	// Old row comes back if the runtime apply fails
	prevConfig, _, prevErr := s.store.GetProxyConfig(ctx)

	// Save to database
	proxyConfig := &v1.ProxyConfig{
		Id:          "default",
		Enabled:     msg.Enabled,
		BaseUrl:     baseURL,
		StrictHttps: msg.StrictHttps,
	}

	// Mapping settings ride along untouched
	if prevErr == nil && prevConfig != nil {
		proxyConfig.PortmapKeepalive = prevConfig.PortmapKeepalive
	}

	if err := s.store.SaveProxyConfig(ctx, proxyConfig); err != nil {
		s.log.Error("Failed to save proxy configuration: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save proxy configuration"))
	}

	s.log.Info("Proxy configuration saved to database: enabled=%v, base_url=%v", msg.Enabled, baseURL)

	// Old row comes back if apply or validation fails
	restorePrev := func() {
		if prevErr != nil || prevConfig == nil {
			return
		}
		if rerr := s.store.SaveProxyConfig(ctx, prevConfig); rerr != nil {
			s.log.Error("Failed to restore previous proxy configuration: %v", rerr)
		} else {
			s.proxyManager.ApplyConfig(ctx, prevConfig.Enabled, prevConfig.BaseUrl)
		}
	}

	// Manager owns runtime state and reconciles sockets
	if s.proxyManager != nil {
		if err := s.proxyManager.ApplyConfig(ctx, msg.Enabled, baseURL); err != nil {
			s.log.Error("Failed to apply proxy configuration: %v", err)
			restorePrev()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to apply proxy configuration: %w", err))
		}

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

	// Return updated status, callers read it like GetProxyStatus
	return s.GetProxyStatus(ctx, connect.NewRequest(&v1.GetProxyStatusRequest{}))
}

// Cached access snapshot every surface renders from
func (s *ProxyService) GetAccessStatus(ctx context.Context, req *connect.Request[v1.GetAccessStatusRequest]) (*connect.Response[v1.GetAccessStatusResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy manager not available"))
	}
	snap, err := s.proxyManager.AccessStatus(ctx, false)
	if err != nil {
		s.log.Error("Failed to build access snapshot: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to build access snapshot"))
	}
	return connect.NewResponse(snap), nil
}

// Reruns every probe and refreshes the snapshot
func (s *ProxyService) CheckAccess(ctx context.Context, req *connect.Request[v1.CheckAccessRequest]) (*connect.Response[v1.GetAccessStatusResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy manager not available"))
	}
	snap, err := s.proxyManager.AccessStatus(ctx, true)
	if err != nil {
		s.log.Error("Failed to refresh access snapshot: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to refresh access snapshot"))
	}
	return connect.NewResponse(snap), nil
}

// Plans certificate issuance for hostnames
func (s *ProxyService) GetSecurePlan(ctx context.Context, req *connect.Request[v1.GetSecurePlanRequest]) (*connect.Response[v1.GetSecurePlanResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy manager not available"))
	}
	plan, err := s.proxyManager.SecurePlan(ctx, req.Msg)
	if err != nil {
		s.log.Error("Failed to build secure plan: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to build secure plan"))
	}
	return connect.NewResponse(plan), nil
}

// Deduped active hostnames with certificate coverage
func (s *ProxyService) GetHostnameCoverage(ctx context.Context, req *connect.Request[v1.GetHostnameCoverageRequest]) (*connect.Response[v1.GetHostnameCoverageResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy manager not available"))
	}
	hostnames, err := s.proxyManager.HostnameCoverage(ctx)
	if err != nil {
		s.log.Error("Failed to build hostname coverage: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to build hostname coverage"))
	}
	return connect.NewResponse(&v1.GetHostnameCoverageResponse{Hostnames: hostnames}), nil
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

	// Checkouts stay out, the plan already owns these ports
	converted := make(map[string]bool)
	var recreateServers []string
	for _, sv := range impact.Servers {
		server, err := s.store.GetServer(ctx, sv.ServerId)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server %s not found", sv.ServerId))
		}
		s.flipServerPorts(ctx, server, serverPortPlan[server.Id])
		if err := s.applyServerRouting(ctx, server, "", "", sv.ProposedPort, true); err != nil {
			return nil, nil, err
		}
		converted[server.Id] = true
		if server.ContainerId != "" {
			recreateServers = append(recreateServers, server.Id)
		}
	}

	// Direct servers with proxied ports flip and rebind too
	for serverID := range serverPortPlan {
		if converted[serverID] {
			continue
		}
		server, err := s.store.GetServer(ctx, serverID)
		if err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("server %s not found", serverID))
		}
		s.flipServerPorts(ctx, server, serverPortPlan[serverID])
		if server.ContainerId != "" {
			recreateServers = append(recreateServers, server.Id)
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
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("module %s not found", moduleID))
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

		if err := s.store.UpdateModule(ctx, module); err != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update module %s", module.Name))
		}

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

	return recreateServers, recreate, nil
}

// Flips a server's proxied ports onto their planned numbers
func (s *ProxyService) flipServerPorts(ctx context.Context, server *v1.Server, landing []int32) {
	if !proxy.HasProxyPorts(server.AdditionalPorts) {
		return
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
	}
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
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync listeners: %v", err)
	}

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
	if err := s.applyServerRouting(ctx, server, hostname, listenerID, requestedPort, false); err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.UpdateServerRoutingResponse{
		Hostname:        hostname,
		ProxyListenerId: listenerID,
	}), nil
}

// Lists certificates without key material
func (s *ProxyService) GetProxyCertificates(ctx context.Context, req *connect.Request[v1.GetProxyCertificatesRequest]) (*connect.Response[v1.GetProxyCertificatesResponse], error) {
	rows, err := s.store.ListProxyCertificates(ctx)
	if err != nil {
		s.log.Error("Failed to list certificates: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list certificates"))
	}
	out := make([]*v1.ProxyCertificate, len(rows))
	for i, row := range rows {
		out[i] = row.Redact()
	}
	return connect.NewResponse(&v1.GetProxyCertificatesResponse{Certificates: out}), nil
}

// Normalizes uploaded material and stores it for serving
func (s *ProxyService) UploadProxyCertificate(ctx context.Context, req *connect.Request[v1.UploadProxyCertificateRequest]) (*connect.Response[v1.UploadProxyCertificateResponse], error) {
	msg := req.Msg
	chainPEM, keyPEM, err := proxy.NormalizeCertUpload(msg.Files)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	material, err := proxy.ParseCertificateMaterial(chainPEM, keyPEM)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	sealed, err := s.proxyManager.SealPrivateKey(keyPEM)
	if err != nil {
		s.log.Error("Failed to seal private key: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to seal private key"))
	}

	// Leaf's first name stands in for an omitted name
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		name = material.Domains[0]
	}

	row := &v1.ProxyCertificate{
		Id:            uuid.New().String(),
		Name:          name,
		Domains:       material.Domains,
		Source:        v1.CertificateSource_CERTIFICATE_SOURCE_UPLOADED,
		CertChainPem:  chainPEM,
		PrivateKeyPem: sealed,
		Issuer:        material.Issuer,
		NotBefore:     timestamppb.New(material.NotBefore),
		NotAfter:      timestamppb.New(material.NotAfter),
	}
	if err := s.store.CreateProxyCertificate(ctx, row); err != nil {
		s.log.Error("Failed to store certificate: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to store certificate"))
	}
	if err := s.proxyManager.ReloadCertificates(ctx); err != nil {
		s.log.Error("Failed to reload certificates: %v", err)
	}
	return connect.NewResponse(&v1.UploadProxyCertificateResponse{Certificate: row.Redact()}), nil
}

// Orders certificates from a public acme authority
func (s *ProxyService) OrderProxyCertificate(ctx context.Context, req *connect.Request[v1.OrderProxyCertificateRequest]) (*connect.Response[v1.OrderProxyCertificateResponse], error) {
	msg := req.Msg
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("certificate name is required"))
	}

	solver := v1.AcmeSolver_ACME_SOLVER_HTTP
	credentialID := ""
	if dns := msg.GetDns(); dns != nil {
		solver = v1.AcmeSolver_ACME_SOLVER_DNS_CREDENTIAL
		credentialID = dns.CredentialId
	}
	dnsSolver := solver != v1.AcmeSolver_ACME_SOLVER_HTTP

	var domains []string
	seen := make(map[string]bool)
	for _, domain := range msg.Domains {
		domain = proxy.NormalizeHostname(domain)
		if domain == "" || seen[domain] {
			continue
		}
		if err := proxy.ValidACMEDomain(domain, dnsSolver); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		seen[domain] = true
		domains = append(domains, domain)
	}
	if len(domains) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one domain is required"))
	}

	// Challenge path proven before any order burns budget
	switch solver {
	case v1.AcmeSolver_ACME_SOLVER_HTTP:
		for _, domain := range domains {
			_, _, ok, err := s.proxyManager.ProbeChallengePath(ctx, domain)
			if err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s cannot be probed, %v", domain, err))
			}
			if !ok {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("%s does not echo back on port 80 or 443, fix forwarding and probe again", domain))
			}
		}
	case v1.AcmeSolver_ACME_SOLVER_DNS_CREDENTIAL:
		cred, err := s.store.GetDnsProviderCredential(ctx, credentialID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("dns credential not found"))
		}
		for _, domain := range domains {
			if _, err := s.proxyManager.CheckDnsCredential(ctx, cred, domain); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition,
					fmt.Errorf("%s failed the credential check, %v", domain, err))
			}
		}
	}

	email := strings.TrimSpace(msg.Email)
	directory := strings.TrimSpace(msg.Directory)
	issued, orderErr := s.proxyManager.OrderACMECertificates(ctx, domains, email, directory, solver, credentialID)

	// Issued certificates persist even when a later one failed
	var stored []*v1.ProxyCertificate
	for _, material := range issued {
		row, err := s.proxyManager.StoreACMEMaterial(ctx, name, email, directory, len(domains) > 1, solver, credentialID, &material)
		if err != nil {
			s.log.Error("Failed to store acme certificate for %s: %v", material.Domain, err)
			continue
		}
		stored = append(stored, row.Redact())
	}
	if len(stored) > 0 {
		if err := s.proxyManager.ReloadCertificates(ctx); err != nil {
			s.log.Error("Failed to reload certificates: %v", err)
		}
	}
	if orderErr != nil {
		if len(stored) > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("stored %d of %d certificates, then %v", len(stored), len(domains), orderErr))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, orderErr)
	}
	return connect.NewResponse(&v1.OrderProxyCertificateResponse{Certificates: stored}), nil
}

// Renews an acme certificate immediately
func (s *ProxyService) RenewProxyCertificate(ctx context.Context, req *connect.Request[v1.RenewProxyCertificateRequest]) (*connect.Response[v1.RenewProxyCertificateResponse], error) {
	row, err := s.store.GetProxyCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("certificate not found"))
	}
	if row.Source != v1.CertificateSource_CERTIFICATE_SOURCE_ACME {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("only acme certificates renew"))
	}
	if err := s.proxyManager.RenewACMECertificate(ctx, row); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&v1.RenewProxyCertificateResponse{Certificate: row.Redact()}), nil
}

// Renames or replaces a certificate's pem material
func (s *ProxyService) UpdateProxyCertificate(ctx context.Context, req *connect.Request[v1.UpdateProxyCertificateRequest]) (*connect.Response[v1.UpdateProxyCertificateResponse], error) {
	msg := req.Msg
	row, err := s.store.GetProxyCertificate(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("certificate not found"))
	}

	if name := strings.TrimSpace(msg.Name); name != "" {
		row.Name = name
	}

	if msg.CertChainPem != "" || msg.PrivateKeyPem != "" {
		if msg.CertChainPem == "" || msg.PrivateKeyPem == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("certificate and key must be replaced together"))
		}
		material, err := proxy.ParseCertificateMaterial(msg.CertChainPem, msg.PrivateKeyPem)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		sealed, err := s.proxyManager.SealPrivateKey(msg.PrivateKeyPem)
		if err != nil {
			s.log.Error("Failed to seal private key: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to seal private key"))
		}
		row.CertChainPem = msg.CertChainPem
		row.PrivateKeyPem = sealed
		row.Domains = material.Domains
		row.Issuer = material.Issuer
		row.NotBefore = timestamppb.New(material.NotBefore)
		row.NotAfter = timestamppb.New(material.NotAfter)
		row.Source = v1.CertificateSource_CERTIFICATE_SOURCE_UPLOADED
	}

	if err := s.store.UpdateProxyCertificate(ctx, row); err != nil {
		s.log.Error("Failed to update certificate: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update certificate"))
	}
	if err := s.proxyManager.ReloadCertificates(ctx); err != nil {
		s.log.Error("Failed to reload certificates: %v", err)
	}
	return connect.NewResponse(&v1.UpdateProxyCertificateResponse{Certificate: row.Redact()}), nil
}

// Sets the hostnames a certificate serves by assignment
func (s *ProxyService) AssignProxyCertificate(ctx context.Context, req *connect.Request[v1.AssignProxyCertificateRequest]) (*connect.Response[v1.AssignProxyCertificateResponse], error) {
	row, err := s.store.GetProxyCertificate(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("certificate not found"))
	}

	var hostnames []string
	seen := make(map[string]bool)
	for _, hostname := range req.Msg.Hostnames {
		hostname = proxy.NormalizeHostname(hostname)
		if hostname == "" || seen[hostname] {
			continue
		}
		if !proxy.ValidCertDomain(hostname) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a valid hostname", hostname))
		}
		seen[hostname] = true
		hostnames = append(hostnames, hostname)
	}

	row.AssignedHostnames = hostnames
	if err := s.store.UpdateProxyCertificate(ctx, row); err != nil {
		s.log.Error("Failed to update certificate assignments: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update certificate"))
	}
	if err := s.proxyManager.ReloadCertificates(ctx); err != nil {
		s.log.Error("Failed to reload certificates: %v", err)
	}
	return connect.NewResponse(&v1.AssignProxyCertificateResponse{Certificate: row.Redact()}), nil
}

// Deletes a certificate
func (s *ProxyService) DeleteProxyCertificate(ctx context.Context, req *connect.Request[v1.DeleteProxyCertificateRequest]) (*connect.Response[v1.DeleteProxyCertificateResponse], error) {
	if _, err := s.store.GetProxyCertificate(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("certificate not found"))
	}
	if err := s.store.DeleteProxyCertificate(ctx, req.Msg.Id); err != nil {
		s.log.Error("Failed to delete certificate: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete certificate"))
	}
	if err := s.proxyManager.ReloadCertificates(ctx); err != nil {
		s.log.Error("Failed to reload certificates: %v", err)
	}
	return connect.NewResponse(&v1.DeleteProxyCertificateResponse{}), nil
}

// Applies a routing shape to one server end to end
func (s *ProxyService) applyServerRouting(ctx context.Context, server *v1.Server, hostname, listenerID string, requestedPort int32, planned bool) error {
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
	var netClaim *proxy.NetClaim
	if !planned {
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
		claim, err := s.proxyManager.CheckoutNetwork(ctx, proxy.NetOwner{Kind: proxy.OwnerServer, ID: server.Id}, netReqs)
		if err != nil {
			// Reconcile retires any listener row made just above
			if serr := s.proxyManager.SyncListeners(ctx); serr != nil {
				s.log.Error("Failed to sync after checkout failure: %v", serr)
			}
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		netClaim = claim
		defer netClaim.Release()
	}

	// Planned conversions rebind after the sockets release
	needsRecreation := !planned &&
		(proxyModeChanged || (listenerChanged && hostname != "" && oldProxyHostname != "") || (portChanged && hostname == ""))

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

// Registry of dns providers and their fields
func (s *ProxyService) GetDnsProviders(ctx context.Context, req *connect.Request[v1.GetDnsProvidersRequest]) (*connect.Response[v1.GetDnsProvidersResponse], error) {
	return connect.NewResponse(&v1.GetDnsProvidersResponse{Providers: proxy.DnsProviderKinds()}), nil
}

// Lists dns credentials without secret material
func (s *ProxyService) GetDnsCredentials(ctx context.Context, req *connect.Request[v1.GetDnsCredentialsRequest]) (*connect.Response[v1.GetDnsCredentialsResponse], error) {
	rows, err := s.store.ListDnsProviderCredentials(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list dns credentials"))
	}
	redacted := make([]*v1.DnsProviderCredential, len(rows))
	for i, row := range rows {
		redacted[i] = row.Redact()
	}
	return connect.NewResponse(&v1.GetDnsCredentialsResponse{Credentials: redacted}), nil
}

// Seals and stores credentials for a dns provider
func (s *ProxyService) CreateDnsCredential(ctx context.Context, req *connect.Request[v1.CreateDnsCredentialRequest]) (*connect.Response[v1.CreateDnsCredentialResponse], error) {
	msg := req.Msg
	name := strings.TrimSpace(msg.Name)
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("credential name is required"))
	}
	row := &v1.DnsProviderCredential{
		Id:            uuid.New().String(),
		Name:          name,
		Provider:      strings.TrimSpace(msg.Provider),
		ApiToken:      strings.TrimSpace(msg.ApiToken),
		Nameserver:    strings.TrimSpace(msg.Nameserver),
		TsigKeyName:   strings.TrimSpace(msg.TsigKeyName),
		TsigSecret:    strings.TrimSpace(msg.TsigSecret),
		TsigAlgorithm: strings.TrimSpace(msg.TsigAlgorithm),
	}
	if err := proxy.ValidateDnsCredential(row); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.sealDnsSecrets(row); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.store.CreateDnsProviderCredential(ctx, row); err != nil {
		s.log.Error("Failed to create dns credential: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create dns credential"))
	}
	return connect.NewResponse(&v1.CreateDnsCredentialResponse{Credential: row.Redact()}), nil
}

// Seals secret columns before a credential persists
func (s *ProxyService) sealDnsSecrets(row *v1.DnsProviderCredential) error {
	for _, field := range []*string{&row.ApiToken, &row.TsigSecret} {
		if *field == "" {
			continue
		}
		sealed, err := s.proxyManager.SealPrivateKey(*field)
		if err != nil {
			return fmt.Errorf("failed to seal credential secret: %w", err)
		}
		*field = sealed
	}
	return nil
}

// Renames or replaces credential fields
func (s *ProxyService) UpdateDnsCredential(ctx context.Context, req *connect.Request[v1.UpdateDnsCredentialRequest]) (*connect.Response[v1.UpdateDnsCredentialResponse], error) {
	msg := req.Msg
	row, err := s.store.GetDnsProviderCredential(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("dns credential not found"))
	}

	if name := strings.TrimSpace(msg.Name); name != "" {
		row.Name = name
	}
	changed := false
	apply := func(target *string, value string, secret bool) error {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		if secret {
			sealed, err := s.proxyManager.SealPrivateKey(value)
			if err != nil {
				return err
			}
			value = sealed
		}
		if *target != value {
			*target = value
			changed = true
		}
		return nil
	}
	for _, item := range []struct {
		target *string
		value  string
		secret bool
	}{
		{&row.ApiToken, msg.ApiToken, true},
		{&row.TsigSecret, msg.TsigSecret, true},
		{&row.Nameserver, msg.Nameserver, false},
		{&row.TsigKeyName, msg.TsigKeyName, false},
		{&row.TsigAlgorithm, msg.TsigAlgorithm, false},
	} {
		if err := apply(item.target, item.value, item.secret); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	// Fresh material invalidates the old proof
	if changed {
		row.CheckedDomain = ""
		row.CheckError = ""
		row.CheckedAt = nil
	}
	if err := s.store.UpdateDnsProviderCredential(ctx, row); err != nil {
		s.log.Error("Failed to update dns credential: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update dns credential"))
	}
	return connect.NewResponse(&v1.UpdateDnsCredentialResponse{Credential: row.Redact()}), nil
}

// Deletes a credential no certificate depends on
func (s *ProxyService) DeleteDnsCredential(ctx context.Context, req *connect.Request[v1.DeleteDnsCredentialRequest]) (*connect.Response[v1.DeleteDnsCredentialResponse], error) {
	certs, err := s.store.ListProxyCertificates(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list certificates"))
	}
	for _, cert := range certs {
		if cert.DnsCredentialId == req.Msg.Id {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("certificate %s renews through this credential, delete it first", cert.Name))
		}
	}
	if err := s.store.DeleteDnsProviderCredential(ctx, req.Msg.Id); err != nil {
		s.log.Error("Failed to delete dns credential: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete dns credential"))
	}
	s.proxyManager.InvalidateAccessSnapshot()
	return connect.NewResponse(&v1.DeleteDnsCredentialResponse{}), nil
}

// Proves a credential writes the zone behind a domain
func (s *ProxyService) CheckDnsCredential(ctx context.Context, req *connect.Request[v1.CheckDnsCredentialRequest]) (*connect.Response[v1.CheckDnsCredentialResponse], error) {
	domain := proxy.NormalizeHostname(req.Msg.Domain)
	if domain == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a domain is required to pick the zone"))
	}
	row, err := s.store.GetDnsProviderCredential(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("dns credential not found"))
	}

	zone, checkErr := s.proxyManager.CheckDnsCredential(ctx, row, domain)
	row.CheckedDomain = domain
	row.CheckedAt = timestamppb.Now()
	row.CheckError = ""
	if checkErr != nil {
		row.CheckError = checkErr.Error()
	}
	if err := s.store.UpdateDnsProviderCredential(ctx, row); err != nil {
		s.log.Error("Failed to record dns credential check: %v", err)
	}
	// Proof state feeds the snapshot and secure plans
	s.proxyManager.InvalidateAccessSnapshot()

	resp := &v1.CheckDnsCredentialResponse{
		Ok:   checkErr == nil,
		Zone: strings.TrimSuffix(zone, "."),
	}
	if checkErr != nil {
		resp.Error = checkErr.Error()
	}
	return connect.NewResponse(resp), nil
}

// Detects the dns provider and plans records for a domain
func (s *ProxyService) GetDnsSetup(ctx context.Context, req *connect.Request[v1.GetDnsSetupRequest]) (*connect.Response[v1.GetDnsSetupResponse], error) {
	domain := strings.TrimPrefix(proxy.NormalizeHostname(req.Msg.Domain), "*.")
	if domain == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("domain is required"))
	}
	if !proxy.ValidHostname(domain) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a valid hostname", domain))
	}
	return connect.NewResponse(s.proxyManager.DnsSetup(ctx, domain)), nil
}

// Builds the mapping rpc payload from manager state
func (s *ProxyService) portMappingsResponse(ctx context.Context, state proxy.PortMapState) *v1.GetPortMappingsResponse {
	resp := &v1.GetPortMappingsResponse{Gateway: state.Gateway}
	if cfg, _, err := s.store.GetProxyConfig(ctx); err == nil {
		resp.Keepalive = cfg.PortmapKeepalive
	}
	if !state.AttemptedAt.IsZero() {
		resp.AttemptedAt = timestamppb.New(state.AttemptedAt)
	}
	for _, result := range state.Results {
		resp.Results = append(resp.Results, &v1.PortMappingResult{
			Port:         int32(result.Port),
			Transport:    result.Transport,
			Ok:           result.OK,
			Method:       result.Method,
			LeaseSeconds: int32(result.LeaseSeconds),
			Error:        result.Err,
			Detail:       result.Detail,
		})
	}
	return resp
}

// Last port mapping outcome and keepalive state
func (s *ProxyService) GetPortMappings(ctx context.Context, req *connect.Request[v1.GetPortMappingsRequest]) (*connect.Response[v1.GetPortMappingsResponse], error) {
	return connect.NewResponse(s.portMappingsResponse(ctx, s.proxyManager.PortMappingState())), nil
}

// Asks the router to open the panel's public ports
func (s *ProxyService) AttemptPortMappings(ctx context.Context, req *connect.Request[v1.AttemptPortMappingsRequest]) (*connect.Response[v1.GetPortMappingsResponse], error) {
	keepalive := false
	if cfg, _, err := s.store.GetProxyConfig(ctx); err == nil {
		keepalive = cfg.PortmapKeepalive
	}
	state := s.proxyManager.AttemptPortMappings(ctx, keepalive)
	return connect.NewResponse(s.portMappingsResponse(ctx, state)), nil
}

// Toggles the lease renewal loop
func (s *ProxyService) SetPortMappingKeepalive(ctx context.Context, req *connect.Request[v1.SetPortMappingKeepaliveRequest]) (*connect.Response[v1.GetPortMappingsResponse], error) {
	cfgRow, _, err := s.store.GetProxyConfig(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load proxy configuration"))
	}
	cfgRow.Id = "default"
	cfgRow.PortmapKeepalive = req.Msg.Enabled
	if err := s.store.SaveProxyConfig(ctx, cfgRow); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save proxy configuration"))
	}
	if err := s.proxyManager.SyncPortmapKeepalive(ctx); err != nil {
		s.log.Error("Failed to reconcile port mapping keepalive: %v", err)
	}
	return connect.NewResponse(s.portMappingsResponse(ctx, s.proxyManager.PortMappingState())), nil
}
