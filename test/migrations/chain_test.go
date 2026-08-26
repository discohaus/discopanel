// Proves the v2 intake lands real data on the head schema
package migrationtests

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/discohaus/discopanel/internal/db/migrations"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/test/migrations/v2schema"
	"github.com/nickheyer/protogorm/migrate"
	"gorm.io/gorm"
)

// Chain snapshots must end exactly on the head spec
func TestChainTargetMatchesHead(t *testing.T) {
	d, err := migrate.DialectByName("sqlite")
	if err != nil {
		t.Fatalf("dialect: %v", err)
	}
	headFP, err := migrations.Head().Fingerprint(d)
	if err != nil {
		t.Fatalf("head fingerprint: %v", err)
	}
	last := migrations.Registry.At(migrations.Registry.Len())
	lastFP, err := last.Target.Fingerprint(d)
	if err != nil {
		t.Fatalf("target fingerprint: %v", err)
	}
	if lastFP != headFP {
		t.Fatalf("migration %s target differs from head, copy head.snapshot.json over it or scaffold", last.Name)
	}
}

// Seeds one lived in v2 database through the replica
func seedV2(t *testing.T, db *gorm.DB, dataPath string) {
	t.Helper()
	now := time.Now().UTC()
	rows := []any{
		&v2schema.User{ID: "u-admin", Username: "admin", AuthProvider: "local", PasswordHash: "x", IsActive: true},
		&v2schema.User{ID: "u-sso", Username: "sso", AuthProvider: "oidc", OIDCSubject: "sub-1", OIDCIssuer: "https://idp", IsActive: true},
		&v2schema.Role{ID: "role-admin", Name: "admin", IsSystem: true},
		&v2schema.Role{ID: "role-user", Name: "user", IsSystem: true, IsDefault: true},
		&v2schema.UserRole{ID: "ur-1", UserID: "u-admin", RoleName: "admin", Source: "migration"},
		&v2schema.UserRole{ID: "ur-2", UserID: "u-sso", RoleName: "user", Source: "oidc"},
		&v2schema.Session{ID: "sess-1", UserID: "u-admin", Token: "tok-1", ExpiresAt: now.Add(time.Hour)},
		&v2schema.APIToken{ID: "at-1", UserID: "u-admin", Name: "ci", TokenHash: "hash-1"},
		&v2schema.RegistrationInvite{ID: "inv-1", Code: "JOIN", Roles: json.RawMessage(`["user"]`), CreatedBy: "u-admin"},
		&v2schema.Server{
			ID: "s1", Name: "Fixture Survival", ModLoader: "forge", MCVersion: "1.20.1",
			Status: "stopped", Port: 25565, ProxyHostname: "play.example.com",
			MaxPlayers: 20, Memory: 4096, JavaVersion: "17", DataPath: dataPath,
			DockerImage:     "stable",
			AdditionalPorts: json.RawMessage(`[{"name":"map","container_port":8100,"host_port":8100,"protocol":"udp"}]`),
		},
		&v2schema.Server{
			ID: "s2", Name: "Graal Pinned", ModLoader: "fabric", MCVersion: "1.21.1",
			Status: "stopped", Port: 25566, MaxPlayers: 10, Memory: 4096,
			JavaVersion: "21", DataPath: dataPath, DockerImage: "java21-graalvm",
		},
		&v2schema.Server{
			ID: "s3", Name: "Plain Pinned", ModLoader: "paper", MCVersion: "1.21.1",
			Status: "stopped", Port: 25567, MaxPlayers: 10, Memory: 4096,
			JavaVersion: "21", DataPath: dataPath, DockerImage: "java21",
		},
		&v2schema.ServerConfig{
			ID: "c1", ServerID: "s1",
			InitMemory: ptr("2048M"), MaxMemory: ptr("8192M"),
			SpawnNPCs: ptrBool(true), CFAPIKey: ptr("cf-key"), JVMOpts: ptr("-XX:+UseZGC"),
		},
		&v2schema.ServerConfig{ID: "global-settings", ServerID: "global-settings", MOTD: ptr("hello")},
		&v2schema.Mod{ID: "orphan-mod", ServerID: "ghost", Name: "gone", FileName: "gone.jar"},
		&v2schema.ScheduledTask{
			ID: "t1", ServerID: "s1", Name: "announce", TaskType: "command",
			Status: "enabled", Schedule: "cron", CronExpr: "0 * * * *",
			Config: `{"command":"say hi"}`,
		},
		&v2schema.ScheduledTask{
			ID: "t2", ServerID: "s1", Name: "nightly", TaskType: "backup",
			Status: "disabled", Schedule: "interval", IntervalSecs: 3600,
			Config: `{"backup_name":"nightly","retention_days":7}`,
		},
		&v2schema.TaskExecution{ID: "e1", TaskID: "t1", ServerID: "s1", Status: "completed", StartedAt: now, Trigger: "startup"},
		&v2schema.TaskExecution{ID: "e2", TaskID: "t1", ServerID: "s1", Status: "failed", StartedAt: now, Trigger: "scheduled"},
		&v2schema.ModuleTemplate{
			ID: "builtin-mc-backup", Name: "MC Backup", Type: "builtin", DockerImage: "img/backup",
		},
		&v2schema.ModuleTemplate{
			ID: "builtin-rcon-web", Name: "RCON Web Admin", Type: "builtin", DockerImage: "img/rcon",
		},
		&v2schema.ModuleTemplate{
			ID: "builtin-geyser", Name: "Geyser", Type: "builtin", DockerImage: "img/geyser",
		},
		&v2schema.ModuleTemplate{
			ID: "tpl-custom", Name: "Custom Web", Type: "custom", DockerImage: "img/web",
			Ports: json.RawMessage(`[{"name":"web","container_port":4326,"host_port":0,"protocol":"http","proxy_enabled":true}]`),
		},
		&v2schema.Module{
			ID: "m1", Name: "backup", ServerID: "s1", TemplateID: "builtin-mc-backup",
			Status: "stopped", Config: `{"x":1}`, EnvOverrides: `{"A":"1"}`,
			TokenPlaintext: "sekrit",
			Ports:          json.RawMessage(`[{"name":"sync","container_port":9000,"host_port":0,"protocol":"udp"}]`),
		},
		&v2schema.IndexedModpack{ID: "cf-1", IndexerID: "1", Indexer: "fuego", Name: "Pack", JavaVersion: "17", DownloadCount: 5, DockerImage: "java17-alpine"},
		&v2schema.IndexedModpackFile{ID: "f-1", ModpackID: "cf-1", FileName: "pack.zip", ReleaseType: "beta"},
		&v2schema.ModpackFavorite{ID: "fav-1", ModpackID: "cf-1"},
		&v2schema.ProxyConfig{ID: "default", Enabled: true, BaseURL: "mc.example.com"},
		&v2schema.ProxyListener{ID: "l1", Port: 25565, Name: "Primary", Enabled: true, IsDefault: true},
		&v2schema.SystemSetting{Key: "recovery_key", Value: "abc"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	execs := []string{
		"INSERT INTO migrations (id) VALUES ('20260306_001_retry_backfill_user_roles')",
		"INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES ('p', 'user', 'server_config', 'read', '*')",
		"INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES ('p', 'user', 'servers', 'read', '*')",
		"INSERT INTO casbin_rule (ptype, v0, v1) VALUES ('g', 'u-admin', 'admin')",
	}
	for _, sql := range execs {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("seed sql: %v", err)
		}
	}
}

func ptr(s string) *string { return &s }

func ptrBool(b bool) *bool { return &b }

// Runs the real chain against a lived in v2 database
func TestIntakeCarriesV2Data(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, filepath.Join(dir, "v2.db"))
	if err := v2schema.Materialize(db); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	seedV2(t, db, dir)

	report, err := (&migrate.Engine{
		DB:       db,
		Registry: migrations.Registry,
		Head:     migrations.Head(),
		Baseline: migrations.V2Baseline{},
		Apply:    true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if len(report.Applied) != 1 || report.Applied[0] != "v2_intake" {
		t.Fatalf("unexpected applied %v", report.Applied)
	}

	// Landed exactly on the head schema
	d, _ := migrate.DialectByName("sqlite")
	headFP, _ := migrations.Head().Fingerprint(d)
	observed, err := migrate.SpecOfDB(db)
	if err != nil {
		t.Fatalf("spec of db: %v", err)
	}
	observedFP, _ := observed.Fingerprint(d)
	if observedFP != headFP {
		t.Fatal("intaken database does not match head")
	}

	// Enum columns hold proto numbers now
	assertInt(t, db, "SELECT mod_loader FROM servers WHERE id = 's1'", int64(v1.ModLoader_MOD_LOADER_FORGE))
	assertInt(t, db, "SELECT status FROM servers WHERE id = 's1'", int64(v1.ServerStatus_SERVER_STATUS_STOPPED))
	assertInt(t, db, "SELECT java_version FROM servers WHERE id = 's1'", 17)
	assertInt(t, db, "SELECT auth_provider FROM users WHERE id = 'u-sso'", int64(v1.AuthProvider_AUTH_PROVIDER_OIDC))
	assertInt(t, db, "SELECT source FROM user_roles WHERE id = 'ur-1'", int64(v1.RoleSource_ROLE_SOURCE_LOCAL))
	assertInt(t, db, `SELECT "trigger" FROM task_executions WHERE id = 'e1'`, int64(v1.TaskTrigger_TASK_TRIGGER_SCHEDULED))
	assertInt(t, db, "SELECT task_type FROM scheduled_tasks WHERE id = 't1'", int64(v1.TaskType_TASK_TYPE_COMMAND))
	assertInt(t, db, "SELECT release_type FROM indexed_modpack_files WHERE id = 'f-1'", int64(v1.ReleaseType_RELEASE_TYPE_BETA))
	assertInt(t, db, "SELECT java_version FROM indexed_modpacks WHERE id = 'cf-1'", 17)

	// Single hostname became a list
	assertString(t, db, "SELECT proxy_hostnames FROM servers WHERE id = 's1'", `["play.example.com"]`)

	// Itzg era tags translate or clear for resolution
	assertString(t, db, "SELECT docker_image FROM servers WHERE id = 's1'", "")
	assertString(t, db, "SELECT docker_image FROM servers WHERE id = 's2'", "java21-graal")
	assertString(t, db, "SELECT docker_image FROM servers WHERE id = 's3'", "java21")
	assertString(t, db, "SELECT docker_image FROM indexed_modpacks WHERE id = 'cf-1'", "")

	// Heap bounds derived and clamped to the container
	assertInt(t, db, "SELECT memory_min FROM servers WHERE id = 's1'", 2048)
	assertInt(t, db, "SELECT memory_max FROM servers WHERE id = 's1'", 4096)

	// Config row moved with its odd v2 column names
	assertString(t, db, "SELECT cf_api_key FROM server_properties WHERE id = 'c1'", "cf-key")
	assertString(t, db, "SELECT j_vm_opts FROM server_properties WHERE id = 'c1'", "-XX:+UseZGC")
	assertInt(t, db, "SELECT spawn_np_cs FROM server_properties WHERE id = 'c1'", 1)
	assertString(t, db, "SELECT init_memory FROM server_properties WHERE id = 'c1'", "2048M")
	assertString(t, db, "SELECT motd FROM server_properties WHERE id = 'global-settings'", "hello")

	// Task configs fanned into their typed columns
	assertString(t, db, "SELECT command_config FROM scheduled_tasks WHERE id = 't1'", `{"command":"say hi"}`)
	assertString(t, db, "SELECT backup_config FROM scheduled_tasks WHERE id = 't2'", `{"backup_name":"nightly","retention_days":7}`)

	// Port json now carries enum numbers
	var ports string
	if err := db.Raw("SELECT ports FROM modules WHERE id = 'm1'").Scan(&ports).Error; err != nil {
		t.Fatalf("read ports: %v", err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(ports), &parsed); err != nil {
		t.Fatalf("ports json: %v", err)
	}
	if len(parsed) != 1 || parsed[0]["protocol"] != float64(v1.ModuleProtocol_MODULE_PROTOCOL_UDP) {
		t.Fatalf("module ports not reshaped, %s", ports)
	}

	// Retired builtins wiped or kept as custom
	assertInt(t, db, "SELECT COUNT(*) FROM module_templates WHERE id = 'builtin-rcon-web'", 0)
	assertInt(t, db, "SELECT type FROM module_templates WHERE id = 'builtin-mc-backup'", int64(v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_CUSTOM))
	assertInt(t, db, "SELECT type FROM module_templates WHERE id = 'builtin-geyser'", int64(v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN))

	// Casbin rows follow the resource rename
	assertInt(t, db, "SELECT COUNT(*) FROM casbin_rule WHERE v1 = 'server_properties'", 1)
	assertInt(t, db, "SELECT COUNT(*) FROM casbin_rule WHERE v1 = 'server_config'", 0)

	// Orphans swept, secrets and old tables gone
	assertInt(t, db, "SELECT COUNT(*) FROM mods", 0)
	assertInt(t, db, "SELECT COUNT(*) FROM pragma_table_info('scheduled_tasks') WHERE name = 'config'", 0)
	assertInt(t, db, "SELECT COUNT(*) FROM pragma_table_info('modules') WHERE name = 'token_plaintext'", 0)
	if db.Migrator().HasTable("server_configs") {
		t.Fatal("server_configs survived the intake")
	}
	if db.Migrator().HasTable("migrations") {
		t.Fatal("gormigrate table survived the intake")
	}

	// Second run finds nothing left to do
	again, err := (&migrate.Engine{
		DB:       db,
		Registry: migrations.Registry,
		Head:     migrations.Head(),
		Baseline: migrations.V2Baseline{},
		Apply:    true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(again.Applied) != 0 {
		t.Fatalf("second run reapplied %v", again.Applied)
	}
}

// Fresh install and intake must produce one schema
func TestFreshInstallMatchesIntake(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "fresh.db"))
	report, err := (&migrate.Engine{
		DB:       db,
		Registry: migrations.Registry,
		Head:     migrations.Head(),
		Apply:    true,
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if !report.Fresh {
		t.Fatal("expected a fresh install")
	}
}

func assertInt(t *testing.T, db *gorm.DB, query string, want int64) {
	t.Helper()
	var got int64
	if err := db.Raw(query).Scan(&got).Error; err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func assertString(t *testing.T, db *gorm.DB, query string, want string) {
	t.Helper()
	var got string
	if err := db.Raw(query).Scan(&got).Error; err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", query, got, want)
	}
}
