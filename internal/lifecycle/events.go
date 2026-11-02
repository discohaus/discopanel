package lifecycle

import (
	"context"
	"errors"
	"fmt"
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

	cfg, cfgErr := m.store.GetServerProperties(ctx, serverID)
	if cfgErr != nil {
		cfg = nil
	}
	motd := proxy.SyntheticMOTD(cfg, "§bsleeping", "§7join to wake it up")

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
	if err := m.BeginStart(serverID); err != nil {
		// A start already in flight serves this login too
		var busy *BusyError
		if errors.As(err, &busy) && busy.Kind == "start" {
			return nil
		}
		// Stops, deletes and restores must not look like a boot
		return fmt.Errorf("%w: %v", proxy.ErrServerBusy, err)
	}
	m.RunStartAsync(metrics.WithTrace(metrics.WithSource(context.Background(), "wake-on-connect")), serverID)
	return nil
}

// Transition counter a status reader captures before asking docker
func (m *Manager) StatusEpoch(serverID string) uint64 {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	return m.epochs[serverID]
}

// Mirrors docker's paused state into the wake gate. The paused set lives in
// memory, so without this a panel restart leaves paused servers relaying to
// a frozen backend instead of answering as sleeping and waking on join.
// Reads taken before a transition began, or during one, are dropped so a
// wake or pause that raced the docker read always wins.
func (m *Manager) ObserveContainerStatus(serverID string, status v1.ServerStatus, epoch uint64) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.transitions[serverID] != nil || m.epochs[serverID] != epoch {
		return
	}
	m.setPaused(serverID, status == v1.ServerStatus_SERVER_STATUS_PAUSED)
}
