// Proves the committed chain is whole before any fixture runs
package migrationtests

import (
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/internal/db/migrations"
)

// Chain snapshots must end exactly on the head spec
func TestChainTargetMatchesHead(t *testing.T) {
	headFP := fingerprint(t, migrations.Head())
	last := migrations.Registry.At(migrations.Registry.Len())
	if last == nil {
		t.Fatal("registry holds no migrations")
	}
	if fingerprint(t, last.Target) != headFP {
		t.Fatalf("migration %s target differs from head, copy head.snapshot.json over it or scaffold", last.Name)
	}
}

// Fresh installs land on head in one step
func TestFreshInstallMatchesHead(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "fresh.db"))
	report, err := runEngine(t, db)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if !report.Fresh {
		t.Fatal("expected a fresh install")
	}
	if observedFingerprint(t, db) != fingerprint(t, migrations.Head()) {
		t.Fatal("fresh install differs from head")
	}
}
