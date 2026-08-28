package migrations

import (
	"strings"
	"testing"

	"github.com/nickheyer/protogorm/migrate"
)

// Fresh genesis spec safe to mutate
func genesis(t *testing.T) *migrate.Spec {
	t.Helper()
	return mustSnapshot("genesis.snapshot.json")
}

// Drops one column from a spec table
func dropColumn(spec *migrate.Spec, table, column string) {
	tbl := spec.Table(table)
	kept := tbl.Columns[:0]
	for _, c := range tbl.Columns {
		if c.Name != column {
			kept = append(kept, c)
		}
	}
	tbl.Columns = kept
}

// Drops one table from a spec
func dropTable(spec *migrate.Spec, table string) {
	kept := spec.Tables[:0]
	for _, t := range spec.Tables {
		if t.Name != table {
			kept = append(kept, t)
		}
	}
	spec.Tables = kept
}

func TestBaselineAcceptsGenesis(t *testing.T) {
	applied, err := V2Baseline{}.Detect(nil, genesis(t))
	if err != nil || applied != 0 {
		t.Fatalf("genesis refused: %d %v", applied, err)
	}
}

func TestBaselineAcceptsOlderSchemas(t *testing.T) {
	spec := genesis(t)
	dropColumn(spec, "servers", "auto_start")
	dropTable(spec, "server_configs")
	applied, err := V2Baseline{}.Detect(nil, spec)
	if err != nil || applied != 0 {
		t.Fatalf("older schema refused: %d %v", applied, err)
	}
	gaps := GenesisGaps(spec)
	if len(gaps) != 2 || gaps[0] != "table server_configs" || gaps[1] != "servers.auto_start" {
		t.Fatalf("gaps %v", gaps)
	}
}

func TestBaselineRefusesForeignDatabases(t *testing.T) {
	spec := genesis(t)
	dropTable(spec, "servers")
	_, err := V2Baseline{}.Detect(nil, spec)
	if err == nil || !strings.Contains(err.Error(), "not a discopanel database") {
		t.Fatalf("foreign schema accepted: %v", err)
	}
}

func TestBaselineRefusesUnledgeredV3(t *testing.T) {
	spec := genesis(t)
	spec.Tables = append(spec.Tables, &migrate.TableSpec{Name: "server_properties"})
	_, err := V2Baseline{}.Detect(nil, spec)
	if err == nil || !strings.Contains(err.Error(), "restore from backup") {
		t.Fatalf("unledgered v3 schema accepted: %v", err)
	}
}
