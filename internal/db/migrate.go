package db

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlclient"
	"ariga.io/atlas/sql/sqlite"
	"github.com/discohaus/discopanel/pkg/config"
	gormsqlite "gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql migrations/atlas.sum
var migrationFiles embed.FS

// Model schema the loader wrote, the live target
//
//go:embed schema.sql
var desiredSchema string

// Brings the database onto the embedded migration head
func (s *Store) migrate(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	dir, err := migrationDir()
	if err != nil {
		return err
	}
	if err := migrate.Validate(dir); err != nil {
		return fmt.Errorf("migration directory: %w", err)
	}
	files, err := dir.Files()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("no migration files embedded")
	}
	tables, err := tableNames(ctx, conn)
	if err != nil {
		return err
	}

	if tables[revisionTable] { // Rowless revision table counts as untracked
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+revisionTable+`"`).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			delete(tables, revisionTable)
		}
	}
	baseline, err := chainEntry(tables, files)
	if err != nil {
		return err
	}
	operator := migrate.WithOperatorVersion("discopanel " + config.AppVersion())
	drv, err := sqlite.Open(conn)
	if err != nil {
		return err
	}
	var (
		pending []migrate.File
		conform *migrate.Plan
	)
	if baseline == nil {
		if err := ensureRevisionTable(ctx, conn); err != nil {
			return err
		}
		if pending, err = pendingFiles(ctx, drv, dir, conn, operator); err != nil {
			return err
		}
	} else {
		pending = filesAfter(files, baseline.Version())
		if conform, err = conformPlan(ctx, drv, files, baseline.Version()); err != nil {
			return err
		}
	}
	if conform == nil && len(pending) == 0 {
		// Matching untracked database only needs its baseline row
		if baseline != nil {
			if _, err := adopt(ctx, drv, dir, conn, baseline, operator); err != nil {
				return err
			}
		}
		return s.checkDrift(ctx, conn)
	}
	// Gate protects existing data, fresh databases always initialize
	if !s.cfg.Database.AutoMigrate && len(tables) > 0 {
		var names []string
		if conform != nil {
			names = append(names, "conform to "+baseline.Name())
		}
		for _, f := range pending {
			names = append(names, f.Name())
		}
		return fmt.Errorf("database needs migrations %v, enable database.auto_migrate", names)
	}
	if err := s.backupBeforeMigrate(ctx, conn, len(tables) == 0); err != nil {
		return err
	}
	if baseline != nil {
		if conform != nil {
			if err := applyPlan(ctx, conn, conform); err != nil {
				return fmt.Errorf("conform onto %s: %w", baseline.Name(), err)
			}
		}
		if pending, err = adopt(ctx, drv, dir, conn, baseline, operator); err != nil {
			return err
		}
	}
	for _, f := range pending {
		if err := applyFile(ctx, conn, dir, f, operator); err != nil {
			return fmt.Errorf("apply %s: %w", f.Name(), err)
		}
		log.Printf("[migrate] Applied %s", f.Name())
	}
	return s.checkDrift(ctx, conn)
}

// Picks the file an untracked database already matches
// Nil means fresh or already tracked, so the executor decides
func chainEntry(tables map[string]bool, files []migrate.File) (migrate.File, error) {
	if len(tables) == 0 || tables[revisionTable] {
		return nil, nil
	}
	switch {
	case tables["server_properties"]:
		f := fileByDesc(files, intakeDesc)
		if f == nil {
			return nil, fmt.Errorf("migration %s missing from the directory", intakeDesc)
		}
		return f, nil
	case tables["servers"]:
		return files[0], nil
	default:
		return nil, errors.New("database holds tables but no servers table, not a discopanel database")
	}
}

func fileByDesc(files []migrate.File, desc string) migrate.File {
	for i := len(files) - 1; i >= 0; i-- {
		if files[i].Desc() == desc {
			return files[i]
		}
	}
	return nil
}

func filesAfter(files []migrate.File, version string) []migrate.File {
	var out []migrate.File
	for _, f := range files {
		if f.Version() > version {
			out = append(out, f)
		}
	}
	return out
}

// Copies the embedded migration files into an atlas memory dir
func migrationDir() (*migrate.MemDir, error) {
	dir := &migrate.MemDir{}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		data, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, err
		}
		if err := dir.WriteFile(entry.Name(), data); err != nil {
			return nil, err
		}
	}
	return dir, nil
}

