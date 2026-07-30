package module

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/discohaus/discopanel/internal/alias"
	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handles the lifecycle of modules
type Manager struct {
	store        *storage.Store
	docker       *docker.Client
	sender       *command.Sender
	config       *config.Config
	proxyManager *proxy.Manager
	logger       *logger.Logger
	logStreamer  *logger.LogStreamer
	tokenMinter  TokenMinter
	mu           sync.Mutex
	running      bool
}

// Mints scoped API tokens for module containers
type TokenMinter interface {
	GenerateModuleToken(ctx context.Context, userID, moduleName, moduleID, role string) (string, *v1.ApiToken, error)
}

// Creates a new module manager
func NewManager(store *storage.Store, docker *docker.Client, sender *command.Sender, cfg *config.Config, proxyManager *proxy.Manager, log *logger.Logger) *Manager {
	return &Manager{
		store:        store,
		docker:       docker,
		sender:       sender,
		config:       cfg,
		proxyManager: proxyManager,
		logger:       log,
	}
}

// Sets the module token minter
func (m *Manager) SetTokenMinter(minter TokenMinter) {
	m.tokenMinter = minter
}

// Sets log streamer for module containers
func (m *Manager) SetLogStreamer(streamer *logger.LogStreamer) {
	m.logStreamer = streamer
}

// Initializes the module manager
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil
	}

	if !m.config.Module.Enabled {
		m.logger.Info("Module system is disabled in configuration")
		return nil
	}

	// Initialize built-in templates
	if err := InitBuiltinTemplates(m.store); err != nil {
		m.logger.Error("Failed to initialize built-in module templates: %v", err)
	}

	m.running = true
	m.logger.Info("Module manager started")

	// Doctor module lives for the panel's lifetime
	if m.config.Module.DoctorEnabled {
		go m.seedDoctorModule()
	}
	return nil
}

// Seeds and starts the global doctor module instance
func (m *Manager) seedDoctorModule() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	modules, err := m.store.ListModules(ctx)
	if err != nil {
		m.logger.Error("Doctor seed: failed to list modules: %v", err)
		return
	}
	var doctor *v1.Module
	for _, mod := range modules {
		if mod.TemplateId == doctorTemplateID {
			doctor = mod
			break
		}
	}

	if doctor == nil {
		ports := doctorPorts(m.config)
		// Registry moves the doctor off a taken default port
		if m.proxyManager != nil && len(ports) > 0 {
			owner := proxy.NetOwner{Kind: proxy.OwnerModule, ID: "builtin-doctor-instance"}
			probe := &v1.Module{Id: owner.ID, Ports: ports}
			if err := m.proxyManager.ValidateNetwork(ctx, owner, m.proxyManager.ModuleNetRequests(probe, "")); err != nil {
				free, ferr := m.AllocateModulePortExcluding(ctx, ports[0].Protocol, nil)
				if ferr != nil {
					m.logger.Error("Doctor seed: no free port for doctor: %v", ferr)
					return
				}
				ports[0].HostPort = int32(free)
			}
		}

		// Bootstrapped builtins have no owner, supermodule token instead
		doctor = &v1.Module{
			Id:                    "builtin-doctor-instance",
			Name:                  "Doctor",
			TemplateId:            doctorTemplateID,
			Status:                v1.ModuleStatus_MODULE_STATUS_STOPPED,
			AutoStart:             true,
			FollowServerLifecycle: false,
			Memory:                512,
			Ports:                 ports,
			EnvOverrides:          doctorEnv(),
			VolumeOverrides:       doctorVolumes(),
			AccessUrls:            doctorAccessURLs(),
			Uid:                   doctorUID,
			Gid:                   doctorGID,
		}
		if err := m.store.CreateModule(ctx, doctor); err != nil {
			m.logger.Error("Doctor seed: failed to create module: %v", err)
			return
		}
		m.logger.Info("Seeded the global doctor module")
	}

	// AutoStart off means the user disabled it, respect that
	if !doctor.AutoStart {
		return
	}
	if err := m.StartModule(ctx, doctor.Id); err != nil {
		m.logger.Error("Doctor seed: failed to start doctor module: %v", err)
	}
}

