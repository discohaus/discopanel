-- Schema of a v2.0.15 release database
-- Create "servers" table
CREATE TABLE `servers` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `mod_loader` text NOT NULL,
  `mc_version` text NOT NULL,
  `container_id` text NULL,
  `status` text NOT NULL,
  `port` integer NULL,
  `proxy_port` integer NULL,
  `proxy_hostname` text NULL,
  `proxy_listener_id` text NULL,
  `max_players` integer NULL DEFAULT 20,
  `memory` integer NULL DEFAULT 4096,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  `last_started` datetime NULL,
  `java_version` text NULL,
  `docker_image` text NULL,
  `data_path` text NOT NULL,
  `detached` numeric NULL DEFAULT false,
  `auto_start` numeric NULL DEFAULT false,
  `tps_command` text NULL,
  `additional_ports` text NULL,
  `docker_overrides` text NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_proxy_hostname_listener" to table: "servers"
CREATE UNIQUE INDEX `idx_proxy_hostname_listener` ON `servers` (`proxy_hostname`, `proxy_listener_id`) WHERE proxy_hostname != '';
-- Create "server_configs" table
CREATE TABLE `server_configs` (
  `id` text NULL,
  `server_id` text NOT NULL,
  `updated_at` datetime NULL,
  `uid` integer NULL,
  `g_id` integer NULL,
  `memory` text NULL,
  `init_memory` text NULL,
  `max_memory` text NULL,
  `tz` text NULL,
  `enable_rolling_logs` numeric NULL,
  `enable_jmx` numeric NULL,
  `jmx_host` text NULL,
  `use_aikar_flags` numeric NULL,
  `use_meowice_flags` numeric NULL,
  `use_meowice_graal_vm_flags` numeric NULL,
  `j_vm_opts` text NULL,
  `j_vm_xx_opts` text NULL,
  `j_vm_dd_opts` text NULL,
  `extra_args` text NULL,
  `log_timestamp` numeric NULL,
  `type` text NULL,
  `custom_server` text NULL,
  `custom_jar_exec` text NULL,
  `eula` text NULL,
  `version` text NULL,
  `motd` text NULL,
  `difficulty` text NULL,
  `icon` text NULL,
  `override_icon` numeric NULL,
  `max_players` integer NULL,
  `max_world_size` integer NULL,
  `allow_nether` numeric NULL,
  `announce_player_achievements` numeric NULL,
  `enable_command_block` numeric NULL,
  `force_gamemode` numeric NULL,
  `generate_structures` numeric NULL,
  `hardcore` numeric NULL,
  `snooper_enabled` numeric NULL,
  `max_build_height` integer NULL,
  `spawn_animals` numeric NULL,
  `spawn_monsters` numeric NULL,
  `spawn_np_cs` numeric NULL,
  `spawn_protection` integer NULL,
  `view_distance` integer NULL,
  `seed` text NULL,
  `mode` text NULL,
  `pvp` numeric NULL,
  `level_type` text NULL,
  `generator_settings` text NULL,
  `level` text NULL,
  `online_mode` numeric NULL,
  `allow_flight` numeric NULL,
  `server_name` text NULL,
  `server_port` integer NULL,
  `player_idle_timeout` integer NULL,
  `sync_chunk_writes` numeric NULL,
  `enable_status` numeric NULL,
  `entity_broadcast_range_percentage` integer NULL,
  `function_permission_level` integer NULL,
  `network_compression_threshold` integer NULL,
  `op_permission_level` integer NULL,
  `prevent_proxy_connections` numeric NULL,
  `use_native_transport` numeric NULL,
  `simulation_distance` integer NULL,
  `enable_query` numeric NULL,
  `query_port` integer NULL,
  `server_properties_escape_unicode` numeric NULL,
  `accepts_transfers` numeric NULL,
  `broadcast_console_to_ops` numeric NULL,
  `bug_report_link` text NULL,
  `enforce_secure_profile` numeric NULL,
  `hide_online_players` numeric NULL,
  `log_ips` numeric NULL,
  `max_chained_neighbor_updates` integer NULL,
  `pause_when_empty_seconds` integer NULL,
  `rate_limit` integer NULL,
  `region_file_compression` text NULL,
  `resource_pack_id` text NULL,
  `resource_pack_prompt` text NULL,
  `status_heartbeat_interval` integer NULL,
  `exec_directly` numeric NULL,
  `stop_server_announce_delay` integer NULL,
  `proxy` text NULL,
  `console` numeric NULL,
  `g_ui` numeric NULL,
  `stop_duration` integer NULL,
  `setup_only` numeric NULL,
  `use_flare_flags` numeric NULL,
  `use_simd_flags` numeric NULL,
  `custom_server_properties` text NULL,
  `resource_pack` text NULL,
  `resource_pack_sha1` text NULL,
  `resource_pack_enforce` numeric NULL,
  `management_server_allowed_origins` text NULL,
  `management_server_enabled` numeric NULL,
  `management_server_host` text NULL,
  `management_server_port` integer NULL,
  `management_server_secret` text NULL,
  `management_server_tls_enabled` numeric NULL,
  `management_server_tls_keystore` text NULL,
  `management_server_tls_keystore_password` text NULL,
  `user_api_provider` text NULL,
  `ops` text NULL,
  `ops_file` text NULL,
  `existing_ops_file` text NULL,
  `enable_whitelist` numeric NULL,
  `whitelist` text NULL,
  `whitelist_file` text NULL,
  `override_whitelist` numeric NULL,
  `existing_whitelist_file` text NULL,
  `enforce_whitelist` numeric NULL,
  `enable_rcon` numeric NULL,
  `rcon_password` text NULL,
  `rcon_port` integer NULL,
  `broadcast_rcon_to_ops` numeric NULL,
  `rcon_cmds_startup` text NULL,
  `rcon_cmds_on_connect` text NULL,
  `rcon_cmds_first_connect` text NULL,
  `rcon_cmds_on_disconnect` text NULL,
  `rcon_cmds_last_disconnect` text NULL,
  `enable_autopause` numeric NULL,
  `autopause_timeout_est` integer NULL,
  `autopause_timeout_init` integer NULL,
  `autopause_timeout_kn` integer NULL,
  `autopause_period` integer NULL,
  `autopause_knock_interface` text NULL,
  `debug_autopause` numeric NULL,
  `enable_autostop` numeric NULL,
  `autostop_timeout_est` integer NULL,
  `autostop_timeout_init` integer NULL,
  `autostop_period` integer NULL,
  `debug_autostop` numeric NULL,
  `forge_version` text NULL,
  `forge_installer` text NULL,
  `forge_installer_url` text NULL,
  `cf_api_key` text NULL,
  `cf_api_key_file` text NULL,
  `cf_page_url` text NULL,
  `cf_slug` text NULL,
  `cf_file_id` text NULL,
  `cf_modpack_zip` text NULL,
  `cf_filename_matcher` text NULL,
  `cf_exclude_include_file` text NULL,
  `cf_exclude_mods` text NULL,
  `cf_force_include_mods` text NULL,
  `cf_force_synchronize` numeric NULL,
  `cf_set_level_from` text NULL,
  `cf_parallel_downloads` integer NULL,
  `cf_overrides_skip_existing` numeric NULL,
  `cf_force_reinstall_modloader` numeric NULL,
  `modrinth_modpack` text NULL,
  `modrinth_modpack_version_type` text NULL,
  `modrinth_version` text NULL,
  `modrinth_loader` text NULL,
  `modrinth_ignore_missing_files` text NULL,
  `modrinth_exclude_files` text NULL,
  `modrinth_force_include_files` text NULL,
  `modrinth_force_synchronize` numeric NULL,
  `modrinth_default_exclude_includes` text NULL,
  `modrinth_overrides_exclusions` text NULL,
  `modrinth_projects` text NULL,
  `modrinth_download_dependencies` text NULL,
  `modrinth_projects_default_version_type` text NULL,
  `version_from_modrinth_projects` numeric NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_server_configs_server` FOREIGN KEY (`server_id`) REFERENCES `servers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_server_configs_server_id" to table: "server_configs"
CREATE INDEX `idx_server_configs_server_id` ON `server_configs` (`server_id`);
-- Create "mods" table
CREATE TABLE `mods` (
  `id` text NULL,
  `server_id` text NOT NULL,
  `name` text NOT NULL,
  `file_name` text NOT NULL,
  `version` text NULL,
  `mod_id` text NULL,
  `description` text NULL,
  `enabled` numeric NULL DEFAULT true,
  `uploaded_at` datetime NULL,
  `file_size` integer NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_mods_server` FOREIGN KEY (`server_id`) REFERENCES `servers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_mods_server_id" to table: "mods"
CREATE INDEX `idx_mods_server_id` ON `mods` (`server_id`);
-- Create "indexed_modpacks" table
CREATE TABLE `indexed_modpacks` (
  `id` text NULL,
  `indexer_id` text NULL,
  `indexer` text NULL,
  `name` text NOT NULL,
  `slug` text NULL,
  `summary` text NULL,
  `description` text NULL,
  `logo_url` text NULL,
  `website_url` text NULL,
  `download_count` integer NULL,
  `categories` text NULL,
  `game_versions` text NULL,
  `mod_loaders` text NULL,
  `latest_file_id` text NULL,
  `date_created` datetime NULL,
  `date_modified` datetime NULL,
  `date_released` datetime NULL,
  `updated_at` datetime NULL,
  `indexed_at` datetime NULL,
  `mc_version` text NULL,
  `java_version` text NULL,
  `docker_image` text NULL,
  `recommended_ram` integer NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_indexed_modpacks_slug" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_slug` ON `indexed_modpacks` (`slug`);
-- Create index "idx_indexed_modpacks_name" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_name` ON `indexed_modpacks` (`name`);
-- Create index "idx_indexed_modpacks_indexer" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_indexer` ON `indexed_modpacks` (`indexer`);
-- Create index "idx_indexed_modpacks_indexer_id" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_indexer_id` ON `indexed_modpacks` (`indexer_id`);
-- Create "indexed_modpack_files" table
CREATE TABLE `indexed_modpack_files` (
  `id` text NULL,
  `modpack_id` text NULL,
  `display_name` text NULL,
  `file_name` text NULL,
  `file_date` datetime NULL,
  `file_length` integer NULL,
  `release_type` text NULL,
  `download_url` text NULL,
  `game_versions` text NULL,
  `mod_loader` text NULL,
  `server_pack_file_id` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_indexed_modpack_files_modpack` FOREIGN KEY (`modpack_id`) REFERENCES `indexed_modpacks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_indexed_modpack_files_modpack_id" to table: "indexed_modpack_files"
CREATE INDEX `idx_indexed_modpack_files_modpack_id` ON `indexed_modpack_files` (`modpack_id`);
-- Create "modpack_favorites" table
CREATE TABLE `modpack_favorites` (
  `id` text NULL,
  `modpack_id` text NULL,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_modpack_favorites_modpack` FOREIGN KEY (`modpack_id`) REFERENCES `indexed_modpacks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_modpack_favorites_modpack_id" to table: "modpack_favorites"
CREATE INDEX `idx_modpack_favorites_modpack_id` ON `modpack_favorites` (`modpack_id`);
-- Create "proxy_configs" table
CREATE TABLE `proxy_configs` (
  `id` text NULL,
  `enabled` numeric NOT NULL DEFAULT false,
  `base_url` text NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create "proxy_listeners" table
CREATE TABLE `proxy_listeners` (
  `id` text NULL,
  `port` integer NOT NULL,
  `name` text NULL,
  `description` text NULL,
  `enabled` numeric NOT NULL DEFAULT true,
  `is_default` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_proxy_listeners_port" to table: "proxy_listeners"
CREATE UNIQUE INDEX `idx_proxy_listeners_port` ON `proxy_listeners` (`port`);
-- Create "users" table
CREATE TABLE `users` (
  `id` text NULL,
  `username` text NOT NULL,
  `email` text NULL,
  `password_hash` text NULL,
  `auth_provider` text NOT NULL DEFAULT 'local',
  `oidc_subject` text NULL,
  `oidc_issuer` text NULL,
  `is_active` numeric NOT NULL DEFAULT true,
  `last_login` datetime NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_oidc_identity" to table: "users"
CREATE UNIQUE INDEX `idx_oidc_identity` ON `users` (`oidc_subject`, `oidc_issuer`) WHERE oidc_subject != '';
-- Create index "idx_users_email" to table: "users"
CREATE INDEX `idx_users_email` ON `users` (`email`);
-- Create index "idx_user_provider" to table: "users"
CREATE UNIQUE INDEX `idx_user_provider` ON `users` (`username`, `auth_provider`);
-- Create "roles" table
CREATE TABLE `roles` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `is_system` numeric NOT NULL DEFAULT false,
  `is_default` numeric NOT NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_roles_name" to table: "roles"
CREATE UNIQUE INDEX `idx_roles_name` ON `roles` (`name`);
-- Create "user_roles" table
CREATE TABLE `user_roles` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `role_name` text NOT NULL,
  `source` text NOT NULL DEFAULT 'local',
  `created_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_user_roles_role_name" to table: "user_roles"
CREATE INDEX `idx_user_roles_role_name` ON `user_roles` (`role_name`);
-- Create index "idx_user_roles_user_id" to table: "user_roles"
CREATE INDEX `idx_user_roles_user_id` ON `user_roles` (`user_id`);
-- Create "sessions" table
CREATE TABLE `sessions` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `token` text NOT NULL,
  `expires_at` datetime NOT NULL,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_sessions_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_sessions_expires_at" to table: "sessions"
CREATE INDEX `idx_sessions_expires_at` ON `sessions` (`expires_at`);
-- Create index "idx_sessions_token" to table: "sessions"
CREATE UNIQUE INDEX `idx_sessions_token` ON `sessions` (`token`);
-- Create index "idx_sessions_user_id" to table: "sessions"
CREATE INDEX `idx_sessions_user_id` ON `sessions` (`user_id`);
-- Create "api_tokens" table
CREATE TABLE `api_tokens` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `name` text NOT NULL,
  `token_hash` text NOT NULL,
  `expires_at` datetime NULL,
  `last_used_at` datetime NULL,
  `is_module_token` numeric NULL DEFAULT false,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_api_tokens_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_api_tokens_token_hash" to table: "api_tokens"
CREATE UNIQUE INDEX `idx_api_tokens_token_hash` ON `api_tokens` (`token_hash`);
-- Create index "idx_api_tokens_user_id" to table: "api_tokens"
CREATE INDEX `idx_api_tokens_user_id` ON `api_tokens` (`user_id`);
-- Create "registration_invites" table
CREATE TABLE `registration_invites` (
  `id` text NULL,
  `code` text NOT NULL,
  `description` text NULL,
  `roles` text NULL,
  `pin_hash` text NULL,
  `max_uses` integer NULL DEFAULT 0,
  `use_count` integer NULL DEFAULT 0,
  `expires_at` datetime NULL,
  `created_by` text NOT NULL,
  `created_at` datetime NULL,
  PRIMARY KEY (`id`)
);
-- Create index "idx_registration_invites_code" to table: "registration_invites"
CREATE UNIQUE INDEX `idx_registration_invites_code` ON `registration_invites` (`code`);
-- Create "scheduled_tasks" table
CREATE TABLE `scheduled_tasks` (
  `id` text NULL,
  `server_id` text NOT NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `task_type` text NOT NULL,
  `status` text NOT NULL DEFAULT 'enabled',
  `schedule` text NOT NULL,
  `cron_expr` text NULL,
  `interval_secs` integer NULL,
  `run_at` datetime NULL,
  `event_triggers` text NULL,
  `next_run` datetime NULL,
  `last_run` datetime NULL,
  `timezone` text NULL DEFAULT 'UTC',
  `config` text NULL,
  `timeout` integer NULL DEFAULT 300,
  `retry_count` integer NULL DEFAULT 0,
  `retry_delay` integer NULL DEFAULT 60,
  `require_online` numeric NULL DEFAULT true,
  `failure_notify` numeric NULL DEFAULT false,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_scheduled_tasks_server` FOREIGN KEY (`server_id`) REFERENCES `servers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_scheduled_tasks_next_run" to table: "scheduled_tasks"
CREATE INDEX `idx_scheduled_tasks_next_run` ON `scheduled_tasks` (`next_run`);
-- Create index "idx_scheduled_tasks_server_id" to table: "scheduled_tasks"
CREATE INDEX `idx_scheduled_tasks_server_id` ON `scheduled_tasks` (`server_id`);
-- Create "task_executions" table
CREATE TABLE `task_executions` (
  `id` text NULL,
  `task_id` text NOT NULL,
  `server_id` text NOT NULL,
  `status` text NOT NULL,
  `started_at` datetime NOT NULL,
  `ended_at` datetime NULL,
  `duration` integer NULL DEFAULT 0,
  `output` text NULL,
  `error` text NULL,
  `retry_num` integer NULL DEFAULT 0,
  `trigger` text NULL DEFAULT 'scheduled',
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_task_executions_server` FOREIGN KEY (`server_id`) REFERENCES `servers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE,
  CONSTRAINT `fk_task_executions_task` FOREIGN KEY (`task_id`) REFERENCES `scheduled_tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_task_executions_server_id" to table: "task_executions"
CREATE INDEX `idx_task_executions_server_id` ON `task_executions` (`server_id`);
-- Create index "idx_task_executions_task_id" to table: "task_executions"
CREATE INDEX `idx_task_executions_task_id` ON `task_executions` (`task_id`);
-- Create "module_templates" table
CREATE TABLE `module_templates` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `type` text NOT NULL DEFAULT 'custom',
  `docker_image` text NOT NULL,
  `default_env` text NULL,
  `default_volumes` text NULL,
  `health_check_path` text NULL,
  `health_check_port` integer NULL,
  `requires_server` numeric NULL DEFAULT true,
  `supports_proxy` numeric NULL DEFAULT true,
  `icon` text NULL,
  `category` text NULL,
  `documentation` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  `ports` text NULL,
  `suggested_dependencies` text NULL,
  `default_hooks` text NULL,
  `metadata` text NULL,
  `default_cmd` text NULL,
  `default_access_urls` text NULL,
  `default_memory` integer NULL DEFAULT 512,
  `default_uid` text NULL DEFAULT '',
  `default_gid` text NULL DEFAULT '',
  `default_init_command` text NULL DEFAULT '',
  `default_init_command_delay` integer NULL DEFAULT 0,
  `default_restart_after_init` numeric NULL DEFAULT false,
  PRIMARY KEY (`id`)
);
-- Create index "idx_module_templates_name" to table: "module_templates"
CREATE UNIQUE INDEX `idx_module_templates_name` ON `module_templates` (`name`);
-- Create "modules" table
CREATE TABLE `modules` (
  `id` text NULL,
  `name` text NOT NULL,
  `server_id` text NOT NULL,
  `template_id` text NOT NULL,
  `container_id` text NULL,
  `status` text NOT NULL DEFAULT 'stopped',
  `config` text NULL,
  `env_overrides` text NULL,
  `volume_overrides` text NULL,
  `memory` integer NULL DEFAULT 512,
  `cpu_limit` real NULL,
  `uid` text NULL DEFAULT '',
  `gid` text NULL DEFAULT '',
  `auto_start` numeric NULL DEFAULT false,
  `follow_server_lifecycle` numeric NULL DEFAULT true,
  `detached` numeric NULL DEFAULT false,
  `data_path` text NULL,
  `created_at` datetime NULL,
  `updated_at` datetime NULL,
  `last_started` datetime NULL,
  `ports` text NULL,
  `dependencies` text NULL,
  `health_check_interval` integer NULL DEFAULT 30,
  `health_check_timeout` integer NULL DEFAULT 5,
  `health_check_retries` integer NULL DEFAULT 3,
  `event_hooks` text NULL,
  `metadata` text NULL,
  `cmd_override` text NULL,
  `init_command` text NULL DEFAULT '',
  `init_command_delay` integer NULL DEFAULT 0,
  `restart_after_init` numeric NULL DEFAULT false,
  `access_urls` text NULL,
  `created_by` text NULL,
  `token_id` text NULL,
  `token_plaintext` text NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_modules_template` FOREIGN KEY (`template_id`) REFERENCES `module_templates` (`id`) ON UPDATE NO ACTION ON DELETE RESTRICT,
  CONSTRAINT `fk_modules_server` FOREIGN KEY (`server_id`) REFERENCES `servers` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create index "idx_modules_template_id" to table: "modules"
CREATE INDEX `idx_modules_template_id` ON `modules` (`template_id`);
-- Create index "idx_modules_server_id" to table: "modules"
CREATE INDEX `idx_modules_server_id` ON `modules` (`server_id`);
-- Create "system_settings" table
CREATE TABLE `system_settings` (
  `key` text NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`key`)
);
-- Create "migrations" table
CREATE TABLE `migrations` (
  `id` text NULL,
  PRIMARY KEY (`id`)
);
-- Create "casbin_rule" table
CREATE TABLE `casbin_rule` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `ptype` text NULL,
  `v0` text NULL,
  `v1` text NULL,
  `v2` text NULL,
  `v3` text NULL,
  `v4` text NULL,
  `v5` text NULL
);
-- Create index "idx_casbin_rule" to table: "casbin_rule"
CREATE UNIQUE INDEX `idx_casbin_rule` ON `casbin_rule` (`ptype`, `v0`, `v1`, `v2`, `v3`, `v4`, `v5`);
