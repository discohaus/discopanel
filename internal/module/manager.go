package module

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nickheyer/discopanel/internal/command"
	"github.com/nickheyer/discopanel/internal/config"
	storage "github.com/nickheyer/discopanel/internal/db"
	"github.com/nickheyer/discopanel/internal/docker"
	"github.com/nickheyer/discopanel/internal/proxy"
	"github.com/nickheyer/discopanel/pkg/logger"
)

// Manager handles the lifecycle of modules
type Manager struct {
	store        *storage.Store
	docker       *docker.Client
	sender       *command.Sender
	config       *config.Config
	proxyManager *proxy.Manager
	logger       *logger.Logger
	logStreamer  *logger.LogStreamer
	mu           sync.Mutex
	running      bool
}

// NewManager creates a new module manager
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

// SetLogStreamer sets the log streamer for module containers
func (m *Manager) SetLogStreamer(streamer *logger.LogStreamer) {
	m.logStreamer = streamer
}

// Start initializes the module manager
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

	// Module proxy listeners live in the DiscoPanel process, while module
	// containers can survive a panel restart. Restore routes for modules that
	// are still running.
	m.restoreProxyRoutes(context.Background())

	m.running = true
	m.logger.Info("Module manager started")
	return nil
}

// restoreProxyRoutes restores proxy routes for module containers that survived
// a DiscoPanel restart.
func (m *Manager) restoreProxyRoutes(ctx context.Context) {
	if m.proxyManager == nil {
		return
	}

	modules, err := m.store.ListModules(ctx)
	if err != nil {
		m.logger.Error("Failed to restore module proxy routes: %v", err)
		return
	}

	for _, module := range modules {
		if module.ContainerID == "" {
			continue
		}

		hasProxyPort := false
		for _, port := range module.Ports {
			if port != nil && port.ProxyEnabled && port.HostPort > 0 {
				hasProxyPort = true
				break
			}
		}
		if !hasProxyPort {
			continue
		}

		status, err := m.GetModuleStatus(ctx, module.ID)
		if err != nil {
			m.logger.Warn("Failed to get status for module %s while restoring proxy routes: %v", module.Name, err)
			continue
		}
		if status != storage.ModuleStatusRunning && status != storage.ModuleStatusStarting {
			continue
		}

		server, err := m.store.GetServer(ctx, module.ServerID)
		if err != nil {
			m.logger.Warn("Failed to get server for module %s while restoring proxy routes: %v", module.Name, err)
			continue
		}

		if err := m.proxyManager.AddModuleRoute(module, server); err != nil {
			m.logger.Error("Failed to restore proxy route for module %s: %v", module.Name, err)
		}
	}
}

// Stop gracefully stops all managed modules
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

			if module.Status == storage.ModuleStatusRunning {
				m.logger.Info("Stopping module: %s", module.Name)
				if err := m.StopModule(ctx, module.ID); err != nil {
					m.logger.Error("Failed to stop module %s: %v", module.Name, err)
				}
			}
		}
	}

	m.running = false
	m.logger.Info("Module manager stopped")
	return nil
}

// CreateAndStartModule creates a container and optionally starts the module
func (m *Manager) CreateAndStartModule(ctx context.Context, moduleID string, startImmediately bool) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	template, err := m.store.GetModuleTemplate(ctx, module.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to get module template: %w", err)
	}

	server, err := m.store.GetServer(ctx, module.ServerID)
	if err != nil {
		return fmt.Errorf("failed to get server: %w", err)
	}

	// Update status to creating
	module.Status = storage.ModuleStatusCreating
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Fetch server config for alias resolution
	serverConfig, _ := m.store.GetServerConfig(ctx, server.ID)

	// Fetch sibling modules for inter-module alias resolution
	siblingModules := make(map[string]*storage.Module)
	serverModules, err := m.store.ListServerModules(ctx, module.ServerID)
	if err == nil {
		for _, sibling := range serverModules {
			if sibling.ID != module.ID {
				siblingModules[sibling.Name] = sibling
			}
		}
	}

	// Create the container
	containerID, err := m.docker.CreateModuleContainer(ctx, module, template, server, serverConfig, m.config, siblingModules)
	if err != nil {
		module.Status = storage.ModuleStatusError
		m.store.UpdateModule(ctx, module)
		return fmt.Errorf("failed to create module container: %w", err)
	}

	// Update module with container ID
	module.ContainerID = containerID
	module.Status = storage.ModuleStatusStopped
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module with container ID: %w", err)
	}

	m.logger.Info("Created module container %s for module %s", containerID[:12], module.Name)

	if startImmediately {
		return m.StartModule(ctx, moduleID)
	}

	return nil
}

