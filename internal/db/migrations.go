package db

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/discohaus/discopanel/internal/db/migrations"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/nickheyer/protogorm/migrate"
)

// Proves the schema and applies pending migrations
// Disabled auto migrate verifies and refuses instead
func (s *Store) Migrate() error {
	backup := ""
	if s.cfg.Database.Path != "" && s.cfg.Database.Path != ":memory:" {
		backup = s.cfg.Database.Path + ".pre-migrate.bak"
	}

	engine := &migrate.Engine{
		DB:         s.db,
		Registry:   migrations.Registry,
		Head:       migrations.Head(),
		Baseline:   migrations.V2Baseline{},
		AppVersion: config.AppVersion(),
		BackupPath: backup,
		Log:        log.Printf,
	}

	report, err := engine.Run(context.Background())
	if err != nil {
		return fmt.Errorf("schema migration failed: %w", err)
	}
	if len(report.Pending) > 0 {
		if !s.cfg.Database.AutoMigrate {
			return fmt.Errorf("database needs migrations %v, enable database.auto_migrate", report.Pending)
		}
		fresh := len(report.Pending) == 1 && report.Pending[0] == "fresh install"
		if backup != "" && !fresh {
			os.Remove(backup)
		}
		engine.Apply = true
		report, err = engine.Run(context.Background())
		if err != nil {
			return fmt.Errorf("schema migration failed: %w", err)
		}
		if len(report.Applied) > 0 && backup != "" {
			log.Printf("[migrate] Pre migration backup kept at %s", backup)
		}
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
