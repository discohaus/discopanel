// Data rewrites carrying v2 rows into the v3 schema
package migrations

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/pkg/javaversions"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/nickheyer/protogorm/migrate"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
)

// Fixed id of the global settings row
const globalSettingsID = "global-settings"

// Builtin templates v3 no longer ships
var retiredBuiltins = []string{"builtin-mc-backup", "builtin-rcon-web"}

// Resolver from a v2 string onto an enum number
func named[E interface {
	protoreflect.Enum
	~int32
}](aliases map[string]int32) func(string) (int32, bool) {
	return func(s string) (int32, bool) {
		if n, ok := aliases[s]; ok {
			return n, true
		}
		if s == "" {
			return 0, true
		}
		e, ok := protometa.FromName[E](s)
		return int32(e), ok
	}
}

// Rewrites one string enum column onto proto numbers
// Numeric values pass through so reruns stay safe
func mapEnumColumn(tx *gorm.DB, d migrate.Dialect, table, column string, resolve func(string) (int32, bool)) error {
	col := d.Quote(column)
	tbl := d.Quote(table)
	var values []string
	if err := tx.Raw("SELECT DISTINCT " + col + " FROM " + tbl + " WHERE " + col + " IS NOT NULL").Scan(&values).Error; err != nil {
		return err
	}
	for _, v := range values {
		if _, err := strconv.Atoi(v); err == nil {
			continue
		}
		n, ok := resolve(v)
		if !ok {
			return fmt.Errorf("table %s has unknown %s value %q", table, column, v)
		}
		if err := tx.Exec("UPDATE "+tbl+" SET "+col+" = ? WHERE "+col+" = ?", n, v).Error; err != nil {
			return err
		}
	}
	return nil
}