// Gracefully stops all managed modules
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	ctx := context.Background()
	modules, err := m.store.ListModules(ctx)
	if err != nil {
		m.logger.Error("Failed to list modules for shutdown: %v", err)
	} else {
		for _, module := range modules {
			if module.Detached {
				m.logger.Info("Skipping shutdown of detached module: %s", module.Name)
				continue
			}

			if module.Status == v1.ModuleStatus_MODULE_STATUS_RUNNING {
				m.logger.Info("Stopping module: %s", module.Name)
				if err := m.StopModule(ctx, module.Id); err != nil {
					m.logger.Error("Failed to stop module %s: %v", module.Name, err)
				}
			}
		}
	}

	m.running = false
	m.logger.Info("Module manager stopped")
	return nil
}

// Creates a container and optionally starts the module
func (m *Manager) CreateAndStartModule(ctx context.Context, moduleID string, startImmediately bool) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	template, err := m.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return fmt.Errorf("failed to get module template: %w", err)
	}

	// Global modules run without a server attachment
	var server *v1.Server
	if module.ServerId != "" {
		server, err = m.loadServer(ctx, module.ServerId)
		if err != nil {
			return fmt.Errorf("failed to get server: %w", err)
		}
	}

	// Fresh scoped token each create, plaintext lives only in env
	if moduleRequiresToken(module, template) {
		if m.tokenMinter == nil {
			return fmt.Errorf("module %s requires an api token but minter is not ready", module.Name)
		}
		if module.TokenId != "" {
			if err := m.store.DeleteApiToken(ctx, module.TokenId); err != nil {
				m.logger.Warn("Failed to delete stale module token: %v", err)
			}
		}
		plaintext, token, err := m.tokenMinter.GenerateModuleToken(ctx, module.CreatedByUserId, module.Name, module.Id, template.Metadata["module_role"])
		if err != nil {
			return fmt.Errorf("failed to mint module token: %w", err)
		}
		module.TokenId = token.Id
		module.TokenPlaintext = plaintext
	}

	// Update status to creating
	module.Status = v1.ModuleStatus_MODULE_STATUS_CREATING
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Fetch server config for alias resolution
	var serverConfig *v1.ServerProperties
	if server != nil {
		serverConfig, _ = m.store.GetServerProperties(ctx, server.Id)
	}

	// Create the container
	containerID, err := m.docker.CreateModuleContainer(ctx, module, template, server, serverConfig, m.config, m.siblingModules(ctx, module))
	if err != nil {
		module.Status = v1.ModuleStatus_MODULE_STATUS_ERROR
		m.store.UpdateModule(ctx, module)
		return fmt.Errorf("failed to create module container: %w", err)
	}

	// Update module with container ID
	module.ContainerId = containerID
	module.Status = v1.ModuleStatus_MODULE_STATUS_STOPPED
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module with container ID: %w", err)
	}

	m.logger.Info("Created module container %s for module %s", containerID[:12], module.Name)

	if startImmediately {
		return m.StartModule(ctx, moduleID)
	}

	return nil
}

// True when module runs with a scoped or supermodule token
func moduleRequiresToken(module *v1.Module, template *v1.ModuleTemplate) bool {
	if module.CreatedByUserId != "" {
		return true
	}
	return template.Type == v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN &&
		template.Metadata["module_role"] != ""
}

// True when module should hold a token but none exists
func (m *Manager) moduleTokenMissing(ctx context.Context, module *v1.Module) bool {
	if m.tokenMinter == nil {
		return false
	}
	template, err := m.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return false
	}
	if !moduleRequiresToken(module, template) {
		return false
	}
	if module.TokenId == "" {
		return true
	}
	_, err = m.store.GetApiToken(ctx, module.TokenId)
	return err != nil
}

// Loads a server with transient proxy port filled
func (m *Manager) loadServer(ctx context.Context, serverID string) (*v1.Server, error) {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if err := m.store.HydrateProxyPorts(ctx, server); err != nil {
		return nil, err
	}
	return server, nil
}

