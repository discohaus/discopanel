package migrations

import (
	"fmt"

	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Maps unledgered pre framework databases onto the chain
type V2Baseline struct{}

// Confirms a v2 era schema and resumes before intake
// Older data dirs must boot v2.0.15 once first
func (V2Baseline) Detect(_ *gorm.DB, observed *migrate.Spec) (int, error) {
	if observed.Table("server_configs") == nil || observed.Table("server_properties") != nil {
		return 0, fmt.Errorf("database is not a v2 install, the upgrade path starts at v2.0.15")
	}
	servers := observed.Table("servers")
	if servers == nil || servers.Column("proxy_hostname") == nil {
		return 0, fmt.Errorf("servers table predates v2.0.15, boot v2.0.15 once before upgrading")
	}
	if observed.Table("modules") == nil || observed.Table("module_templates") == nil {
		return 0, fmt.Errorf("database predates modules, boot v2.0.15 once before upgrading")
	}
	return 0, nil
}
