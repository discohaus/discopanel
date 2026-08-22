package metrics

import (
	"context"
	"slices"
	"sync"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type ServerMetrics struct {
	ServerID      string
	CpuPercent    float64
	CpuCount      int
	MemoryUsage   float64 // MB
	DiskUsage     int64   // Bytes (total server data)
	DiskTotal     int64   // Bytes (volume total)
	DiskUsed      int64   // Bytes (volume used, all data)
	WorldSize     int64   // Bytes (world directory only)
	PlayersOnline int
	Tps           float64
	LastUpdated   time.Time

	// SLP fields
	SlpAvailable    bool
	SlpLatencyMs    int64
	Motd            string
	ServerVersion   string
	ProtocolVersion int
	PlayerSample    []string
	MaxPlayers      int
	SLPLastUpdated  time.Time

	// Agent-sourced fields, take precedence over SLP when fresh
	AgentConnected     bool
	AgentJvmActive     bool      // Javaagent is feeding JVM/tick telemetry
	AgentReady         bool      // Agent reported server ready this run
	AgentTickUpdated   time.Time // Freshness of TPS/Mspt below
	AgentRosterUpdated time.Time // Freshness of PlayerSample/PlayersOnline
	AgentProcUpdated   time.Time // Freshness of java process attribution below
	Mspt               float64   // Mean ms per tick
	MsptMax            float64   // Worst tick in the sample window
	HeapUsedMb         float64
	HeapMaxMb          float64
	ThreadCount        int
	ClassCount         int
	CpuQuotaCores      float64 // Cgroup CPU quota (0 = unlimited)
	CpuThrottlePercent float64 // Share of CFS periods throttled (0-100)
	GCPauseCount       int64   // Pauses in the last sample window
	GCPauseTotalMs     float64
	GCPauseMaxMs       float64
	GCLogWindowAt      time.Time // Last gc.log sourced window arrival
	StartupSeconds     float64
	HostTHPMode        string // Host transparent hugepage mode from the runtime hello
	PSIAvailable       bool   // Kernel exposes pressure stall info for the cgroup
	PSICpuSome         float64
	PSIMemSome         float64
	PSIMemFull         float64
	PSIIoSome          float64

	// Last process exit reported by the agent (crash forensics)
	LastExitCode       int
	LastExitCrashed    bool
	LastExitOomKilled  bool
	LastExitBootFailed bool // Boot died and the runtime ended the hung JVM
	LastExitWasReady   bool // Server reached ready before this exit
	LastExitedAt       time.Time
	LastExitExcerpt    string              // Crash report head for display
	LastExitFatal      *agentv1.FatalError // Structured diagnosis from the runtime

	// Live proxied connection count from the routing layer
	ProxyActiveConns int64
}

// Renders the sampled subset as one telemetry point
func (m *ServerMetrics) Sample(serverID string) *v1.MetricsSample {
	// Stale heap and GC without a live agent must not report
	var heapUsed float64
	var gcCount int64
	var gcTotalMs, gcMaxMs float64
	if m.AgentConnected {
		heapUsed = m.HeapUsedMb
		gcCount, gcTotalMs, gcMaxMs = m.GCPauseCount, m.GCPauseTotalMs, m.GCPauseMaxMs
	}
	return &v1.MetricsSample{
		ServerId:         serverID,
		Timestamp:        timestamppb.Now(),
		Tps:              m.Tps,
		Mspt:             m.Mspt,
		Players:          int32(m.PlayersOnline),
		CpuPercent:       m.CpuPercent,
		MemoryMb:         m.MemoryUsage,
		HeapUsedMb:       heapUsed,
		DiskBytes:        m.DiskUsage,
		ProxyActiveConns: m.ProxyActiveConns,
		GcPauseCount:     gcCount,
		GcPauseTotalMs:   gcTotalMs,
		GcPauseMaxMs:     gcMaxMs,
	}
}

// Copies the metrics with slices, safe outside the lock
func (m *ServerMetrics) snapshot() *ServerMetrics {
	if m == nil {
		return nil
	}
	cp := *m
	cp.PlayerSample = slices.Clone(m.PlayerSample)
	return &cp
}

// Cached usage sample for one module container
type ModuleStats struct {
	CpuPercent  float64
	MemoryUsage float64
	LastUpdated time.Time
}

// Agent process attribution younger than this beats docker stats
const agentProcFreshFor = 45 * time.Second

// Reports whether agent roster is recent enough to be authoritative
func (m *ServerMetrics) agentRosterFresh() bool {
	return m != nil && m.AgentConnected && time.Since(m.AgentRosterUpdated) < 90*time.Second
}

// Reports whether a live agent vouches for server health
func (m *ServerMetrics) agentHealthProof() bool {
	return m != nil && m.AgentConnected && m.AgentReady
}

// Configuration for metrics collector
type CollectorConfig struct {
	StatsInterval   time.Duration // Default 5s
	EventsInterval  time.Duration // Default 10s
	DiskInterval    time.Duration // Default 60s
	SLPInterval     time.Duration // Default 15s
	SLPTimeout      time.Duration // Default 5s
	SLPEnabled      bool          // Default true
	HistoryInterval time.Duration // Default 30s

	// Panel-side health, SLP is the source, replaces in-container checks
	HealthStartupGrace  time.Duration // Starting to unhealthy without a ping, default 15m
	HealthFailThreshold int           // Healthy to unhealthy after failed pings, default 3
}

// Get default collector configuration
func DefaultConfig() CollectorConfig {
	return CollectorConfig{
		StatsInterval:       5 * time.Second,
		EventsInterval:      10 * time.Second,
		DiskInterval:        60 * time.Second,
		SLPInterval:         15 * time.Second,
		SLPTimeout:          5 * time.Second,
		SLPEnabled:          true,
		HistoryInterval:     30 * time.Second,
		HealthStartupGrace:  15 * time.Minute,
		HealthFailThreshold: 3,
	}
}

// Fills unset config fields with their defaults
func (cc CollectorConfig) withDefaults() CollectorConfig {
	def := DefaultConfig()
	if cc.StatsInterval <= 0 {
		cc.StatsInterval = def.StatsInterval
	}
	if cc.EventsInterval <= 0 {
		cc.EventsInterval = def.EventsInterval
	}
	if cc.DiskInterval <= 0 {
		cc.DiskInterval = def.DiskInterval
	}
	if cc.SLPInterval <= 0 {
		cc.SLPInterval = def.SLPInterval
	}
	if cc.SLPTimeout <= 0 {
		cc.SLPTimeout = def.SLPTimeout
	}
	if cc.HistoryInterval <= 0 {
		cc.HistoryInterval = def.HistoryInterval
	}
	if cc.HealthStartupGrace <= 0 {
		cc.HealthStartupGrace = def.HealthStartupGrace
	}
	if cc.HealthFailThreshold <= 0 {
		cc.HealthFailThreshold = def.HealthFailThreshold
	}
	return cc
}

// Tracks SLP results for one container run
type containerHealth struct {
	startedAt        time.Time
	everHealthy      bool
	consecutiveFails int
}

// Snapshot of a servers derived lifecycle state
type lifecycleState struct {
	healthy bool // Last observed docker health (StatusRunning)
}

// Collects server metrics in the background
type Collector struct {
	store  *storage.Store
	docker *docker.Client
	config *config.Config
	log    *logger.Logger
	bus    *events.Bus

	metrics map[string]*ServerMetrics
	mu      sync.RWMutex

	// Per-server lifecycle state for event derivation
	lifecycle   map[string]lifecycleState
	lifecycleMu sync.Mutex

	// Per-container SLP health records (the panel-side health source)
	health   map[string]*containerHealth
	healthMu sync.Mutex

	// Containers owned by minecraft servers, modules stay out
	serverContainers   map[string]bool
	serverContainersMu sync.Mutex

	// Cached module usage samples keyed by module id
	moduleStats   map[string]*ModuleStats
	moduleStatsMu sync.RWMutex

	// Proxy counter totals feeding per-window history deltas
	proxyTraffic    func() map[string]*v1.ProxyRoute
	lastProxyTotals map[string]*v1.ProxyRoute

	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup

	collectorConfig CollectorConfig
}

// Wires the proxy counter source after construction
func (c *Collector) SetProxyTrafficSource(fn func() map[string]*v1.ProxyRoute) {
	c.proxyTraffic = fn
}

// Creates a new metrics collector
func NewCollector(store *storage.Store, docker *docker.Client, cfg *config.Config, bus *events.Bus, log *logger.Logger, collectorCfg CollectorConfig) *Collector {
	cc := collectorCfg.withDefaults()

	return &Collector{
		store:           store,
		docker:          docker,
		config:          cfg,
		bus:             bus,
		log:             log,
		metrics:         make(map[string]*ServerMetrics),
		lifecycle:       make(map[string]lifecycleState),
		health:          make(map[string]*containerHealth),
		moduleStats:     make(map[string]*ModuleStats),
		collectorConfig: cc,
	}
}

// Starts background metrics collection
func (c *Collector) Start() error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = true
	c.stopChan = make(chan struct{})
	c.mu.Unlock()

	c.log.Info("Starting metrics collector")

	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	c.refreshServerContainers(initCtx)
	initCancel()

	// Disk samples refresh on start and stop too
	if c.bus != nil {
		c.bus.Subscribe(c.onLifecycleEvent)
	}

	// Start collection goroutines
	loopCount := 4 // Stats, disk, lifecycle-events, history
	if c.collectorConfig.SLPEnabled {
		loopCount += 1
	}
	c.wg.Add(loopCount)
	go c.loop(c.collectorConfig.StatsInterval, c.collectDockerStats)
	go c.loop(c.collectorConfig.DiskInterval, c.collectDiskUsage)
	eventsInterval := c.collectorConfig.EventsInterval
	if eventsInterval <= 0 {
		eventsInterval = 10 * time.Second
	}
	// First pass seeds baselines so running servers stay quiet
	go c.loop(eventsInterval, c.detectLifecycleEvents)
	go c.collectHistoryLoop()
	if c.collectorConfig.SLPEnabled {
		go c.loop(c.collectorConfig.SLPInterval, c.collectSLPData)
	}

	return nil
}

