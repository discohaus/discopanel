package db

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store on a temp sqlite database
func testStore(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "test.db")
	cfg.Database.AutoMigrate = true
	store, err := NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func rawSample(serverID string, at time.Time, tps float64, players int32) *v1.MetricsSample {
	return &v1.MetricsSample{
		ServerId:    serverID,
		Timestamp:   timestamppb.New(at),
		Tps:         tps,
		Players:     players,
		ProxyLogins: 1,
	}
}

func TestMetricsRollupAndHistory(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	samples := []*v1.MetricsSample{
		rawSample("srv", base, 10, 3),
		rawSample("srv", base.Add(30*time.Second), 20, 5),
		rawSample("srv", base.Add(60*time.Second), 30, 4),
		rawSample("srv", base.Add(310*time.Second), 50, 2),
		rawSample("other", base, 5, 1),
	}
	if err := store.CreateMetricsSample(ctx, samples...); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Cutoff floors to base plus 300 so bucket stays whole
	if err := store.RollupMetricsSamples(ctx, base.Add(400*time.Second), 300); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	rows, err := store.GetMetricsHistory(ctx, "srv", base.Add(-time.Minute), base.Add(time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want rolled plus surviving raw, got %d rows", len(rows))
	}
	rolledRow, rawRow := rows[0], rows[1]
	if rolledRow.Resolution != 300 || !rolledRow.Timestamp.AsTime().Equal(base) {
		t.Fatalf("rolled row wrong: res=%d ts=%v", rolledRow.Resolution, rolledRow.Timestamp.AsTime())
	}
	if math.Abs(rolledRow.Tps-20) > 1e-9 || rolledRow.Players != 5 || rolledRow.ProxyLogins != 3 {
		t.Fatalf("rolled aggregates wrong: %+v", rolledRow)
	}
	if rawRow.Resolution != 0 || rawRow.Tps != 50 {
		t.Fatalf("raw row wrong: %+v", rawRow)
	}

	// Second run must find nothing new to fold
	if err := store.RollupMetricsSamples(ctx, base.Add(400*time.Second), 300); err != nil {
		t.Fatalf("rollup rerun: %v", err)
	}
	rows, err = store.GetMetricsHistory(ctx, "srv", base.Add(-time.Minute), base.Add(time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("history rerun: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rerun changed row count to %d", len(rows))
	}

	// Rolled row weighs 300 seconds against 30 for raw
	rows, err = store.GetMetricsHistory(ctx, "srv", base.Add(-time.Minute), base.Add(time.Hour), 600, 30)
	if err != nil {
		t.Fatalf("bucketed history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one display bucket, got %d", len(rows))
	}
	want := (20*300 + 50*30) / 330.0
	if math.Abs(rows[0].Tps-want) > 1e-9 {
		t.Fatalf("weighted tps: want %.4f got %.4f", want, rows[0].Tps)
	}
	if rows[0].ProxyLogins != 4 || rows[0].Players != 5 {
		t.Fatalf("bucket aggregates wrong: %+v", rows[0])
	}

	// Other server stays isolated
	rows, err = store.GetMetricsHistory(ctx, "other", base.Add(-time.Minute), base.Add(time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("other history: %v", err)
	}
	if len(rows) != 1 || rows[0].Tps != 5 {
		t.Fatalf("other server rows wrong: %+v", rows)
	}
}

func TestMetricsPruneUsesBareTimestamps(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	old := rawSample("srv", base, 10, 1)
	old.Resolution = 300
	fresh := rawSample("srv", base.Add(time.Hour), 20, 1)
	fresh.Resolution = 300
	// Raw rows must outlive a rolled prune
	raw := rawSample("srv", base, 30, 1)
	if err := store.CreateMetricsSample(ctx, old, fresh, raw); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := store.PruneMetricsSamples(ctx, 300, base.Add(30*time.Minute)); err != nil {
		t.Fatalf("prune: %v", err)
	}
	rows, err := store.GetMetricsHistory(ctx, "srv", base.Add(-time.Hour), base.Add(2*time.Hour), 0, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("prune kept wrong rows: %+v", rows)
	}
	if rows[0].Resolution != 0 || rows[0].Tps != 30 {
		t.Fatalf("raw row must survive the rolled prune: %+v", rows[0])
	}
	if rows[1].Resolution != 300 || rows[1].Tps != 20 {
		t.Fatalf("fresh rolled row must survive: %+v", rows[1])
	}
}
