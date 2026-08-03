package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/module"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"github.com/discohaus/discopanel/pkg/transfer"
	utils "github.com/discohaus/discopanel/pkg/utils"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that ServerService implements the interface
var _ discopanelv1connect.ServerServiceHandler = (*ServerService)(nil)

// ServerService implements the Server service
type ServerService struct {
	store            *storage.Store
	docker           *docker.Client
	sender           *command.Sender
	config           *config.Config
	proxy            *proxy.Manager
	lifecycle        *lifecycle.Manager
	rec              *metrics.Recorder
	log              *logger.Logger
	logStreamer      *logger.LogStreamer
	metricsCollector *metrics.Collector
	moduleManager    *module.Manager
	bus              *events.Bus
	uploadManager    *transfer.UploadManager

	// Encoded server-icon.png keyed by server, checked by mtime
	faviconMu sync.Mutex
	favicons  map[string]faviconEntry
}

type faviconEntry struct {
	modTime time.Time
	size    int64
	dataURI string
}

// Normalizes additional ports, proxied ones may route
func normalizeAdditionalPorts(ports []*v1.NetworkPort, serverHostnames []string, proxyOn bool) ([]*v1.NetworkPort, error) {
	var out []*v1.NetworkPort
	for _, p := range ports {
		if p == nil {
			continue
		}
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return nil, fmt.Errorf("invalid container port %d", p.ContainerPort)
		}
		if p.HostPort < 1 || p.HostPort > 65535 {
			return nil, fmt.Errorf("invalid host port %d", p.HostPort)
		}
		if p.ProxyEnabled && !proxyOn {
			return nil, fmt.Errorf("proxy is disabled, port %s cannot be proxied", p.Name)
		}
		protocol := p.Protocol
		switch protocol {
		case v1.ModuleProtocol_MODULE_PROTOCOL_UNSPECIFIED:
			protocol = v1.ModuleProtocol_MODULE_PROTOCOL_TCP
		case v1.ModuleProtocol_MODULE_PROTOCOL_TCP, v1.ModuleProtocol_MODULE_PROTOCOL_UDP:
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
			// Hostname routed protocols only work through the proxy
			if !p.ProxyEnabled {
				return nil, fmt.Errorf("port %s must enable the proxy to speak %s", p.Name, protometa.Name(protocol))
			}
		default:
			return nil, fmt.Errorf("invalid protocol %s", protometa.Name(protocol))
		}
		hostnames, err := proxy.NormalizeHostnames(p.Hostnames)
		if err != nil {
			return nil, fmt.Errorf("%w on port %s", err, p.Name)
		}
		normalized := &v1.NetworkPort{
			ContainerPort: p.ContainerPort,
			HostPort:      p.HostPort,
			Protocol:      protocol,
			Name:          p.Name,
			ProxyEnabled:  p.ProxyEnabled,
			Hostnames:     hostnames,
			CatchAll:      p.CatchAll,
		}
		if err := proxy.ValidatePortRouting(normalized, serverHostnames); err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

// Reports whether both port lists match exactly
func networkPortsEqual(a, b []*v1.NetworkPort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// NewServerService creates a new server service
func NewServerService(store *storage.Store, docker *docker.Client, sender *command.Sender, config *config.Config, proxy *proxy.Manager, lifecycleManager *lifecycle.Manager, logStreamer *logger.LogStreamer, metricsCollector *metrics.Collector, moduleManager *module.Manager, bus *events.Bus, uploadManager *transfer.UploadManager, rec *metrics.Recorder, log *logger.Logger) *ServerService {
	return &ServerService{
		store:            store,
		docker:           docker,
		sender:           sender,
		config:           config,
		proxy:            proxy,
		lifecycle:        lifecycleManager,
		rec:              rec,
		log:              log,
		logStreamer:      logStreamer,
		metricsCollector: metricsCollector,
		moduleManager:    moduleManager,
		bus:              bus,
		uploadManager:    uploadManager,
		favicons:         make(map[string]faviconEntry),
	}
}

// Detaches request work from cancellation, values ride along
func detach(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// Loads a server or returns the canonical not found error
func getServer(ctx context.Context, store *storage.Store, id string) (*v1.Server, error) {
	server, err := store.GetServer(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	return server, nil
}

// Serves server-icon.png from disk, cached by file identity
func (s *ServerService) serverFavicon(server *v1.Server) string {
	if server.DataPath == "" {
		return ""
	}
	iconPath := filepath.Join(server.DataPath, "server-icon.png")
	info, err := os.Stat(iconPath)
	if err != nil {
		return ""
	}
	s.faviconMu.Lock()
	defer s.faviconMu.Unlock()
	if e, ok := s.favicons[server.Id]; ok && e.modTime.Equal(info.ModTime()) && e.size == info.Size() {
		return e.dataURI
	}
	data, err := os.ReadFile(iconPath)
	if err != nil {
		return ""
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	s.favicons[server.Id] = faviconEntry{modTime: info.ModTime(), size: info.Size(), dataURI: uri}
	return uri
}

// Copies cached runtime stats onto transient server fields
func (s *ServerService) applyMetrics(server *v1.Server) {
	if s.metricsCollector == nil {
		return
	}
	m := s.metricsCollector.GetMetrics(server.Id)
	if m == nil {
		return
	}
	server.MemoryUsage = int64(m.MemoryUsage)
	server.CpuPercent = m.CpuPercent
	server.CpuCores = int32(m.CpuCount)
	server.DiskUsage = m.DiskUsage
	server.DiskTotal = m.DiskTotal
	server.DiskUsed = m.DiskUsed
	server.WorldSize = m.WorldSize
	server.PlayersOnline = int32(m.PlayersOnline)
	server.Tps = m.Tps

	// SLP fields
	server.SlpAvailable = m.SlpAvailable
	server.SlpLatencyMs = m.SlpLatencyMs
	server.Motd = m.Motd
	server.ServerVersion = m.ServerVersion
	server.ProtocolVersion = int32(m.ProtocolVersion)
	server.PlayerSample = m.PlayerSample
	server.MaxPlayersSlp = int32(m.MaxPlayers)

	// Agent-sourced fields
	server.AgentConnected = m.AgentConnected
	server.Mspt = m.Mspt
	server.HeapUsedMb = m.HeapUsedMb
	server.HeapMaxMb = m.HeapMaxMb
	server.CpuThrottlePercent = m.CpuThrottlePercent
	server.ClassCount = int32(m.ClassCount)
}

// ListServers lists all servers
func (s *ServerService) ListServers(ctx context.Context, req *connect.Request[v1.ListServersRequest]) (*connect.Response[v1.ListServersResponse], error) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("Failed to list servers: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list servers"))
	}

	if err := s.store.HydrateProxyPorts(ctx, servers...); err != nil {
		s.log.Error("Failed to hydrate proxy ports: %v", err)
	}

	// Update status from Docker and apply cached metrics
	for _, server := range servers {
		// Icon comes from disk, cheap enough for light polls
		server.Favicon = s.serverFavicon(server)

		// Stored status only unless the caller wants live stats
		if server.ContainerId != "" && req.Msg.FullStats {
			status, err := s.docker.GetContainerStatus(ctx, server.ContainerId)
			if err == nil {
				server.Status = status
			}

			// Apply cached metrics from the background collector
			s.applyMetrics(server)
		}
	}

	// Convert to proto
	protoServers := make([]*v1.Server, len(servers))
	for i, server := range servers {
		protoServers[i] = server.Redact()
	}

	return connect.NewResponse(&v1.ListServersResponse{
		Servers: protoServers,
	}), nil
}

// GetServer gets a specific server
func (s *ServerService) GetServer(ctx context.Context, req *connect.Request[v1.GetServerRequest]) (*connect.Response[v1.GetServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	if err := s.store.HydrateProxyPorts(ctx, server); err != nil {
		s.log.Error("Failed to hydrate proxy port: %v", err)
	}

	// Update status from Docker
	if server.ContainerId != "" {
		status, err := s.docker.GetContainerStatus(ctx, server.ContainerId)
		if err == nil {
			server.Status = status
		}
	}

	// Apply cached metrics from the background collector
	s.applyMetrics(server)
	server.Favicon = s.serverFavicon(server)

	return connect.NewResponse(&v1.GetServerResponse{
		Server: server.Redact(),
	}), nil
}

func (s *ServerService) CreateServer(ctx context.Context, req *connect.Request[v1.CreateServerRequest]) (*connect.Response[v1.CreateServerResponse], error) {
	msg := req.Msg

	// Convert mod loader from proto
	modLoader := msg.ModLoader

	// If modpack is selected, load it and derive settings
	var modpack *v1.IndexedModpack
	if msg.ModpackId != "" {
		var err error
		modpack, err = s.store.GetIndexedModpack(ctx, msg.ModpackId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid modpack"))
		}

		// Override mod loader based on the pack platform
		loader, ok := minecraft.LoaderForPackSource(indexers.PackSourceFor(modpack.Indexer))
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported modpack indexer %q", modpack.Indexer))
		}
		modLoader = loader

		// Get MC version from modpack if not explicitly set
		if msg.McVersion == "" && len(modpack.GameVersions) > 0 {
			msg.McVersion = minecraft.FindMostRecentMinecraftVersion(modpack.GameVersions)
		}

		// Set minimum memory for modpacks
		if msg.Memory < 4096 {
			msg.Memory = 4096
		}
	}

	// Validate request
	if msg.Name == "" || msg.McVersion == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name and MC version are required"))
	}

	// Handle proxy configuration
	proxyHostnames, err := proxy.NormalizeHostnames(msg.ProxyHostnames)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	proxyListenerID := msg.ProxyListenerId
	port := int(msg.Port)

	if len(proxyHostnames) > 0 {
		// Routing needs the proxy on
		if !s.proxy.Enabled() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("proxy is disabled"))
		}

		// Validate listener selection
		if proxyListenerID != "" {
			listener, err := s.store.GetProxyListener(ctx, proxyListenerID)
			if err != nil || !listener.Enabled {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or disabled proxy listener"))
			}
			port = int(listener.Port)
		} else {
			// No listener specified, get the default one
			listeners, err := s.store.ListProxyListeners(ctx)
			if err != nil || len(listeners) == 0 {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("no proxy listeners configured"))
			}

			// Find default or first enabled listener
			var defaultListener *v1.ProxyListener
			for _, l := range listeners {
				if l.IsDefault && l.Enabled {
					defaultListener = l
					break
				}
			}
			if defaultListener == nil {
				for _, l := range listeners {
					if l.Enabled {
						defaultListener = l
						break
					}
				}
			}
			if defaultListener == nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("no enabled proxy listeners available"))
			}
			proxyListenerID = defaultListener.Id
			port = int(defaultListener.Port)
		}
	} else {
		// For non-proxy servers, must have a unique port
		if port == 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("port is required for non-proxy servers"))
		}
	}

	// Only an explicit valid tag pins, java version drives otherwise
	dockerImage := msg.DockerImage
	if !docker.IsValidRuntimeTag(dockerImage) {
		dockerImage = ""
	}

	additionalPorts, err := normalizeAdditionalPorts(msg.AdditionalPorts, proxyHostnames, s.proxy.Enabled())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Create server object
	serverUUID := uuid.New().String()

	// Registry checkout guards every port until the row persists
	proxyOn := s.proxy.Enabled()
	netOwner := proxy.NetOwner{Kind: proxy.OwnerServer, ID: serverUUID}
	var netReqs []proxy.NetRequest
	if len(proxyHostnames) > 0 {
		netReqs = proxy.ServerProxiedNetRequests(proxyHostnames, port, additionalPorts, proxyOn)
	} else {
		netReqs = proxy.ServerDirectNetRequests(port, additionalPorts, proxyOn)
	}
	if err := s.proxy.EnsureListenersFor(ctx, netReqs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	netClaim, err := s.proxy.CheckoutNetwork(ctx, netOwner, netReqs)
	if err != nil {
		// Reconcile retires any listener row made just above
		if serr := s.proxy.SyncListeners(ctx); serr != nil {
			s.log.Error("Failed to sync after checkout failure: %v", serr)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	defer netClaim.Release()
	serverDataDir := fmt.Sprintf("%s_%s", files.SanitizePathName(msg.Name), serverUUID)
	serverDataPath := filepath.Join(s.config.Storage.DataDir, "servers", serverDataDir)

	server := &v1.Server{
		Id:                serverUUID,
		Name:              msg.Name,
		Description:       msg.Description,
		ModLoader:         modLoader,
		McVersion:         msg.McVersion,
		Status:            v1.ServerStatus_SERVER_STATUS_CREATING,
		Port:              int32(port),
		ProxyHostnames:    proxyHostnames,
		ProxyListenerId:   proxyListenerID,
		MaxPlayers:        msg.MaxPlayers,
		Memory:            msg.Memory,
		MemoryMin:         msg.MemoryMin,
		MemoryMax:         msg.MemoryMax,
		DataPath:          serverDataPath,
		JavaVersion:       docker.GetRequiredJavaVersion(msg.McVersion, modLoader),
		DockerImage:       dockerImage,
		AutoStart:         msg.AutoStart,
		Detached:          msg.Detached,
		AdditionalPorts:   additionalPorts,
		DockerOverrides:   msg.DockerOverrides,
	}

	// Set defaults
	if server.MaxPlayers == 0 {
		server.MaxPlayers = 20
	}
	if server.Memory == 0 {
		server.Memory = 4096
	}
	if server.ModLoader == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		server.ModLoader = v1.ModLoader_MOD_LOADER_VANILLA
	}

	if err := runtimespec.NormalizeServerMemory(server); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// When using proxy, set the ports correctly
	if len(server.ProxyHostnames) > 0 && proxyListenerID != "" {
		server.Port = int32(storage.MinecraftDefaultPort)
		if err := s.store.HydrateProxyPorts(ctx, server); err != nil {
			s.log.Error("Failed to hydrate proxy port: %v", err)
		}
	}

	// Create data directory
	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		s.log.Error("Failed to create data directory: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create server directory"))
	}

	// Imported world lands before the row so failures stay clean
	importedLevelName := ""
	if msg.WorldUploadSessionId != "" {
		levelName, err := s.importUploadedWorld(server, msg.WorldUploadSessionId)
		if err != nil {
			os.RemoveAll(server.DataPath)
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("world import failed: %w", err))
		}
		importedLevelName = levelName
	}

	// Save to database
	if err := s.store.CreateServer(ctx, server); err != nil {
		s.log.Error("Failed to create server: %v", err)
		os.RemoveAll(server.DataPath)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create server"))
	}
	netClaim.Confirm()
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_CREATE, nil, "created the server")

	// Reconcile starts any auto created listener sockets
	if err := s.proxy.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync routes after server create: %v", err)
	}

	// Get the server config
	serverConfig, err := s.store.GetServerProperties(ctx, server.Id)
	if err != nil {
		s.log.Error("Failed to get server config: %v", err)
		serverConfig = s.store.CreateDefaultServerProperties(server.Id)
	}
	if importedLevelName != "" {
		serverConfig.Level = &importedLevelName
	}

	// Reflects heap sizing into read-only properties
	runtimespec.SyncPropertiesMemory(serverConfig, server)

	if err := s.store.UpdateServerProperties(ctx, serverConfig); err != nil {
		s.log.Error("Failed to update server config with memory settings: %v", err)
	}

	// Configure modpack if selected
	if modpack != nil {
		if err := s.applyModpackSelection(ctx, server, serverConfig, modpack, msg.ModpackVersionId); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if err := s.store.UpdateServerProperties(ctx, serverConfig); err != nil {
			s.log.Error("Failed to update server config with modpack settings: %v", err)
		}
	}

	// Provisioning and container creation happen on first start.
	if msg.StartImmediately {
		server.Status = v1.ServerStatus_SERVER_STATUS_PROVISIONING
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
		}
		go func() {
			bgCtx, cancel := context.WithTimeout(detach(ctx), 2*time.Hour)
			defer cancel()
			if err := s.lifecycle.Start(bgCtx, server.Id); err != nil {
				s.log.Error("Failed to start newly created server %s: %v", server.Name, err)
			}
		}()
	} else {
		server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
		if err := s.store.UpdateServer(ctx, server); err != nil {
			s.log.Error("Failed to update server status: %v", err)
		}
		s.log.Info("Server %s created but not started immediately", server.Id)
	}

	return connect.NewResponse(&v1.CreateServerResponse{
		Server: server.Redact(),
	}), nil
}

