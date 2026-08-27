// Shared plumbing for the fixture matrix
package migrationtests

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/internal/db/migrations"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const fixtureDir = "fixtures"

// Opens a sqlite database for schema work
func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return db
}

// Unpacks one fixture database into a temp file
func unpackFixture(t *testing.T, path string) string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer gz.Close()
	out := filepath.Join(t.TempDir(), strings.TrimSuffix(filepath.Base(path), ".gz"))
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, gz); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return out
}

// Every captured fixture, skipping when none exist
func fixtureFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.db.gz"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no fixtures captured, run go generate ./test/migrations first")
	}
	sort.Strings(files)
	return files
}

// Final v2 tag fixtures hop through, from the manifest
func hopTag(t *testing.T) string {
	t.Helper()
	var manifest struct {
		Hop string `json:"hop"`
	}
	if data, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json")); err == nil {
		if json.Unmarshal(data, &manifest) == nil && manifest.Hop != "" {
			return manifest.Hop
		}
	}
	for _, f := range fixtureFiles(t) {
		if i := strings.Index(f, ".via-"); i >= 0 {
			return strings.TrimSuffix(f[i+len(".via-"):], ".db.gz")
		}
	}
	return ""
}

// Whether a fixture already passed through the hop release
func hopped(path string) bool {
	return strings.Contains(filepath.Base(path), ".via-")
}

// Tag a fixture was captured from
func fixtureTag(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".db.gz")
	if i := strings.Index(base, ".via-"); i >= 0 {
		base = base[:i]
	}
	return base
}

// Engine bookkeeping tables never count as data
var internalTables = map[string]bool{
	migrate.LedgerTable: true,
	"sqlite_sequence":   true,
	"migrations":        true,
}

// Row counts for every data table
func rowCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	counts := map[string]int64{}
	for _, name := range tables {
		if internalTables[name] {
			continue
		}
		var n int64
		if err := db.Raw("SELECT COUNT(*) FROM " + quoteIdent(name)).Scan(&n).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = n
	}
	return counts
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Sqlite fingerprint of one spec
func fingerprint(t *testing.T, spec *migrate.Spec) string {
	t.Helper()
	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	fp, err := spec.Fingerprint(d)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	return fp
}

// Live schema fingerprint of one database
func observedFingerprint(t *testing.T, db *gorm.DB) string {
	t.Helper()
	spec, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	return fingerprint(t, spec)
}

// Runs the production chain against one database
func runEngine(t *testing.T, db *gorm.DB) (*migrate.Report, error) {
	t.Helper()
	return (&migrate.Engine{
		DB:         db,
		Registry:   migrations.Registry,
		Head:       migrations.Head(),
		Baseline:   migrations.V2Baseline{},
		AppVersion: "test",
		Apply:      true,
		Log:        t.Logf,
	}).Run(context.Background())
}