// StartModule starts an existing module container
func (m *Manager) StartModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	if module.ContainerID == "" {
		return m.CreateAndStartModule(ctx, moduleID, true)
	}

	// Check if container still exists in Docker
	_, err = m.docker.GetContainerStatus(ctx, module.ContainerID)
	if err != nil {
		// Container doesn't exist, recreate it
		m.logger.Info("Container for module %s no longer exists, recreating", module.Name)
		module.ContainerID = ""
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
	module.Status = storage.ModuleStatusStarting
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Start the container
	if err := m.docker.StartContainer(ctx, module.ContainerID); err != nil {
		module.Status = storage.ModuleStatusError
		m.store.UpdateModule(ctx, module)
		return fmt.Errorf("failed to start module container: %w", err)
	}

	// Start log streaming
	if m.logStreamer != nil {
		if err := m.logStreamer.StartStreaming(module.ContainerID); err != nil {
			m.logger.Warn("Failed to start log streaming for module %s: %v", module.Name, err)
		}
	}

	// Update status and timestamps
	now := time.Now()
	module.Status = storage.ModuleStatusRunning
	module.LastStarted = &now
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Update proxy route if enabled (handles primary and additional ports)
	if m.proxyManager != nil {
		server, err := m.store.GetServer(ctx, module.ServerID)
		if err == nil && server.ProxyHostname != "" {
			if err := m.proxyManager.AddModuleRoute(module, server); err != nil {
				m.logger.Error("Failed to add proxy route for module %s: %v", module.Name, err)
			}
		}
	}

	m.logger.Info("Started module: %s", module.Name)

	// Run init command in background if configured
	if module.InitCommand != "" {
		go m.runInitCommand(module.ID)
	}

	return nil
}

// runInitCommand executes the module's init command after an optional delay
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
	if err != nil || status != storage.ModuleStatusRunning {
		m.logger.Warn("Init command: module %s is no longer running, skipping", module.Name)
		return
	}

	m.logger.Info("Init command: executing for module %s: %s", module.Name, module.InitCommand)
	output, err := m.docker.Exec(ctx, module.ContainerID, []string{"sh", "-c", module.InitCommand})
	if err != nil {
		m.logger.Error("Init command: failed for module %s: %v", module.Name, err)
		return
	}
	if output != "" {
		m.logger.Info("Init command: output for module %s: %s", module.Name, output)
	}

	if module.RestartAfterInit {
		m.logger.Info("Init command: restarting module %s after init", module.Name)
		if err := m.docker.RestartContainer(ctx, module.ContainerID, 5*time.Second); err != nil {
			m.logger.Error("Init command: failed to restart module %s: %v", module.Name, err)
		}
	}
}