// UpdateServer updates a server
func (s *ServerService) UpdateServer(ctx context.Context, req *connect.Request[v1.UpdateServerRequest]) (*connect.Response[v1.UpdateServerResponse], error) {
	msg := req.Msg

	server, err := getServer(ctx, s.store, msg.Id)
	if err != nil {
		return nil, err
	}

	// Check if container recreation is needed
	needsRecreation := false
	originalMemory := server.Memory
	originalModLoader := server.ModLoader
	originalMCVersion := server.McVersion
	originalDockerImage := server.DockerImage

	// Update fields
	if msg.Name != "" {
		server.Name = msg.Name
	}
	if msg.Description != "" {
		server.Description = msg.Description
	}
	if msg.Port != nil && int(*msg.Port) != int(server.Port) {
		newPort := int(*msg.Port)

		if len(server.ProxyHostnames) > 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot change port for proxy-enabled servers"))
		}

		if newPort < 1 || newPort > 65535 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid port %d", newPort))
		}

		server.Port = int32(newPort)
		needsRecreation = true
	}
	if msg.MaxPlayers > 0 && msg.MaxPlayers != server.MaxPlayers {
		server.MaxPlayers = msg.MaxPlayers
		needsRecreation = true
	}
	if msg.Memory > 0 || msg.MemoryMin > 0 || msg.MemoryMax > 0 {
		originalMemoryMin := server.MemoryMin
		originalMemoryMax := server.MemoryMax
		if msg.Memory > 0 {
			server.Memory = msg.Memory
		}

		// Zero heap values rescale to defaults in normalize
		server.MemoryMin = msg.MemoryMin
		server.MemoryMax = msg.MemoryMax
		if err := runtimespec.NormalizeServerMemory(server); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}

		if server.Memory != originalMemory || server.MemoryMin != originalMemoryMin || server.MemoryMax != originalMemoryMax {
			needsRecreation = true
			if err := s.store.SyncServerPropertiesWithServer(ctx, server); err != nil {
				s.log.Error("Failed to sync server config memory: %v", err)
			}
		}
	}
	if msg.ModLoader != v1.ModLoader_MOD_LOADER_UNSPECIFIED && msg.ModLoader != originalModLoader {
		server.ModLoader = msg.ModLoader
		needsRecreation = true
	}
	if msg.McVersion != "" && msg.McVersion != originalMCVersion {
		server.McVersion = msg.McVersion
		needsRecreation = true
	}
	if msg.DockerImage != "" && msg.DockerImage != originalDockerImage {
		if !docker.IsValidRuntimeTag(msg.DockerImage) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown runtime image tag %q", msg.DockerImage))
		}
		server.DockerImage = msg.DockerImage
		needsRecreation = true
	}
	if msg.AutoStart != nil {
		server.AutoStart = *msg.AutoStart
	}
	if msg.Detached != nil {
		server.Detached = *msg.Detached
	}

	// Handle additional ports update
	if len(msg.AdditionalPorts) > 0 {
		additionalPorts, err := normalizeAdditionalPorts(msg.AdditionalPorts, server.ProxyHostnames, s.proxy.Enabled())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if !networkPortsEqual(additionalPorts, server.AdditionalPorts) {
			server.AdditionalPorts = additionalPorts
			needsRecreation = true
		}
	} else if msg.ClearAdditionalPorts && len(server.AdditionalPorts) > 0 {
		server.AdditionalPorts = nil
		needsRecreation = true
	}

	// Handle docker overrides update
	if msg.DockerOverrides != nil {
		// Check that labels do not start with "discopanel."
		for key := range msg.DockerOverrides.Labels {
			if strings.HasPrefix(key, "discopanel.") {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("docker label keys cannot start with 'discopanel.', namespace reserved for internal management"))
			}
		}

		if !proto.Equal(server.DockerOverrides, msg.DockerOverrides) {
			server.DockerOverrides = msg.DockerOverrides
			needsRecreation = true
		}
	}

	// Handle modpack version update
	if msg.ModpackId != "" {
		serverConfig, err := s.store.GetServerProperties(ctx, server.Id)
		if err != nil {
			s.log.Error("Failed to get server config: %v", err)
			serverConfig = s.store.CreateDefaultServerProperties(server.Id)
		}

		modpack, err := s.store.GetIndexedModpack(ctx, msg.ModpackId)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid modpack"))
		}
		if err := s.applyModpackSelection(ctx, server, serverConfig, modpack, msg.ModpackVersionId); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		needsRecreation = true

		if err := s.store.UpdateServerProperties(ctx, serverConfig); err != nil {
			s.log.Error("Failed to update server config with modpack settings: %v", err)
		}
	}

	// Registry checkout guards the merged network state until persist
	proxyOn := s.proxy.Enabled()
	var netReqs []proxy.NetRequest
	if len(server.ProxyHostnames) > 0 {
		netReqs = proxy.PortNetRequests(server.AdditionalPorts, server.ProxyHostnames, proxyOn)
		if listener, lerr := s.store.GetProxyListener(ctx, server.ProxyListenerId); lerr == nil && listener != nil {
			netReqs = proxy.ServerProxiedNetRequests(server.ProxyHostnames, int(listener.Port), server.AdditionalPorts, proxyOn)
		}
	} else {
		netReqs = proxy.ServerDirectNetRequests(int(server.Port), server.AdditionalPorts, proxyOn)
	}
	if err := s.proxy.EnsureListenersFor(ctx, netReqs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	netClaim, err := s.proxy.CheckoutNetwork(ctx, proxy.NetOwner{Kind: proxy.OwnerServer, ID: server.Id}, netReqs)
	if err != nil {
		// Reconcile retires any listener row made just above
		if serr := s.proxy.SyncListeners(ctx); serr != nil {
			s.log.Error("Failed to sync after checkout failure: %v", serr)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	defer netClaim.Release()

	// Save server updates first
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to update server: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update server"))
	}
	netClaim.Confirm()

	// Reconcile keeps routes matching the saved shape
	if err := s.proxy.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync routes after server update: %v", err)
	}

	if needsRecreation {
		s.recreateAfterConfigChange(ctx, server)
	}

	if err := s.store.HydrateProxyPorts(ctx, server); err != nil {
		s.log.Error("Failed to hydrate proxy port: %v", err)
	}

	return connect.NewResponse(&v1.UpdateServerResponse{
		Server: server.Redact(),
	}), nil
}