// Lists files the executor still has to run
func pendingFiles(ctx context.Context, drv migrate.Driver, dir migrate.Dir, conn schema.ExecQuerier, opts ...migrate.ExecutorOption) ([]migrate.File, error) {
	ex, err := migrate.NewExecutor(drv, dir, revisionStore{conn}, opts...)
	if err != nil {
		return nil, err
	}
	pending, err := ex.Pending(ctx)
	if errors.Is(err, migrate.ErrNoPendingFiles) {
		return nil, nil
	}
	return pending, err
}

// Runs one migration file inside its own transaction
func applyFile(ctx context.Context, conn *sql.Conn, dir migrate.Dir, f migrate.File, opts ...migrate.ExecutorOption) error {
	return inMigrationTx(ctx, conn, func(tx *sqlclient.Tx, drv migrate.Driver) error {
		ex, err := migrate.NewExecutor(drv, dir, revisionStore{tx}, opts...)
		if err != nil {
			return err
		}
		if err := ex.Execute(ctx, f); err != nil {
			return err
		}
		hook := migrationHooks[f.Desc()]
		if hook == nil {
			return nil
		}
		session, err := gorm.Open(gormsqlite.Dialector{Conn: tx}, &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			return err
		}
		if err := hook(session); err != nil {
			return fmt.Errorf("hook %s: %w", f.Desc(), err)
		}
		log.Printf("[migrate] Ran %s hook", f.Desc())
		return nil
	})
}

// Runs fn in a migration transaction, commits when it succeeds
func inMigrationTx(ctx context.Context, conn *sql.Conn, fn func(*sqlclient.Tx, migrate.Driver) error) error {
	tx, err := beginMigrationTx(ctx, conn)
	if err != nil {
		return err
	}
	drv, err := sqlite.Open(tx)
	if err == nil {
		err = fn(tx, drv)
	}
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// Pauses foreign keys around the transaction and restores them after
// Hooks sweep orphans so commit skips the atlas violation diff
func beginMigrationTx(ctx context.Context, conn *sql.Conn) (*sqlclient.Tx, error) {
	var on sql.NullBool
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
		return nil, fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if on.Bool {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = off"); err != nil {
			return nil, fmt.Errorf("pause foreign keys: %w", err)
		}
	}
	restore := func(err error) error {
		if !on.Bool {
			return err
		}
		if _, perr := conn.ExecContext(ctx, "PRAGMA foreign_keys = on"); perr != nil {
			return errors.Join(err, fmt.Errorf("resume foreign keys: %w", perr))
		}
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, restore(err)
	}
	return &sqlclient.Tx{
		Tx:         tx,
		CommitFn:   func() error { return restore(tx.Commit()) },
		RollbackFn: func() error { return restore(tx.Rollback()) },
	}, nil
}

// Plans reshaping an untracked database onto one chain version
// Nil plan means the schema already matches
func conformPlan(ctx context.Context, drv migrate.Driver, files []migrate.File, version string) (*migrate.Plan, error) {
	var stmts []string
	for _, f := range files {
		if f.Version() > version {
			break
		}
		part, err := f.Stmts()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, part...)
	}
	want, err := inspectStatements(ctx, stmts)
	if err != nil {
		return nil, err
	}
	have, err := drv.InspectRealm(ctx, &schema.InspectRealmOption{Exclude: []string{"main." + revisionTable}})
	if err != nil {
		return nil, err
	}
	changes, err := drv.RealmDiff(have, want)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	return drv.PlanChanges(ctx, "conform", changes)
}

// Executes a conform plan inside one migration transaction
func applyPlan(ctx context.Context, conn *sql.Conn, plan *migrate.Plan) error {
	return inMigrationTx(ctx, conn, func(tx *sqlclient.Tx, _ migrate.Driver) error {
		for _, c := range plan.Changes {
			log.Printf("[migrate] Conform %s", cmp.Or(c.Comment, c.Cmd))
			if _, err := tx.ExecContext(ctx, c.Cmd, c.Args...); err != nil {
				return fmt.Errorf("%s: %w", c.Cmd, err)
			}
		}
		return nil
	})
}

// Records the baseline row and lists the files after it
func adopt(ctx context.Context, drv migrate.Driver, dir migrate.Dir, conn *sql.Conn, baseline migrate.File, opts ...migrate.ExecutorOption) ([]migrate.File, error) {
	if err := ensureRevisionTable(ctx, conn); err != nil {
		return nil, err
	}
	// Pending writes the baseline row before listing the rest
	opts = append(opts, migrate.WithBaselineVersion(baseline.Version()))
	pending, err := pendingFiles(ctx, drv, dir, conn, opts...)
	if err != nil {
		return nil, err
	}
	log.Printf("[migrate] Baselined at %s", baseline.Name())
	return pending, nil
}

