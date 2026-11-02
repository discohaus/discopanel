package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Provisioning that runs until a stop cancels it
type blockingEnsurer struct {
	calls     atomic.Int32
	entered   chan struct{}
	cancelled chan struct{}
	once      sync.Once
}

func newBlockingEnsurer() *blockingEnsurer {
	return &blockingEnsurer{entered: make(chan struct{}, 16), cancelled: make(chan struct{})}
}

func (e *blockingEnsurer) Ensure(ctx context.Context, _ *v1.Server, _ *v1.ServerProperties) (*provisioner.Result, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	<-ctx.Done()
	e.once.Do(func() { close(e.cancelled) })
	return nil, ctx.Err()
}

// Manager whose starts park in provisioning
func wakeManager(t *testing.T) (*Manager, *blockingEnsurer, *v1.Server) {
	t.Helper()
	m, store := testManager(t)
	m.rec = metrics.NewRecorder(store, logger.New())
	ens := newBlockingEnsurer()
	m.prov = ens
	server := seedServer(t, store, "srv-lazy", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
	})
	wake := true
	if err := store.CreateServerProperties(context.Background(), &v1.ServerProperties{
		Id: "srv-lazy", ServerId: "srv-lazy", EnableWakeOnConnect: &wake,
	}); err != nil {
		t.Fatalf("properties create failed %v", err)
	}
	return m, ens, server
}

func waitStatus(t *testing.T, m *Manager, id string, want v1.ServerStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := m.store.GetServer(context.Background(), id)
		if err == nil && got.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %v, want %v", got.GetStatus(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Login burst claims one start, a stop cancels it, the next login starts again
func TestWakeStartClaimsSlotOnceAndStopCancels(t *testing.T) {
	m, ens, server := wakeManager(t)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.StartServer(server.Id)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("login start asks must ride along, got %v", err)
		}
	}
	<-ens.entered
	if !m.IsStarting(server.Id) {
		t.Fatal("wake start must hold the start slot")
	}
	waitStatus(t, m, server.Id, v1.ServerStatus_SERVER_STATUS_PROVISIONING)
	time.Sleep(50 * time.Millisecond)
	if n := ens.calls.Load(); n != 1 {
		t.Fatalf("provisioning ran %d times, want 1", n)
	}

	// Stop preempts the wake start through the bound slot
	stopCtx := metrics.WithSource(context.Background(), "test")
	if err := m.Stop(stopCtx, server.Id); err != nil {
		t.Fatalf("stop failed %v", err)
	}
	select {
	case <-ens.cancelled:
	default:
		t.Fatal("stop returned before the wake start was cancelled")
	}
	if m.Busy(server.Id) {
		t.Fatal("slot still held after stop")
	}
	waitStatus(t, m, server.Id, v1.ServerStatus_SERVER_STATUS_STOPPED)

	// Server is wakeable again afterwards
	if err := m.StartServer(server.Id); err != nil {
		t.Fatalf("restart ask failed %v", err)
	}
	<-ens.entered
	if n := ens.calls.Load(); n != 2 {
		t.Fatalf("provisioning ran %d times, want 2", n)
	}
	m.Stop(stopCtx, server.Id)
}

// Non start transitions refuse the wake as busy
func TestWakeStartBusyKinds(t *testing.T) {
	cases := []struct {
		kind string
		busy bool
	}{
		{"start", false},
		{"stop", true},
		{"restart", true},
		{"delete", true},
		{"restore", true},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			m := newTestManager()
			if err := m.claim("srv", tc.kind); err != nil {
				t.Fatal(err)
			}
			err := m.StartServer("srv")
			if got := errors.Is(err, proxy.ErrServerBusy); got != tc.busy {
				t.Fatalf("StartServer during %s = %v, want busy %v", tc.kind, err, tc.busy)
			}
			if !tc.busy && err != nil {
				t.Fatalf("in flight start must serve the login, got %v", err)
			}
			if sl := m.heldSlot("srv", tc.kind); sl == nil {
				t.Fatal("refused ask must leave the holder's slot alone")
			}
		})
	}
}

// Docker paused state rebuilds the wake gate after a restart
func TestObserveContainerStatusRestoresPausedGate(t *testing.T) {
	m, store := testManager(t)
	seedServer(t, store, "srv", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_PAUSED
	})
	sleeping := func() bool {
		_, ok := m.SleepingInfo("srv")
		return ok
	}

	if sleeping() {
		t.Fatal("fresh manager must not know the server is paused")
	}
	m.ObserveContainerStatus("srv", v1.ServerStatus_SERVER_STATUS_PAUSED, m.StatusEpoch("srv"))
	if !sleeping() {
		t.Fatal("paused container must report sleeping")
	}

	// Running containers clear a stale flag
	m.ObserveContainerStatus("srv", v1.ServerStatus_SERVER_STATUS_RUNNING, m.StatusEpoch("srv"))
	if sleeping() {
		t.Fatal("running container must stop reporting sleeping")
	}

	// Observations during a transition are ignored
	epoch := m.StatusEpoch("srv")
	if err := m.claim("srv", "wake"); err != nil {
		t.Fatal(err)
	}
	m.ObserveContainerStatus("srv", v1.ServerStatus_SERVER_STATUS_PAUSED, epoch)
	if sleeping() {
		t.Fatal("busy server must not be marked paused")
	}
	m.release("srv")
}

// A wake that lands between the docker read and the observe wins
func TestObserveContainerStatusDropsStaleRead(t *testing.T) {
	m, store := testManager(t)
	seedServer(t, store, "srv", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_PAUSED
	})
	m.setPaused("srv", true)

	// Monitor reads paused, then a wake runs to completion
	epoch := m.StatusEpoch("srv")
	if err := m.transition("srv", "wake", func() error {
		m.setPaused("srv", false)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.ObserveContainerStatus("srv", v1.ServerStatus_SERVER_STATUS_PAUSED, epoch)
	if _, ok := m.SleepingInfo("srv"); ok {
		t.Fatal("stale paused read re-marked a woken server as sleeping")
	}

	// Pause racing a running read keeps its flag too
	epoch = m.StatusEpoch("srv")
	if err := m.transition("srv", "pause", func() error {
		m.setPaused("srv", true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m.ObserveContainerStatus("srv", v1.ServerStatus_SERVER_STATUS_RUNNING, epoch)
	if _, ok := m.SleepingInfo("srv"); !ok {
		t.Fatal("stale running read cleared a freshly paused server")
	}
}
