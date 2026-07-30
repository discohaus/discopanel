package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/discohaus/discopanel/internal/alias"
	"github.com/discohaus/discopanel/internal/auth"
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

// Compile-time check that ModuleService implements the interface
var _ discopanelv1connect.ModuleServiceHandler = (*ModuleService)(nil)

// ModuleService implements the Module service
type ModuleService struct {
	store            *storage.Store
	docker           *docker.Client
	moduleManager    *module.Manager
	proxyManager     *proxy.Manager
	authManager      *auth.Manager
	config           *config.Config
	metricsCollector *metrics.Collector
	rec              *metrics.Recorder
	log              *logger.Logger
	logStreamer      *logger.LogStreamer
}

func NewModuleService(
	store *storage.Store,
	docker *docker.Client,
	moduleManager *module.Manager,
	proxyManager *proxy.Manager,
	authManager *auth.Manager,
	cfg *config.Config,
	logStreamer *logger.LogStreamer,
	metricsCollector *metrics.Collector,
	rec *metrics.Recorder,
	log *logger.Logger,
) *ModuleService {
	return &ModuleService{
		store:            store,
		docker:           docker,
		moduleManager:    moduleManager,
		proxyManager:     proxyManager,
		authManager:      authManager,
		config:           cfg,
		logStreamer:      logStreamer,
		metricsCollector: metricsCollector,
		rec:              rec,
		log:              log,
	}
}

// Copies cached collector usage onto transient module fields
func (s *ModuleService) applyModuleStats(m *v1.Module) {
	if s.metricsCollector == nil || m.ContainerId == "" || m.Status != v1.ModuleStatus_MODULE_STATUS_RUNNING {
		return
	}
	stats := s.metricsCollector.GetModuleStats(m.Id)
	if stats == nil {
		return
	}
	m.CpuPercent = stats.CpuPercent
	m.MemoryUsage = stats.MemoryUsage
}

// Looks up the username behind a module's creator id
func (s *ModuleService) resolveCreatedByUsername(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	return user.Username
}

// Template operations

func (s *ModuleService) ListModuleTemplates(ctx context.Context, req *connect.Request[v1.ListModuleTemplatesRequest]) (*connect.Response[v1.ListModuleTemplatesResponse], error) {
	templates, err := s.store.ListModuleTemplates(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list templates: %w", err))
	}

	msg := req.Msg
	var protoTemplates []*v1.ModuleTemplate
	for _, t := range templates {
		// Filter by type if specified
		if msg.Type != nil && *msg.Type != v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_UNSPECIFIED {
			if t.Type != *msg.Type {
				continue
			}
		}
		// Filter by category if specified
		if msg.Category != nil && *msg.Category != "" && t.Category != *msg.Category {
			continue
		}
		protoTemplates = append(protoTemplates, t)
	}

	return connect.NewResponse(&v1.ListModuleTemplatesResponse{
		Templates: protoTemplates,
	}), nil
}

func (s *ModuleService) GetModuleTemplate(ctx context.Context, req *connect.Request[v1.GetModuleTemplateRequest]) (*connect.Response[v1.GetModuleTemplateResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("template ID is required"))
	}

	template, err := s.store.GetModuleTemplate(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("template not found"))
	}

	return connect.NewResponse(&v1.GetModuleTemplateResponse{
		Template: template,
	}), nil
}

func (s *ModuleService) CreateModuleTemplate(ctx context.Context, req *connect.Request[v1.CreateModuleTemplateRequest]) (*connect.Response[v1.CreateModuleTemplateResponse], error) {
	msg := req.Msg
	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if msg.DockerImage == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("docker_image is required"))
	}

	// Check for duplicate name
	if _, err := s.store.GetModuleTemplateByName(ctx, msg.Name); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("template with this name already exists"))
	}

	if err := module.ValidateConfigFieldDefs(msg.ConfigFields); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	template := &v1.ModuleTemplate{
		Id:                      uuid.New().String(),
		Name:                    msg.Name,
		Description:             msg.Description,
		Type:                    v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_CUSTOM, // User-created templates are always custom
		DockerImage:             msg.DockerImage,
		ConfigFields:            msg.ConfigFields,
		DefaultEnv:              msg.DefaultEnv,
		DefaultVolumes:          msg.DefaultVolumes,
		HealthCheckPath:         msg.HealthCheckPath,
		HealthCheckPort:         msg.HealthCheckPort,
		RequiresServer:          msg.RequiresServer,
		SupportsProxy:           msg.SupportsProxy,
		Icon:                    msg.Icon,
		Category:                msg.Category,
		Documentation:           msg.Documentation,
		Ports:                   msg.Ports,
		SuggestedDependencies:   msg.SuggestedDependencies,
		DefaultHooks:            msg.DefaultHooks,
		Metadata:                msg.Metadata,
		DefaultCmd:              msg.DefaultCmd,
		DefaultAccessUrls:       msg.DefaultAccessUrls,
		DefaultUid:              msg.DefaultUid,
		DefaultGid:              msg.DefaultGid,
		DefaultInitCommand:      msg.DefaultInitCommand,
		DefaultInitCommandDelay: msg.DefaultInitCommandDelay,
		DefaultRestartAfterInit: msg.DefaultRestartAfterInit,
		DefaultSecurityOpt:      msg.DefaultSecurityOpt,
	}

	if err := s.store.CreateModuleTemplate(ctx, template); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create template: %w", err))
	}

	return connect.NewResponse(&v1.CreateModuleTemplateResponse{
		Template: template,
	}), nil
}

