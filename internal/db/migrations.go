package db

import (
	"fmt"
	"log"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Materializes the schema from proto models and seeds base rows
func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(v1.AllModels()...); err != nil {
		return fmt.Errorf("schema migration failed: %w", err)
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