// Points server loader and properties at the selected modpack
func (s *ServerService) applyModpackSelection(ctx context.Context, server *v1.Server, serverConfig *v1.ServerProperties, modpack *v1.IndexedModpack, versionID string) error {
	loader, ok := minecraft.LoaderForPackSource(indexers.PackSourceFor(modpack.Indexer))
	if !ok {
		return fmt.Errorf("unsupported modpack indexer %q", modpack.Indexer)
	}
	server.ModLoader = loader

	switch modpack.Indexer {
	case indexers.ManualIndexer:
		packFiles, err := s.store.GetIndexedModpackFiles(ctx, modpack.Id)
		if err != nil || len(packFiles) == 0 {
			return fmt.Errorf("uploaded modpack has no archive")
		}
		if err := files.CopyFile(packFiles[0].DownloadUrl, filepath.Join(server.DataPath, "modpack.zip")); err != nil {
			return fmt.Errorf("failed to stage modpack archive: %w", err)
		}
		cfModpackZip := "/data/modpack.zip"
		serverConfig.CfModpackZip = &cfModpackZip
		cfSlug := "manual-" + modpack.Id
		serverConfig.CfSlug = &cfSlug
	case "fuego":
		pageURL := modpack.WebsiteUrl
		if versionID != "" && versionID != "latest" {
			pageURL = fmt.Sprintf("%s/files/%s", modpack.WebsiteUrl, versionID)
		}
		serverConfig.CfPageUrl = &pageURL
	case "modrinth":
		projectSpec := modpack.IndexerId
		if versionID != "" && versionID != "latest" {
			projectSpec = fmt.Sprintf("%s:%s", modpack.IndexerId, versionID)
		}
		serverConfig.ModrinthModpack = &projectSpec
		downloadDeps := "required"
		serverConfig.ModrinthDownloadDependencies = &downloadDeps
		if versionID == "" || versionID == "latest" {
			versionType := "release"
			serverConfig.ModrinthModpackVersionType = &versionType
		}
	}

	// Pack art becomes the server icon like an upload would
	s.adoptModpackIcon(ctx, server, modpack)
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MODPACK_SELECT, metrics.Attrs{"modpack": modpack.Name}, "selected modpack %s", modpack.Name)
	return nil
}

