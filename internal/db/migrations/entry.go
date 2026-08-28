// Entry steps carrying pre genesis databases onto genesis
package migrations

import (
	"fmt"
	"log"
	"time"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Conforms any older schema onto genesis and proves it landed
// Reproduces what booting the genesis release once did
func enterGenesis() migrate.Transform {
	conform := conformToTarget("genesis_entry", Registry.Genesis)
	return migrate.Transform{Name: conform.Name, Fn: func(tx *gorm.DB, d migrate.Dialect) error {
		if err := conform.Fn(tx, d); err != nil {
			return err
		}
		want, err := Registry.Genesis.Fingerprint(d)
		if err != nil {
			return err
		}
		reached, err := migrate.SpecOfDB(tx)
		if err != nil {
			return err
		}
		got, err := reached.Fingerprint(d)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("schema landed off %s", GenesisTag)
		}
		return nil
	}}
}

// Gives roleless users a role, first becomes admin
// Reproduces the last gormigrate step the genesis release ran
func backfillUserRoles(tx *gorm.DB, _ migrate.Dialect) error {
	var users []struct {
		ID       string
		Username string
	}
	q := "SELECT id AS id, username AS username FROM users WHERE id NOT IN (SELECT DISTINCT user_id FROM user_roles) ORDER BY created_at ASC"
	if err := tx.Raw(q).Scan(&users).Error; err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	var admins int64
	if err := tx.Raw("SELECT COUNT(*) FROM user_roles WHERE role_name = 'admin'").Scan(&admins).Error; err != nil {
		return err
	}
	for i, user := range users {
		role := "user"
		if i == 0 && admins == 0 {
			role = "admin"
		}
		if err := tx.Exec("INSERT INTO user_roles (id, user_id, role_name, source, created_at) VALUES (?, ?, ?, 'migration', ?)",
			user.ID+"-"+role, user.ID, role, time.Now().UTC()).Error; err != nil {
			return err
		}
		log.Printf("[migrate] user %s assigned role %s", user.Username, role)
	}
	return nil
}