// Runs fn now then on every tick until stop
func (c *Collector) loop(interval time.Duration, fn func()) {
	defer c.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	fn()
	for {
		select {
		case <-ticker.C:
			fn()
		case <-c.stopChan:
			return
		}
	}
}

// Stops background metrics collection
func (c *Collector) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	close(c.stopChan)
	c.mu.Unlock()

	c.wg.Wait()
	c.log.Info("Metrics collector stopped")
}

// Returns an isolated metrics snapshot, nil for unknown servers
func (c *Collector) GetMetrics(serverID string) *ServerMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metrics[serverID].snapshot()
}

// Returns isolated snapshots of every server's metrics
func (c *Collector) GetAllMetrics() map[string]*ServerMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]*ServerMetrics, len(c.metrics))
	for id, m := range c.metrics {
		result[id] = m.snapshot()
	}
	return result
}

// Returns cached module usage, nil when unknown
func (c *Collector) GetModuleStats(moduleID string) *ModuleStats {
	c.moduleStatsMu.RLock()
	defer c.moduleStatsMu.RUnlock()
	s, ok := c.moduleStats[moduleID]
	if !ok {
		return nil
	}
	cp := *s
	return &cp
}

// Rebuilds the server container set from the store
func (c *Collector) refreshServerContainers(ctx context.Context) {
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return
	}
	c.setServerContainers(servers)
}