func (s *ModuleService) UpdateModuleTemplate(ctx context.Context, req *connect.Request[v1.UpdateModuleTemplateRequest]) (*connect.Response[v1.UpdateModuleTemplateResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("template ID is required"))
	}

	template, err := s.store.GetModuleTemplate(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("template not found"))
	}

	// Don't allow modifying built-in templates' core fields
	if template.Type == v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot modify built-in template"))
	}

	// Update fields if provided
	if msg.Name != nil {
		template.Name = *msg.Name
	}
	if msg.Description != nil {
		template.Description = *msg.Description
	}
	if msg.DockerImage != nil {
		template.DockerImage = *msg.DockerImage
	}
	if len(msg.ConfigFields) > 0 {
		if err := module.ValidateConfigFieldDefs(msg.ConfigFields); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		template.ConfigFields = msg.ConfigFields
	}
	if msg.DefaultEnv != nil {
		template.DefaultEnv = msg.DefaultEnv
	}
	if msg.DefaultVolumes != nil {
		template.DefaultVolumes = msg.DefaultVolumes
	}
	if msg.HealthCheckPath != nil {
		template.HealthCheckPath = *msg.HealthCheckPath
	}
	if msg.HealthCheckPort != nil {
		template.HealthCheckPort = *msg.HealthCheckPort
	}
	if msg.RequiresServer != nil {
		template.RequiresServer = *msg.RequiresServer
	}
	if msg.SupportsProxy != nil {
		template.SupportsProxy = *msg.SupportsProxy
	}
	if msg.Icon != nil {
		template.Icon = *msg.Icon
	}
	if msg.Category != nil {
		template.Category = *msg.Category
	}
	if msg.Documentation != nil {
		template.Documentation = *msg.Documentation
	}
	if len(msg.Ports) > 0 {
		template.Ports = msg.Ports
	}
	if len(msg.SuggestedDependencies) > 0 {
		template.SuggestedDependencies = msg.SuggestedDependencies
	}
	if len(msg.DefaultHooks) > 0 {
		template.DefaultHooks = msg.DefaultHooks
	}
	if len(msg.Metadata) > 0 {
		template.Metadata = msg.Metadata
	}
	if msg.DefaultCmd != nil {
		template.DefaultCmd = *msg.DefaultCmd
	}
	if len(msg.DefaultAccessUrls) > 0 {
		template.DefaultAccessUrls = msg.DefaultAccessUrls
	}
	if msg.DefaultUid != nil {
		template.DefaultUid = *msg.DefaultUid
	}
	if msg.DefaultGid != nil {
		template.DefaultGid = *msg.DefaultGid
	}
	if msg.DefaultInitCommand != nil {
		template.DefaultInitCommand = *msg.DefaultInitCommand
	}
	if msg.DefaultInitCommandDelay != nil {
		template.DefaultInitCommandDelay = *msg.DefaultInitCommandDelay
	}
	if msg.DefaultRestartAfterInit != nil {
		template.DefaultRestartAfterInit = *msg.DefaultRestartAfterInit
	}
	if len(msg.DefaultSecurityOpt) > 0 {
		template.DefaultSecurityOpt = msg.DefaultSecurityOpt
	}

	if err := s.store.UpdateModuleTemplate(ctx, template); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update template: %w", err))
	}

	return connect.NewResponse(&v1.UpdateModuleTemplateResponse{
		Template: template,
	}), nil
}

func (s *ModuleService) DeleteModuleTemplate(ctx context.Context, req *connect.Request[v1.DeleteModuleTemplateRequest]) (*connect.Response[v1.DeleteModuleTemplateResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("template ID is required"))
	}

	template, err := s.store.GetModuleTemplate(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("template not found"))
	}

	// Don't allow deleting built-in templates
	if template.Type == v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot delete built-in template"))
	}

	if err := s.store.DeleteModuleTemplate(ctx, msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete template: %w", err))
	}

	return connect.NewResponse(&v1.DeleteModuleTemplateResponse{}), nil
}

// Module operations

