// Locks the committed genesis snapshot to the v2 replica
package migrationtests

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/test/migrations/v2schema"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var update = flag.Bool("update", false, "rewrite the genesis snapshot")

const genesisPath = "../../internal/db/migrations/genesis.snapshot.json"

// Opens a fresh sqlite database for schema work
func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

// Builds the replica schema and reads it back as a spec
func replicaSpec(t *testing.T) *migrate.Spec {
	t.Helper()
	db := openDB(t, filepath.Join(t.TempDir(), "v2.db"))
	if err := v2schema.Materialize(db); err != nil {
		t.Fatalf("materialize v2 schema: %v", err)
	}
	spec, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	return spec
}

func TestGenesisMatchesV2Replica(t *testing.T) {
	spec := replicaSpec(t)
	derived, err := spec.MarshalCanonical()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if *update {
		if err := os.WriteFile(genesisPath, derived, 0644); err != nil {
			t.Fatalf("write genesis: %v", err)
		}
		t.Log("genesis snapshot written from the v2 replica, commit it")
		return
	}

	committed, readErr := os.ReadFile(genesisPath)
	if readErr != nil {
		t.Fatalf("read genesis snapshot: %v, rerun with -update", readErr)
	}
	parsed, err := migrate.ParseSpec(committed)
	if err != nil {
		t.Fatalf("parse committed genesis: %v", err)
	}
	if len(parsed.Tables) == 0 {
		t.Fatal("genesis snapshot is an empty placeholder, capture it with -update or seedgen")
	}

	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(derived)) {
		t.Fatal("genesis snapshot drifted from the v2 replica, inspect then rerun with -update")
	}
}
