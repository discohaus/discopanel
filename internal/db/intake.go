// Data rewrites carrying v2 rows into the v3 schema
package db

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/javaversions"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
)

// Description of the file whose state v3 beta databases hold
const intakeDesc = "v3_intake"

// Hooks run inside the transaction right after their file
var migrationHooks = map[string]func(*gorm.DB) error{
	"v3_prepare": intakeV2,
}

const globalSettingsID = "global-settings"

// Builtin templates v3 no longer ships
var retiredBuiltins = []string{"builtin-mc-backup", "builtin-rcon-web"}

// Carries v2 rows onto v3 meanings before v3_intake reshapes tables
func intakeV2(tx *gorm.DB) error {
	steps := []struct {
		name string
		fn   func(*gorm.DB) error
	}{
		{"user_roles_backfill", backfillUserRoles},
		{"sweep_orphans", sweepOrphans},
		{"servers_normalize", normalizeServers},
		{"servers_backfill", backfillServers},
		{"users_normalize", normalizeUsers},
		{"user_roles_normalize", normalizeUserRoles},
		{"modpacks_normalize", normalizeModpacks},
		{"modpack_files_normalize", normalizeModpackFiles},
		{"tasks_normalize", normalizeTasks},
		{"executions_normalize", normalizeExecutions},
		{"templates_normalize", normalizeTemplates},
		{"modules_normalize", normalizeModules},
		{"server_configs_copy", copyServerConfigs},
		{"casbin_rename", renameCasbinResources},
	}
	for _, step := range steps {
		if err := step.fn(tx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}

func quoteIdent(ident string) string {
	return "`" + ident + "`"
}

// Resolver from a v2 string onto an enum number
func named[E interface {
	protoreflect.Enum
	~int32
}](aliases map[string]int32) func(string) (int32, bool) {
	return func(s string) (int32, bool) {
		if n, ok := aliases[s]; ok {
			return n, true
		}
		e, ok := protometa.FromName[E](s)
		return int32(e), ok
	}
}

// Rewrites enum column onto proto numbers
func mapEnumColumn(tx *gorm.DB, table, column string, resolve func(string) (int32, bool), fallback int32) error {
	col := quoteIdent(column)
	tbl := quoteIdent(table)
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
			n = fallback
			if v != "" {
				log.Printf("[migrate] %s.%s value %q unknown, set to %d, original held in backup", table, column, v, fallback)
			}
		}
		if err := tx.Exec("UPDATE "+tbl+" SET "+col+" = ? WHERE "+col+" = ?", n, v).Error; err != nil {
			return err
		}
	}
	return nil
}

// Turns empty string json columns into real nulls
func jsonEmptyToNull(tx *gorm.DB, table string, columns ...string) error {
	tbl := quoteIdent(table)
	for _, column := range columns {
		col := quoteIdent(column)
		if err := tx.Exec("UPDATE " + tbl + " SET " + col + " = NULL WHERE " + col + " IN ('', 'null')").Error; err != nil {
			return err
		}
	}
	return nil
}

