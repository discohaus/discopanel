package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestManager() *Manager {
	return &Manager{transitions: make(map[string]*slot)}
}

func TestClaimRejectsOverlap(t *testing.T) {
	m := newTestManager()
	if err := m.claim("s1", "start"); err != nil {
		t.Fatal(err)
	}
	err := m.claim("s1", "stop")
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Kind != "start" {
		t.Fatalf("second claim err = %v, want BusyError{start}", err)
	}
	if err := m.claim("s2", "stop"); err != nil {
		t.Fatalf("other server blocked: %v", err)
	}
	m.release("s1")
	if m.Busy("s1") {
		t.Fatal("slot still held after release")
	}
	if err := m.claim("s1", "stop"); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
}

// Simulates a start that runs until its context is cancelled, the way a
// long provisioning download does.
func fakeStart(t *testing.T, m *Manager, id string) <-chan struct{} {
	t.Helper()
	sl, err := m.claimSlot(id, "start")
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := sl.bind(context.Background())
	finished := make(chan struct{})
	go func() {
		defer done()
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // unwinding
		// Signal before releasing: once the slot is free a waiter may run
		// immediately, and it must already see the start as finished
		close(finished)
		m.release(id)
	}()
	return finished
}

func TestExclusivePreemptsStart(t *testing.T) {
	m := newTestManager()
	finished := fakeStart(t, m, "s1")

	ran := false
	err := m.Exclusive(context.Background(), "s1", "delete", true, func() error {
		select {
		case <-finished:
		default:
			t.Error("delete ran while the start still held the server")
		}
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("Exclusive err=%v ran=%v", err, ran)
	}
}

func TestExclusiveWithoutPreemptReportsBusy(t *testing.T) {
	m := newTestManager()
	fakeStart(t, m, "s1")
	err := m.Exclusive(context.Background(), "s1", "restore", false, func() error {
		t.Error("restore ran during a start")
		return nil
	})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v, want BusyError", err)
	}
}

func TestPreemptLeavesNonStartAlone(t *testing.T) {
	m := newTestManager()
	sl, err := m.claimSlot("s1", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if m.preemptStart(context.Background(), "s1") {
		t.Fatal("preempt reported cancelling a restart")
	}
	if sl.abortCtx.Err() != nil {
		t.Fatal("preempt cancelled a restart, only starts are abortable")
	}
}

// Starts launched synchronously (scheduler, autostart) must be abortable
// too, not only the ones the start RPC launched.
func TestSyncStartIsAbortable(t *testing.T) {
	m := newTestManager()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- m.transitionCtx(context.Background(), "s1", "start", func(ctx context.Context, _ string) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started

	ran := false
	if err := m.Exclusive(context.Background(), "s1", "delete", true, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("delete after preempt: %v", err)
	}
	if !ran {
		t.Fatal("delete did not run")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("start returned %v, want context.Canceled", err)
	}
}

// RunStartAsync binds to the slot BeginStart claimed
func TestHeldSlotBindsAsyncStart(t *testing.T) {
	m := newTestManager()
	if err := m.BeginStart("s1"); err != nil {
		t.Fatal(err)
	}
	sl := m.heldSlot("s1", "start")
	if sl == nil {
		t.Fatal("no held start slot")
	}
	ctx, done := sl.bind(context.Background())
	defer done()
	if !m.IsStarting("s1") {
		t.Fatal("not starting")
	}
	go m.preemptStart(context.Background(), "s1")
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("bound context not cancelled by preempt")
	}
	m.release("s1")
}