// Turns empty string json columns into real nulls
func jsonEmptyToNull(tx *gorm.DB, d migrate.Dialect, table string, columns ...string) error {
	tbl := d.Quote(table)
	for _, column := range columns {
		col := d.Quote(column)
		stmts := []string{
			"UPDATE " + tbl + " SET " + col + " = NULL WHERE " + col + " = ''",
			"UPDATE " + tbl + " SET " + col + " = NULL WHERE " + col + " = 'null'",
		}
		for _, sql := range stmts {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// Rewrites port json protocols from strings onto numbers
func reshapePorts(tx *gorm.DB, d migrate.Dialect, table, column string) error {
	tbl := d.Quote(table)
	col := d.Quote(column)
	var rows []struct {
		ID    string
		Ports string
	}
	q := "SELECT id AS id, " + col + " AS ports FROM " + tbl + " WHERE " + col + " IS NOT NULL AND " + col + " != ''"
	if err := tx.Raw(q).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var ports []map[string]any
		if err := json.Unmarshal([]byte(row.Ports), &ports); err != nil {
			return fmt.Errorf("table %s row %s has bad %s json, %w", table, row.ID, column, err)
		}
		changed := false
		for _, port := range ports {
			proto, ok := port["protocol"].(string)
			if !ok {
				continue
			}
			n, known := protometa.FromName[v1.ModuleProtocol](proto)
			if !known {
				if proto == "" {
					n = v1.ModuleProtocol_MODULE_PROTOCOL_UNSPECIFIED
				} else {
					return fmt.Errorf("table %s row %s has unknown protocol %q", table, row.ID, proto)
				}
			}
			port["protocol"] = int32(n)
			changed = true
		}
		if !changed {
			continue
		}
		out, err := json.Marshal(ports)
		if err != nil {
			return err
		}
		if err := tx.Exec("UPDATE "+tbl+" SET "+col+" = ? WHERE id = ?", string(out), row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Megabytes parsed from an itc style memory string
func parseMemMB(s string) int32 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult = 1024
		s = strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult = 0
		s = strings.TrimSuffix(s, "K")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	if mult == 0 {
		return int32(n / 1024)
	}
	return int32(n * mult)
}

// Carries a v2 tag onto the runtime tag vocabulary
func mapRuntimeTag(tag string) string {
	if javaversions.ValidTag(tag) {
		return tag
	}
	if base, ok := strings.CutSuffix(tag, "-graalvm"); ok {
		if candidate := base + "-graal"; javaversions.ValidTag(candidate) {
			return candidate
		}
	}
	return ""
}

// Rewrites one docker_image column onto runtime tags
func normalizeRuntimeTags(tx *gorm.DB, table string) error {
	var tags []string
	q := fmt.Sprintf("SELECT DISTINCT docker_image FROM %s WHERE docker_image IS NOT NULL AND docker_image != ''", table)
	if err := tx.Raw(q).Scan(&tags).Error; err != nil {
		return err
	}
	for _, tag := range tags {
		mapped := mapRuntimeTag(tag)
		if mapped == tag {
			continue
		}
		res := tx.Exec(fmt.Sprintf("UPDATE %s SET docker_image = ? WHERE docker_image = ?", table), mapped, tag)
		if res.Error != nil {
			return res.Error
		}
		if mapped == "" {
			log.Printf("[migrate] %s docker_image %q cleared for %d rows, image resolves from java version", table, tag, res.RowsAffected)
		} else {
			log.Printf("[migrate] %s docker_image %q became %q for %d rows", table, tag, mapped, res.RowsAffected)
		}
	}
	return nil
}

// Deletes child rows whose parents no longer exist
func sweepOrphans(tx *gorm.DB, _ migrate.Dialect) error {
	sweeps := []struct {
		table string
		where string
	}{
		{"server_configs", "id != '" + globalSettingsID + "' AND server_id != '" + globalSettingsID + "' AND server_id NOT IN (SELECT id FROM servers)"},
		{"mods", "server_id NOT IN (SELECT id FROM servers)"},
		{"scheduled_tasks", "server_id NOT IN (SELECT id FROM servers)"},
		{"task_executions", "server_id NOT IN (SELECT id FROM servers) OR task_id NOT IN (SELECT id FROM scheduled_tasks)"},
		{"modules", "server_id NOT IN (SELECT id FROM servers) OR template_id NOT IN (SELECT id FROM module_templates)"},
		{"sessions", "user_id NOT IN (SELECT id FROM users)"},
		{"api_tokens", "user_id NOT IN (SELECT id FROM users)"},
		{"user_roles", "user_id NOT IN (SELECT id FROM users)"},
		{"indexed_modpack_files", "modpack_id NOT IN (SELECT id FROM indexed_modpacks)"},
		{"modpack_favorites", "modpack_id NOT IN (SELECT id FROM indexed_modpacks)"},
	}
	for _, s := range sweeps {
		res := tx.Exec("DELETE FROM " + s.table + " WHERE " + s.where)
		if res.Error != nil {
			return fmt.Errorf("sweep %s: %w", s.table, res.Error)
		}
		if res.RowsAffected > 0 {
			log.Printf("[migrate] swept %d orphaned %s rows", res.RowsAffected, s.table)
		}
	}
	return nil
}

// Normalizes server rows while still in v2 shape
func normalizeServers(tx *gorm.DB, d migrate.Dialect) error {
	if err := mapEnumColumn(tx, d, "servers", "mod_loader", named[v1.ModLoader](nil)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, d, "servers", "status", named[v1.ServerStatus](nil)); err != nil {
		return err
	}
	if err := tx.Exec("UPDATE servers SET java_version = 0 WHERE java_version IS NULL OR java_version = ''").Error; err != nil {
		return err
	}
	if err := tx.Exec("UPDATE servers SET java_version = CAST(java_version AS INTEGER)").Error; err != nil {
		return err
	}
	if err := normalizeRuntimeTags(tx, "servers"); err != nil {
		return err
	}

	// Single hostname becomes a one element list
	var rows []struct {
		ID       string
		Hostname string
	}
	if err := tx.Raw("SELECT id AS id, proxy_hostname AS hostname FROM servers WHERE proxy_hostname IS NOT NULL AND proxy_hostname != ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		list, err := json.Marshal([]string{row.Hostname})
		if err != nil {
			return err
		}
		if err := tx.Exec("UPDATE servers SET proxy_hostname = ? WHERE id = ?", string(list), row.ID).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec("UPDATE servers SET proxy_hostname = NULL WHERE proxy_hostname = ''").Error; err != nil {
		return err
	}

	if err := jsonEmptyToNull(tx, d, "servers", "additional_ports", "docker_overrides"); err != nil {
		return err
	}
	return reshapePorts(tx, d, "servers", "additional_ports")
}

// Fills computed server columns after the reshape
func backfillServers(tx *gorm.DB, _ migrate.Dialect) error {
	var rows []struct {
		ID       string
		Memory   int32
		DataPath string
		InitMem  string
		MaxMem   string
	}
	q := `SELECT s.id AS id, s.memory AS memory, s.data_path AS data_path,
		COALESCE(c.init_memory, '') AS init_mem, COALESCE(c.max_memory, '') AS max_mem
		FROM servers s LEFT JOIN server_configs c ON c.server_id = s.id`
	if err := tx.Raw(q).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		memMin := parseMemMB(row.InitMem)
		memMax := parseMemMB(row.MaxMem)
		if row.Memory > 0 && memMax > row.Memory {
			log.Printf("[migrate] server %s max heap %dM clamped to container %dM", row.ID, memMax, row.Memory)
			memMax = row.Memory
		}
		if memMax > 0 && memMin > memMax {
			log.Printf("[migrate] server %s min heap %dM clamped to max %dM", row.ID, memMin, memMax)
			memMin = memMax
		}
		icon := int32(v1.IconSource_ICON_SOURCE_UNSPECIFIED)
		if row.DataPath != "" {
			if _, err := os.Stat(filepath.Join(row.DataPath, "server-icon.png")); err == nil {
				icon = int32(v1.IconSource_ICON_SOURCE_UPLOAD)
			}
		}
		if err := tx.Exec("UPDATE servers SET memory_min = ?, memory_max = ?, icon_source = ? WHERE id = ?",
			memMin, memMax, icon, row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Maps user provider strings onto enum numbers
func normalizeUsers(tx *gorm.DB, d migrate.Dialect) error {
	return mapEnumColumn(tx, d, "users", "auth_provider", named[v1.AuthProvider](nil))
}

// Maps role sources, migration era rows count as local
func normalizeUserRoles(tx *gorm.DB, d migrate.Dialect) error {
	aliases := map[string]int32{"migration": int32(v1.RoleSource_ROLE_SOURCE_LOCAL)}
	return mapEnumColumn(tx, d, "user_roles", "source", named[v1.RoleSource](aliases))
}

// Casts modpack java versions onto integers
func normalizeModpacks(tx *gorm.DB, _ migrate.Dialect) error {
	if err := tx.Exec("UPDATE indexed_modpacks SET java_version = 0 WHERE java_version IS NULL OR java_version = ''").Error; err != nil {
		return err
	}
	if err := tx.Exec("UPDATE indexed_modpacks SET java_version = CAST(java_version AS INTEGER)").Error; err != nil {
		return err
	}
	return normalizeRuntimeTags(tx, "indexed_modpacks")
}

// Maps modpack file release channels onto enum numbers
func normalizeModpackFiles(tx *gorm.DB, d migrate.Dialect) error {
	return mapEnumColumn(tx, d, "indexed_modpack_files", "release_type", named[v1.ReleaseType](nil))
}

// Maps task enums and fans configs into typed columns
func normalizeTasks(tx *gorm.DB, d migrate.Dialect) error {
	if err := mapEnumColumn(tx, d, "scheduled_tasks", "task_type", named[v1.TaskType](nil)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, d, "scheduled_tasks", "status", named[v1.TaskStatus](nil)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, d, "scheduled_tasks", "schedule", named[v1.ScheduleType](nil)); err != nil {
		return err
	}
	if err := jsonEmptyToNull(tx, d, "scheduled_tasks", "event_triggers"); err != nil {
		return err
	}

	// Config json lands in the column its type owns
	targets := map[int32]string{
		int32(v1.TaskType_TASK_TYPE_COMMAND): "command_config",
		int32(v1.TaskType_TASK_TYPE_BACKUP):  "backup_config",
		int32(v1.TaskType_TASK_TYPE_SCRIPT):  "script_config",
		int32(v1.TaskType_TASK_TYPE_WEBHOOK): "webhook_config",
	}
	var rows []struct {
		ID       string
		TaskType int32
		Config   string
	}
	q := "SELECT id AS id, task_type AS task_type, COALESCE(config, '') AS config FROM scheduled_tasks WHERE config IS NOT NULL AND config != ''"
	if err := tx.Raw(q).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		column, ok := targets[row.TaskType]
		if !ok {
			log.Printf("[migrate] task %s config dropped, type carries none", row.ID)
			continue
		}
		if !json.Valid([]byte(row.Config)) {
			log.Printf("[migrate] task %s config dropped, invalid json", row.ID)
			continue
		}
		if err := tx.Exec("UPDATE scheduled_tasks SET "+d.Quote(column)+" = ? WHERE id = ?", row.Config, row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Maps execution enums, startup runs count as scheduled
func normalizeExecutions(tx *gorm.DB, d migrate.Dialect) error {
	if err := mapEnumColumn(tx, d, "task_executions", "status", named[v1.ExecutionStatus](nil)); err != nil {
		return err
	}
	aliases := map[string]int32{"startup": int32(v1.TaskTrigger_TASK_TRIGGER_SCHEDULED)}
	return mapEnumColumn(tx, d, "task_executions", "trigger", named[v1.TaskTrigger](aliases))
}

// Retires dead builtins and normalizes template rows
func normalizeTemplates(tx *gorm.DB, d migrate.Dialect) error {
	for _, id := range retiredBuiltins {
		var refs int64
		if err := tx.Raw("SELECT COUNT(*) FROM modules WHERE template_id = ?", id).Scan(&refs).Error; err != nil {
			return err
		}
		if refs == 0 {
			if err := tx.Exec("DELETE FROM module_templates WHERE id = ?", id).Error; err != nil {
				return err
			}
			continue
		}
		log.Printf("[migrate] template %s kept as custom, %d modules use it", id, refs)
	}
	if err := mapEnumColumn(tx, d, "module_templates", "type", named[v1.ModuleTemplateType](nil)); err != nil {
		return err
	}
	// Survivors stop pretending to be builtins
	for _, id := range retiredBuiltins {
		if err := tx.Exec("UPDATE module_templates SET type = ? WHERE id = ?",
			int32(v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_CUSTOM), id).Error; err != nil {
			return err
		}
	}
	if err := jsonEmptyToNull(tx, d, "module_templates",
		"default_env", "default_volumes", "ports", "suggested_dependencies",
		"default_hooks", "metadata", "default_access_urls"); err != nil {
		return err
	}
	return reshapePorts(tx, d, "module_templates", "ports")
}

// Normalizes module instance rows in place
func normalizeModules(tx *gorm.DB, d migrate.Dialect) error {
	if err := mapEnumColumn(tx, d, "modules", "status", named[v1.ModuleStatus](nil)); err != nil {
		return err
	}
	if err := jsonEmptyToNull(tx, d, "modules",
		"env_overrides", "volume_overrides", "ports", "dependencies",
		"event_hooks", "metadata", "access_urls"); err != nil {
		return err
	}
	return reshapePorts(tx, d, "modules", "ports")
}

// Copies server_configs rows into server_properties
// Columns pair up by their underscore free names
func copyServerConfigs(target *migrate.Spec) func(tx *gorm.DB, d migrate.Dialect) error {
	return func(tx *gorm.DB, _ migrate.Dialect) error {
		props := target.Table("server_properties")
		if props == nil {
			return fmt.Errorf("target spec is missing server_properties")
		}
		byNorm := map[string]string{}
		for _, col := range props.Columns {
			byNorm[normName(col.Name)] = col.Name
		}

		var rows []map[string]any
		if err := tx.Table("server_configs").Find(&rows).Error; err != nil {
			return err
		}
		dropped := map[string]bool{}
		for _, row := range rows {
			out := map[string]any{}
			for column, value := range row {
				name, ok := byNorm[normName(column)]
				if !ok {
					if value != nil {
						dropped[column] = true
					}
					continue
				}
				out[name] = value
			}
			if err := tx.Table("server_properties").Create(out).Error; err != nil {
				return fmt.Errorf("copy config %v: %w", row["id"], err)
			}
		}
		for column := range dropped {
			log.Printf("[migrate] server_configs.%s dropped, held only in backup", column)
		}
		log.Printf("[migrate] carried %d config rows into server_properties", len(rows))
		return nil
	}
}

// Underscore free lowercase form of a column name
func normName(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}