// Rewrites port json protocols from strings onto numbers
func reshapePorts(tx *gorm.DB, table, column string) error {
	tbl := quoteIdent(table)
	col := quoteIdent(column)
	resolve := named[v1.ModuleProtocol](nil)
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
			log.Printf("[migrate] %s row %s %s json unreadable, cleared, original held in backup", table, row.ID, column)
			if err := tx.Exec("UPDATE "+tbl+" SET "+col+" = NULL WHERE id = ?", row.ID).Error; err != nil {
				return err
			}
			continue
		}
		changed := false
		for _, port := range ports {
			proto, ok := port["protocol"].(string)
			if !ok {
				continue
			}
			n, ok := resolve(proto)
			if !ok {
				n = int32(v1.ModuleProtocol_MODULE_PROTOCOL_UNSPECIFIED)
				if proto != "" {
					log.Printf("[migrate] %s row %s protocol %q unknown, set unspecified", table, row.ID, proto)
				}
			}
			port["protocol"] = n
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

// Gives roleless users a role, first becomes admin
func backfillUserRoles(tx *gorm.DB) error {
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

// Deletes child rows whose parents no longer exist
func sweepOrphans(tx *gorm.DB) error {
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
func normalizeServers(tx *gorm.DB) error {
	if err := mapEnumColumn(tx, "servers", "mod_loader", named[v1.ModLoader](nil), int32(v1.ModLoader_MOD_LOADER_UNSPECIFIED)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, "servers", "status", named[v1.ServerStatus](nil), int32(v1.ServerStatus_SERVER_STATUS_STOPPED)); err != nil {
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
	if err := tx.Raw("SELECT id AS id, proxy_hostnames AS hostname FROM servers WHERE proxy_hostnames IS NOT NULL AND proxy_hostnames != ''").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Hostname, "[") {
			continue
		}
		list, err := json.Marshal([]string{row.Hostname})
		if err != nil {
			return err
		}
		if err := tx.Exec("UPDATE servers SET proxy_hostnames = ? WHERE id = ?", string(list), row.ID).Error; err != nil {
			return err
		}
	}
	if err := tx.Exec("UPDATE servers SET proxy_hostnames = NULL WHERE proxy_hostnames = ''").Error; err != nil {
		return err
	}

	if err := jsonEmptyToNull(tx, "servers", "additional_ports", "docker_overrides"); err != nil {
		return err
	}
	return reshapePorts(tx, "servers", "additional_ports")
}

// Fills computed server columns from the old config rows
func backfillServers(tx *gorm.DB) error {
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

func normalizeUsers(tx *gorm.DB) error {
	return mapEnumColumn(tx, "users", "auth_provider", named[v1.AuthProvider](nil), int32(v1.AuthProvider_AUTH_PROVIDER_LOCAL))
}

// Maps role sources, migration era rows count as local
func normalizeUserRoles(tx *gorm.DB) error {
	aliases := map[string]int32{"migration": int32(v1.RoleSource_ROLE_SOURCE_LOCAL)}
	return mapEnumColumn(tx, "user_roles", "source", named[v1.RoleSource](aliases), int32(v1.RoleSource_ROLE_SOURCE_LOCAL))
}

// Casts modpack java versions onto integers
func normalizeModpacks(tx *gorm.DB) error {
	if err := tx.Exec("UPDATE indexed_modpacks SET java_version = 0 WHERE java_version IS NULL OR java_version = ''").Error; err != nil {
		return err
	}
	if err := tx.Exec("UPDATE indexed_modpacks SET java_version = CAST(java_version AS INTEGER)").Error; err != nil {
		return err
	}
	return normalizeRuntimeTags(tx, "indexed_modpacks")
}

func normalizeModpackFiles(tx *gorm.DB) error {
	return mapEnumColumn(tx, "indexed_modpack_files", "release_type", named[v1.ReleaseType](nil), int32(v1.ReleaseType_RELEASE_TYPE_UNSPECIFIED))
}

// Maps task enums and fans configs into typed columns
func normalizeTasks(tx *gorm.DB) error {
	if err := mapEnumColumn(tx, "scheduled_tasks", "task_type", named[v1.TaskType](nil), int32(v1.TaskType_TASK_TYPE_UNSPECIFIED)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, "scheduled_tasks", "status", named[v1.TaskStatus](nil), int32(v1.TaskStatus_TASK_STATUS_ENABLED)); err != nil {
		return err
	}
	if err := mapEnumColumn(tx, "scheduled_tasks", "schedule", named[v1.ScheduleType](nil), int32(v1.ScheduleType_SCHEDULE_TYPE_UNSPECIFIED)); err != nil {
		return err
	}
	if err := jsonEmptyToNull(tx, "scheduled_tasks", "event_triggers"); err != nil {
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
		if err := tx.Exec("UPDATE scheduled_tasks SET "+quoteIdent(column)+" = ? WHERE id = ?", row.Config, row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Maps execution enums, startup runs count as scheduled
func normalizeExecutions(tx *gorm.DB) error {
	if err := mapEnumColumn(tx, "task_executions", "status", named[v1.ExecutionStatus](nil), int32(v1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED)); err != nil {
		return err
	}
	aliases := map[string]int32{"startup": int32(v1.TaskTrigger_TASK_TRIGGER_SCHEDULED)}
	return mapEnumColumn(tx, "task_executions", "trigger", named[v1.TaskTrigger](aliases), int32(v1.TaskTrigger_TASK_TRIGGER_SCHEDULED))
}

// Retires dead builtins and normalizes template rows
func normalizeTemplates(tx *gorm.DB) error {
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
		if err := tx.Exec("UPDATE module_templates SET type = ? WHERE id = ?",
			int32(v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_CUSTOM), id).Error; err != nil {
			return err
		}
		log.Printf("[migrate] template %s kept as custom, %d modules use it", id, refs)
	}
	if err := mapEnumColumn(tx, "module_templates", "type", named[v1.ModuleTemplateType](nil), int32(v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_CUSTOM)); err != nil {
		return err
	}
	if err := jsonEmptyToNull(tx, "module_templates",
		"default_env", "default_volumes", "ports", "suggested_dependencies",
		"default_hooks", "metadata", "default_access_urls"); err != nil {
		return err
	}
	return reshapePorts(tx, "module_templates", "ports")
}

// Normalizes module instance rows in place
func normalizeModules(tx *gorm.DB) error {
	if err := mapEnumColumn(tx, "modules", "status", named[v1.ModuleStatus](nil), int32(v1.ModuleStatus_MODULE_STATUS_STOPPED)); err != nil {
		return err
	}
	if err := jsonEmptyToNull(tx, "modules",
		"env_overrides", "volume_overrides", "ports", "dependencies",
		"event_hooks", "metadata", "access_urls"); err != nil {
		return err
	}
	return reshapePorts(tx, "modules", "ports")
}

// Copies server_configs rows into server_properties
func copyServerConfigs(tx *gorm.DB) error {
	var cols []struct{ Name string }
	if err := tx.Raw("PRAGMA table_info(`server_properties`)").Scan(&cols).Error; err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("server_properties table is missing")
	}
	byNorm := map[string]string{}
	for _, col := range cols {
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
	if len(rows) > 0 {
		log.Printf("[migrate] carried %d config rows into server_properties", len(rows))
	}
	return nil
}

func renameCasbinResources(tx *gorm.DB) error {
	return tx.Exec("UPDATE casbin_rule SET v1 = 'server_properties' WHERE v1 = 'server_config'").Error
}

func normName(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}