func (c *Collector) setServerContainers(servers []*v1.Server) {
	ids := make(map[string]bool, len(servers))
	for _, server := range servers {
		if server.ContainerId != "" {
			ids[server.ContainerId] = true
		}
	}
	c.serverContainersMu.Lock()
	c.serverContainers = ids
	c.serverContainersMu.Unlock()
}

func (c *Collector) isServerContainer(containerID string) bool {
	c.serverContainersMu.Lock()
	defer c.serverContainersMu.Unlock()
	return c.serverContainers[containerID]
}

// Collects CPU and memory stats from Docker
func (c *Collector) collectDockerStats() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Debug("Metrics collector: failed to list servers: %v", err)
		return
	}
	c.setServerContainers(servers)

	var traffic map[string]*v1.ProxyRoute
	if c.proxyTraffic != nil {
		traffic = c.proxyTraffic()
	}

	for _, server := range servers {
		if server.ContainerId == "" {
			continue
		}

		// Transient docker error keeps last sample
		status, err := c.docker.GetContainerStatus(ctx, server.ContainerId)
		if err != nil {
			continue
		}
		// Stopped container clears stale usage and tick stats
		if status != v1.ServerStatus_SERVER_STATUS_RUNNING && status != v1.ServerStatus_SERVER_STATUS_UNHEALTHY && status != v1.ServerStatus_SERVER_STATUS_STARTING {
			c.updateMetrics(server.Id, func(m *ServerMetrics) {
				m.CpuPercent = 0
				m.MemoryUsage = 0
				m.Tps = 0
				m.Mspt = 0
				m.MsptMax = 0
				m.HeapUsedMb = 0
				m.HeapMaxMb = 0
				m.PlayersOnline = 0
				m.PlayerSample = nil
			})
			continue
		}

		// Fresh agent attribution skips the stats round trip
		if m := c.GetMetrics(server.Id); m != nil && time.Since(m.AgentProcUpdated) <= agentProcFreshFor {
			c.updateMetrics(server.Id, func(m *ServerMetrics) {
				m.ProxyActiveConns = traffic[server.Id].GetActiveConnections()
				m.LastUpdated = time.Now()
			})
			continue
		}

		// Get container stats
		stats, err := c.docker.GetContainerStats(ctx, server.ContainerId)
		if err != nil {
			c.log.Debug("Metrics collector: failed to get stats for %s: %v", server.Id, err)
			continue
		}

		c.updateMetrics(server.Id, func(m *ServerMetrics) {
			// Recheck inside the lock, an agent sample may have landed
			if time.Since(m.AgentProcUpdated) > agentProcFreshFor {
				m.CpuPercent = stats.CpuPercent
				m.MemoryUsage = stats.MemoryUsage
			}
			m.CpuCount = stats.CpuCount
			m.ProxyActiveConns = traffic[server.Id].GetActiveConnections()
			m.LastUpdated = time.Now()
		})
	}

	c.collectModuleStats(ctx)
}