func (s *ModuleService) ListModules(ctx context.Context, req *connect.Request[v1.ListModulesRequest]) (*connect.Response[v1.ListModulesResponse], error) {
	msg := req.Msg

	var modules []*v1.Module
	var err error

	if msg.ServerId != nil && *msg.ServerId != "" {
		modules, err = s.store.ListServerModules(ctx, *msg.ServerId)
	} else if msg.TemplateId != nil && *msg.TemplateId != "" {
		modules, err = s.store.ListModulesByTemplate(ctx, *msg.TemplateId)
	} else {
		modules, err = s.store.ListModules(ctx)
	}

	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list modules: %w", err))
	}

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list servers: %w", err))
	}
	serversByID := make(map[string]*v1.Server, len(servers))
	for _, srv := range servers {
		serversByID[srv.Id] = srv
	}
	templates, err := s.store.ListModuleTemplates(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list templates: %w", err))
	}
	templateNames := make(map[string]string, len(templates))
	for _, t := range templates {
		templateNames[t.Id] = t.Name
	}
	usernames := map[string]string{}

	fullStats := msg.FullStats != nil && *msg.FullStats
	if fullStats {
		// Live docker state serves the response, never the row
		var wg sync.WaitGroup
		for _, m := range modules {
			if m.ContainerId == "" {
				continue
			}
			wg.Add(1)
			go func(mod *v1.Module) {
				defer wg.Done()
				if actualStatus, err := s.moduleManager.StatusForModule(ctx, mod); err == nil {
					mod.Status = actualStatus
				}
			}(m)
		}
		wg.Wait()
	}
	var protoModules []*v1.Module
	for _, m := range modules {
		if m.ContainerId == "" && m.Status == v1.ModuleStatus_MODULE_STATUS_CREATING && time.Since(m.UpdatedAt.AsTime()) > 30*time.Second {
			m.Status = v1.ModuleStatus_MODULE_STATUS_ERROR
		}
		if fullStats {
			s.applyModuleStats(m)
		}

		serverName := ""
		serverProxyHostname := ""
		if srv := serversByID[m.ServerId]; srv != nil {
			serverName = srv.Name
			serverProxyHostname = srv.ProxyHostname
		}
		if _, ok := usernames[m.CreatedByUserId]; !ok {
			usernames[m.CreatedByUserId] = s.resolveCreatedByUsername(ctx, m.CreatedByUserId)
		}
		m.Template = nil
		m.ServerName = serverName
		m.TemplateName = templateNames[m.TemplateId]
		m.ServerProxyHostname = serverProxyHostname
		m.CreatedByUsername = usernames[m.CreatedByUserId]
		protoModules = append(protoModules, m.Redact())
	}

	return connect.NewResponse(&v1.ListModulesResponse{
		Modules: protoModules,
	}), nil
}

func (s *ModuleService) GetModule(ctx context.Context, req *connect.Request[v1.GetModuleRequest]) (*connect.Response[v1.GetModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	module, err := s.store.GetModule(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	// Get actual status from Docker and update if different
	if module.ContainerId != "" {
		actualStatus, err := s.moduleManager.StatusForModule(ctx, module)
		if err == nil && actualStatus != module.Status {
			module.Status = actualStatus
			s.store.UpdateModule(ctx, module)
		}
	}

	s.applyModuleStats(module)

	module.Template = nil
	if server, err := s.store.GetServer(ctx, module.ServerId); err == nil {
		module.ServerName = server.Name
		module.ServerProxyHostname = server.ProxyHostname
	}
	if template, err := s.store.GetModuleTemplate(ctx, module.TemplateId); err == nil {
		module.TemplateName = template.Name
	}
	module.CreatedByUsername = s.resolveCreatedByUsername(ctx, module.CreatedByUserId)
	return connect.NewResponse(&v1.GetModuleResponse{
		Module: module.Redact(),
	}), nil
}

// Proxied ports make no sense while the proxy is off
func (s *ModuleService) rejectProxiedPortsWhileDisabled(ports []*v1.NetworkPort) error {
	if s.proxyManager.Enabled() {
		return nil
	}
	for _, port := range ports {
		if port != nil && port.ProxyEnabled {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("proxy is disabled, port %s cannot be proxied", port.Name))
		}
	}
	return nil
}

// Validates hostname overrides on a port list in place
func normalizeModulePorts(ports []*v1.NetworkPort) error {
	for _, port := range ports {
		if port == nil {
			continue
		}
		hostname := proxy.NormalizeHostname(port.Hostname)
		if hostname == "" {
			port.Hostname = ""
			continue
		}
		if !proxy.ValidHostname(hostname) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid hostname %q on port %s", port.Hostname, port.Name))
		}
		switch port.Protocol {
		case v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT:
		default:
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hostnames only apply to http and minecraft ports, not %s", port.Name))
		}
		port.Hostname = hostname
	}
	return nil
}

