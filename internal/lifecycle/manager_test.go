package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Manager on a real store with docker left out
func testManager(t *testing.T) (*Manager, *storage.Store) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "lifecycle.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	m := NewManager(store, nil, nil, nil, nil, nil, cfg, nil, logger.New())
	return m, store
}

// Bare server row shaped like real creates
func seedServer(t *testing.T, store *storage.Store, id string, mutate func(*v1.Server)) *v1.Server {
	t.Helper()
	server := &v1.Server{
		Id:        id,
		Name:      id,
		ModLoader: v1.ModLoader_MOD_LOADER_VANILLA,
		McVersion: "1.21.1",
		DataPath:  t.TempDir(),
	}
	if mutate != nil {
		mutate(server)
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatalf("create server failed %v", err)
	}
	return server
}

// Overlapping transitions on one server must reject
func TestTransitionGateRejectsOverlap(t *testing.T) {
	m, _ := testManager(t)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.transition("srv", "start", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	err := m.transition("srv", "stop", func() error {
		t.Error("overlapping transition must never run")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("busy error must name the running kind, got %v", err)
	}
	if !m.IsStarting("srv") {
		t.Fatal("start must report as in flight")
	}

	// Other servers stay unaffected
	if err := m.transition("other", "stop", func() error { return nil }); err != nil {
		t.Fatalf("other server must transition freely, got %v", err)
	}
	if m.IsStarting("other") {
		t.Fatal("finished transition must not linger")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("held transition failed %v", err)
	}
	if m.IsStarting("srv") {
		t.Fatal("gate must clear after completion")
	}
	if err := m.transition("srv", "stop", func() error { return nil }); err != nil {
		t.Fatalf("gate must free after completion, got %v", err)
	}
}

// Failed transitions free the gate and surface the error
func TestTransitionGateFreesAfterError(t *testing.T) {
	m, _ := testManager(t)

	boom := errors.New("boom")
	if err := m.transition("srv", "start", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("transition must surface fn error, got %v", err)
	}
	if m.IsStarting("srv") {
		t.Fatal("failed transition must clear the gate")
	}
	if err := m.transition("srv", "restart", func() error { return nil }); err != nil {
		t.Fatalf("gate must free after failure, got %v", err)
	}
}

// Hammering one server never runs two transitions at once
func TestTransitionGateSerializes(t *testing.T) {
	m, _ := testManager(t)

	var active, overlapped, ran int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.transition("srv", "stop", func() error {
				if atomic.AddInt32(&active, 1) > 1 {
					atomic.StoreInt32(&overlapped, 1)
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&ran, 1)
				atomic.AddInt32(&active, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&overlapped) != 0 {
		t.Fatal("two transitions ran concurrently")
	}
	if atomic.LoadInt32(&ran) == 0 {
		t.Fatal("no transition ever ran")
	}
}

// Stop intent survives until a start clears it
func TestStopIntentLifecycle(t *testing.T) {
	m, _ := testManager(t)

	if src := m.StopRequestedBy("srv"); src != "" {
		t.Fatalf("fresh server must have no intent, got %q", src)
	}
	m.setStopIntent("srv", "autostop")
	if src := m.StopRequestedBy("srv"); src != "autostop" {
		t.Fatalf("intent lost, got %q", src)
	}
	// Start clears intent before loading the row
	if err := m.Start(context.Background(), "srv"); err == nil {
		t.Fatal("start without a row must fail")
	}
	if src := m.StopRequestedBy("srv"); src != "" {
		t.Fatalf("start must clear the intent, got %q", src)
	}
}

// Containerless stop persists stopped and records intent
func TestStopWithoutContainerMarksStopped(t *testing.T) {
	m, store := testManager(t)
	ctx := metrics.WithSource(context.Background(), "test")

	server := seedServer(t, store, "srv-1", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_RUNNING
	})

	if err := m.Stop(ctx, server.Id); err != nil {
		t.Fatalf("stop failed %v", err)
	}
	got, err := store.GetServer(ctx, server.Id)
	if err != nil {
		t.Fatalf("get failed %v", err)
	}
	if got.Status != v1.ServerStatus_SERVER_STATUS_STOPPED {
		t.Fatalf("status must persist stopped, got %v", got.Status)
	}
	if src := m.StopRequestedBy(server.Id); src != "test" {
		t.Fatalf("stop intent must record the source, got %q", src)
	}
}

// Paused flag gates the proxy sleeping info
func TestPausedGatesSleepingInfo(t *testing.T) {
	m, store := testManager(t)

	seedServer(t, store, "srv", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_PAUSED
		s.MaxPlayers = 7
	})

	if _, ok := m.SleepingInfo("srv"); ok {
		t.Fatal("unpaused server must not report sleeping")
	}
	m.setPaused("srv", true)
	info, ok := m.SleepingInfo("srv")
	if !ok || info == nil || info.MaxPlayers != 7 {
		t.Fatalf("paused server must expose sleeping info, got %v %+v", ok, info)
	}
	if !strings.Contains(info.Motd, "sleeping") {
		t.Fatalf("motd must mention sleeping, got %q", info.Motd)
	}
	m.setPaused("srv", false)
	if _, ok := m.SleepingInfo("srv"); ok {
		t.Fatal("unpaused server must stop reporting sleeping")
	}
}

// Idle timers pick the timeout by player history
func TestIdleTimeouts(t *testing.T) {
	m, _ := testManager(t)

	if got := m.timeoutFor(3600, 600, false); got != 600*time.Second {
		t.Fatalf("no players yet must use the initial timeout, got %v", got)
	}
	if got := m.timeoutFor(3600, 600, true); got != 3600*time.Second {
		t.Fatalf("seen players must use the established timeout, got %v", got)
	}

	zero := int32(0)
	custom := int32(42)
	if got := intOrDefault(nil, 600); got != 600 {
		t.Fatalf("nil must default, got %d", got)
	}
	if got := intOrDefault(&zero, 600); got != 600 {
		t.Fatalf("zero must default, got %d", got)
	}
	if got := intOrDefault(&custom, 600); got != 42 {
		t.Fatalf("set value must win, got %d", got)
	}

	// First observation takes the seed, later seeds never move it
	seed := time.Now().Add(-time.Minute)
	m.idleMu.Lock()
	st := m.idleStateFor("srv", seed)
	st.hadPlayers = true
	again := m.idleStateFor("srv", time.Now())
	m.idleMu.Unlock()
	if !st.lastActive.Equal(seed) || again != st {
		t.Fatalf("state must seed once, got %v and same=%v", st.lastActive, again == st)
	}
	// Touch restarts the clock, keeps player history
	m.touchIdle("srv")
	m.idleMu.Lock()
	restarted := *m.idle["srv"]
	m.idleMu.Unlock()
	if time.Since(restarted.lastActive) > time.Second || !restarted.hadPlayers {
		t.Fatalf("touch must restart the clock and keep history, got %+v", restarted)
	}

	cfg := &v1.ServerProperties{}
	if got := m.autostopTimeout(cfg, true); got != 3600*time.Second {
		t.Fatalf("seen players must pick established autostop, got %v", got)
	}
	if got := m.autostopTimeout(cfg, false); got != 1800*time.Second {
		t.Fatalf("no players must pick initial autostop, got %v", got)
	}
	if got := m.autopauseTimeout(cfg, false); got != 600*time.Second {
		t.Fatalf("no players must pick initial autopause, got %v", got)
	}
}

// Watcher starts once and stops clean
func TestIdleWatcherStartStop(t *testing.T) {
	m, _ := testManager(t)

	m.StartIdleWatcher()
	first := m.stopWatch
	if first == nil {
		t.Fatal("watcher must arm its stop channel")
	}
	m.StartIdleWatcher()
	if m.stopWatch != first {
		t.Fatal("second start must be a no op")
	}
	m.StopIdleWatcher()
	if m.stopWatch != nil {
		t.Fatal("stop must clear the channel")
	}
	// Double stop must not panic or hang
	m.StopIdleWatcher()
}