// Sibling modules by name for inter-module alias references
func (m *Manager) siblingModules(ctx context.Context, module *v1.Module) map[string]*v1.Module {
	siblings := make(map[string]*v1.Module)
	serverModules, err := m.store.ListServerModules(ctx, module.ServerId)
	if err == nil {
		for _, sibling := range serverModules {
			if sibling.Id != module.Id {
				siblings[sibling.Name] = sibling
			}
		}
	}
	return siblings
}

// Blocks persist when deny severity config checks fail
func (m *Manager) GateModuleConfig(ctx context.Context, module *v1.Module, template *v1.ModuleTemplate) error {
	if len(template.ConfigFields) == 0 {
		return nil
	}
	aliasCtx := alias.NewContext()
	aliasCtx.Config = m.config
	aliasCtx.Module = module
	if module.ServerId != "" {
		if server, err := m.loadServer(ctx, module.ServerId); err == nil {
			aliasCtx.Server = server
			if props, err := m.store.GetServerProperties(ctx, module.ServerId); err == nil {
				aliasCtx.ServerProperties = props
			}
		}
	}
	aliasCtx.Modules = m.siblingModules(ctx, module)
	return DenyError(ValidateConfigFields(template.ConfigFields, module.EnvOverrides, aliasCtx))
}

// True when the container's create-time hash drifted from config
func (m *Manager) NeedsRecreate(ctx context.Context, moduleID string) (bool, error) {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return false, err
	}
	if module.ContainerId == "" {
		return false, nil
	}
	template, err := m.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return false, err
	}
	// Global modules run without a server attachment
	var server *v1.Server
	var serverConfig *v1.ServerProperties
	if module.ServerId != "" {
		server, err = m.loadServer(ctx, module.ServerId)
		if err != nil {
			return false, err
		}
		serverConfig, _ = m.store.GetServerProperties(ctx, module.ServerId)
	}
	current, err := m.docker.ModuleContainerConfigHash(ctx, module.ContainerId)
	if err != nil {
		return false, err
	}
	desired := m.docker.DesiredModuleConfigHash(module, template, server, serverConfig, m.config, m.siblingModules(ctx, module))
	return current != desired, nil
}

// Starts an existing module container
func (m *Manager) StartModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	if module.ContainerId == "" {
		return m.CreateAndStartModule(ctx, moduleID, true)
	}

	// Check if container still exists in Docker
	_, err = m.docker.GetContainerStatus(ctx, module.ContainerId)
	if err != nil {
		// Container doesn't exist, recreate it
		m.logger.Info("Container for module %s no longer exists, recreating", module.Name)
		module.ContainerId = ""
		if err := m.store.UpdateModule(ctx, module); err != nil {
			return fmt.Errorf("failed to clear module container ID: %w", err)
		}
		return m.CreateAndStartModule(ctx, moduleID, true)
	}

	// Stale config hash rebuilds the container before start
	stale, err := m.NeedsRecreate(ctx, moduleID)
	if err != nil {
		m.logger.Warn("Failed to check config drift for module %s: %v", module.Name, err)
	}
	if stale {
		m.logger.Info("Container for module %s has stale config, recreating", module.Name)
	}

	// Token excluded from hash, missing one still forces rebuild
	if !stale && m.moduleTokenMissing(ctx, module) {
		m.logger.Info("Container for module %s has no api token, recreating", module.Name)
		stale = true
	}
	if stale {
		if err := m.docker.RemoveContainer(ctx, module.ContainerId); err != nil {
			m.logger.Error("Failed to remove stale module container: %v", err)
		}
		module.ContainerId = ""
		if err := m.store.UpdateModule(ctx, module); err != nil {
			return fmt.Errorf("failed to clear module container ID: %w", err)
		}
		return m.CreateAndStartModule(ctx, moduleID, true)
	}

	// Start dependencies first
	if err := m.startDependencies(ctx, module); err != nil {
		return fmt.Errorf("failed to start dependencies: %w", err)
	}

	// Update status
	module.Status = v1.ModuleStatus_MODULE_STATUS_STARTING
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Start the container
	if err := m.docker.StartContainer(ctx, module.ContainerId); err != nil {
		module.Status = v1.ModuleStatus_MODULE_STATUS_ERROR
		m.store.UpdateModule(ctx, module)
		return fmt.Errorf("failed to start module container: %w", err)
	}

	// Start log streaming
	if m.logStreamer != nil {
		if err := m.logStreamer.StartStreaming(module.Id, module.ContainerId); err != nil {
			m.logger.Warn("Failed to start log streaming for module %s: %v", module.Name, err)
		}
	}

	// Update status and timestamps
	module.Status = v1.ModuleStatus_MODULE_STATUS_RUNNING
	module.LastStarted = timestamppb.Now()
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Reconcile registers the module's routes
	if m.proxyManager != nil {
		if err := m.proxyManager.SyncListeners(ctx); err != nil {
			m.logger.Error("Failed to sync routes after starting %s: %v", module.Name, err)
		}
	}

	m.logger.Info("Started module: %s", module.Name)

	// Run init command in background if configured
	if module.InitCommand != "" {
		go m.runInitCommand(module.Id)
	}

	return nil
}

