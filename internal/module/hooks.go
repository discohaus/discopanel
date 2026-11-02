package module

import (
	"context"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/alias"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/events"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Module subsystem subscription to the central event bus
func (m *Manager) HandleServerEvent(ctx context.Context, event events.Event) {
	switch event.Type {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START:
		m.autoStartModules(ctx, event.ServerId)
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP:
		m.stopLifecycleModules(ctx, event.ServerId, event.Data[events.DataStopReason])
	}

	// Execute configured event hooks for every event type
	m.dispatchHooks(ctx, event.ServerId, event.Type)
}

// Starts modules with AutoStart enabled when the parent server starts
func (m *Manager) autoStartModules(ctx context.Context, serverID string) {
	modules, err := m.store.ListServerModules(ctx, serverID)
	if err != nil {
		m.logger.Error("Failed to list server modules for auto-start: %v", err)
		return
	}

	for _, module := range modules {
		if module.AutoStart && !module.Detached {
			go func(mod *v1.Module) {
				// Small delay to let the server settle before starting modules
				time.Sleep(2 * time.Second)
				// Gateways kept up across an idle stop keep running, but still
				// pick up config changes made while the server slept
				if m.moduleLive(context.Background(), mod) {
					if err := m.refreshRunningModule(context.Background(), mod.Id); err != nil {
						m.logger.Error("Failed to refresh running module %s on server start: %v", mod.Name, err)
					}
					return
				}
				if err := m.StartModule(context.Background(), mod.Id); err != nil {
					m.logger.Error("Failed to start module %s on server start: %v", mod.Name, err)
				} else {
					m.logger.Info("Started module %s with server", mod.Name)
				}
			}(module)
		}
	}
}

// Reports whether the module's container is up right now
func (m *Manager) moduleLive(ctx context.Context, module *v1.Module) bool {
	switch module.Status {
	case v1.ModuleStatus_MODULE_STATUS_RUNNING, v1.ModuleStatus_MODULE_STATUS_STARTING:
	default:
		return false
	}
	if module.ContainerId == "" || m.docker == nil {
		return false
	}
	status, err := m.docker.GetContainerStatus(ctx, module.ContainerId)
	return err == nil && status == v1.ServerStatus_SERVER_STATUS_RUNNING
}

// Stops modules following server lifecycle when the parent server stops
func (m *Manager) stopLifecycleModules(ctx context.Context, serverID, reason string) {
	proxyOn := m.proxyManager != nil && m.proxyManager.Enabled()
	for _, module := range m.lifecycleStopModules(ctx, serverID, reason, proxyOn) {
		if err := m.StopModule(ctx, module.Id); err != nil {
			m.logger.Error("Failed to stop module %s on server stop: %v", module.Name, err)
		} else {
			m.logger.Info("Stopped module %s with server", module.Name)
		}
	}
}

// Running lifecycle modules a stop takes down. An idle stop of a wake on
// connect server spares bedrock gateways like geyser, a bedrock join
// reaches the proxy through them and is what wakes the server again.
func (m *Manager) lifecycleStopModules(ctx context.Context, serverID, reason string, proxyOn bool) []*v1.Module {
	modules, err := m.store.ListModulesFollowingServerLifecycle(ctx, serverID)
	if err != nil {
		m.logger.Error("Failed to list lifecycle modules for stop: %v", err)
		return nil
	}

	spare := false
	if reason == events.StopReasonAutostop && proxyOn {
		if cfg, err := m.store.GetServerProperties(ctx, serverID); err == nil {
			spare = cfg.EnableWakeOnConnect != nil && *cfg.EnableWakeOnConnect
		}
	}

	var stop []*v1.Module
	for _, module := range modules {
		if module.Status != v1.ModuleStatus_MODULE_STATUS_RUNNING || module.Detached {
			continue
		}
		if spare && proxy.HasBedrockRelay(module.TemplateId, module.Ports) {
			m.logger.Info("Keeping module %s up so Bedrock joins can wake its server", module.Name)
			continue
		}
		stop = append(stop, module)
	}
	return stop
}

// Stops bedrock gateways an idle stop spared once Wake on Connect or the
// proxy goes off. Empty serverID checks every server.
func (m *Manager) ReconcileSparedModules(ctx context.Context, serverID string) {
	proxyOn := m.proxyManager != nil && m.proxyManager.Enabled()
	for _, server := range m.offlineServers(ctx, serverID) {
		// Still spared under an idle stop means sparing still applies
		for _, module := range m.lifecycleStopModules(ctx, server.Id, events.StopReasonAutostop, proxyOn) {
			if !proxy.HasBedrockRelay(module.TemplateId, module.Ports) {
				continue
			}
			if err := m.StopModule(ctx, module.Id); err != nil {
				m.logger.Error("Failed to stop module %s no longer needed for Bedrock wake: %v", module.Name, err)
			} else {
				m.logger.Info("Stopped module %s, its server no longer wakes on Bedrock joins", module.Name)
			}
		}
	}
}

// Panel shutdown takes down lifecycle modules of servers already stopped,
// running servers' modules go with their own stop
func (m *Manager) StopModulesOfStoppedServers(ctx context.Context) {
	for _, server := range m.offlineServers(ctx, "") {
		if server.Detached {
			continue
		}
		m.stopLifecycleModules(ctx, server.Id, events.StopReasonSystem)
	}
}

// Stopped servers, or just serverID when it is stopped
func (m *Manager) offlineServers(ctx context.Context, serverID string) []*v1.Server {
	var servers []*v1.Server
	if serverID != "" {
		server, err := m.store.GetServer(ctx, serverID)
		if err != nil {
			return nil
		}
		servers = []*v1.Server{server}
	} else {
		all, err := m.store.ListServers(ctx)
		if err != nil {
			m.logger.Error("Failed to list servers for module reconcile: %v", err)
			return nil
		}
		servers = all
	}
	var out []*v1.Server
	for _, server := range servers {
		switch server.Status {
		case v1.ServerStatus_SERVER_STATUS_STOPPED, v1.ServerStatus_SERVER_STATUS_ERROR:
			out = append(out, server)
		}
	}
	return out
}

// Runs every module hook subscribed to the event
func (m *Manager) dispatchHooks(ctx context.Context, serverID string, eventType v1.TriggeredEventType) {
	modules, err := m.store.ListServerModules(ctx, serverID)
	if err != nil {
		m.logger.Error("Failed to list modules for event hook dispatch: %v", err)
		return
	}

	for _, module := range modules {
		if len(module.EventHooks) == 0 {
			continue
		}
		for _, hook := range module.EventHooks {
			if hook == nil || hook.Event != eventType {
				continue
			}
			// Execute hook asynchronously
			go m.executeHook(context.Background(), module, hook, serverID)
		}
	}
}

// Executes a single module event hook
func (m *Manager) executeHook(ctx context.Context, module *v1.Module, hook *v1.ModuleEventHook, serverID string) {
	// Apply delay if configured
	if hook.DelaySeconds > 0 {
		m.logger.Debug("Delaying hook action for module %s by %d seconds", module.Name, hook.DelaySeconds)
		time.Sleep(time.Duration(hook.DelaySeconds) * time.Second)
	}

	// Evaluate condition if specified
	if hook.Condition != "" {
		server, err := m.loadServer(ctx, serverID)
		if err != nil {
			m.logger.Error("Failed to get server for condition evaluation: %v", err)
			return
		}
		if !m.evaluateCondition(hook.Condition, server, module) {
			m.logger.Debug("Condition not met for hook on module %s: %s", module.Name, hook.Condition)
			return
		}
	}

	m.logger.Info("Executing hook action %s for module %s (event: %s)",
		hook.Action.String(), module.Name, hook.Event.String())

	switch hook.Action {
	case v1.ModuleEventAction_MODULE_EVENT_ACTION_START:
		if err := m.ensureModuleStarted(ctx, module); err != nil {
			m.logger.Error("Hook failed to start module %s: %v", module.Name, err)
		}

	case v1.ModuleEventAction_MODULE_EVENT_ACTION_STOP:
		if err := m.StopModule(ctx, module.Id); err != nil {
			m.logger.Error("Hook failed to stop module %s: %v", module.Name, err)
		}

	case v1.ModuleEventAction_MODULE_EVENT_ACTION_RESTART:
		if err := m.RestartModule(ctx, module.Id); err != nil {
			m.logger.Error("Hook failed to restart module %s: %v", module.Name, err)
		}

	case v1.ModuleEventAction_MODULE_EVENT_ACTION_EXEC:
		if err := m.execInModule(ctx, module, hook.Command); err != nil {
			m.logger.Error("Hook failed to exec in module %s: %v", module.Name, err)
		}

	case v1.ModuleEventAction_MODULE_EVENT_ACTION_RCON:
		if err := m.sendRCON(ctx, serverID, hook.Command); err != nil {
			m.logger.Error("Hook failed to send RCON for module %s: %v", module.Name, err)
		}

	default:
		m.logger.Warn("Unknown hook action for module %s: %v", module.Name, hook.Action)
	}
}

// Starts a module, creating its container if needed
func (m *Manager) ensureModuleStarted(ctx context.Context, module *v1.Module) error {
	if module.ContainerId == "" {
		return m.CreateAndStartModule(ctx, module.Id, true)
	}
	return m.StartModule(ctx, module.Id)
}

// Executes a command inside a module container
func (m *Manager) execInModule(ctx context.Context, module *v1.Module, command string) error {
	if module.ContainerId == "" {
		return nil // Cannot exec in non-existent container
	}

	_, _, err := m.docker.Exec(ctx, module.ContainerId, []string{"sh", "-c", command})
	return err
}

// Sends an RCON command to the parent server
func (m *Manager) sendRCON(ctx context.Context, serverID string, command string) error {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}

	if server.ContainerId == "" {
		m.logger.Warn("Cannot send RCON: server %s has no container", server.Name)
		return nil
	}

	_, err = m.sender.SendCommand(ctx, server.Id, command)
	return err
}

// Evaluates a hook condition like {{server.players_online}} > 5
func (m *Manager) evaluateCondition(condition string, server *v1.Server, module *v1.Module) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}
	result, err := alias.EvaluateCondition(condition, &alias.Context{
		Server: server,
		Module: module,
	})
	if err != nil {
		m.logger.Warn("Invalid hook condition %q: %v", condition, err)
		return false
	}
	return result
}
