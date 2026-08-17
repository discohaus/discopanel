package db

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/discohaus/discopanel/internal/db/migrations"
	"github.com/nickheyer/protogorm/migrate"
)

// Proves the schema and applies pending migrations
// Disabled auto migrate verifies and refuses instead
func (s *Store) Migrate() error {
	backup := ""
	if s.cfg.Database.Path != "" && s.cfg.Database.Path != ":memory:" {
		backup = s.cfg.Database.Path + ".pre-migrate.bak"
		os.Remove(backup)
	}

	report, err := (&migrate.Engine{
		DB:         s.db,
		Registry:   migrations.Registry,
		Head:       migrations.Head(),
		Baseline:   migrations.V2Baseline{},
		AppVersion: os.Getenv("APP_VERSION"),
		BackupPath: backup,
		Apply:      s.cfg.Database.AutoMigrate,
		Log:        log.Printf,
	}).Run(context.Background())
	if err != nil {
		return fmt.Errorf("schema migration failed: %w", err)
	}
	if len(report.Pending) > 0 {
		return fmt.Errorf("database needs migrations %v, enable database.auto_migrate", report.Pending)
	}
	if report.Fresh {
		log.Println("[migrate] Fresh schema created at head")
	}
	for _, name := range report.Applied {
		log.Printf("[migrate] Applied %s", name)
	}

	for _, seed := range []func() error{
		s.SeedSystemRoles,
		s.SeedGlobalSettings,
	} {
		if err := seed(); err != nil {
			return fmt.Errorf("seed failed: %w", err)
		}
	}

	log.Println("[migrate] Schema up to date")
	return nil
}