// Executes the module's init command after an optional delay
func (m *Manager) runInitCommand(moduleID string) {
	ctx := context.Background()

	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		m.logger.Error("Init command: failed to get module %s: %v", moduleID, err)
		return
	}

	if module.InitCommandDelay > 0 {
		m.logger.Info("Init command: waiting %ds before executing for module %s", module.InitCommandDelay, module.Name)
		time.Sleep(time.Duration(module.InitCommandDelay) * time.Second)
	}

	// Verify container is still running
	status, err := m.GetModuleStatus(ctx, moduleID)
	if err != nil || status != v1.ModuleStatus_MODULE_STATUS_RUNNING {
		m.logger.Warn("Init command: module %s is no longer running, skipping", module.Name)
		return
	}

	m.logger.Info("Init command: executing for module %s: %s", module.Name, module.InitCommand)
	stdout, stderr, err := m.docker.Exec(ctx, module.ContainerId, []string{"sh", "-c", module.InitCommand})
	if err != nil {
		m.logger.Error("Init command: failed for module %s: %v", module.Name, err)
		return
	}
	if stdout != "" {
		m.logger.Info("Init command: output for module %s: %s", module.Name, stdout)
	}
	if stderr != "" {
		m.logger.Warn("Init command: stderr for module %s: %s", module.Name, stderr)
	}

	if module.RestartAfterInit {
		m.logger.Info("Init command: restarting module %s after init", module.Name)
		if err := m.docker.RestartContainer(ctx, module.ContainerId, 5*time.Second); err != nil {
			m.logger.Error("Init command: failed to restart module %s: %v", module.Name, err)
		}
	}
}

// Starts and waits for module dependencies
func (m *Manager) startDependencies(ctx context.Context, module *v1.Module) error {
	if len(module.Dependencies) == 0 {
		return nil
	}

	for _, dep := range module.Dependencies {
		if dep == nil || dep.ModuleId == "" {
			continue
		}

		depModule, err := m.store.GetModule(ctx, dep.ModuleId)
		if err != nil {
			return fmt.Errorf("dependency module %s not found: %w", dep.ModuleId, err)
		}

		// Start dependency if not running
		if depModule.Status != v1.ModuleStatus_MODULE_STATUS_RUNNING {
			m.logger.Info("Starting dependency %s for module %s", depModule.Name, module.Name)

			// Create container if needed
			if depModule.ContainerId == "" {
				if err := m.CreateAndStartModule(ctx, dep.ModuleId, true); err != nil {
					return fmt.Errorf("failed to create and start dependency %s: %w", depModule.Name, err)
				}
			} else {
				if err := m.StartModule(ctx, dep.ModuleId); err != nil {
					return fmt.Errorf("failed to start dependency %s: %w", depModule.Name, err)
				}
			}
		}

		// Wait for dependency to be healthy if configured
		if dep.WaitForHealthy {
			timeout := int(dep.TimeoutSeconds)
			if timeout == 0 {
				timeout = 60 // Default 60 seconds
			}
			if err := m.waitForHealthy(ctx, dep.ModuleId, timeout); err != nil {
				return fmt.Errorf("dependency %s not healthy: %w", depModule.Name, err)
			}
		}
	}

	return nil
}

