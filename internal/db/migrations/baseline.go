package migrations

import (
	"fmt"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Maps unledgered pre framework databases onto the chain
type V2Baseline struct{}

// Accepts a schema holding every genesis table and column
// Leftovers on top pass, older data dirs boot v2.0.15 first
func (V2Baseline) Detect(_ *gorm.DB, observed *migrate.Spec) (int, error) {
	if observed.Table("server_configs") == nil || observed.Table("server_properties") != nil {
		return 0, fmt.Errorf("database is not a v2 install, the upgrade path starts at v2.0.15")
	}
	for _, want := range Registry.Genesis.Tables {
		have := observed.Table(want.Name)
		if have == nil {
			return 0, fmt.Errorf("table %s predates v2.0.15, boot v2.0.15 once before upgrading", want.Name)
		}
		for _, col := range want.Columns {
			if have.Column(col.Name) == nil {
				return 0, fmt.Errorf("%s.%s predates v2.0.15, boot v2.0.15 once before upgrading", want.Name, col.Name)
			}
		}
	}
	return 0, nil
}
