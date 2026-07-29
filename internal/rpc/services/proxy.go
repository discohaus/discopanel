package services

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
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
	store        *storage.Store
	docker       *docker.Client
	proxyManager *proxy.Manager
	config       *config.Config
	rec          *metrics.Recorder
	log          *logger.Logger
}

// Creates a new proxy service
func NewProxyService(store *storage.Store, dockerClient *docker.Client, proxyManager *proxy.Manager, cfg *config.Config, rec *metrics.Recorder, log *logger.Logger) *ProxyService {
	return &ProxyService{
		store:        store,
		docker:       dockerClient,
		proxyManager: proxyManager,
		config:       cfg,
		rec:          rec,
		log:          log,
	}
}

// Gets proxy routes
func (s *ProxyService) GetProxyRoutes(ctx context.Context, req *connect.Request[v1.GetProxyRoutesRequest]) (*connect.Response[v1.GetProxyRoutesResponse], error) {
	if s.proxyManager == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("proxy not enabled"))
	}

	routes := s.proxyManager.GetRoutes()
	stats := s.proxyManager.GetRouteStats()

	// Stats snapshots become the rows, route facts fill the rest
	protoRoutes := make([]*v1.ProxyRoute, 0, len(routes))
	for _, route := range routes {
		pr := stats[route.ServerID]
		if pr == nil {
			pr = &v1.ProxyRoute{}
		}
		pr.ServerId = route.ServerID
		pr.Hostname = route.Hostname
		pr.BackendHost = route.BackendHost
		pr.BackendPort = int32(route.BackendPort)
		pr.Active = true
		pr.State = route.State
		pr.Wakeable = route.Wakeable
		pr.ProxyProtocol = route.ProxyProtocol
		pr.PreserveHostname = route.PreserveHost
		protoRoutes = append(protoRoutes, pr)
	}

	return connect.NewResponse(&v1.GetProxyRoutesResponse{
		Routes: protoRoutes,
	}), nil
}

// Gets proxy status
func (s *ProxyService) GetProxyStatus(ctx context.Context, req *connect.Request[v1.GetProxyStatusRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	// Load proxy config from database
	proxyConfig, _, err := s.store.GetProxyConfig(ctx)
	if err != nil {
		s.log.Error("Failed to load proxy configuration: %v", err)
		proxyConfig = &v1.ProxyConfig{
			Enabled: s.config.Proxy.Enabled,
			BaseUrl: s.config.Proxy.BaseUrl,
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

	// Primary port
	var primaryPort int32
	if len(listenPorts) > 0 {
		primaryPort = listenPorts[0]
	}

	// Get running status and active routes count
	running := false
	activeRoutes := int32(0)
	if s.proxyManager != nil {
		running = s.proxyManager.IsRunning()
		routes := s.proxyManager.GetRoutes()
		activeRoutes = int32(len(routes))
	}

	return connect.NewResponse(&v1.GetProxyStatusResponse{
		Enabled:      proxyConfig.Enabled,
		BaseUrl:      proxyConfig.BaseUrl,
		ListenPorts:  listenPorts,
		Listeners:    listeners,
		ListenPort:   primaryPort,
		Running:      running,
		ActiveRoutes: activeRoutes,
	}), nil
}

// Updates proxy configuration
func (s *ProxyService) UpdateProxyConfig(ctx context.Context, req *connect.Request[v1.UpdateProxyConfigRequest]) (*connect.Response[v1.GetProxyStatusResponse], error) {
	msg := req.Msg

	// Save to database
	proxyConfig := &v1.ProxyConfig{
		Id:      "default",
		Enabled: msg.Enabled,
		BaseUrl: msg.BaseUrl,
	}

	if err := s.store.SaveProxyConfig(ctx, proxyConfig); err != nil {
		s.log.Error("Failed to save proxy configuration: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save proxy configuration"))
	}

	// Update in-memory configuration
	s.config.Proxy.Enabled = msg.Enabled
	s.config.Proxy.BaseUrl = msg.BaseUrl

	s.log.Info("Proxy configuration saved to database: enabled=%v, base_url=%v", msg.Enabled, msg.BaseUrl)

	// Starts or stops the manager to match enabled
	if s.proxyManager != nil {
		if msg.Enabled {
			if _, err := s.proxyManager.EnsureDefaultListener(); err != nil {
				s.log.Error("Failed to ensure default listener: %v", err)
			}

			if !s.proxyManager.IsRunning() {
				if err := s.proxyManager.Start(); err != nil {
					s.log.Error("Failed to start proxy manager: %v", err)
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start proxy: %w", err))
				}
			}
		} else if s.proxyManager.IsRunning() {
			if err := s.proxyManager.Stop(); err != nil {
				s.log.Error("Failed to stop proxy manager: %v", err)
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stop proxy: %w", err))
			}
		}
	}

	// Return updated status, callers read it like GetProxyStatus
	return s.GetProxyStatus(ctx, connect.NewRequest(&v1.GetProxyStatusRequest{}))
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

	// Convert to proto format with server count
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
			Listener:    listener,
			ServerCount: count,
		}
	}

	return connect.NewResponse(&v1.GetProxyListenersResponse{
		Listeners: protoListeners,
	}), nil
}