// Rebuilds the container after config changes, restarts if running
func (s *ServerService) recreateAfterConfigChange(ctx context.Context, server *v1.Server) bool {
	if server.ContainerId == "" {
		return false
	}

	wasRunning := false
	if status, err := s.docker.GetContainerStatus(ctx, server.ContainerId); err == nil {
		switch status {
		case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_STARTING, v1.ServerStatus_SERVER_STATUS_UNHEALTHY, v1.ServerStatus_SERVER_STATUS_PAUSED:
			wasRunning = true
		}
	}

	// Running servers come back through the full lifecycle
	if wasRunning {
		go func() {
			bgCtx, cancel := context.WithTimeout(detach(ctx), 2*time.Hour)
			defer cancel()
			if err := s.lifecycle.Recreate(bgCtx, server.Id); err != nil {
				s.log.Error("Failed to recreate server %s after update: %v", server.Name, err)
			}
		}()
		return true
	}

	if err := s.docker.RemoveContainer(ctx, server.ContainerId); err != nil {
		s.log.Debug("Failed to remove container after update (may not exist): %v", err)
	}
	server.ContainerId = ""
	server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to update server after container removal: %v", err)
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_REMOVE, nil, "removed the container so new settings apply on next start")
	return false
}

// Adopts modpack art as the server icon, uploads win
func (s *ServerService) adoptModpackIcon(ctx context.Context, server *v1.Server, modpack *v1.IndexedModpack) {
	if server.IconSource == v1.IconSource_ICON_SOURCE_UPLOAD || modpack.LogoUrl == "" {
		return
	}
	iconPNG, err := provisioner.FetchServerIcon(ctx, s.config.Server.UserAgent, modpack.LogoUrl)
	if err != nil {
		s.log.Warn("Modpack icon fetch failed for %s: %v", server.Name, err)
		return
	}
	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		s.log.Error("Failed to create server data dir: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(server.DataPath, "server-icon.png"), iconPNG, 0644); err != nil {
		s.log.Error("Failed to write modpack icon: %v", err)
		return
	}
	server.IconSource = v1.IconSource_ICON_SOURCE_MODPACK
}