func (s *ModuleService) CreateModule(ctx context.Context, req *connect.Request[v1.CreateModuleRequest]) (*connect.Response[v1.CreateModuleResponse], error) {
	msg := req.Msg
	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if msg.ServerId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server_id is required"))
	}
	if msg.TemplateId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("template_id is required"))
	}

	// Verify server exists
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Verify template exists
	template, err := s.store.GetModuleTemplate(ctx, msg.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("template not found"))
	}

	// Global templates run panel wide, never per server
	if template.Global {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this module runs panel wide and cannot attach to a server"))
	}

	// Use ports from request, or fall back to template defaults
	ports := msg.Ports
	if len(ports) == 0 {
		ports = template.Ports
	}

	if err := s.rejectProxiedPortsWhileDisabled(ports); err != nil {
		return nil, err
	}

	moduleID := uuid.New().String()

	// Registry allocates host ports for any entries that need one
	allocatedInRequest := make(map[int]bool)
	for _, port := range ports {
		if port == nil || port.ContainerPort == 0 {
			continue
		}
		if port.HostPort == 0 {
			allocatedPort, err := s.moduleManager.AllocateModulePortExcluding(ctx, port.Protocol, allocatedInRequest)
			if err != nil {
				return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("failed to allocate port: %w", err))
			}
			port.HostPort = int32(allocatedPort)
			allocatedInRequest[allocatedPort] = true
		}
	}

	if err := normalizeModulePorts(ports); err != nil {
		return nil, err
	}

	// Registry checkout guards every port until the row persists
	netOwner := proxy.NetOwner{Kind: proxy.OwnerModule, ID: moduleID}
	netReqs := s.proxyManager.ModuleNetRequests(&v1.Module{Id: moduleID, Ports: ports}, server.ProxyHostname)
	if err := s.proxyManager.EnsureListenersFor(ctx, netReqs); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	netClaim, err := s.proxyManager.CheckoutNetwork(ctx, netOwner, netReqs)
	if err != nil {
		// Reconcile retires any listener row made just above
		if serr := s.proxyManager.SyncListeners(ctx); serr != nil {
			s.log.Error("Failed to sync after checkout failure: %v", serr)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	defer netClaim.Release()

	module := &v1.Module{
		Id:                    moduleID,
		Name:                  msg.Name,
		ServerId:              msg.ServerId,
		TemplateId:            msg.TemplateId,
		Status:                v1.ModuleStatus_MODULE_STATUS_STOPPED,
		EnvOverrides:          msg.EnvOverrides,
		VolumeOverrides:       msg.VolumeOverrides,
		Memory:                msg.Memory,
		CpuLimit:              msg.CpuLimit,
		AutoStart:             msg.AutoStart,
		FollowServerLifecycle: msg.FollowServerLifecycle,
		Detached:              msg.Detached,
		Ports:                 ports,
		Dependencies:          msg.Dependencies,
		HealthCheckInterval:   msg.HealthCheckInterval,
		HealthCheckTimeout:    msg.HealthCheckTimeout,
		HealthCheckRetries:    msg.HealthCheckRetries,
		EventHooks:            msg.EventHooks,
		Metadata:              msg.Metadata,
		CmdOverride:           msg.CmdOverride,
		AccessUrls:            msg.AccessUrls,
		Uid:                   msg.Uid,
		Gid:                   msg.Gid,
		InitCommand:           msg.InitCommand,
		InitCommandDelay:      msg.InitCommandDelay,
		RestartAfterInit:      msg.RestartAfterInit,
	}

	// Manager mints a scoped token at container create
	if user := auth.GetUserFromContext(ctx); user != nil {
		module.CreatedByUserId = user.Id
	}

	// Use template defaults for access URLs if not provided
	if len(module.AccessUrls) == 0 {
		module.AccessUrls = template.DefaultAccessUrls
	}

	if module.Memory == 0 {
		if template.DefaultMemory > 0 {
			module.Memory = template.DefaultMemory
		} else {
			module.Memory = 512 // Default 512MB
		}
	}

	if module.Uid == "" && template.DefaultUid != "" {
		module.Uid = template.DefaultUid
	}
	if module.Gid == "" && template.DefaultGid != "" {
		module.Gid = template.DefaultGid
	}
	if module.InitCommand == "" && template.DefaultInitCommand != "" {
		module.InitCommand = template.DefaultInitCommand
	}
	if module.InitCommandDelay == 0 && template.DefaultInitCommandDelay > 0 {
		module.InitCommandDelay = template.DefaultInitCommandDelay
	}
	if !module.RestartAfterInit && template.DefaultRestartAfterInit {
		module.RestartAfterInit = template.DefaultRestartAfterInit
	}

	// Fill missing env from config field defaults
	for _, field := range template.ConfigFields {
		if field == nil || field.Env == "" || field.DefaultValue == "" {
			continue
		}
		if module.EnvOverrides == nil {
			module.EnvOverrides = make(map[string]string)
		}
		if _, ok := module.EnvOverrides[field.Env]; !ok {
			module.EnvOverrides[field.Env] = field.DefaultValue
		}
	}

	// Deny gate runs before anything persists
	if err := s.moduleManager.GateModuleConfig(ctx, module, template); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.store.CreateModule(ctx, module); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create module: %w", err))
	}
	netClaim.Confirm()

	// Reconcile starts any auto created listener sockets
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync routes after module create: %v", err)
	}
	s.rec.Record(ctx, module.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_MODULE_CREATE, metrics.Attrs{"module": module.Name, "template": template.Name}, "created module %s", module.Name)

	// Create container in background
	bgCtx := detach(ctx)
	go func() {
		if err := s.moduleManager.CreateAndStartModule(bgCtx, module.Id, msg.StartImmediately); err != nil {
			s.log.Error("Failed to create module container: %v", err)
		}
	}()

	module.Template = nil
	module.ServerName = server.Name
	module.TemplateName = template.Name
	module.ServerProxyHostname = server.ProxyHostname
	module.CreatedByUsername = s.resolveCreatedByUsername(ctx, module.CreatedByUserId)
	return connect.NewResponse(&v1.CreateModuleResponse{
		Module: module.Redact(),
	}), nil
}