// Waits for a module to pass its health check
func (m *Manager) waitForHealthy(ctx context.Context, moduleID string, timeoutSeconds int) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return err
	}

	template, err := m.store.GetModuleTemplate(ctx, module.TemplateId)
	if err != nil {
		return err
	}

	// No health check configured, just wait for container running
	if template.HealthCheckPath == "" && template.HealthCheckPort == 0 {
		m.logger.Debug("No health check configured for %s, checking container status", module.Name)
		return m.waitForRunning(ctx, moduleID, timeoutSeconds)
	}

	// Perform HTTP health check
	interval := time.Duration(module.HealthCheckInterval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	timeout := time.After(time.Duration(timeoutSeconds) * time.Second)
	retries := module.HealthCheckRetries
	if retries == 0 {
		retries = 3 // Default 3 retries
	}

	failCount := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("health check timed out after %d seconds", timeoutSeconds)
		case <-ticker.C:
			// Get container IP
			containerIP, err := m.docker.GetModuleContainerIP(ctx, module.ContainerId)
			if err != nil {
				failCount++
				if failCount >= int(retries) {
					return fmt.Errorf("failed to get container IP after %d retries", retries)
				}
				continue
			}

			// Perform health check
			healthURL := fmt.Sprintf("http://%s:%d%s", containerIP, template.HealthCheckPort, template.HealthCheckPath)
			if m.checkHealth(healthURL, int(module.HealthCheckTimeout)) {
				m.logger.Info("Module %s is healthy", module.Name)
				return nil
			}

			failCount++
			m.logger.Debug("Health check failed for %s (attempt %d/%d)", module.Name, failCount, retries)
		}
	}
}

// Waits for a module container to reach running state
func (m *Manager) waitForRunning(ctx context.Context, moduleID string, timeoutSeconds int) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(time.Duration(timeoutSeconds) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("container not running after %d seconds", timeoutSeconds)
		case <-ticker.C:
			status, err := m.GetModuleStatus(ctx, moduleID)
			if err != nil {
				continue
			}
			if status == v1.ModuleStatus_MODULE_STATUS_RUNNING {
				return nil
			}
		}
	}
}

// Performs an HTTP health check
func (m *Manager) checkHealth(url string, timeoutSeconds int) bool {
	if timeoutSeconds == 0 {
		timeoutSeconds = 5
	}

	client := &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// Stops a running module
func (m *Manager) StopModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	if module.ContainerId == "" {
		return fmt.Errorf("module has no container")
	}

	// Update status
	module.Status = v1.ModuleStatus_MODULE_STATUS_STOPPING
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Stop the container
	if _, err := m.docker.StopContainer(ctx, module.ContainerId, 30); err != nil {
		m.logger.Error("Failed to stop module container: %v", err)
	}

	// Update status
	module.Status = v1.ModuleStatus_MODULE_STATUS_STOPPED
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Reconcile drops the module's routes
	if m.proxyManager != nil {
		if err := m.proxyManager.SyncListeners(ctx); err != nil {
			m.logger.Error("Failed to sync routes after stopping %s: %v", module.Name, err)
		}
	}

	m.logger.Info("Stopped module: %s", module.Name)
	return nil
}

// Restarts a module
func (m *Manager) RestartModule(ctx context.Context, moduleID string) error {
	if err := m.StopModule(ctx, moduleID); err != nil {
		return fmt.Errorf("failed to stop module: %w", err)
	}

	time.Sleep(1 * time.Second)

	return m.StartModule(ctx, moduleID)
}

// Recreates a module container
func (m *Manager) RecreateModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	wasRunning := module.Status == v1.ModuleStatus_MODULE_STATUS_RUNNING

	// Stop if running
	if wasRunning {
		if err := m.StopModule(ctx, moduleID); err != nil {
			m.logger.Error("Failed to stop module for recreation: %v", err)
		}
	}

	// Remove old container
	if module.ContainerId != "" {
		if err := m.docker.RemoveContainer(ctx, module.ContainerId); err != nil {
			m.logger.Error("Failed to remove old module container: %v", err)
		}
		module.ContainerId = ""
		m.store.UpdateModule(ctx, module)
	}

	// Create new container
	if err := m.CreateAndStartModule(ctx, moduleID, wasRunning); err != nil {
		return fmt.Errorf("failed to recreate module: %w", err)
	}

	return nil
}