// UploadServerIcon converts an uploaded image into server-icon.png
func (s *ServerService) UploadServerIcon(ctx context.Context, req *connect.Request[v1.UploadServerIconRequest]) (*connect.Response[v1.UploadServerIconResponse], error) {
	const maxIconBytes = 4 << 20

	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.Image) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image data is required"))
	}
	if len(req.Msg.Image) > maxIconBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("image must be under 4 MB"))
	}

	iconPNG, err := provisioner.ConvertServerIcon(bytes.NewReader(req.Msg.Image))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported image format"))
	}

	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		s.log.Error("Failed to create server data dir: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save icon"))
	}
	iconPath := filepath.Join(server.DataPath, "server-icon.png")
	if err := os.WriteFile(iconPath, iconPNG, 0644); err != nil {
		s.log.Error("Failed to write server icon: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save icon"))
	}

	server.IconSource = v1.IconSource_ICON_SOURCE_UPLOAD
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Error("Failed to persist icon source: %v", err)
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_ICON_UPLOAD, nil, "uploaded a server icon")

	favicon := "data:image/png;base64," + base64.StdEncoding.EncodeToString(iconPNG)
	return connect.NewResponse(&v1.UploadServerIconResponse{
		Favicon: favicon,
	}), nil
}

// DeleteServer deletes a server
func (s *ServerService) DeleteServer(ctx context.Context, req *connect.Request[v1.DeleteServerRequest]) (*connect.Response[v1.DeleteServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	// Delete every module row with its container and token
	if s.moduleManager != nil {
		modules, err := s.store.ListServerModules(ctx, server.Id)
		if err == nil {
			for _, mod := range modules {
				if err := s.moduleManager.DeleteModule(ctx, mod.Id); err != nil {
					s.log.Error("Failed to delete module %s: %v", mod.Id, err)
				}
			}
		}
	}

	// Stop and remove container
	if server.ContainerId != "" {
		if _, err := s.docker.StopContainer(ctx, server.ContainerId, 30); err != nil {
			s.log.Error("Failed to stop container: %v", err)
		}
		if err := s.docker.RemoveContainer(ctx, server.ContainerId); err != nil {
			s.log.Error("Failed to remove container: %v", err)
		}
	}

	// Delete from database
	if err := s.store.DeleteServer(ctx, req.Msg.Id); err != nil {
		s.log.Error("Failed to delete server: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete server"))
	}

	// Delete data directory
	if err := os.RemoveAll(server.DataPath); err != nil {
		s.log.Error("Failed to delete server data: %v", err)
	}

	// Reconcile drops the server's routes and counters
	if s.proxy != nil {
		if err := s.proxy.SyncListeners(ctx); err != nil {
			s.log.Error("Failed to sync routes after server delete: %v", err)
		}
		s.proxy.DropOwnerStats(server.Id)
	}

	return connect.NewResponse(&v1.DeleteServerResponse{}), nil
}

