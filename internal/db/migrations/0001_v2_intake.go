// Carries any pre framework database onto the v3 schema
package migrations

import "github.com/nickheyer/protogorm/migrate"

func init() {
	target := mustSnapshot("0001_v2_intake.snapshot.json")
	Registry.MustAdd(&migrate.Migration{
		Ordinal: 1,
		Name:    "v2_intake",
		Target:  target,
		Ops: []migrate.Op{
			// Older databases first become exactly the genesis schema
			enterGenesis(),
			migrate.Transform{Name: "user_roles_backfill", Fn: backfillUserRoles},

			// Gormigrate bookkeeping dies with v2
			migrate.Exec{SQL: []string{"DROP TABLE IF EXISTS migrations"}},
			migrate.Transform{Name: "sweep_orphans", Fn: sweepOrphans},

			// Servers normalize, reshape, then backfill
			migrate.Transform{Name: "servers_normalize", Fn: normalizeServers},
			migrate.TableChange{
				Table:    target.Table("servers"),
				Adds:     []string{"memory_min", "memory_max", "proxy_catch_all", "runtime_digest", "agent_token_hash", "icon_source"},
				Drops:    []string{"proxy_port", "tps_command"},
				Renames:  map[string]string{"proxy_hostnames": "proxy_hostname"},
				Modifies: []string{"mod_loader", "status", "java_version"},
			},
			migrate.Transform{Name: "servers_backfill", Fn: backfillServers},

			// Identity tables trade strings for enums
			migrate.Transform{Name: "users_normalize", Fn: normalizeUsers},
			migrate.TableChange{
				Table:    target.Table("users"),
				Modifies: []string{"auth_provider"},
			},
			migrate.Transform{Name: "user_roles_normalize", Fn: normalizeUserRoles},
			migrate.TableChange{
				Table:    target.Table("user_roles"),
				Modifies: []string{"source"},
			},
			migrate.TableChange{
				Table: target.Table("api_tokens"),
				Adds:  []string{"module_role"},
			},

			// Proxy tables grow their v3 switches
			migrate.TableChange{
				Table: target.Table("proxy_configs"),
				Adds:  []string{"hostnames", "catch_all", "lobby", "lobby_online"},
			},
			migrate.TableChange{
				Table: target.Table("proxy_listeners"),
				Adds:  []string{"auto_created"},
			},

			// Mods lose their description column
			migrate.TableChange{
				Table: target.Table("mods"),
				Adds:  []string{"updated_at"},
				Drops: []string{"description"},
			},

			// Modpack columns tighten their types
			migrate.Transform{Name: "modpacks_normalize", Fn: normalizeModpacks},
			migrate.TableChange{
				Table:    target.Table("indexed_modpacks"),
				Modifies: []string{"java_version", "download_count"},
			},
			migrate.Transform{Name: "modpack_files_normalize", Fn: normalizeModpackFiles},
			migrate.TableChange{
				Table:    target.Table("indexed_modpack_files"),
				Modifies: []string{"release_type"},
			},

			// Typed config columns land before the fan out
			migrate.TableChange{
				Table: target.Table("scheduled_tasks"),
				Adds:  []string{"command_config", "backup_config", "script_config", "webhook_config"},
			},
			migrate.Transform{Name: "tasks_normalize", Fn: normalizeTasks},
			migrate.TableChange{
				Table:    target.Table("scheduled_tasks"),
				Drops:    []string{"config"},
				Modifies: []string{"task_type", "status", "schedule"},
			},
			migrate.Transform{Name: "executions_normalize", Fn: normalizeExecutions},
			migrate.TableChange{
				Table:    target.Table("task_executions"),
				Modifies: []string{"status", "trigger"},
			},

			// Module tables retire dead builtins and secrets
			migrate.Transform{Name: "templates_normalize", Fn: normalizeTemplates},
			migrate.TableChange{
				Table:    target.Table("module_templates"),
				Adds:     []string{"config_fields", "default_security_opt", "global", "cert_mount_path"},
				Modifies: []string{"type"},
			},
			migrate.Transform{Name: "modules_normalize", Fn: normalizeModules},
			migrate.TableChange{
				Table:    target.Table("modules"),
				Adds:     []string{"cert_pem", "key_pem"},
				Drops:    []string{"config", "token_plaintext", "data_path"},
				Modifies: []string{"status"},
			},

			// Server configs become server properties
			migrate.CreateTable{Table: target.Table("server_properties")},
			migrate.Transform{Name: "server_configs_copy", Fn: copyServerConfigs(target)},
			migrate.Exec{SQL: []string{"DROP TABLE server_configs"}},

			// Casbin rows follow the resource rename
			migrate.Exec{SQL: []string{
				"UPDATE casbin_rule SET v1 = 'server_properties' WHERE v1 = 'server_config'",
			}},

			// Tables new in v3
			migrate.CreateTable{Table: target.Table("server_actions")},
			migrate.CreateTable{Table: target.Table("metrics_samples")},
			migrate.CreateTable{Table: target.Table("finding_dismissals")},

			// Whatever the explicit ops missed settles last
			conformToTarget("conform_target", target),
		},
	})
}
