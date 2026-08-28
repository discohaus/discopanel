// Settles any live schema onto a migration target
package migrations

import (
	"fmt"
	"log"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Diffs the live schema onto the target, applies the gap
// Nothing is kept outside the target, the backup holds it
func conformToTarget(name string, target *migrate.Spec) migrate.Transform {
	return migrate.Transform{Name: name, Fn: func(tx *gorm.DB, d migrate.Dialect) error {
		observed, err := migrate.SpecOfDB(tx)
		if err != nil {
			return err
		}
		carryIndexWhere(observed, target)
		res, err := confirmAll(d, observed, target)
		if err != nil {
			return err
		}
		ops, demands, err := migrate.Diff(observed, target, res)
		if err != nil {
			return err
		}
		if len(demands) > 0 {
			return fmt.Errorf("target needs authoring, %s", demands[0])
		}
		for _, op := range ops {
			logResidue(op)
			statements, err := residueSQL(d, op)
			if err != nil {
				return err
			}
			for _, sql := range statements {
				if err := tx.Exec(sql).Error; err != nil {
					return fmt.Errorf("conform %q: %w", sql, err)
				}
			}
		}
		return nil
	}}
}

// Inspection never reads index predicates, the target lends them
func carryIndexWhere(observed, target *migrate.Spec) {
	for _, t := range observed.Tables {
		want := target.Table(t.Name)
		if want == nil {
			continue
		}
		for _, idx := range t.Indexes {
			if same := want.Index(idx.Name); same != nil {
				idx.Where = same.Where
			}
		}
	}
}

// Resolution confirming every drop and rewrite the target implies
// New required columns fill with the zero value of their type
func confirmAll(d migrate.Dialect, observed, target *migrate.Spec) (*migrate.Resolution, error) {
	res := &migrate.Resolution{
		Copy:          map[string]map[string]string{},
		DropColumns:   map[string][]string{},
		ConfirmModify: map[string][]string{},
	}
	for _, t := range observed.Tables {
		res.DropTables = append(res.DropTables, t.Name)
		for _, c := range t.Columns {
			res.DropColumns[t.Name] = append(res.DropColumns[t.Name], c.Name)
		}
	}
	for _, t := range target.Tables {
		have := observed.Table(t.Name)
		for _, c := range t.Columns {
			res.ConfirmModify[t.Name] = append(res.ConfirmModify[t.Name], c.Name)
			if !c.NotNull || c.PK || c.Default != "" || hasSource(have, c) {
				continue
			}
			zero, err := zeroLiteral(d, c)
			if err != nil {
				return nil, err
			}
			if res.Copy[t.Name] == nil {
				res.Copy[t.Name] = map[string]string{}
			}
			res.Copy[t.Name][c.Name] = zero
		}
	}
	return res, nil
}

// Whether a live table holds the column or a former name of it
func hasSource(have *migrate.TableSpec, c *migrate.ColumnSpec) bool {
	if have == nil {
		return false
	}
	if have.Column(c.Name) != nil {
		return true
	}
	for _, was := range c.Was {
		if have.Column(was) != nil {
			return true
		}
	}
	return false
}

// Sql literal a new required column fills with
func zeroLiteral(d migrate.Dialect, c *migrate.ColumnSpec) (string, error) {
	typ, err := c.TypeFor(d.Name())
	if err != nil {
		return "", err
	}
	switch d.NormalizeType(typ) {
	case "integer", "real", "numeric":
		return "0", nil
	}
	return "''", nil
}

// Sql for one diff op, the diff never yields transforms
func residueSQL(d migrate.Dialect, op migrate.Op) ([]string, error) {
	switch o := op.(type) {
	case migrate.CreateTable:
		sql, err := d.CreateTableSQL(o.Table)
		if err != nil {
			return nil, err
		}
		out := []string{sql}
		for _, idx := range o.Table.Indexes {
			out = append(out, d.CreateIndexSQL(o.Table.Name, idx))
		}
		return out, nil
	case migrate.DropTable:
		return []string{d.DropTableSQL(o.Name)}, nil
	case migrate.TableChange:
		return d.AlterPlan(&o)
	}
	return nil, fmt.Errorf("diff produced unexpected op %T", op)
}

// Names every leftover the conform step settles
func logResidue(op migrate.Op) {
	switch o := op.(type) {
	case migrate.CreateTable:
		log.Printf("[migrate] table %s created onto target", o.Table.Name)
	case migrate.DropTable:
		log.Printf("[migrate] leftover table %s dropped, held only in backup", o.Name)
	case migrate.TableChange:
		for _, c := range o.Drops {
			log.Printf("[migrate] leftover %s.%s dropped, held only in backup", o.Table.Name, c)
		}
		for _, c := range o.Adds {
			log.Printf("[migrate] %s.%s added onto target", o.Table.Name, c)
		}
		for _, c := range o.Modifies {
			log.Printf("[migrate] %s.%s rewritten onto target", o.Table.Name, c)
		}
		for _, i := range o.DropIndexes {
			log.Printf("[migrate] leftover index %s on %s dropped", i, o.Table.Name)
		}
		for _, i := range o.AddIndexes {
			log.Printf("[migrate] index %s on %s created onto target", i, o.Table.Name)
		}
	}
}