func (s *ModuleService) UpdateModule(ctx context.Context, req *connect.Request[v1.UpdateModuleRequest]) (*connect.Response[v1.UpdateModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	module, err := s.store.GetModule(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	// Update fields if provided
	if msg.Name != nil {
		module.Name = *msg.Name
	}
	if msg.EnvOverrides != nil {
		module.EnvOverrides = msg.EnvOverrides
	}
	if msg.VolumeOverrides != nil {
		module.VolumeOverrides = msg.VolumeOverrides
	}
	if msg.Memory != nil {
		module.Memory = *msg.Memory
	}
	if msg.CpuLimit != nil {
		module.CpuLimit = *msg.CpuLimit
	}
	if msg.AutoStart != nil {
		module.AutoStart = *msg.AutoStart
	}
	if msg.FollowServerLifecycle != nil {
		module.FollowServerLifecycle = *msg.FollowServerLifecycle
	}
	if msg.Detached != nil {
		module.Detached = *msg.Detached
	}
	var netClaim *proxy.NetClaim
	if len(msg.Ports) > 0 {
		if err := s.rejectProxiedPortsWhileDisabled(msg.Ports); err != nil {
			return nil, err
		}

		// Global modules carry no server hostname context
		hostname := ""
		if module.ServerId != "" {
			server, err := s.store.GetServer(ctx, module.ServerId)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get server: %w", err))
			}
			hostname = server.ProxyHostname
		}

		// Registry allocates host ports for any entries that need one
		allocatedInRequest := make(map[int]bool)
		for _, port := range msg.Ports {
			if port == nil || port.ContainerPort == 0 || port.HostPort != 0 {
				continue
			}
			allocatedPort, err := s.moduleManager.AllocateModulePortExcluding(ctx, port.Protocol, allocatedInRequest)
			if err != nil {
				return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("failed to allocate port: %w", err))
			}
			port.HostPort = int32(allocatedPort)
			allocatedInRequest[allocatedPort] = true
		}

		if err := normalizeModulePorts(msg.Ports); err != nil {
			return nil, err
		}

		// Registry checkout guards the new ports until persist
		netReqs := s.proxyManager.ModuleNetRequests(&v1.Module{Id: module.Id, Ports: msg.Ports}, hostname)
		if err := s.proxyManager.EnsureListenersFor(ctx, netReqs); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		claim, err := s.proxyManager.CheckoutNetwork(ctx, proxy.NetOwner{Kind: proxy.OwnerModule, ID: module.Id}, netReqs)
		if err != nil {
			// Reconcile retires any listener row made just above
			if serr := s.proxyManager.SyncListeners(ctx); serr != nil {
				s.log.Error("Failed to sync after checkout failure: %v", serr)
			}
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		netClaim = claim
		defer netClaim.Release()

		module.Ports = msg.Ports
	} else if msg.ClearPorts {
		module.Ports = nil
	}
	if len(msg.Dependencies) > 0 {
		module.Dependencies = msg.Dependencies
	}
	if msg.HealthCheckInterval != nil {
		module.HealthCheckInterval = *msg.HealthCheckInterval
	}
	if msg.HealthCheckTimeout != nil {
		module.HealthCheckTimeout = *msg.HealthCheckTimeout
	}
	if msg.HealthCheckRetries != nil {
		module.HealthCheckRetries = *msg.HealthCheckRetries
	}
	if len(msg.EventHooks) > 0 {
		module.EventHooks = msg.EventHooks
	}
	if len(msg.Metadata) > 0 {
		module.Metadata = msg.Metadata
	}
	if msg.CmdOverride != nil {
		module.CmdOverride = *msg.CmdOverride
	}
	if len(msg.AccessUrls) > 0 {
		module.AccessUrls = msg.AccessUrls
	}
	if msg.Uid != nil {
		module.Uid = *msg.Uid
	}
	if msg.Gid != nil {
		module.Gid = *msg.Gid
	}
	if msg.InitCommand != nil {
		module.InitCommand = *msg.InitCommand
	}
	if msg.InitCommandDelay != nil {
		module.InitCommandDelay = *msg.InitCommandDelay
	}
	if msg.RestartAfterInit != nil {
		module.RestartAfterInit = *msg.RestartAfterInit
	}

	template, err := s.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("template not found"))
	}

	// Deny gate runs on the fully merged state
	if err := s.moduleManager.GateModuleConfig(ctx, module, template); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.store.UpdateModule(ctx, module); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update module: %w", err))
	}
	netClaim.Confirm()

	// Reconcile keeps routes matching the saved ports
	if err := s.proxyManager.SyncListeners(ctx); err != nil {
		s.log.Error("Failed to sync routes after module update: %v", err)
	}

	// Config hash decides whether the container must rebuild
	if needsRecreate, err := s.moduleManager.NeedsRecreate(ctx, module.Id); err == nil && needsRecreate {
		go func() {
			bgCtx := context.Background()
			if err := s.moduleManager.RecreateModule(bgCtx, module.Id); err != nil {
				s.log.Error("Failed to recreate module container: %v", err)
			}
		}()
	}

	module.Template = nil
	if server, err := s.store.GetServer(ctx, module.ServerId); err == nil {
		module.ServerName = server.Name
		module.ServerProxyHostname = server.ProxyHostname
	}
	module.TemplateName = template.Name
	module.CreatedByUsername = s.resolveCreatedByUsername(ctx, module.CreatedByUserId)
	return connect.NewResponse(&v1.UpdateModuleResponse{
		Module: module.Redact(),
	}), nil
}

