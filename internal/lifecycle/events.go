package lifecycle

import (
	"context"
	"strings"
	"time"

	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/events"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Runs lifecycle RCON commands as an event bus subscriber
func (m *Manager) HandleServerEvent(ctx context.Context, event events.Event) {
	switch event.Type {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
		v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP:
		m.resetRoster(event.ServerId)
		return
	}

	cfg, err := m.store.GetServerProperties(ctx, event.ServerId)
	if err != nil {
		return
	}

	switch event.Type {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_HEALTHY:
		m.runRCONCommands(ctx, event.ServerId, cfg.RconCmdsStartup)

	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_JOIN:
		first := m.trackJoin(event.ServerId, eventPlayer(event))
		m.runRCONCommands(ctx, event.ServerId, cfg.RconCmdsOnConnect)
		if first {
			m.runRCONCommands(ctx, event.ServerId, cfg.RconCmdsFirstConnect)
		}

	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_LEAVE:
		last := m.trackLeave(event.ServerId, eventPlayer(event))
		m.runRCONCommands(ctx, event.ServerId, cfg.RconCmdsOnDisconnect)
		if last {
			m.runRCONCommands(ctx, event.ServerId, cfg.RconCmdsLastDisconnect)
		}
	}
}

func eventPlayer(event events.Event) string {
	return event.Data["player"]
}

// Executes newline-delimited commands via RCON
func (m *Manager) runRCONCommands(ctx context.Context, serverID string, commands *string) {
	if commands == nil {
		return
	}
	for _, line := range strings.Split(*commands, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := m.sender.SendCommand(ctx, serverID, line); err != nil {
			m.log.Warn("lifecycle: rcon command %q failed for server %s: %v", line, serverID, err)
		}
	}
}

func (m *Manager) resetRoster(serverID string) {
	m.rosterMu.Lock()
	delete(m.roster, serverID)
	delete(m.firstConnect, serverID)
	m.rosterMu.Unlock()
}

// Records a join, true if first connect since start
func (m *Manager) trackJoin(serverID, player string) bool {
	m.rosterMu.Lock()
	defer m.rosterMu.Unlock()
	if m.roster[serverID] == nil {
		m.roster[serverID] = make(map[string]bool)
	}
	if player != "" {
		m.roster[serverID][player] = true
	}
	if !m.firstConnect[serverID] {
		m.firstConnect[serverID] = true
		return true
	}
	return false
}

// Records a leave, true when server is now empty
func (m *Manager) trackLeave(serverID, player string) bool {
	m.rosterMu.Lock()
	defer m.rosterMu.Unlock()
	if player != "" && m.roster[serverID] != nil {
		delete(m.roster[serverID], player)
	}
	return len(m.roster[serverID]) == 0
}

// --- proxy wake gate -------------------------------------------------------

// Compile-time check that Manager implements the proxy's wake gate
var _ proxy.ServerGate = (*Manager)(nil)

// Reports whether server is paused, with data for sleeping status
func (m *Manager) SleepingInfo(serverID string) (*proxy.SleepingServer, bool) {
	if !m.isPausedFast(serverID) {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, false
	}

	motd := server.Name + " is sleeping - join to wake it up"
	if cfg, err := m.store.GetServerProperties(ctx, serverID); err == nil && cfg.Motd != nil && *cfg.Motd != "" {
		motd = *cfg.Motd + " (sleeping - join to wake)"
	}

	return &proxy.SleepingServer{
		Motd:       motd,
		MaxPlayers: int(server.MaxPlayers),
	}, true
}

// Resumes a paused server for an incoming login (hot path)
func (m *Manager) WakeServer(ctx context.Context, serverID string) error {
	return m.Wake(metrics.WithTrace(metrics.WithSource(ctx, "wake-on-connect")), serverID)
}

// Cold-starts a stopped server asynchronously for wake-on-connect login
func (m *Manager) StartServer(serverID string) error {
	if m.IsStarting(serverID) {
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		if err := m.Start(metrics.WithTrace(metrics.WithSource(ctx, "wake-on-connect")), serverID); err != nil {
			m.log.Error("lifecycle: wake-on-connect start failed for %s: %v", serverID, err)
		}
	}()
	return nil
}
