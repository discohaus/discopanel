package metrics

import (
	"context"
	"time"

	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
)

// Collector intake for agent-pushed telemetry, takes precedence when fresh

// Records session attach or detach for a server
func (c *Collector) SetAgentConnected(serverID string, connected bool) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.AgentConnected = connected
		if !connected {
			m.AgentJvmActive = false
			m.AgentReady = false
			m.PSIAvailable = false
			m.GCLogWindowAt = time.Time{}
		}
		m.LastUpdated = time.Now()
	})
}

// Records that the javaagent is feeding JVM telemetry
func (c *Collector) SetAgentJvmActive(serverID string, active bool) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.AgentJvmActive = active
	})
}

// Marks server healthy on agent ready signal
func (c *Collector) ApplyAgentReady(ctx context.Context, serverID string, startupSeconds float64) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.AgentReady = true
		if startupSeconds > 0 {
			m.StartupSeconds = startupSeconds
		}
		m.LastUpdated = time.Now()
	})

	server, err := c.store.GetServer(ctx, serverID)
	if err != nil || server.ContainerId == "" {
		return
	}
	info, err := c.docker.GetContainerRunInfo(ctx, server.ContainerId)
	if err != nil || !info.Running {
		return
	}
	c.recordHealth(server.ContainerId, info.StartedAt, true)
}

// Seeds the exit dedup floor from the durable ack stamp
func (c *Collector) SeedExitFloor(serverID string, floor time.Time) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		if m.LastExitedAt.Before(floor) {
			m.LastExitedAt = floor
		}
	})
}

// Records a process exit report, reports false for stale replays
func (c *Collector) ApplyAgentExit(serverID string, exit *agentv1.Exited) bool {
	exitedAt := time.Now()
	if ms := exit.GetExitedAtUnixMs(); ms > 0 {
		exitedAt = time.UnixMilli(ms)
	}
	fresh := false
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		if !m.LastExitedAt.IsZero() && !exitedAt.After(m.LastExitedAt) {
			return
		}
		fresh = true
		m.LastExitCode = int(exit.GetExitCode())
		m.LastExitCrashed = exit.GetCrashed()
		m.LastExitOomKilled = exit.GetOomKilled()
		m.LastExitBootFailed = exit.GetBootFailed()
		m.LastExitWasReady = exit.GetWasReady()
		m.LastExitedAt = exitedAt
		m.LastExitExcerpt = exit.GetCrashReportExcerpt()
		m.LastExitFatal = exit.GetFatalError()
	})
	return fresh
}

// Stores javaagent tick timing, authoritative TPS and MSPT
func (c *Collector) ApplyAgentTick(serverID string, sample *agentv1.TickSample) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.Tps = sample.GetTps()
		m.Mspt = sample.GetMsptAvg()
		m.MsptMax = sample.GetMsptMax()
		m.AgentTickUpdated = time.Now()
		m.LastUpdated = time.Now()
	})
}

// Log windows this fresh keep MX bean GC deltas out
const gcLogWindowFresh = 45 * time.Second

// Stores JVM telemetry from the javaagent
func (c *Collector) ApplyAgentJvm(serverID string, sample *agentv1.JvmSample) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.HeapUsedMb = sample.GetHeapUsedMb()
		m.HeapMaxMb = sample.GetHeapMaxMb()
		m.ThreadCount = int(sample.GetThreadCount())
		m.ClassCount = int(sample.GetClassCount())
		// MX bean GC only speaks while no gc.log window flows
		if gc := sample.GetGc(); gc != nil && time.Since(m.GCLogWindowAt) > gcLogWindowFresh {
			m.GCPauseCount = gc.GetCount()
			m.GCPauseTotalMs = gc.GetTotalMs()
			m.GCPauseMaxMs = gc.GetMaxMs()
		}
		m.LastUpdated = time.Now()
	})
}

// Stores cgroup and GC-log telemetry docker stats cannot see
func (c *Collector) ApplyAgentProc(serverID string, sample *agentv1.ProcSample) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		// Java process attribution beats whole-container docker stats
		m.CpuPercent = sample.GetCpuPercent()
		if rss := sample.GetRssMb(); rss > 0 {
			m.MemoryUsage = rss
		}
		m.AgentProcUpdated = time.Now()
		m.CpuQuotaCores = sample.GetCpuQuotaCores()
		if periods := sample.GetCfsPeriods(); periods > 0 {
			m.CpuThrottlePercent = float64(sample.GetCfsThrottledPeriods()) / float64(periods) * 100
		} else {
			m.CpuThrottlePercent = 0
		}
		if gc := sample.GetGc(); gc != nil {
			m.GCPauseCount = gc.GetCount()
			m.GCPauseTotalMs = gc.GetTotalMs()
			m.GCPauseMaxMs = gc.GetMaxMs()
			m.GCLogWindowAt = time.Now()
		}
		if psi := sample.GetPsi(); psi != nil {
			m.PSIAvailable = true
			m.PSICpuSome = psi.GetCpuSomeAvg10()
			m.PSIMemSome = psi.GetMemSomeAvg10()
			m.PSIMemFull = psi.GetMemFullAvg10()
			m.PSIIoSome = psi.GetIoSomeAvg10()
		} else {
			m.PSIAvailable = false
		}
		m.LastUpdated = time.Now()
	})
}

// Stores static host facts from the runtime hello
func (c *Collector) ApplyAgentHello(serverID string, hello *agentv1.Hello) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		if mode := hello.GetHostThpMode(); mode != "" {
			m.HostTHPMode = mode
		}
	})
}

// Applies one exact join or leave event to roster
func (c *Collector) ApplyAgentPlayerChange(serverID, player string, joined bool, playersOnline int) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		roster := make([]string, 0, len(m.PlayerSample)+1)
		for _, name := range m.PlayerSample {
			if name != player {
				roster = append(roster, name)
			}
		}
		if joined {
			roster = append(roster, player)
		}
		m.PlayerSample = roster
		if playersOnline >= 0 {
			m.PlayersOnline = playersOnline
		} else {
			m.PlayersOnline = len(roster)
		}
		m.AgentRosterUpdated = time.Now()
		m.LastUpdated = time.Now()
	})
}

// Stores the authoritative supervisor-tracked player list
func (c *Collector) ApplyAgentRoster(serverID string, players []string) {
	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.PlayerSample = players
		m.PlayersOnline = len(players)
		m.AgentRosterUpdated = time.Now()
		m.LastUpdated = time.Now()
	})
}