func (s *ModuleService) DeleteModule(ctx context.Context, req *connect.Request[v1.DeleteModuleRequest]) (*connect.Response[v1.DeleteModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}
	module, _ := s.store.GetModule(ctx, msg.Id)

	// System modules only ever disable, never delete
	if module != nil && module.ServerId == "" {
		if template, terr := s.store.GetModuleTemplate(ctx, module.TemplateId); terr == nil &&
			template.Type == v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("system module %s can only be disabled", module.Name))
		}
	}

	if err := s.moduleManager.DeleteModule(ctx, msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to delete module: %w", err))
	}
	if module != nil {
		s.rec.Record(ctx, module.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_MODULE_DELETE, metrics.Attrs{"module": module.Name}, "deleted module %s", module.Name)
	}

	return connect.NewResponse(&v1.DeleteModuleResponse{}), nil
}

// Lifecycle operations

func (s *ModuleService) StartModule(ctx context.Context, req *connect.Request[v1.StartModuleRequest]) (*connect.Response[v1.StartModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	module, err := s.store.GetModule(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	// If no container exists, create one first
	if module.ContainerId == "" {
		if err := s.moduleManager.CreateAndStartModule(ctx, msg.Id, true); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create and start module: %w", err))
		}
	} else {
		if err := s.moduleManager.StartModule(ctx, msg.Id); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to start module: %w", err))
		}
	}

	s.rec.Record(ctx, module.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_MODULE_START, metrics.Attrs{"module": module.Name}, "started module %s", module.Name)

	return connect.NewResponse(&v1.StartModuleResponse{
		Status: s.moduleStatus(ctx, msg.Id),
	}), nil
}

