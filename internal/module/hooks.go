package module

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/nickheyer/discopanel/internal/alias"
	storage "github.com/nickheyer/discopanel/internal/db"
	"github.com/nickheyer/discopanel/internal/events"
	v1 "github.com/nickheyer/discopanel/pkg/proto/discopanel/v1"
)

// Module subsystem subscription to the central event bus
func (m *Manager) HandleServerEvent(ctx context.Context, event events.Event) {
	switch event.Type {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START:
		m.autoStartModules(ctx, event.ServerID)
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP:
		m.stopLifecycleModules(ctx, event.ServerID)
	}

	// Execute configured event hooks for every event type
	m.dispatchHooks(ctx, event.ServerID, event.Type)
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
			go func(mod *storage.Module) {
				// Small delay to let the server settle before starting modules
				time.Sleep(2 * time.Second)

				// Geyser remains reachable while a lazy parent server sleeps, so a
				// wake event must not try to start its running container again.
				if mod.TemplateID == "builtin-geyser" {
					status, err := m.GetModuleStatus(context.Background(), mod.ID)
					if err == nil && (status == storage.ModuleStatusRunning || status == storage.ModuleStatusStarting) {
						return
					}
				}

				if err := m.StartModule(context.Background(), mod.ID); err != nil {
					m.logger.Error("Failed to start module %s on server start: %v", mod.Name, err)
				} else {
					m.logger.Info("Started module %s with server", mod.Name)
				}
			}(module)
		}
	}
}

// Stops modules following server lifecycle when the parent server stops
func (m *Manager) stopLifecycleModules(ctx context.Context, serverID string) {
	modules, err := m.store.ListModulesFollowingServerLifecycle(ctx, serverID)
	if err != nil {
		m.logger.Error("Failed to list lifecycle modules for stop: %v", err)
		return
	}

	for _, module := range modules {
		if module.Status == storage.ModuleStatusRunning && !module.Detached {
			if err := m.StopModule(ctx, module.ID); err != nil {
				m.logger.Error("Failed to stop module %s on server stop: %v", module.Name, err)
			} else {
				m.logger.Info("Stopped module %s with server", module.Name)
			}
		}
	}
}

// Runs every module event hook subscribed to eventType for the server
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
func (m *Manager) executeHook(ctx context.Context, module *storage.Module, hook *v1.ModuleEventHook, serverID string) {
	// Apply delay if configured
	if hook.DelaySeconds > 0 {
		m.logger.Debug("Delaying hook action for module %s by %d seconds", module.Name, hook.DelaySeconds)
		time.Sleep(time.Duration(hook.DelaySeconds) * time.Second)
	}

	// Evaluate condition if specified
	if hook.Condition != "" {
		server, err := m.store.GetServer(ctx, serverID)
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
		if err := m.StopModule(ctx, module.ID); err != nil {
			m.logger.Error("Hook failed to stop module %s: %v", module.Name, err)
		}

	case v1.ModuleEventAction_MODULE_EVENT_ACTION_RESTART:
		if err := m.RestartModule(ctx, module.ID); err != nil {
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
func (m *Manager) ensureModuleStarted(ctx context.Context, module *storage.Module) error {
	if module.ContainerID == "" {
		return m.CreateAndStartModule(ctx, module.ID, true)
	}
	return m.StartModule(ctx, module.ID)
}

// Executes a command inside a module container
func (m *Manager) execInModule(ctx context.Context, module *storage.Module, command string) error {
	if module.ContainerID == "" {
		return nil // Cannot exec in non-existent container
	}

	_, err := m.docker.Exec(ctx, module.ContainerID, []string{command})
	return err
}

// Sends an RCON command to the parent server via the command sender
func (m *Manager) sendRCON(ctx context.Context, serverID string, command string) error {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}

	if server.ContainerID == "" {
		m.logger.Warn("Cannot send RCON: server %s has no container", server.Name)
		return nil
	}

	_, err = m.sender.SendCommand(ctx, server.ID, command)
	return err
}

// evaluateCondition evaluates a simple condition expression using the alias system.
// Condition format: <alias> <operator> <value>
// Examples:
//   - "{{server.players_online}} == 0"
//   - "{{server.players_online}} > 5"
//   - "{{server.status}} == running"
//   - "{{module.status}} == stopped"
//
// The alias system dynamically resolves any field from Server or Module structs.
// See alias.GetAvailableAliases() for all available aliases.
func (m *Manager) evaluateCondition(condition string, server *storage.Server, module *storage.Module) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}

	// Build alias context for resolution
	ctx := &alias.Context{
		Server: server,
		Module: module,
	}

	// Resolve all aliases in the condition string first
	resolved := alias.Substitute(condition, ctx)

	// Parse condition: <resolved_value> <operator> <expected_value>
	var actualValue, operator, expectedValue string

	// Try different operators in order of specificity
	operators := []string{"==", "!=", "<=", ">=", "<", ">"}
	for _, op := range operators {
		if parts := strings.SplitN(resolved, op, 2); len(parts) == 2 {
			actualValue = strings.TrimSpace(parts[0])
			operator = op
			expectedValue = strings.TrimSpace(parts[1])
			break
		}
	}

	if operator == "" {
		m.logger.Warn("Invalid condition format (no operator found): %s", condition)
		return false
	}

	// Compare values
	return m.compareValues(actualValue, operator, expectedValue)
}

// Compares two values using the specified operator
// TODO: Move all of these hacky comparators to a pkg where they belond...
func (m *Manager) compareValues(actual, operator, expected string) bool {
	// Try numeric comparison first
	actualNum, actualErr := strconv.ParseFloat(actual, 64)
	expectedNum, expectedErr := strconv.ParseFloat(expected, 64)

	if actualErr == nil && expectedErr == nil {
		// Both are numeric
		switch operator {
		case "==":
			return actualNum == expectedNum
		case "!=":
			return actualNum != expectedNum
		case "<":
			return actualNum < expectedNum
		case ">":
			return actualNum > expectedNum
		case "<=":
			return actualNum <= expectedNum
		case ">=":
			return actualNum >= expectedNum
		}
	}

	// String comparison
	actual = strings.ToLower(actual)
	expected = strings.ToLower(expected)
	switch operator {
	case "==":
		return actual == expected
	case "!=":
		return actual != expected
	default:
		m.logger.Warn("Operator %s not supported for string comparison", operator)
		return false
	}
}