// Samples module container usage in parallel
func (c *Collector) collectModuleStats(ctx context.Context) {
	modules, err := c.store.ListModules(ctx)
	if err != nil {
		c.log.Debug("Metrics collector: failed to list modules: %v", err)
		return
	}

	fresh := make(map[string]*ModuleStats, len(modules))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, mod := range modules {
		if mod.ContainerId == "" {
			continue
		}
		wg.Add(1)
		go func(moduleID, containerID string) {
			defer wg.Done()
			status, err := c.docker.GetContainerStatus(ctx, containerID)
			if err != nil || status != v1.ServerStatus_SERVER_STATUS_RUNNING {
				return
			}
			stats, err := c.docker.GetContainerStats(ctx, containerID)
			if err != nil {
				return
			}
			mu.Lock()
			fresh[moduleID] = &ModuleStats{
				CpuPercent:  stats.CpuPercent,
				MemoryUsage: stats.MemoryUsage,
				LastUpdated: time.Now(),
			}
			mu.Unlock()
		}(mod.Id, mod.ContainerId)
	}
	wg.Wait()

	c.moduleStatsMu.Lock()
	c.moduleStats = fresh
	c.moduleStatsMu.Unlock()
}

// Collects disk usage for server worlds
func (c *Collector) collectDiskUsage() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Debug("Metrics collector: failed to list servers: %v", err)
		return
	}

	for _, server := range servers {
		c.sampleDiskUsage(server.Id, server.DataPath)
	}
}

// Samples one server data dir and its volume
func (c *Collector) sampleDiskUsage(serverID, dataPath string) {
	if dataPath == "" {
		return
	}

	diskTotal, diskUsed, err := files.GetDiskSpace(dataPath)
	if err != nil {
		c.log.Debug("Metrics collector: failed to get disk space: %v", err)
		diskTotal, diskUsed = 0, 0
	}

	totalSize, err := files.CalculateDirSize(dataPath)
	if err != nil {
		return
	}

	// A missing world means zero, other data still counts
	var totalWorldSize int64
	if worldPaths, err := files.FindWorldDirs(dataPath); err == nil {
		for _, worldPath := range worldPaths {
			size, err := files.CalculateDirSize(worldPath)
			if err != nil {
				continue
			}
			totalWorldSize += size
		}
	}

	c.updateMetrics(serverID, func(m *ServerMetrics) {
		m.DiskUsage = totalSize
		m.DiskTotal = diskTotal
		m.DiskUsed = diskUsed
		m.WorldSize = totalWorldSize
		m.LastUpdated = time.Now()
	})
}

// Refreshes disk usage promptly after provisioning and world saves
func (c *Collector) onLifecycleEvent(ctx context.Context, e events.Event) {
	switch e.Type {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
		v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
		v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART:
	default:
		return
	}
	go func() {
		lookupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server, err := c.store.GetServer(lookupCtx, e.ServerId)
		if err != nil {
			return
		}
		c.sampleDiskUsage(server.Id, server.DataPath)
	}()
}

