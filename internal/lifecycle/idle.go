package lifecycle

import (
	"context"
	"time"

	"github.com/discohaus/discopanel/internal/metrics"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

const idleCheckInterval = 30 * time.Second

// Tracks how long a server has been without players
type idleState struct {
	lastActive time.Time
	hadPlayers bool
}

// Launches the autopause/autostop policy loop
func (m *Manager) StartIdleWatcher() {
	m.idleMu.Lock()
	if m.stopWatch != nil {
		m.idleMu.Unlock()
		return
	}
	m.stopWatch = make(chan struct{})
	stop := m.stopWatch
	m.idleMu.Unlock()

	m.watchWG.Add(1)
	go func() {
		defer m.watchWG.Done()
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.checkIdleServers()
			case <-stop:
				return
			}
		}
	}()
	m.log.Info("lifecycle: idle watcher started (autopause/autostop)")
}

// Stops the autopause/autostop policy loop
func (m *Manager) StopIdleWatcher() {
	m.idleMu.Lock()
	if m.stopWatch != nil {
		close(m.stopWatch)
		m.stopWatch = nil
	}
	m.idleMu.Unlock()
	m.watchWG.Wait()
}

func (m *Manager) resetIdle(serverID string) {
	m.idleMu.Lock()
	delete(m.idle, serverID)
	m.idleMu.Unlock()
}

// Tracked idle state, new entries start at the seed
func (m *Manager) idleStateFor(serverID string, seed time.Time) *idleState {
	st, ok := m.idle[serverID]
	if !ok {
		st = &idleState{lastActive: seed}
		m.idle[serverID] = st
	}
	return st
}

// Restarts the idle clock, player history survives
func (m *Manager) touchIdle(serverID string) {
	now := time.Now()
	m.idleMu.Lock()
	m.idleStateFor(serverID, now).lastActive = now
	m.idleMu.Unlock()
}

// Applies autopause/autostop policies to running servers
func (m *Manager) checkIdleServers() {
	ctx, cancel := context.WithTimeout(context.Background(), idleCheckInterval)
	defer cancel()

	servers, err := m.store.ListServers(ctx)
	if err != nil {
		return
	}

	for _, server := range servers {
		if server.ContainerId == "" || server.Detached || m.Busy(server.Id) {
			continue
		}

		cfg, err := m.store.GetServerProperties(ctx, server.Id)
		if err != nil {
			continue
		}
		autopause := cfg.EnableAutopause != nil && *cfg.EnableAutopause && len(server.ProxyHostnames) > 0
		autostop := cfg.EnableAutostop != nil && *cfg.EnableAutostop
		if !autopause && !autostop {
			m.resetIdle(server.Id)
			continue
		}

		status, err := m.docker.GetContainerStatus(ctx, server.ContainerId)
		if err != nil {
			m.resetIdle(server.Id)
			continue
		}

		now := time.Now()

		// Paused servers autostop once the clock since pause runs out
		if status == v1.ServerStatus_SERVER_STATUS_PAUSED {
			if !autostop {
				continue
			}
			m.idleMu.Lock()
			st := m.idleStateFor(server.Id, now)
			idleFor, hadPlayers := now.Sub(st.lastActive), st.hadPlayers
			m.idleMu.Unlock()
			if idleFor >= m.autostopTimeout(cfg, hadPlayers) {
				m.log.Info("lifecycle: autostopping paused idle server %s", server.Name)
				go m.stopIdle(server.Id)
			}
			continue
		}

		if status != v1.ServerStatus_SERVER_STATUS_RUNNING {
			continue
		}

		players := 0
		known := false
		if m.players != nil {
			players, known = m.players.PlayersOnline(server.Id)
		}
		if !known {
			// Without player data, never take idle actions
			continue
		}

		seed := now
		if server.LastStarted != nil {
			seed = server.LastStarted.AsTime()
		}
		m.idleMu.Lock()
		st := m.idleStateFor(server.Id, seed)
		if players > 0 {
			st.lastActive = now
			st.hadPlayers = true
		}
		idleFor := now.Sub(st.lastActive)
		hadPlayers := st.hadPlayers
		m.idleMu.Unlock()

		if players > 0 {
			continue
		}

		if autopause && idleFor >= m.autopauseTimeout(cfg, hadPlayers) {
			if err := m.Pause(metrics.WithTrace(metrics.WithSource(ctx, "autopause")), server.Id); err != nil {
				m.log.Error("lifecycle: autopause failed for %s: %v", server.Name, err)
			}
			continue
		}

		if autostop && idleFor >= m.autostopTimeout(cfg, hadPlayers) {
			m.log.Info("lifecycle: autostopping idle server %s", server.Name)
			go m.stopIdle(server.Id)
		}
	}
}

// Autopause budget by player history
func (m *Manager) autopauseTimeout(cfg *v1.ServerProperties, hadPlayers bool) time.Duration {
	return m.timeoutFor(intOrDefault(cfg.AutopauseTimeoutEst, 3600), intOrDefault(cfg.AutopauseTimeoutInit, 600), hadPlayers)
}

// Autostop budget by player history
func (m *Manager) autostopTimeout(cfg *v1.ServerProperties, hadPlayers bool) time.Duration {
	return m.timeoutFor(intOrDefault(cfg.AutostopTimeoutEst, 3600), intOrDefault(cfg.AutostopTimeoutInit, 1800), hadPlayers)
}

func (m *Manager) timeoutFor(establishedSecs, initialSecs int, hadPlayers bool) time.Duration {
	if hadPlayers {
		return time.Duration(establishedSecs) * time.Second
	}
	return time.Duration(initialSecs) * time.Second
}

func (m *Manager) stopIdle(serverID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := m.Stop(metrics.WithTrace(metrics.WithSource(ctx, "autostop")), serverID); err != nil {
		m.log.Error("lifecycle: autostop failed for server %s: %v", serverID, err)
	}
}

func intOrDefault(v *int32, def int) int {
	if v == nil || *v <= 0 {
		return def
	}
	return int(*v)
}
