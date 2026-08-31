// Sheds legacy v2 foreign keys the intake could not see
package migrations

import (
	"strings"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

func init() {
	target := mustSnapshot("0002_fk_strip.snapshot.json")
	Registry.MustAdd(&migrate.Migration{
		Ordinal: 2,
		Name:    "fk_strip",
		Target:  target,
		Ops: []migrate.Op{
			migrate.Transform{Name: "strip_foreign_keys", Fn: stripForeignKeys(target)},
		},
	})
}

// Rebuilds every table still carrying a v2 foreign key
func stripForeignKeys(target *migrate.Spec) func(*gorm.DB, migrate.Dialect) error {
	return func(tx *gorm.DB, d migrate.Dialect) error {
		// Only sqlite databases ever came from v2
		if d.Name() != "sqlite" {
			return nil
		}
		var rows []struct {
			Name string
			SQL  string
		}
		query := "SELECT name AS name, sql AS sql FROM sqlite_master WHERE type = 'table'"
		if err := tx.Raw(query).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if !strings.Contains(strings.ToUpper(row.SQL), "REFERENCES") {
				continue
			}
			spec := target.Table(row.Name)
			if spec == nil {
				continue
			}
			// Modify forces the full spec shaped rebuild
			change := migrate.TableChange{Table: spec, Modifies: []string{spec.Columns[0].Name}}
			statements, err := d.AlterPlan(&change)
			if err != nil {
				return err
			}
			for _, sql := range statements {
				if err := tx.Exec(sql).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}
}