// Creates a proxy listener
func (s *ProxyService) CreateProxyListener(ctx context.Context, req *connect.Request[v1.CreateProxyListenerRequest]) (*connect.Response[v1.CreateProxyListenerResponse], error) {
	msg := req.Msg

	// Validate port
	if msg.Port < 1 || msg.Port > 65535 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid port number"))
	}

	// Check if port is already in use
	existing, _ := s.store.GetProxyListenerByPort(ctx, int(msg.Port))
	if existing != nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("port already in use by another listener"))
	}

	// Check if port is used by a non-proxied server
	servers, _ := s.store.ListServers(ctx)
	for _, server := range servers {
		if server.ProxyHostname == "" && server.Port == msg.Port {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("port already in use by a non-proxied server"))
		}
	}

	listener := &v1.ProxyListener{
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

	// Add the listener to the proxy manager if it's running
	if s.proxyManager != nil {
		if err := s.proxyManager.AddListener(listener); err != nil {
			s.log.Error("Failed to add listener to proxy manager: %v", err)
			// Non-critical, proxy restart picks it up later
		}
	}

	return connect.NewResponse(&v1.CreateProxyListenerResponse{Listener: listener}), nil
}

// Updates a proxy listener
func (s *ProxyService) UpdateProxyListener(ctx context.Context, req *connect.Request[v1.UpdateProxyListenerRequest]) (*connect.Response[v1.UpdateProxyListenerResponse], error) {
	msg := req.Msg

	listener, err := s.store.GetProxyListener(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("listener not found"))
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

	oldPort := listener.Port
	if msg.Port != 0 && msg.Port != int32(oldPort) {
		listener.Port = msg.Port // Update port if provided and different
	}

	if err := s.store.UpdateProxyListener(ctx, listener); err != nil {
		s.log.Error("Failed to update proxy listener: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update proxy listener"))
	}

	// Handle proxy manager updates if running
	if s.proxyManager != nil {
		// If port changed, remove old and add new
		if oldPort != listener.Port {
			s.proxyManager.RemoveListener(int(oldPort))
			if listener.Enabled {
				if err := s.proxyManager.AddListener(listener); err != nil {
					s.log.Error("Failed to add updated listener to proxy manager: %v", err)
				}
			}
		} else if !listener.Enabled {
			// If disabled, remove it
			s.proxyManager.RemoveListener(int(listener.Port))
		} else if listener.Enabled {
			// Re-adds listener if enabled and port unchanged
			s.proxyManager.AddListener(listener)
		}
	}

	return connect.NewResponse(&v1.UpdateProxyListenerResponse{Listener: listener}), nil
}