// Updates metrics for a server
func (c *Collector) updateMetrics(serverID string, update func(*ServerMetrics)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	metrics, exists := c.metrics[serverID]
	if !exists {
		metrics = &ServerMetrics{ServerID: serverID}
		c.metrics[serverID] = metrics
	}
	update(metrics)
}

// Removes metrics for a server on delete
func (c *Collector) RemoveMetrics(serverID string) {
	c.mu.Lock()
	delete(c.metrics, serverID)
	c.mu.Unlock()
	c.clearLifecycle(serverID)
}

// Folds an SLP result into the container's health record
func (c *Collector) recordHealth(containerID string, startedAt time.Time, ok bool) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	h := c.health[containerID]
	// A different StartedAt means the container restarted, reset it
	if h == nil || !h.startedAt.Equal(startedAt) {
		h = &containerHealth{startedAt: startedAt}
		c.health[containerID] = h
	}
	if !ok {
		h.consecutiveFails++
		return
	}
	h.everHealthy = true
	h.consecutiveFails = 0
}

func (c *Collector) clearHealth(containerID string) {
	c.healthMu.Lock()
	delete(c.health, containerID)
	c.healthMu.Unlock()
}

// Implements docker.HealthChecker using the SLP ping record
func (c *Collector) ContainerHealth(containerID string, startedAt time.Time) v1.ServerStatus {
	// Module containers skip SLP health, docker state decides
	if !c.isServerContainer(containerID) {
		return v1.ServerStatus_SERVER_STATUS_UNSPECIFIED
	}
	c.healthMu.Lock()
	h := c.health[containerID]
	var record containerHealth
	if h != nil {
		record = *h
	}
	c.healthMu.Unlock()

	grace := c.collectorConfig.HealthStartupGrace
	threshold := c.collectorConfig.HealthFailThreshold

	// No record yet, collector hasn't pinged since (re)start
	if h == nil || !record.startedAt.Equal(startedAt) {
		if time.Since(startedAt) < grace {
			return v1.ServerStatus_SERVER_STATUS_STARTING
		}
		// Long-running container with no data assumes healthy until proven otherwise
		return v1.ServerStatus_SERVER_STATUS_UNSPECIFIED
	}

	if record.everHealthy {
		if record.consecutiveFails >= threshold {
			return v1.ServerStatus_SERVER_STATUS_UNHEALTHY
		}
		return v1.ServerStatus_SERVER_STATUS_RUNNING
	}

	// Never answered a ping this run
	if time.Since(startedAt) >= grace {
		return v1.ServerStatus_SERVER_STATUS_UNHEALTHY
	}
	return v1.ServerStatus_SERVER_STATUS_STARTING
}

// Implements lifecycle.PlayerCounter from agent roster or fresh SLP
func (c *Collector) PlayersOnline(serverID string) (int, bool) {
	m := c.GetMetrics(serverID)
	if m == nil {
		return 0, false
	}
	if m.agentRosterFresh() {
		return m.PlayersOnline, true
	}
	if m.SlpAvailable && time.Since(m.SLPLastUpdated) < 2*c.collectorConfig.SLPInterval {
		return m.PlayersOnline, true
	}
	return 0, false
}