func (s *ModuleService) StopModule(ctx context.Context, req *connect.Request[v1.StopModuleRequest]) (*connect.Response[v1.StopModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	if err := s.moduleManager.StopModule(ctx, msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to stop module: %w", err))
	}
	if module, err := s.store.GetModule(ctx, msg.Id); err == nil {
		s.rec.Record(ctx, module.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_MODULE_STOP, metrics.Attrs{"module": module.Name}, "stopped module %s", module.Name)
	}

	return connect.NewResponse(&v1.StopModuleResponse{
		Status: s.moduleStatus(ctx, msg.Id),
	}), nil
}

func (s *ModuleService) RestartModule(ctx context.Context, req *connect.Request[v1.RestartModuleRequest]) (*connect.Response[v1.RestartModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	if err := s.moduleManager.RestartModule(ctx, msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restart module: %w", err))
	}
	if module, err := s.store.GetModule(ctx, msg.Id); err == nil {
		s.rec.Record(ctx, module.ServerId, v1.ServerActionKind_SERVER_ACTION_KIND_MODULE_RESTART, metrics.Attrs{"module": module.Name}, "restarted module %s", module.Name)
	}

	return connect.NewResponse(&v1.RestartModuleResponse{
		Status: s.moduleStatus(ctx, msg.Id),
	}), nil
}

func (s *ModuleService) RecreateModule(ctx context.Context, req *connect.Request[v1.RecreateModuleRequest]) (*connect.Response[v1.RecreateModuleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	if err := s.moduleManager.RecreateModule(ctx, msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to recreate module: %w", err))
	}

	return connect.NewResponse(&v1.RecreateModuleResponse{
		Status: s.moduleStatus(ctx, msg.Id),
	}), nil
}

// Current stored status for op responses
func (s *ModuleService) moduleStatus(ctx context.Context, id string) v1.ModuleStatus {
	if module, err := s.store.GetModule(ctx, id); err == nil {
		return module.Status
	}
	return v1.ModuleStatus_MODULE_STATUS_UNSPECIFIED
}

// Logs and status

func (s *ModuleService) GetModuleLogs(ctx context.Context, req *connect.Request[v1.GetModuleLogsRequest]) (*connect.Response[v1.GetModuleLogsResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}

	module, err := s.store.GetModule(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	tail := msg.Tail
	if tail == 0 {
		tail = 100
	}

	// Get structured log entries from the log streamer if available
	var protoLogs []*v1.LogEntry
	if s.logStreamer != nil {
		if module.ContainerId != "" {
			if err := s.logStreamer.StartStreaming(module.Id, module.ContainerId); err != nil {
				s.log.Warn("Failed to start log streaming for module %s: %v", module.Id, err)
			}
		}
		protoLogs = s.logStreamer.GetLogs(module.Id, int(tail))
	}

	return connect.NewResponse(&v1.GetModuleLogsResponse{
		Logs:  protoLogs,
		Total: int32(len(protoLogs)),
	}), nil
}

func (s *ModuleService) GetNextAvailableModulePort(ctx context.Context, req *connect.Request[v1.GetNextAvailableModulePortRequest]) (*connect.Response[v1.GetNextAvailableModulePortResponse], error) {
	port, err := s.moduleManager.AllocateModulePort(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}

	// Registry snapshot backs the client side hints
	usedPorts, err := s.proxyManager.UsedNetworkPorts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var protoUsedPorts []*v1.UsedPort
	for _, p := range usedPorts {
		protoUsedPorts = append(protoUsedPorts, &v1.UsedPort{Port: p})
	}

	return connect.NewResponse(&v1.GetNextAvailableModulePortResponse{
		Port:      int32(port),
		UsedPorts: protoUsedPorts,
	}), nil
}

// GetAvailableAliases returns all available aliases for module/template configuration
func (s *ModuleService) GetAvailableAliases(ctx context.Context, req *connect.Request[v1.GetAvailableAliasesRequest]) (*connect.Response[v1.GetAvailableAliasesResponse], error) {
	msg := req.Msg

	// Build alias context from request
	aliasCtx := alias.NewContext()
	aliasCtx.Config = s.config

	// Get server context if provided
	if msg.ServerId != nil && *msg.ServerId != "" {
		if server, err := s.store.GetServer(ctx, *msg.ServerId); err == nil {
			s.store.HydrateProxyPorts(ctx, server)
			aliasCtx.Server = server
			// Also get server config for server.config.* aliases
			if serverConfig, err := s.store.GetServerProperties(ctx, *msg.ServerId); err == nil {
				aliasCtx.ServerProperties = serverConfig
			}
		}
	}

	// Get module context if provided
	if msg.ModuleId != nil && *msg.ModuleId != "" {
		if mod, err := s.store.GetModule(ctx, *msg.ModuleId); err == nil {
			aliasCtx.Module = mod
		}
	}

	return connect.NewResponse(&v1.GetAvailableAliasesResponse{
		Aliases: alias.GetAvailableAliases(aliasCtx),
	}), nil
}

// Get all aliases with resolved values for ctx
func (s *ModuleService) GetResolvedAliases(ctx context.Context, req *connect.Request[v1.GetResolvedAliasesRequest]) (*connect.Response[v1.GetResolvedAliasesResponse], error) {
	msg := req.Msg
	aliasCtx := alias.NewContext()
	aliasCtx.Config = s.config

	if msg.ServerId != nil && *msg.ServerId != "" {
		if server, err := s.store.GetServer(ctx, *msg.ServerId); err == nil {
			s.store.HydrateProxyPorts(ctx, server)
			aliasCtx.Server = server
			if serverConfig, err := s.store.GetServerProperties(ctx, *msg.ServerId); err == nil {
				aliasCtx.ServerProperties = serverConfig
			}
		}
	}

	if msg.ModuleId != nil && *msg.ModuleId != "" {
		if mod, err := s.store.GetModule(ctx, *msg.ModuleId); err == nil {
			aliasCtx.Module = mod
			if siblings, err := s.store.ListServerModules(ctx, mod.ServerId); err == nil {
				aliasCtx.Modules = make(map[string]*v1.Module)
				for _, sib := range siblings {
					aliasCtx.Modules[sib.Name] = sib
				}
			}
		}
	}

	resolved := alias.GetResolvedAliases(aliasCtx)
	return connect.NewResponse(&v1.GetResolvedAliasesResponse{Aliases: resolved}), nil
}

// Runtime input prompts

// Wire shape of a module health port prompt
type modulePromptWire struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Message     string `json:"message"`
	Kind        string `json:"kind"`
	Placeholder string `json:"placeholder"`
	Options     []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"options"`
	CreatedAt time.Time `json:"created_at"`
}

// Maps sidecar prompt kind onto config field type
func promptKind(kind string) v1.ModuleConfigFieldType {
	switch kind {
	case "password":
		return v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_PASSWORD
	case "select":
		return v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT
	default:
		return v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_STRING
	}
}

// Resolves the base URL of a running module's health port
func (s *ModuleService) moduleHTTPBase(ctx context.Context, module *v1.Module) (string, error) {
	if module.ContainerId == "" {
		return "", errors.New("module is not running")
	}
	template, err := s.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return "", errors.New("template not found")
	}
	port := template.HealthCheckPort
	if port == 0 && len(module.Ports) > 0 {
		port = module.Ports[0].ContainerPort
	}
	if port == 0 {
		return "", errors.New("module has no health port")
	}
	ip, err := s.docker.GetModuleContainerIP(ctx, module.ContainerId)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%d", ip, port), nil
}

// Fetches the pending prompt from one module, nil when idle
func (s *ModuleService) fetchModulePrompt(ctx context.Context, module *v1.Module) (*v1.ModulePrompt, error) {
	base, err := s.moduleHTTPBase(ctx, module)
	if err != nil {
		// Not running or no endpoint means nothing pending
		return nil, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/prompt", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		// Module may not implement prompts, treat as nothing pending
		return nil, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var wire modulePromptWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode prompt: %w", err)
	}

	prompt := &v1.ModulePrompt{
		Id:          wire.ID,
		Title:       wire.Title,
		Message:     wire.Message,
		Kind:        promptKind(wire.Kind),
		Placeholder: wire.Placeholder,
		CreatedAt:   timestamppb.New(wire.CreatedAt),
	}
	for _, o := range wire.Options {
		prompt.Options = append(prompt.Options, &v1.ModuleConfigOption{Value: o.Value, Label: o.Label})
	}
	return prompt, nil
}

func (s *ModuleService) GetModulePrompt(ctx context.Context, req *connect.Request[v1.GetModulePromptRequest]) (*connect.Response[v1.GetModulePromptResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}
	module, err := s.store.GetModule(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	prompt, err := s.fetchModulePrompt(ctx, module)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if prompt == nil {
		return connect.NewResponse(&v1.GetModulePromptResponse{Pending: false}), nil
	}
	return connect.NewResponse(&v1.GetModulePromptResponse{Pending: true, Prompt: prompt}), nil
}

func (s *ModuleService) ListModulePrompts(ctx context.Context, req *connect.Request[v1.ListModulePromptsRequest]) (*connect.Response[v1.ListModulePromptsResponse], error) {
	msg := req.Msg

	var modules []*v1.Module
	var err error
	if msg.ServerId != nil && *msg.ServerId != "" {
		modules, err = s.store.ListServerModules(ctx, *msg.ServerId)
	} else {
		modules, err = s.store.ListModules(ctx)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list modules: %w", err))
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	pending := []*v1.PendingModulePrompt{}
	for _, mod := range modules {
		if mod.ContainerId == "" {
			continue
		}
		// Only prompt capable templates get probed
		template, terr := s.store.GetModuleTemplate(ctx, mod.TemplateId)
		if terr != nil || template.Metadata["supports_prompts"] != "true" {
			continue
		}
		wg.Add(1)
		go func(m *v1.Module) {
			defer wg.Done()
			prompt, perr := s.fetchModulePrompt(ctx, m)
			if perr != nil || prompt == nil {
				return
			}
			mu.Lock()
			pending = append(pending, &v1.PendingModulePrompt{
				ModuleId:   m.Id,
				ModuleName: m.Name,
				Prompt:     prompt,
			})
			mu.Unlock()
		}(mod)
	}
	wg.Wait()

	// Stable order keeps the UI from reshuffling between polls
	sort.Slice(pending, func(i, j int) bool { return pending[i].ModuleId < pending[j].ModuleId })
	return connect.NewResponse(&v1.ListModulePromptsResponse{Prompts: pending}), nil
}

// Renders one decoded snapshot value as a display string
func snapshotValue(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case bool:
		return strconv.FormatBool(val)
	case json.Number:
		return val.String()
	default:
		raw, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(raw)
	}
}

func (s *ModuleService) GetModuleStatusSnapshot(ctx context.Context, req *connect.Request[v1.GetModuleStatusSnapshotRequest]) (*connect.Response[v1.GetModuleStatusSnapshotResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID is required"))
	}
	module, err := s.store.GetModule(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	unavailable := connect.NewResponse(&v1.GetModuleStatusSnapshotResponse{Available: false})

	// Only templates declaring a status path get probed
	template, err := s.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil || template.Metadata["status_path"] == "" {
		return unavailable, nil
	}
	base, err := s.moduleHTTPBase(ctx, module)
	if err != nil {
		return unavailable, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+template.Metadata["status_path"], nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return unavailable, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unavailable, nil
	}

	// UseNumber keeps SteamID64 sized values exact
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	var wire map[string]any
	if err := decoder.Decode(&wire); err != nil {
		return unavailable, nil
	}

	fields := make(map[string]string, len(wire))
	for key, value := range wire {
		fields[key] = snapshotValue(value)
	}
	return connect.NewResponse(&v1.GetModuleStatusSnapshotResponse{Available: true, Fields: fields}), nil
}

func (s *ModuleService) AnswerModulePrompt(ctx context.Context, req *connect.Request[v1.AnswerModulePromptRequest]) (*connect.Response[v1.AnswerModulePromptResponse], error) {
	msg := req.Msg
	if msg.Id == "" || msg.PromptId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("module ID and prompt ID are required"))
	}
	module, err := s.store.GetModule(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("module not found"))
	}

	base, err := s.moduleHTTPBase(ctx, module)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	body, err := json.Marshal(map[string]string{"id": msg.PromptId, "value": msg.Value})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/prompt", bytes.NewReader(body))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("module unreachable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, connect.NewError(connect.CodeAborted, errors.New("prompt is no longer waiting for this answer"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("module rejected answer: %d", resp.StatusCode))
	}
	return connect.NewResponse(&v1.AnswerModulePromptResponse{Accepted: true}), nil
}