// Deletes a proxy listener
func (s *ProxyService) DeleteProxyListener(ctx context.Context, req *connect.Request[v1.DeleteProxyListenerRequest]) (*connect.Response[v1.DeleteProxyListenerResponse], error) {
	// Needs listener first to know which port to remove
	listener, err := s.store.GetProxyListener(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("listener not found"))
	}

	if err := s.store.DeleteProxyListener(ctx, req.Msg.Id); err != nil {
		if strings.Contains(err.Error(), "servers are using it") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		} else {
			s.log.Error("Failed to delete proxy listener: %v", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete proxy listener"))
		}
	}

	// Remove the listener from the proxy manager if it's running
	if s.proxyManager != nil {
		if err := s.proxyManager.RemoveListener(int(listener.Port)); err != nil {
			s.log.Error("Failed to remove listener from proxy manager: %v", err)
			// Non-critical, proxy restart cleans it up later
		}
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
	suggestedHostname := ""
	if server.ProxyHostname == "" && s.config.Proxy.BaseUrl != "" {
		suggestedHostname = strings.ToLower(strings.ReplaceAll(server.Name, " ", "-")) + "." + s.config.Proxy.BaseUrl
	}

	// Check if proxy is enabled and get current route
	var currentRoute *v1.ServerRoute
	if s.proxyManager != nil {
		routes := s.proxyManager.GetRoutes()
		for hostname, route := range routes {
			if route.ServerID == server.Id {
				currentRoute = &v1.ServerRoute{
					Hostname: hostname,
					Active:   true,
				}
				break
			}
		}
	}

	// Get listen port from the listener if assigned
	listenPort := int32(s.config.Proxy.ListenPort)
	if server.ProxyListenerId != "" {
		if listener, err := s.store.GetProxyListener(ctx, server.ProxyListenerId); err == nil {
			listenPort = int32(listener.Port)
		}
	}

	return connect.NewResponse(&v1.GetServerRoutingResponse{
		ProxyEnabled:      s.config.Proxy.Enabled,
		ProxyHostname:     server.ProxyHostname,
		ProxyListenerId:   server.ProxyListenerId,
		SuggestedHostname: suggestedHostname,
		BaseUrl:           s.config.Proxy.BaseUrl,
		ListenPort:        listenPort,
		CurrentRoute:      currentRoute,
	}), nil
}

// Updates server routing configuration
func (s *ProxyService) UpdateServerRouting(ctx context.Context, req *connect.Request[v1.UpdateServerRoutingRequest]) (*connect.Response[v1.UpdateServerRoutingResponse], error) {
	msg := req.Msg

	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	// Store old values to detect changes
	oldProxyHostname := server.ProxyHostname
	oldProxyListenerID := server.ProxyListenerId

	// Validate and normalize hostname
	hostname := strings.TrimSpace(strings.ToLower(msg.ProxyHostname))
	if hostname != "" {
		if strings.Contains(hostname, " ") || strings.Contains(hostname, "://") {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid hostname format"))
		}

		// Check for conflicts with other servers
		servers, err := s.store.ListServers(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check hostname conflicts"))
		}
		for _, srv := range servers {
			if srv.Id != server.Id && srv.ProxyHostname == hostname {
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("hostname already in use by another server"))
			}
		}
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

	// Detect what changed
	hostnameChanged := oldProxyHostname != hostname
	listenerChanged := oldProxyListenerID != listenerID
	proxyModeChanged := (oldProxyHostname == "") != (hostname == "")

	// Direct mode binds the requested host port
	oldPort := server.Port
	newPort := oldPort
	if hostname == "" && msg.Port != nil && *msg.Port > 0 {
		newPort = *msg.Port
	}
	if hostname != "" {
		// Proxied containers always listen on the default inside
		newPort = storage.MinecraftDefaultPort
	}
	portChanged := newPort != oldPort

	if hostname == "" && portChanged {
		if newPort < 1 || newPort > 65535 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port must be between 1 and 65535"))
		}
		existing, err := s.store.GetServerByPort(ctx, int(newPort))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to check port availability"))
		}
		if existing != nil && existing.Id != server.Id {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("port already in use by another server"))
		}
		if s.config.Proxy.Enabled && slices.Contains(s.config.Proxy.ListenPorts, int(newPort)) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port is already in use by the proxy server"))
		}
	}

	// Recreate container if proxy mode, listener, or port changes
	needsRecreation := proxyModeChanged || (listenerChanged && hostname != "" && oldProxyHostname != "") || (portChanged && hostname == "")

	// Removes old route before updating server and hostname
	if hostnameChanged && oldProxyHostname != "" && s.proxyManager != nil {
		if err := s.proxyManager.RemoveRouteByHostname(oldProxyHostname, oldProxyListenerID); err != nil {
			s.log.Error("Failed to remove old proxy route: %v", err)
		}
	}

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
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server configuration"))
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
				// Remove route since there's no valid container
				if s.proxyManager != nil && hostname != "" {
					s.proxyManager.RemoveRouteByHostname(hostname, listenerID)
				}
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
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}

	// Add/update new route if proxy is enabled
	if hostname != "" && s.proxyManager != nil {
		if err := s.proxyManager.UpdateServerRoute(server); err != nil {
			s.log.Error("Failed to update proxy route: %v", err)
		}
	}

	return connect.NewResponse(&v1.UpdateServerRoutingResponse{
		Hostname:        hostname,
		ProxyListenerId: listenerID,
	}), nil
}