// startDependencies starts and waits for module dependencies
func (m *Manager) startDependencies(ctx context.Context, module *storage.Module) error {
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
		if depModule.Status != storage.ModuleStatusRunning {
			m.logger.Info("Starting dependency %s for module %s", depModule.Name, module.Name)

			// Create container if needed
			if depModule.ContainerID == "" {
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

// waitForHealthy waits for a module to pass its health check
func (m *Manager) waitForHealthy(ctx context.Context, moduleID string, timeoutSeconds int) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return err
	}

	template, err := m.store.GetModuleTemplate(ctx, module.TemplateID)
	if err != nil {
		return err
	}

	// If no health check configured, just wait for container to be running
	if template.HealthCheckPath == "" && template.HealthCheckPort == 0 {
		m.logger.Debug("No health check configured for %s, checking container status", module.Name)
		return m.waitForRunning(ctx, moduleID, timeoutSeconds)
	}

	// Perform HTTP health check
	ticker := time.NewTicker(time.Duration(module.HealthCheckInterval) * time.Second)
	if module.HealthCheckInterval == 0 {
		ticker = time.NewTicker(5 * time.Second) // Default 5 second interval
	}
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
			containerIP, err := m.docker.GetModuleContainerIP(ctx, module.ContainerID)
			if err != nil {
				failCount++
				if failCount >= retries {
					return fmt.Errorf("failed to get container IP after %d retries", retries)
				}
				continue
			}

			// Perform health check
			healthURL := fmt.Sprintf("http://%s:%d%s", containerIP, template.HealthCheckPort, template.HealthCheckPath)
			if m.checkHealth(healthURL, module.HealthCheckTimeout) {
				m.logger.Info("Module %s is healthy", module.Name)
				return nil
			}

			failCount++
			m.logger.Debug("Health check failed for %s (attempt %d/%d)", module.Name, failCount, retries)
		}
	}
}

// waitForRunning waits for a module container to be in running state
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
			if status == storage.ModuleStatusRunning {
				return nil
			}
		}
	}
}

// checkHealth performs an HTTP health check
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

// StopModule stops a running module
func (m *Manager) StopModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	if module.ContainerID == "" {
		return fmt.Errorf("module has no container")
	}

	// Update status
	module.Status = storage.ModuleStatusStopping
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	// Remove proxy routes if any ports have proxy enabled
	if m.proxyManager != nil {
		hasProxyPort := false
		for _, port := range module.Ports {
			if port != nil && port.ProxyEnabled {
				hasProxyPort = true
				break
			}
		}
		if hasProxyPort {
			if err := m.proxyManager.RemoveModuleRoute(moduleID); err != nil {
				m.logger.Error("Failed to remove proxy route for module %s: %v", module.Name, err)
			}
		}
	}

	// Stop the container
	if _, err := m.docker.StopContainer(ctx, module.ContainerID); err != nil {
		m.logger.Error("Failed to stop module container: %v", err)
	}

	// Update status
	module.Status = storage.ModuleStatusStopped
	if err := m.store.UpdateModule(ctx, module); err != nil {
		return fmt.Errorf("failed to update module status: %w", err)
	}

	m.logger.Info("Stopped module: %s", module.Name)
	return nil
}

// RestartModule restarts a module
func (m *Manager) RestartModule(ctx context.Context, moduleID string) error {
	if err := m.StopModule(ctx, moduleID); err != nil {
		return fmt.Errorf("failed to stop module: %w", err)
	}

	time.Sleep(1 * time.Second)

	return m.StartModule(ctx, moduleID)
}

// RecreateModule recreates a module container
func (m *Manager) RecreateModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	wasRunning := module.Status == storage.ModuleStatusRunning

	// Stop if running
	if wasRunning {
		if err := m.StopModule(ctx, moduleID); err != nil {
			m.logger.Error("Failed to stop module for recreation: %v", err)
		}
	}

	// Remove old container
	if module.ContainerID != "" {
		if err := m.docker.RemoveContainer(ctx, module.ContainerID); err != nil {
			m.logger.Error("Failed to remove old module container: %v", err)
		}
		module.ContainerID = ""
		m.store.UpdateModule(ctx, module)
	}

	// Create new container
	if err := m.CreateAndStartModule(ctx, moduleID, wasRunning); err != nil {
		return fmt.Errorf("failed to recreate module: %w", err)
	}

	return nil
}