// StartServer starts a server (provisioning + container start run async)
func (s *ServerService) StartServer(ctx context.Context, req *connect.Request[v1.StartServerRequest]) (*connect.Response[v1.StartServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	if s.lifecycle.IsStarting(server.Id) {
		return connect.NewResponse(&v1.StartServerResponse{
			Status: v1.ServerStatus_SERVER_STATUS_PROVISIONING,
		}), nil
	}

	if err := s.store.UpdateServerFields(ctx, server.Id, map[string]any{"status": v1.ServerStatus_SERVER_STATUS_PROVISIONING}); err != nil {
		s.log.Error("Failed to update server status: %v", err)
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(detach(ctx), 2*time.Hour)
		defer cancel()
		if err := s.lifecycle.Start(bgCtx, server.Id); err != nil {
			s.log.Error("Failed to start server %s: %v", server.Name, err)
		}
	}()

	return connect.NewResponse(&v1.StartServerResponse{
		Status: v1.ServerStatus_SERVER_STATUS_PROVISIONING,
	}), nil
}

// StopServer stops a server (graceful stop runs async)
func (s *ServerService) StopServer(ctx context.Context, req *connect.Request[v1.StopServerRequest]) (*connect.Response[v1.StopServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	if server.ContainerId == "" {
		if err := s.store.UpdateServerFields(ctx, server.Id, map[string]any{"status": v1.ServerStatus_SERVER_STATUS_STOPPED}); err != nil {
			s.log.Error("Failed to update server status: %v", err)
		}
		return connect.NewResponse(&v1.StopServerResponse{
			Status: v1.ServerStatus_SERVER_STATUS_STOPPED,
		}), nil
	}

	if err := s.store.UpdateServerFields(ctx, server.Id, map[string]any{"status": v1.ServerStatus_SERVER_STATUS_STOPPING}); err != nil {
		s.log.Error("Failed to update server status: %v", err)
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(detach(ctx), 15*time.Minute)
		defer cancel()
		if err := s.lifecycle.Stop(bgCtx, server.Id); err != nil {
			s.log.Error("Failed to stop server %s: %v", server.Name, err)
		}
	}()

	return connect.NewResponse(&v1.StopServerResponse{
		Status: v1.ServerStatus_SERVER_STATUS_STOPPING,
	}), nil
}

// RestartServer restarts a server (runs async)
func (s *ServerService) RestartServer(ctx context.Context, req *connect.Request[v1.RestartServerRequest]) (*connect.Response[v1.RestartServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(detach(ctx), 2*time.Hour)
		defer cancel()
		if err := s.lifecycle.Restart(bgCtx, server.Id); err != nil {
			s.log.Error("Failed to restart server %s: %v", server.Name, err)
		}
	}()

	return connect.NewResponse(&v1.RestartServerResponse{
		Status: v1.ServerStatus_SERVER_STATUS_STARTING,
	}), nil
}

// Destroys and recreates the container from scratch
func (s *ServerService) RecreateServer(ctx context.Context, req *connect.Request[v1.RecreateServerRequest]) (*connect.Response[v1.RecreateServerResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(detach(ctx), 2*time.Hour)
		defer cancel()
		if err := s.lifecycle.Recreate(bgCtx, server.Id); err != nil {
			s.log.Error("Failed to recreate server %s: %v", server.Name, err)
		}
	}()

	return connect.NewResponse(&v1.RecreateServerResponse{
		Status: v1.ServerStatus_SERVER_STATUS_CREATING,
	}), nil
}

// SendCommand sends a command to a server
func (s *ServerService) SendCommand(ctx context.Context, req *connect.Request[v1.SendCommandRequest]) (*connect.Response[v1.SendCommandResponse], error) {
	silent := false
	if req.Msg.Silent != nil {
		silent = *req.Msg.Silent
	}

	output, err := s.sender.Run(ctx, req.Msg.Id, req.Msg.Command, silent)
	switch {
	case errors.Is(err, command.ErrEmptyCommand):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, command.ErrServerNotFound):
		return nil, connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, command.ErrNoContainer), errors.Is(err, command.ErrNotRunning):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		s.log.Error("Failed to execute command: %v", err)
		return connect.NewResponse(&v1.SendCommandResponse{
			Success: false,
			Error:   err.Error(),
		}), nil
	}

	return connect.NewResponse(&v1.SendCommandResponse{
		Success: true,
		Output:  output,
	}), nil
}

