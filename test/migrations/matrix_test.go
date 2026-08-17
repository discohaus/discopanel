// Runs the engine across captured release fixtures
package migrationtests

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/internal/db/migrations"
	"github.com/nickheyer/protogorm/migrate"
)

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

// Every fixture must land on head or refuse honestly
// Fixtures come from seedgen, see its readme header
func TestFixtureMatrix(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("fixtures", "*.db.gz"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(fixtures) == 0 {
		t.Skip("no fixtures captured yet, run go run ./test/migrations/seedgen first")
	}

	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	headFP, err := migrations.Head().Fingerprint(d)
	if err != nil {
		t.Fatalf("head fingerprint: %v", err)
	}
	genesisFP := ""
	if migrations.Registry.Genesis != nil {
		genesisFP, err = migrations.Registry.Genesis.Fingerprint(d)
		if err != nil {
			t.Fatalf("genesis fingerprint: %v", err)
		}
	}

	for _, fixture := range fixtures {
		name := filepath.Base(fixture)
		t.Run(name, func(t *testing.T) {
			path := unpackFixture(t, fixture)
			db := openDB(t, path)

			// Pristine final v2 must match the genesis exactly
			if strings.Contains(name, "pristine-v2.0.15") && genesisFP != "" {
				observed, err := migrate.SpecOfDB(db)
				if err != nil {
					t.Fatalf("spec of db: %v", err)
				}
				fp, _ := observed.Fingerprint(d)
				if fp != genesisFP {
					t.Error("pristine v2.0.15 differs from committed genesis, refresh it from this fixture")
				}
			}

			report, err := (&migrate.Engine{
				DB:       db,
				Registry: migrations.Registry,
				Head:     migrations.Head(),
				Baseline: migrations.V2Baseline{},
				Apply:    true,
			}).Run(context.Background())

			// Pre v2 databases must refuse, not guess
			if strings.Contains(name, "-v1.") {
				if err == nil {
					t.Fatal("v1 era fixture was accepted, baseline must refuse it")
				}
				return
			}
			if err != nil {
				t.Fatalf("engine run: %v", err)
			}
			observed, err := migrate.SpecOfDB(db)
			if err != nil {
				t.Fatalf("spec of db: %v", err)
			}
			fp, _ := observed.Fingerprint(d)
			if fp != headFP {
				t.Fatalf("fixture landed off head, report %+v", report)
			}
		})
	}
}