// Stops and removes a module and its container
func (m *Manager) DeleteModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	// System modules only ever disable, never delete
	if template, terr := m.store.GetModuleTemplate(ctx, module.TemplateId); terr == nil &&
		template.Type == v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN && module.ServerId == "" {
		return fmt.Errorf("system module %s can only be disabled", module.Name)
	}

	// Stop if running
	if module.Status == v1.ModuleStatus_MODULE_STATUS_RUNNING {
		if err := m.StopModule(ctx, moduleID); err != nil {
			m.logger.Error("Failed to stop module for deletion: %v", err)
		}
	}

	// Remove container
	if module.ContainerId != "" {
		if err := m.docker.RemoveContainer(ctx, module.ContainerId); err != nil {
			m.logger.Error("Failed to remove module container: %v", err)
		}
	}

	// Clean up associated API token
	if module.TokenId != "" {
		if err := m.store.DeleteApiToken(ctx, module.TokenId); err != nil {
			m.logger.Error("Failed to delete module API token: %v", err)
		}
	}

	// Delete from database
	if err := m.store.DeleteModule(ctx, moduleID); err != nil {
		return fmt.Errorf("failed to delete module from database: %w", err)
	}

	// Reconcile drops routes now that the row is gone
	if m.proxyManager != nil {
		if err := m.proxyManager.SyncListeners(ctx); err != nil {
			m.logger.Error("Failed to sync routes after deleting %s: %v", module.Name, err)
		}
	}

	m.logger.Info("Deleted module: %s", module.Name)
	return nil
}

// Returns current status from Docker
func (m *Manager) GetModuleStatus(ctx context.Context, moduleID string) (v1.ModuleStatus, error) {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return v1.ModuleStatus_MODULE_STATUS_ERROR, err
	}
	return m.StatusForModule(ctx, module)
}

// Maps container state to status without a row fetch
func (m *Manager) StatusForModule(ctx context.Context, module *v1.Module) (v1.ModuleStatus, error) {
	if module.ContainerId == "" {
		return v1.ModuleStatus_MODULE_STATUS_STOPPED, nil
	}

	status, err := m.docker.GetContainerStatus(ctx, module.ContainerId)
	if err != nil {
		return v1.ModuleStatus_MODULE_STATUS_ERROR, err
	}

	// Map ServerStatus to ModuleStatus
	switch status {
	case v1.ServerStatus_SERVER_STATUS_RUNNING:
		return v1.ModuleStatus_MODULE_STATUS_RUNNING, nil
	case v1.ServerStatus_SERVER_STATUS_STARTING:
		return v1.ModuleStatus_MODULE_STATUS_STARTING, nil
	case v1.ServerStatus_SERVER_STATUS_STOPPING:
		return v1.ModuleStatus_MODULE_STATUS_STOPPING, nil
	case v1.ServerStatus_SERVER_STATUS_STOPPED:
		return v1.ModuleStatus_MODULE_STATUS_STOPPED, nil
	case v1.ServerStatus_SERVER_STATUS_CREATING:
		return v1.ModuleStatus_MODULE_STATUS_CREATING, nil
	default:
		return v1.ModuleStatus_MODULE_STATUS_ERROR, nil
	}
}

// Finds an available port for a module
func (m *Manager) AllocateModulePort(ctx context.Context) (int, error) {
	return m.AllocateModulePortExcluding(ctx, v1.ModuleProtocol_MODULE_PROTOCOL_TCP, nil)
}

// Registry scan across the configured module port range
func (m *Manager) AllocateModulePortExcluding(ctx context.Context, protocol v1.ModuleProtocol, exclude map[int]bool) (int, error) {
	return m.proxyManager.FindFreePort(ctx, proxy.FreePortOpts{
		Protocol: protocol,
		Start:    m.config.Module.PortRangeMin,
		End:      m.config.Module.PortRangeMax,
		Exclude:  exclude,
	})
}

// Reports whether manager is running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
