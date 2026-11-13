package auth

import (
	"context"
	"errors"
	"testing"
)

func TestRecoveryLockoutIsPerClient(t *testing.T) {
	m := &Manager{recoveryKey: "correct-horse"}
	ctx := context.Background()
	for i := 0; i < recoveryMaxFails; i++ {
		if err := m.UseRecoveryKey(ctx, "wrong", "10.0.0.9"); !errors.Is(err, ErrInvalidRecoveryKey) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := m.UseRecoveryKey(ctx, "correct-horse", "10.0.0.9"); !errors.Is(err, ErrRecoveryLocked) {
		t.Fatalf("attacker not locked out: %v", err)
	}
	// A different client is unaffected (a wrong key is still just wrong)
	if err := m.UseRecoveryKey(ctx, "wrong", "192.168.1.5"); !errors.Is(err, ErrInvalidRecoveryKey) {
		t.Fatalf("other client blocked: %v", err)
	}
}