// Builds a throwaway memory database from DDL and inspects it
func inspectStatements(ctx context.Context, stmts []string) (*schema.Realm, error) {
	mem, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	defer mem.Close()
	mem.SetMaxOpenConns(1)
	for _, stmt := range stmts {
		if _, err := mem.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("replay %q: %w", stmt, err)
		}
	}
	drv, err := sqlite.Open(mem)
	if err != nil {
		return nil, err
	}
	return drv.InspectRealm(ctx, nil)
}

// Compares the live schema against the loader written model schema
func (s *Store) checkDrift(ctx context.Context, conn *sql.Conn) error {
	stmts, err := migrate.NewLocalFile("schema.sql", []byte(desiredSchema)).Stmts()
	if err != nil {
		return fmt.Errorf("parse schema.sql: %w", err)
	}
	want, err := inspectStatements(ctx, stmts)
	if err != nil {
		return err
	}
	drv, err := sqlite.Open(conn)
	if err != nil {
		return err
	}
	have, err := drv.InspectRealm(ctx, &schema.InspectRealmOption{Exclude: []string{"main." + revisionTable}})
	if err != nil {
		return err
	}
	changes, err := drv.RealmDiff(have, want)
	if err != nil {
		return err
	}
	s.drift = describeChanges(changes)
	for _, line := range s.drift {
		log.Printf("[migrate] Schema drift, %s", line)
	}
	if len(s.drift) > 0 {
		log.Printf("[migrate] Models and database disagree, a migration is missing")
	}
	return nil
}

// Renders diff changes one line each for logs
func describeChanges(changes []schema.Change) []string {
	var out []string
	for _, c := range changes {
		switch c := c.(type) {
		case *schema.AddTable:
			out = append(out, "table "+c.T.Name+" missing")
		case *schema.DropTable:
			out = append(out, "table "+c.T.Name+" unexpected")
		case *schema.RenameTable:
			out = append(out, "table "+c.From.Name+" renamed to "+c.To.Name)
		case *schema.ModifyTable:
			for _, sub := range c.Changes {
				out = append(out, "table "+c.T.Name+" "+describeTableChange(sub))
			}
		default:
			out = append(out, fmt.Sprintf("%T", c))
		}
	}
	return out
}

func describeTableChange(c schema.Change) string {
	switch c := c.(type) {
	case *schema.AddColumn:
		return "column " + c.C.Name + " missing"
	case *schema.DropColumn:
		return "column " + c.C.Name + " unexpected"
	case *schema.ModifyColumn:
		return "column " + c.To.Name + " differs"
	case *schema.RenameColumn:
		return "column " + c.From.Name + " renamed to " + c.To.Name
	case *schema.AddIndex:
		return "index " + c.I.Name + " missing"
	case *schema.DropIndex:
		return "index " + c.I.Name + " unexpected"
	case *schema.ModifyIndex:
		return "index " + c.To.Name + " differs"
	case *schema.RenameIndex:
		return "index " + c.From.Name + " renamed to " + c.To.Name
	case *schema.AddForeignKey:
		return "foreign key " + c.F.Symbol + " missing"
	case *schema.DropForeignKey:
		return "foreign key " + c.F.Symbol + " unexpected"
	case *schema.ModifyForeignKey:
		return "foreign key " + c.To.Symbol + " differs"
	case *schema.AddCheck:
		return "check " + c.C.Name + " missing"
	case *schema.DropCheck:
		return "check " + c.C.Name + " unexpected"
	case *schema.ModifyCheck:
		return "check " + c.To.Name + " differs"
	default:
		return fmt.Sprintf("%T", c)
	}
}

// Snapshots the database file before schema work touches it
func (s *Store) backupBeforeMigrate(ctx context.Context, conn *sql.Conn, fresh bool) error {
	path := databaseFile(s.cfg.Database.Path)
	if fresh || path == "" {
		return nil
	}
	backup := fmt.Sprintf("%s.pre-migrate.%s.bak", path, time.Now().UTC().Format("20060102T150405"))
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return fmt.Errorf("backup to %s: %w", backup, err)
	}
	log.Printf("[migrate] Pre migration backup at %s", backup)
	return nil
}

// Strips DSN decoration, empty for memory databases
func databaseFile(dsn string) string {
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	if path == "" || path == ":memory:" || strings.Contains(dsn, "mode=memory") {
		return ""
	}
	return path
}