// Reads the server's latest.log and uploads it to mclo.gs
func (s *ServerService) UploadToMCLogs(ctx context.Context, req *connect.Request[v1.UploadToMCLogsRequest]) (*connect.Response[v1.UploadToMCLogsResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	logPath := filepath.Join(server.DataPath, "logs", "latest.log")
	content, err := os.ReadFile(logPath)
	if err != nil {
		s.log.Error("Failed to read server log file: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("log file not found"))
	}

	if len(content) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("log file is empty"))
	}

	// Truncate to 25000 lines if needed
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) > 25000 {
		lines = lines[len(lines)-25000:]
		content = bytes.Join(lines, []byte("\n"))
	}

	// Build mclo.gs request
	payload, _ := json.Marshal(map[string]string{
		"content": string(content),
		"source":  fmt.Sprintf("DiscoPanel-%s", server.Name),
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.mclo.gs/1/log", bytes.NewReader(payload))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create request"))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		s.log.Error("Failed to upload to mclo.gs: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to upload to mclo.gs"))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to read mclo.gs response"))
	}

	var result struct {
		Success bool   `json:"success"`
		URL     string `json:"url"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to parse mclo.gs response"))
	}

	if !result.Success {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mclo.gs error: %s", result.Error))
	}

	return connect.NewResponse(&v1.UploadToMCLogsResponse{
		Url: result.URL,
	}), nil
}

// GetServerLogs gets server logs
func (s *ServerService) GetServerLogs(ctx context.Context, req *connect.Request[v1.GetServerLogsRequest]) (*connect.Response[v1.GetServerLogsResponse], error) {
	// Parse tail parameter
	tail := int(req.Msg.Tail)
	if tail <= 0 {
		tail = 100
	}

	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	// Get structured log entries from the log streamer if available
	var protoLogs []*v1.LogEntry
	if s.logStreamer != nil {
		// Attaches a follow when no stream exists yet
		if server.ContainerId != "" {
			if err := s.logStreamer.StartStreaming(server.Id, server.ContainerId); err != nil {
				s.log.Warn("Failed to start log streaming for server %s: %v", server.Id, err)
			}
		}
		protoLogs = s.logStreamer.GetLogs(server.Id, tail)
	}

	return connect.NewResponse(&v1.GetServerLogsResponse{
		Logs:  protoLogs,
		Total: int32(len(protoLogs)),
	}), nil
}

// ClearServerLogs clears server logs
func (s *ServerService) ClearServerLogs(ctx context.Context, req *connect.Request[v1.ClearServerLogsRequest]) (*connect.Response[v1.ClearServerLogsResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	// Clear structured log entries if log streamer is available
	if s.logStreamer != nil {
		s.logStreamer.ClearLogs(server.Id)
	}

	return connect.NewResponse(&v1.ClearServerLogsResponse{}), nil
}

// GetNextAvailablePort gets the next available port
func (s *ServerService) GetNextAvailablePort(ctx context.Context, req *connect.Request[v1.GetNextAvailablePortRequest]) (*connect.Response[v1.GetNextAvailablePortResponse], error) {
	// Registry scan keeps the candidate and rcon shadow free
	nextPort, err := s.proxy.FindFreePort(ctx, proxy.FreePortOpts{
		Protocol:   v1.ModuleProtocol_MODULE_PROTOCOL_TCP,
		Start:      s.config.Proxy.PortRangeMin,
		End:        65535,
		RconShadow: true,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}

	// Registry snapshot backs the client side hints
	used, err := s.proxy.UsedNetworkPorts(ctx)
	if err != nil {
		s.log.Error("Failed to snapshot used ports: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get available port"))
	}
	usedPorts := make([]*v1.UsedPort, 0, len(used))
	for _, port := range used {
		usedPorts = append(usedPorts, &v1.UsedPort{Port: port})
	}

	return connect.NewResponse(&v1.GetNextAvailablePortResponse{
		Port:      int32(nextPort),
		UsedPorts: usedPorts,
	}), nil
}

// Reports host physical memory and per-server reservations
func (s *ServerService) GetHostMemory(ctx context.Context, req *connect.Request[v1.GetHostMemoryRequest]) (*connect.Response[v1.GetHostMemoryResponse], error) {
	var totalMB int64
	if s.docker != nil {
		if dockerClient := s.docker.GetDockerClient(); dockerClient != nil {
			if info, err := dockerClient.Info(ctx); err == nil {
				totalMB = info.MemTotal / 1024 / 1024
			} else {
				s.log.Error("Failed to read docker host info: %v", err)
			}
		}
	}

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("Failed to list servers: %v", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get host memory"))
	}

	allocations := make([]*v1.ServerMemoryAllocation, 0, len(servers))
	for _, server := range servers {
		allocations = append(allocations, &v1.ServerMemoryAllocation{
			ServerId:   server.Id,
			ServerName: server.Name,
			Memory:     int32(server.Memory),
		})
	}

	return connect.NewResponse(&v1.GetHostMemoryResponse{
		TotalMb:     totalMB,
		Allocations: allocations,
	}), nil
}

// Ranges longer than this are served bucketed
const rawHistoryWindow = 6 * time.Hour

// Returns stored metrics samples for one server's charts
func (s *ServerService) GetServerMetricsHistory(ctx context.Context, req *connect.Request[v1.GetServerMetricsHistoryRequest]) (*connect.Response[v1.GetServerMetricsHistoryResponse], error) {
	if _, err := s.store.GetServer(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}

	to := time.Now()
	if req.Msg.To != nil {
		to = req.Msg.To.AsTime()
	}
	from := to.Add(-time.Hour)
	if req.Msg.From != nil {
		from = req.Msg.From.AsTime()
	}
	if !from.Before(to) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("from must be before to"))
	}

	resolution := int(req.Msg.Resolution)
	if resolution == 0 && to.Sub(from) > rawHistoryWindow {
		resolution = 300
	}

	samples, err := s.store.GetMetricsHistory(ctx, req.Msg.Id, from, to, resolution)
	if err != nil {
		s.log.Error("Failed to load metrics history for %s: %v", req.Msg.Id, err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load metrics history"))
	}

	return connect.NewResponse(&v1.GetServerMetricsHistoryResponse{
		Samples:    samples,
		Resolution: int32(resolution),
	}), nil
}

// Serves findings the doctor module published, panel adds nothing
func (s *ServerService) GetServerPerformanceReport(ctx context.Context, req *connect.Request[v1.GetServerPerformanceReportRequest]) (*connect.Response[v1.GetServerPerformanceReportResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	var agentConnected bool
	if s.metricsCollector != nil {
		if m := s.metricsCollector.GetMetrics(server.Id); m != nil {
			agentConnected = m.AgentConnected
		}
	}

	rows, err := s.store.GetFindingDismissals(ctx, server.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load dismissals: %w", err))
	}
	dismissals := make(map[string]*v1.FindingDismissal, len(rows))
	for _, d := range rows {
		dismissals[d.FindingId] = d
	}

	findings := runtimespec.ReadFindings(server.DataPath)
	for _, f := range findings {
		d, ok := dismissals[f.GetId()]
		f.Dismissed = ok && d.ContentHash == utils.FindingHash(f)
	}

	return connect.NewResponse(&v1.GetServerPerformanceReportResponse{
		Findings:       findings,
		AgentConnected: agentConnected,
	}), nil
}

// Hides or restores one finding, scoped to its current content
func (s *ServerService) DismissPerformanceFinding(ctx context.Context, req *connect.Request[v1.DismissPerformanceFindingRequest]) (*connect.Response[v1.DismissPerformanceFindingResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if req.Msg.Restore {
		if err := s.store.DeleteFindingDismissal(ctx, server.Id, req.Msg.FindingId); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restore finding: %w", err))
		}
		return connect.NewResponse(&v1.DismissPerformanceFindingResponse{}), nil
	}

	for _, f := range runtimespec.ReadFindings(server.DataPath) {
		if f.GetId() != req.Msg.FindingId {
			continue
		}
		dismissal := &v1.FindingDismissal{
			ServerId:    server.Id,
			FindingId:   f.GetId(),
			ContentHash: utils.FindingHash(f),
			DismissedAt: timestamppb.Now(),
		}
		if err := s.store.UpsertFindingDismissal(ctx, dismissal); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to dismiss finding: %w", err))
		}
		return connect.NewResponse(&v1.DismissPerformanceFindingResponse{}), nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("finding not found"))
}

// Returns the activity ledger for one server
func (s *ServerService) GetServerActions(ctx context.Context, req *connect.Request[v1.GetServerActionsRequest]) (*connect.Response[v1.GetServerActionsResponse], error) {
	if _, err := s.store.GetServer(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("server not found"))
	}
	rows, err := s.store.GetServerActions(ctx, req.Msg.Id, uint(req.Msg.AfterId))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load actions: %w", err))
	}
	return connect.NewResponse(&v1.GetServerActionsResponse{Actions: rows}), nil
}
