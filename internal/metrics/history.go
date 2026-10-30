package metrics

import (
	"context"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Raw samples kept one day before rollup
	historyRawRetention = 24 * time.Hour
	// Rollup bucket width in seconds
	historyRollupSeconds = 300
	// Rollup samples kept thirty days
	historyRollupRetention = 30 * 24 * time.Hour
)

// Persists periodic snapshots and maintains the samples table
func (c *Collector) collectHistoryLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.collectorConfig.HistoryInterval)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Hour)
	defer maintenance.Stop()

	c.maintainHistory()

	for {
		select {
		case <-ticker.C:
			c.sampleHistory()
		case <-maintenance.C:
			c.maintainHistory()
		case <-c.stopChan:
			return
		}
	}
}

// Snapshots current metrics of alive servers into the samples table
func (c *Collector) sampleHistory() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	traffic := c.takeProxyDeltas()

	now := time.Now().UTC()
	var batch []*v1.MetricsSample
	for id, m := range c.GetAllMetrics() {
		// Lifecycle baseline only exists while the container is alive
		if !c.ServerAlive(id) {
			continue
		}
		t := traffic[id]
		if t == nil {
			t = &v1.ProxyRoute{}
		}
		sample := m.Sample(id)
		sample.Timestamp = timestamppb.New(now)
		sample.ProxyActiveConns = t.ActiveConnections
		sample.ProxyBytesIn = t.BytesToBackend
		sample.ProxyBytesOut = t.BytesToClient
		sample.ProxyLogins = t.Logins
		batch = append(batch, sample)
	}
	if err := c.store.CreateMetricsSample(ctx, batch...); err != nil {
		c.log.Debug("Metrics history: failed to insert samples: %v", err)
	}
}

// Window deltas from monotonic proxy totals, resets clamp to zero
func (c *Collector) takeProxyDeltas() map[string]*v1.ProxyRoute {
	if c.proxyTraffic == nil {
		return nil
	}
	totals := c.proxyTraffic()
	deltas := make(map[string]*v1.ProxyRoute, len(totals))
	if c.lastProxyTotals == nil {
		c.lastProxyTotals = make(map[string]*v1.ProxyRoute, len(totals))
	}
	clamp := func(cur, last int64) int64 {
		if d := cur - last; d > 0 {
			return d
		}
		return 0
	}
	for id, cur := range totals {
		last := c.lastProxyTotals[id]
		if last == nil {
			last = &v1.ProxyRoute{}
		}
		deltas[id] = &v1.ProxyRoute{
			ActiveConnections: cur.ActiveConnections,
			TotalConnections:  clamp(cur.TotalConnections, last.TotalConnections),
			Logins:            clamp(cur.Logins, last.Logins),
			BytesToBackend:    clamp(cur.BytesToBackend, last.BytesToBackend),
			BytesToClient:     clamp(cur.BytesToClient, last.BytesToClient),
		}
		c.lastProxyTotals[id] = cur
	}
	return deltas
}

// Rolls up old raw samples and prunes expired rollups
func (c *Collector) maintainHistory() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := c.store.RollupMetricsSamples(ctx, time.Now().UTC().Add(-historyRawRetention), historyRollupSeconds); err != nil {
		c.log.Warn("Metrics history: rollup failed: %v", err)
	}
	if err := c.store.PruneMetricsSamples(ctx, historyRollupSeconds, time.Now().UTC().Add(-historyRollupRetention)); err != nil {
		c.log.Warn("Metrics history: prune failed: %v", err)
	}
}

// Seconds between raw history samples
func (c *Collector) HistorySampleSeconds() int {
	return int(c.collectorConfig.HistoryInterval / time.Second)
}

// Reports whether the container was alive at last check
func (c *Collector) ServerAlive(serverID string) bool {
	_, seen := c.getLifecycle(serverID)
	return seen
}
