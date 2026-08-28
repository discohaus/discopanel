// Runs the engine across every captured release fixture
package migrationtests

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/internal/db/migrations"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

var updateGenesis = flag.Bool("update-genesis", false, "rewrite the genesis snapshot from the genesis release fixture")

const genesisPath = "../../internal/db/migrations/genesis.snapshot.json"

// Every fixture must land on head with its rows intact
func TestFixtureMatrix(t *testing.T) {
	headFP := fingerprint(t, migrations.Head())

	for _, fixture := range fixtureFiles(t) {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			db := openDB(t, unpackFixture(t, fixture))
			preCounts := rowCounts(t, db)
			preSpec, err := migrate.SpecOfDB(db)
			if err != nil {
				t.Fatalf("spec of db: %v", err)
			}
			gaps := migrations.GenesisGaps(preSpec)
			if !versionLess(fixtureTag(fixture), migrations.GenesisTag) && len(gaps) > 0 {
				t.Fatalf("release since %s lacks genesis schema: %v", migrations.GenesisTag, gaps)
			}
			t.Logf("%d genesis gaps before intake", len(gaps))

			report, err := runEngine(t, db)
			if err != nil {
				t.Fatalf("engine refused a release: %v", err)
			}
			if observedFingerprint(t, db) != headFP {
				t.Fatalf("fixture landed off head, report %+v", report)
			}
			checkRowsSurvived(t, preSpec, preCounts, rowCounts(t, db))
			checkStorageClasses(t, db, migrations.Head())

			again, err := runEngine(t, db)
			if err != nil {
				t.Fatalf("second run: %v", err)
			}
			if len(again.Applied) != 0 || again.Fresh {
				t.Fatalf("second run reapplied %v", again.Applied)
			}
		})
	}
}

// Tables surviving by name keep their rows
func checkRowsSurvived(t *testing.T, pre *migrate.Spec, before, after map[string]int64) {
	t.Helper()
	for _, table := range pre.Tables {
		preN, ok := before[table.Name]
		if !ok || preN == 0 {
			continue
		}
		postN, ok := after[table.Name]
		if !ok {
			t.Logf("table %s retired by the migration, held %d rows", table.Name, preN)
			continue
		}
		if postN == 0 {
			t.Errorf("table %s lost every one of its %d rows", table.Name, preN)
			continue
		}
		if postN < preN {
			t.Logf("table %s went from %d to %d rows", table.Name, preN, postN)
		}
	}
}

// Numeric head columns must hold numeric values after intake
// Text inside an integer column means a skipped conversion
func checkStorageClasses(t *testing.T, db *gorm.DB, head *migrate.Spec) {
	t.Helper()
	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	for _, table := range head.Tables {
		for _, col := range table.Columns {
			typ, err := col.TypeFor(d.Name())
			if err != nil {
				t.Fatalf("%s.%s: %v", table.Name, col.Name, err)
			}
			switch d.NormalizeType(typ) {
			case "integer", "real", "numeric":
			default:
				continue
			}
			var bad int64
			q := "SELECT COUNT(*) FROM " + quoteIdent(table.Name) + " WHERE " + quoteIdent(col.Name) +
				" IS NOT NULL AND typeof(" + quoteIdent(col.Name) + ") NOT IN ('integer', 'real')"
			if err := db.Raw(q).Scan(&bad).Error; err != nil {
				t.Fatalf("%s.%s: %v", table.Name, col.Name, err)
			}
			if bad > 0 {
				t.Errorf("%s.%s holds %d non numeric values after migration", table.Name, col.Name, bad)
			}
		}
	}
}

// The committed genesis must equal the real genesis release schema
func TestGenesisMatchesRelease(t *testing.T) {
	fixture := filepath.Join(fixtureDir, migrations.GenesisTag+".db.gz")
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("no %s fixture captured", migrations.GenesisTag)
	}
	db := openDB(t, unpackFixture(t, fixture))
	observed, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	if *updateGenesis {
		data, err := observed.MarshalCanonical()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(genesisPath, data, 0644); err != nil {
			t.Fatalf("write genesis: %v", err)
		}
		t.Logf("genesis snapshot written from %s, commit it", migrations.GenesisTag)
		return
	}
	if migrations.Registry.Genesis == nil {
		t.Fatal("registry has no genesis snapshot")
	}
	if fingerprint(t, observed) != fingerprint(t, migrations.Registry.Genesis) {
		t.Fatalf("genesis snapshot differs from a real %s database, inspect then rerun with -update-genesis", migrations.GenesisTag)
	}
}