// DeleteModule stops and removes a module and its container
func (m *Manager) DeleteModule(ctx context.Context, moduleID string) error {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return fmt.Errorf("failed to get module: %w", err)
	}

	// Stop if running
	if module.Status == storage.ModuleStatusRunning {
		if err := m.StopModule(ctx, moduleID); err != nil {
			m.logger.Error("Failed to stop module for deletion: %v", err)
		}
	}

	// Remove container
	if module.ContainerID != "" {
		if err := m.docker.RemoveContainer(ctx, module.ContainerID); err != nil {
			m.logger.Error("Failed to remove module container: %v", err)
		}
	}

	// Clean up associated API token
	if module.TokenID != "" {
		if err := m.store.DeleteAPITokenByID(ctx, module.TokenID); err != nil {
			m.logger.Error("Failed to delete module API token: %v", err)
		}
	}

	// Delete from database
	if err := m.store.DeleteModule(ctx, moduleID); err != nil {
		return fmt.Errorf("failed to delete module from database: %w", err)
	}

	m.logger.Info("Deleted module: %s", module.Name)
	return nil
}

// GetModuleStatus returns current status from Docker
func (m *Manager) GetModuleStatus(ctx context.Context, moduleID string) (storage.ModuleStatus, error) {
	module, err := m.store.GetModule(ctx, moduleID)
	if err != nil {
		return storage.ModuleStatusError, err
	}

	if module.ContainerID == "" {
		return storage.ModuleStatusStopped, nil
	}

	status, err := m.docker.GetContainerStatus(ctx, module.ContainerID)
	if err != nil {
		return storage.ModuleStatusError, err
	}

	// Map ServerStatus to ModuleStatus
	switch status {
	case storage.StatusRunning:
		return storage.ModuleStatusRunning, nil
	case storage.StatusStarting:
		return storage.ModuleStatusStarting, nil
	case storage.StatusStopping:
		return storage.ModuleStatusStopping, nil
	case storage.StatusStopped:
		return storage.ModuleStatusStopped, nil
	case storage.StatusCreating:
		return storage.ModuleStatusCreating, nil
	default:
		return storage.ModuleStatusError, nil
	}
}

// AllocateModulePort finds an available port for a module
func (m *Manager) AllocateModulePort(ctx context.Context) (int, error) {
	return m.AllocateModulePortExcluding(ctx, nil)
}

// AllocateModulePortExcluding finds an available port, excluding any ports in the exclude map
func (m *Manager) AllocateModulePortExcluding(ctx context.Context, exclude map[int]bool) (int, error) {
	modules, err := m.store.ListModules(ctx)
	if err != nil {
		return 0, err
	}

	usedPorts := make(map[int]bool)
	for _, module := range modules {
		for _, port := range module.Ports {
			if port != nil && port.HostPort > 0 {
				usedPorts[int(port.HostPort)] = true
			}
		}
	}

	// Also exclude ports passed in (allocated in same request)
	for port := range exclude {
		usedPorts[port] = true
	}

	// Find an available port in the configured range
	for port := m.config.Module.PortRangeMin; port <= m.config.Module.PortRangeMax; port++ {
		if !usedPorts[port] {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available module ports in range %d-%d", m.config.Module.PortRangeMin, m.config.Module.PortRangeMax)
}

// GetUsedModulePorts returns all ports currently in use by modules
func (m *Manager) GetUsedModulePorts(ctx context.Context) ([]int, error) {
	modules, err := m.store.ListModules(ctx)
	if err != nil {
		return nil, err
	}

	ports := make([]int, 0)
	for _, module := range modules {
		for _, port := range module.Ports {
			if port != nil && port.HostPort > 0 {
				ports = append(ports, int(port.HostPort))
			}
		}
	}

	return ports, nil
}

// IsRunning returns whether the manager is running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
