package migrations

import (
	"fmt"
	"log"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Maps unledgered pre framework databases onto the chain
// Every panel database enters at ordinal zero, intake carries it
type V2Baseline struct{}

// Accepts anything shaped like a panel database
// Older schemas are noted, intake conforms them onto genesis
func (V2Baseline) Detect(_ *gorm.DB, observed *migrate.Spec) (int, error) {
	if observed.Table("servers") == nil {
		return 0, fmt.Errorf("database holds no servers table, not a discopanel database")
	}
	if observed.Table("server_properties") != nil {
		return 0, fmt.Errorf("database holds v3 tables without a migration ledger, restore from backup")
	}
	if gaps := GenesisGaps(observed); len(gaps) > 0 {
		log.Printf("[migrate] database predates %s, %d schema gaps close at intake", GenesisTag, len(gaps))
	}
	return 0, nil
}

// Genesis tables and columns the observed schema lacks
func GenesisGaps(observed *migrate.Spec) []string {
	var gaps []string
	for _, want := range Registry.Genesis.Tables {
		have := observed.Table(want.Name)
		if have == nil {
			gaps = append(gaps, "table "+want.Name)
			continue
		}
		for _, col := range want.Columns {
			if have.Column(col.Name) == nil {
				gaps = append(gaps, want.Name+"."+col.Name)
			}
		}
	}
	return gaps
}