func (c *Collector) collectSLPData() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Debug("Metrics collector SLP: failed to list servers: %v", err)
		return
	}

	slpClient := minecraft.NewSLPClient(c.collectorConfig.SLPTimeout)

	// Recreated containers leave stale health records behind
	live := make(map[string]bool, len(servers))
	for _, server := range servers {
		if server.ContainerId != "" {
			live[server.ContainerId] = true
		}
	}
	c.healthMu.Lock()
	for id := range c.health {
		if !live[id] {
			delete(c.health, id)
		}
	}
	c.healthMu.Unlock()

	for _, server := range servers {
		if server.ContainerId == "" {
			continue
		}

		// Agent-vouched servers only need a slow Motd refresh
		if m := c.GetMetrics(server.Id); m.agentHealthProof() && m.agentRosterFresh() &&
			time.Since(m.SLPLastUpdated) < 4*c.collectorConfig.SLPInterval {
			continue
		}

		// Pings raw containers, must not consult GetContainerStatus here
		info, err := c.docker.GetContainerRunInfo(ctx, server.ContainerId)
		if err != nil || !info.Running || info.Paused {
			c.clearHealth(server.ContainerId)
			c.updateMetrics(server.Id, func(m *ServerMetrics) {
				m.SlpAvailable = false
			})
			continue
		}

		// Get container IP
		containerIP, err := c.docker.ContainerIP(ctx, server.ContainerId)
		if err != nil {
			c.log.Debug("Metrics collector SLP: failed to get container IP for %s: %v", server.Id, err)
			// A live ready agent session vouches when SLP cannot
			c.recordHealth(server.ContainerId, info.StartedAt, c.GetMetrics(server.Id).agentHealthProof())
			c.updateMetrics(server.Id, func(m *ServerMetrics) {
				m.SlpAvailable = false
			})
			continue
		}

		// SLP ping w/ server version for protocol
		slpCtx, slpCancel := context.WithTimeout(ctx, c.collectorConfig.SLPTimeout)
		port := int(server.Port)
		if len(server.ProxyHostnames) > 0 || port == 0 {
			port = docker.DefaultMinecraftPort // Proxy listens on default port (inside container)
		}
		result, err := slpClient.Ping(slpCtx, containerIP, port)
		slpCancel()

		if err != nil {
			c.log.Debug("Metrics collector SLP: failed to ping %s (%s:%d): %v", server.Id, containerIP, port, err)
			// Blocked SLP should not read unhealthy if agent vouches
			c.recordHealth(server.ContainerId, info.StartedAt, c.GetMetrics(server.Id).agentHealthProof())
			c.updateMetrics(server.Id, func(m *ServerMetrics) {
				m.SlpAvailable = false
			})
			continue
		}

		c.recordHealth(server.ContainerId, info.StartedAt, true)

		// Fresh agent roster is authoritative, SLP must not clobber it
		c.updateMetrics(server.Id, func(m *ServerMetrics) {
			m.SlpAvailable = true
			m.SlpLatencyMs = result.LatencyMs
			m.Motd = result.Motd
			m.ServerVersion = result.Version.Name
			m.ProtocolVersion = result.Version.Protocol
			if !m.agentRosterFresh() {
				m.PlayerSample = result.PlayerNames
				m.PlayersOnline = result.Players.Online
			}
			m.MaxPlayers = result.Players.Max
			m.SLPLastUpdated = time.Now()
			m.LastUpdated = time.Now()
		})
	}
}


// Compares each servers current health/player state against the previous state
func (c *Collector) detectLifecycleEvents() {
	if c.bus == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Debug("Metrics collector lifecycle: failed to list servers: %v", err)
		return
	}

	for _, server := range servers {
		if server.ContainerId == "" {
			c.clearLifecycle(server.Id)
			continue
		}

		status, err := c.docker.GetContainerStatus(ctx, server.ContainerId)
		if err != nil {
			c.clearLifecycle(server.Id)
			continue
		}

		// Fully down containers forget baseline so restart reseeds clean
		alive := status == v1.ServerStatus_SERVER_STATUS_RUNNING || status == v1.ServerStatus_SERVER_STATUS_UNHEALTHY || status == v1.ServerStatus_SERVER_STATUS_STARTING
		if !alive {
			c.clearLifecycle(server.Id)
			continue
		}

		// "Healthy" == docker health check passing (StatusRunning)
		healthy := status == v1.ServerStatus_SERVER_STATUS_RUNNING

		prev, seen := c.getLifecycle(server.Id)
		if seen && healthy && !prev.healthy {
			c.emit(ctx, v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_HEALTHY, server.Id, nil)
		}
		c.setLifecycle(server.Id, lifecycleState{healthy: healthy})
	}
}

// Emits a derived lifecycle event with optional data
func (c *Collector) emit(ctx context.Context, t v1.TriggeredEventType, serverID string, data map[string]string) {
	if c.bus == nil {
		return
	}
	c.bus.Emit(ctx, events.Event{Type: t, ServerId: serverID, Data: data})
}

func (c *Collector) getLifecycle(serverID string) (lifecycleState, bool) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	st, ok := c.lifecycle[serverID]
	return st, ok
}

func (c *Collector) setLifecycle(serverID string, st lifecycleState) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.lifecycle[serverID] = st
}

func (c *Collector) clearLifecycle(serverID string) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	delete(c.lifecycle, serverID)
}
