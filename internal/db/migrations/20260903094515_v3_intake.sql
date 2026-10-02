-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_servers" table
CREATE TABLE `new_servers` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `mod_loader` integer NOT NULL,
  `mc_version` text NOT NULL,
  `status` integer NOT NULL,
  `port` integer NULL,
  `proxy_hostnames` text NULL,
  `proxy_listener_id` text NULL,
  `proxy_catch_all` numeric NULL DEFAULT false,
  `max_players` integer NULL DEFAULT 20,
  `memory` integer NULL DEFAULT 4096,
  `memory_min` integer NULL DEFAULT 0,
  `memory_max` integer NULL DEFAULT 0,
  `data_path` text NOT NULL,
  `container_id` text NULL,
  `last_started` time NULL,
  `java_version` integer NULL,
  `docker_image` text NULL,
  `auto_start` numeric NULL DEFAULT false,
  `detached` numeric NULL DEFAULT false,
  `additional_ports` text NULL,
  `docker_overrides` text NULL,
  `created_at` time NULL,
  `updated_at` time NULL,
  `runtime_digest` text NULL,
  `agent_token_hash` text NULL,
  `icon_source` integer NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "servers" to new temporary table "new_servers"
INSERT INTO `new_servers` (`id`, `name`, `description`, `mod_loader`, `mc_version`, `status`, `port`, `proxy_hostnames`, `proxy_listener_id`, `max_players`, `memory`, `memory_min`, `memory_max`, `data_path`, `container_id`, `last_started`, `java_version`, `docker_image`, `auto_start`, `detached`, `additional_ports`, `docker_overrides`, `created_at`, `updated_at`, `icon_source`) SELECT `id`, `name`, `description`, `mod_loader`, `mc_version`, `status`, `port`, `proxy_hostnames`, `proxy_listener_id`, `max_players`, `memory`, `memory_min`, `memory_max`, `data_path`, `container_id`, `last_started`, `java_version`, `docker_image`, `auto_start`, `detached`, `additional_ports`, `docker_overrides`, `created_at`, `updated_at`, `icon_source` FROM `servers`;
-- Drop "servers" table after copying rows
DROP TABLE `servers`;
-- Rename temporary table "new_servers" to "servers"
ALTER TABLE `new_servers` RENAME TO `servers`;
-- Drop "server_configs" table
DROP TABLE `server_configs`;
-- Create "new_mods" table
CREATE TABLE `new_mods` (
  `id` text NULL,
  `server_id` text NOT NULL,
  `file_name` text NOT NULL,
  `name` text NOT NULL,
  `version` text NULL,
  `mod_id` text NULL,
  `file_size` integer NULL,
  `uploaded_at` time NULL,
  `updated_at` time NULL,
  `enabled` numeric NULL DEFAULT true,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "mods" to new temporary table "new_mods"
INSERT INTO `new_mods` (`id`, `server_id`, `file_name`, `name`, `version`, `mod_id`, `file_size`, `uploaded_at`, `enabled`) SELECT `id`, `server_id`, `file_name`, `name`, `version`, `mod_id`, `file_size`, `uploaded_at`, `enabled` FROM `mods`;
-- Drop "mods" table after copying rows
DROP TABLE `mods`;
-- Rename temporary table "new_mods" to "mods"
ALTER TABLE `new_mods` RENAME TO `mods`;
-- Create index "idx_mods_server_id" to table: "mods"
CREATE INDEX `idx_mods_server_id` ON `mods` (`server_id`);
-- Create "new_indexed_modpacks" table
CREATE TABLE `new_indexed_modpacks` (
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
  `date_created` time NULL,
  `date_modified` time NULL,
  `date_released` time NULL,
  `mc_version` text NULL,
  `java_version` integer NULL,
  `docker_image` text NULL,
  `recommended_ram` integer NULL,
  `updated_at` time NULL,
  `indexed_at` time NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "indexed_modpacks" to new temporary table "new_indexed_modpacks"
INSERT INTO `new_indexed_modpacks` (`id`, `indexer_id`, `indexer`, `name`, `slug`, `summary`, `description`, `logo_url`, `website_url`, `download_count`, `categories`, `game_versions`, `mod_loaders`, `latest_file_id`, `date_created`, `date_modified`, `date_released`, `mc_version`, `java_version`, `docker_image`, `recommended_ram`, `updated_at`, `indexed_at`) SELECT `id`, `indexer_id`, `indexer`, `name`, `slug`, `summary`, `description`, `logo_url`, `website_url`, `download_count`, `categories`, `game_versions`, `mod_loaders`, `latest_file_id`, `date_created`, `date_modified`, `date_released`, `mc_version`, `java_version`, `docker_image`, `recommended_ram`, `updated_at`, `indexed_at` FROM `indexed_modpacks`;
-- Drop "indexed_modpacks" table after copying rows
DROP TABLE `indexed_modpacks`;
-- Rename temporary table "new_indexed_modpacks" to "indexed_modpacks"
ALTER TABLE `new_indexed_modpacks` RENAME TO `indexed_modpacks`;
-- Create index "idx_indexed_modpacks_slug" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_slug` ON `indexed_modpacks` (`slug`);
-- Create index "idx_indexed_modpacks_name" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_name` ON `indexed_modpacks` (`name`);
-- Create index "idx_indexed_modpacks_indexer" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_indexer` ON `indexed_modpacks` (`indexer`);
-- Create index "idx_indexed_modpacks_indexer_id" to table: "indexed_modpacks"
CREATE INDEX `idx_indexed_modpacks_indexer_id` ON `indexed_modpacks` (`indexer_id`);
-- Create "new_indexed_modpack_files" table
CREATE TABLE `new_indexed_modpack_files` (
  `id` text NULL,
  `modpack_id` text NULL,
  `display_name` text NULL,
  `file_name` text NULL,
  `file_date` time NULL,
  `file_length` integer NULL,
  `release_type` integer NULL,
  `download_url` text NULL,
  `game_versions` text NULL,
  `mod_loader` text NULL,
  `server_pack_file_id` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "indexed_modpack_files" to new temporary table "new_indexed_modpack_files"
INSERT INTO `new_indexed_modpack_files` (`id`, `modpack_id`, `display_name`, `file_name`, `file_date`, `file_length`, `release_type`, `download_url`, `game_versions`, `mod_loader`, `server_pack_file_id`) SELECT `id`, `modpack_id`, `display_name`, `file_name`, `file_date`, `file_length`, `release_type`, `download_url`, `game_versions`, `mod_loader`, `server_pack_file_id` FROM `indexed_modpack_files`;
-- Drop "indexed_modpack_files" table after copying rows
DROP TABLE `indexed_modpack_files`;
-- Rename temporary table "new_indexed_modpack_files" to "indexed_modpack_files"
ALTER TABLE `new_indexed_modpack_files` RENAME TO `indexed_modpack_files`;
-- Create index "idx_indexed_modpack_files_modpack_id" to table: "indexed_modpack_files"
CREATE INDEX `idx_indexed_modpack_files_modpack_id` ON `indexed_modpack_files` (`modpack_id`);
-- Create "new_modpack_favorites" table
CREATE TABLE `new_modpack_favorites` (
  `id` text NULL,
  `modpack_id` text NULL,
  `created_at` time NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "modpack_favorites" to new temporary table "new_modpack_favorites"
INSERT INTO `new_modpack_favorites` (`id`, `modpack_id`, `created_at`) SELECT `id`, `modpack_id`, `created_at` FROM `modpack_favorites`;
-- Drop "modpack_favorites" table after copying rows
DROP TABLE `modpack_favorites`;
-- Rename temporary table "new_modpack_favorites" to "modpack_favorites"
ALTER TABLE `new_modpack_favorites` RENAME TO `modpack_favorites`;
-- Create index "idx_modpack_favorites_modpack_id" to table: "modpack_favorites"
CREATE INDEX `idx_modpack_favorites_modpack_id` ON `modpack_favorites` (`modpack_id`);
-- Add column "hostnames" to table: "proxy_configs"
ALTER TABLE `proxy_configs` ADD COLUMN `hostnames` text NULL;
-- Add column "catch_all" to table: "proxy_configs"
ALTER TABLE `proxy_configs` ADD COLUMN `catch_all` numeric NOT NULL DEFAULT false;
-- Add column "lobby" to table: "proxy_configs"
ALTER TABLE `proxy_configs` ADD COLUMN `lobby` numeric NOT NULL DEFAULT false;
-- Add column "lobby_online" to table: "proxy_configs"
ALTER TABLE `proxy_configs` ADD COLUMN `lobby_online` numeric NOT NULL DEFAULT true;
-- Add column "auto_created" to table: "proxy_listeners"
ALTER TABLE `proxy_listeners` ADD COLUMN `auto_created` numeric NOT NULL DEFAULT false;
-- Create "new_users" table
CREATE TABLE `new_users` (
  `id` text NULL,
  `username` text NOT NULL,
  `email` text NULL,
  `auth_provider` integer NOT NULL DEFAULT 1,
  `is_active` numeric NOT NULL DEFAULT true,
  `created_at` time NULL,
  `updated_at` time NULL,
  `last_login` time NULL,
  `password_hash` text NULL,
  `oidc_subject` text NULL,
  `oidc_issuer` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "users" to new temporary table "new_users"
INSERT INTO `new_users` (`id`, `username`, `email`, `auth_provider`, `is_active`, `created_at`, `updated_at`, `last_login`, `password_hash`, `oidc_subject`, `oidc_issuer`) SELECT `id`, `username`, `email`, IFNULL(`auth_provider`, 1) AS `auth_provider`, `is_active`, `created_at`, `updated_at`, `last_login`, `password_hash`, `oidc_subject`, `oidc_issuer` FROM `users`;
-- Drop "users" table after copying rows
DROP TABLE `users`;
-- Rename temporary table "new_users" to "users"
ALTER TABLE `new_users` RENAME TO `users`;
-- Create index "idx_oidc_identity" to table: "users"
CREATE UNIQUE INDEX `idx_oidc_identity` ON `users` (`oidc_subject`, `oidc_issuer`) WHERE oidc_subject != '';
-- Create index "idx_users_email" to table: "users"
CREATE INDEX `idx_users_email` ON `users` (`email`);
-- Create index "idx_user_provider" to table: "users"
CREATE UNIQUE INDEX `idx_user_provider` ON `users` (`username`, `auth_provider`);
-- Create "new_user_roles" table
CREATE TABLE `new_user_roles` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `role_name` text NOT NULL,
  `source` integer NOT NULL DEFAULT 1,
  `created_at` time NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "user_roles" to new temporary table "new_user_roles"
INSERT INTO `new_user_roles` (`id`, `user_id`, `role_name`, `source`, `created_at`) SELECT `id`, `user_id`, `role_name`, IFNULL(`source`, 1) AS `source`, `created_at` FROM `user_roles`;
-- Drop "user_roles" table after copying rows
DROP TABLE `user_roles`;
-- Rename temporary table "new_user_roles" to "user_roles"
ALTER TABLE `new_user_roles` RENAME TO `user_roles`;
-- Create index "idx_user_roles_role_name" to table: "user_roles"
CREATE INDEX `idx_user_roles_role_name` ON `user_roles` (`role_name`);
-- Create index "idx_user_roles_user_id" to table: "user_roles"
CREATE INDEX `idx_user_roles_user_id` ON `user_roles` (`user_id`);
-- Create "new_sessions" table
CREATE TABLE `new_sessions` (
  `id` text NULL,
  `user_id` text NOT NULL,
  `token` text NOT NULL,
  `expires_at` time NOT NULL,
  `created_at` time NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "sessions" to new temporary table "new_sessions"
INSERT INTO `new_sessions` (`id`, `user_id`, `token`, `expires_at`, `created_at`) SELECT `id`, `user_id`, `token`, `expires_at`, `created_at` FROM `sessions`;
-- Drop "sessions" table after copying rows
DROP TABLE `sessions`;
-- Rename temporary table "new_sessions" to "sessions"
ALTER TABLE `new_sessions` RENAME TO `sessions`;
-- Create index "idx_sessions_expires_at" to table: "sessions"
CREATE INDEX `idx_sessions_expires_at` ON `sessions` (`expires_at`);
-- Create index "idx_sessions_token" to table: "sessions"
CREATE UNIQUE INDEX `idx_sessions_token` ON `sessions` (`token`);
-- Create index "idx_sessions_user_id" to table: "sessions"
CREATE INDEX `idx_sessions_user_id` ON `sessions` (`user_id`);
-- Create "new_api_tokens" table
CREATE TABLE `new_api_tokens` (
  `id` text NULL,
  `name` text NOT NULL,
  `expires_at` time NULL,
  `last_used_at` time NULL,
  `created_at` time NULL,
  `user_id` text NOT NULL,
  `token_hash` text NOT NULL,
  `is_module_token` numeric NULL DEFAULT false,
  `module_role` text NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "api_tokens" to new temporary table "new_api_tokens"
INSERT INTO `new_api_tokens` (`id`, `name`, `expires_at`, `last_used_at`, `created_at`, `user_id`, `token_hash`, `is_module_token`) SELECT `id`, `name`, `expires_at`, `last_used_at`, `created_at`, `user_id`, `token_hash`, `is_module_token` FROM `api_tokens`;
-- Drop "api_tokens" table after copying rows
DROP TABLE `api_tokens`;
-- Rename temporary table "new_api_tokens" to "api_tokens"
ALTER TABLE `new_api_tokens` RENAME TO `api_tokens`;
-- Create index "idx_api_tokens_token_hash" to table: "api_tokens"
CREATE UNIQUE INDEX `idx_api_tokens_token_hash` ON `api_tokens` (`token_hash`);
-- Create index "idx_api_tokens_user_id" to table: "api_tokens"
CREATE INDEX `idx_api_tokens_user_id` ON `api_tokens` (`user_id`);
-- Create "new_scheduled_tasks" table
CREATE TABLE `new_scheduled_tasks` (
  `id` text NULL,
  `server_id` text NOT NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `task_type` integer NOT NULL,
  `status` integer NOT NULL DEFAULT 1,
  `schedule` integer NOT NULL,
  `cron_expr` text NULL,
  `interval_secs` integer NULL,
  `run_at` time NULL,
  `next_run` time NULL,
  `last_run` time NULL,
  `timezone` text NULL DEFAULT 'UTC',
  `command_config` text NULL,
  `backup_config` text NULL,
  `script_config` text NULL,
  `webhook_config` text NULL,
  `timeout` integer NULL DEFAULT 300,
  `retry_count` integer NULL DEFAULT 0,
  `retry_delay` integer NULL DEFAULT 60,
  `require_online` numeric NULL DEFAULT true,
  `failure_notify` numeric NULL DEFAULT false,
  `created_at` time NULL,
  `updated_at` time NULL,
  `event_triggers` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "scheduled_tasks" to new temporary table "new_scheduled_tasks"
INSERT INTO `new_scheduled_tasks` (`id`, `server_id`, `name`, `description`, `task_type`, `status`, `schedule`, `cron_expr`, `interval_secs`, `run_at`, `next_run`, `last_run`, `timezone`, `command_config`, `backup_config`, `script_config`, `webhook_config`, `timeout`, `retry_count`, `retry_delay`, `require_online`, `failure_notify`, `created_at`, `updated_at`, `event_triggers`) SELECT `id`, `server_id`, `name`, `description`, `task_type`, IFNULL(`status`, 1) AS `status`, `schedule`, `cron_expr`, `interval_secs`, `run_at`, `next_run`, `last_run`, `timezone`, `command_config`, `backup_config`, `script_config`, `webhook_config`, `timeout`, `retry_count`, `retry_delay`, `require_online`, `failure_notify`, `created_at`, `updated_at`, `event_triggers` FROM `scheduled_tasks`;
-- Drop "scheduled_tasks" table after copying rows
DROP TABLE `scheduled_tasks`;
-- Rename temporary table "new_scheduled_tasks" to "scheduled_tasks"
ALTER TABLE `new_scheduled_tasks` RENAME TO `scheduled_tasks`;
-- Create index "idx_scheduled_tasks_next_run" to table: "scheduled_tasks"
CREATE INDEX `idx_scheduled_tasks_next_run` ON `scheduled_tasks` (`next_run`);
-- Create index "idx_scheduled_tasks_server_id" to table: "scheduled_tasks"
CREATE INDEX `idx_scheduled_tasks_server_id` ON `scheduled_tasks` (`server_id`);
-- Create "new_task_executions" table
CREATE TABLE `new_task_executions` (
  `id` text NULL,
  `task_id` text NOT NULL,
  `server_id` text NOT NULL,
  `status` integer NOT NULL,
  `started_at` time NOT NULL,
  `ended_at` time NULL,
  `duration` integer NULL DEFAULT 0,
  `output` text NULL,
  `error` text NULL,
  `retry_num` integer NULL DEFAULT 0,
  `trigger` integer NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "task_executions" to new temporary table "new_task_executions"
INSERT INTO `new_task_executions` (`id`, `task_id`, `server_id`, `status`, `started_at`, `ended_at`, `duration`, `output`, `error`, `retry_num`, `trigger`) SELECT `id`, `task_id`, `server_id`, `status`, `started_at`, `ended_at`, `duration`, `output`, `error`, `retry_num`, IFNULL(`trigger`, 1) AS `trigger` FROM `task_executions`;
-- Drop "task_executions" table after copying rows
DROP TABLE `task_executions`;
-- Rename temporary table "new_task_executions" to "task_executions"
ALTER TABLE `new_task_executions` RENAME TO `task_executions`;
-- Create index "idx_task_executions_server_id" to table: "task_executions"
CREATE INDEX `idx_task_executions_server_id` ON `task_executions` (`server_id`);
-- Create index "idx_task_executions_task_id" to table: "task_executions"
CREATE INDEX `idx_task_executions_task_id` ON `task_executions` (`task_id`);
-- Create "new_module_templates" table
CREATE TABLE `new_module_templates` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NULL,
  `type` integer NOT NULL DEFAULT 2,
  `docker_image` text NOT NULL,
  `config_fields` text NULL,
  `default_env` text NULL,
  `default_volumes` text NULL,
  `health_check_path` text NULL,
  `health_check_port` integer NULL,
  `requires_server` numeric NULL DEFAULT true,
  `supports_proxy` numeric NULL DEFAULT true,
  `icon` text NULL,
  `category` text NULL,
  `documentation` text NULL,
  `created_at` time NULL,
  `updated_at` time NULL,
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
  `default_security_opt` text NULL,
  `global` numeric NULL DEFAULT false,
  `cert_mount_path` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "module_templates" to new temporary table "new_module_templates"
INSERT INTO `new_module_templates` (`id`, `name`, `description`, `type`, `docker_image`, `default_env`, `default_volumes`, `health_check_path`, `health_check_port`, `requires_server`, `supports_proxy`, `icon`, `category`, `documentation`, `created_at`, `updated_at`, `ports`, `suggested_dependencies`, `default_hooks`, `metadata`, `default_cmd`, `default_access_urls`, `default_memory`, `default_uid`, `default_gid`, `default_init_command`, `default_init_command_delay`, `default_restart_after_init`) SELECT `id`, `name`, `description`, IFNULL(`type`, 2) AS `type`, `docker_image`, `default_env`, `default_volumes`, `health_check_path`, `health_check_port`, `requires_server`, `supports_proxy`, `icon`, `category`, `documentation`, `created_at`, `updated_at`, `ports`, `suggested_dependencies`, `default_hooks`, `metadata`, `default_cmd`, `default_access_urls`, `default_memory`, `default_uid`, `default_gid`, `default_init_command`, `default_init_command_delay`, `default_restart_after_init` FROM `module_templates`;
-- Drop "module_templates" table after copying rows
DROP TABLE `module_templates`;
-- Rename temporary table "new_module_templates" to "module_templates"
ALTER TABLE `new_module_templates` RENAME TO `module_templates`;
-- Create index "idx_module_templates_name" to table: "module_templates"
CREATE UNIQUE INDEX `idx_module_templates_name` ON `module_templates` (`name`);
-- Create "new_modules" table
CREATE TABLE `new_modules` (
  `id` text NULL,
  `name` text NOT NULL,
  `server_id` text NOT NULL,
  `template_id` text NOT NULL,
  `container_id` text NULL,
  `status` integer NOT NULL DEFAULT 1,
  `env_overrides` text NULL,
  `volume_overrides` text NULL,
  `memory` integer NULL DEFAULT 512,
  `cpu_limit` real NULL,
  `auto_start` numeric NULL DEFAULT false,
  `follow_server_lifecycle` numeric NULL DEFAULT true,
  `detached` numeric NULL DEFAULT false,
  `created_at` time NULL,
  `updated_at` time NULL,
  `last_started` time NULL,
  `ports` text NULL,
  `dependencies` text NULL,
  `health_check_interval` integer NULL DEFAULT 30,
  `health_check_timeout` integer NULL DEFAULT 5,
  `health_check_retries` integer NULL DEFAULT 3,
  `event_hooks` text NULL,
  `metadata` text NULL,
  `cmd_override` text NULL,
  `access_urls` text NULL,
  `created_by` text NULL,
  `uid` text NULL DEFAULT '',
  `gid` text NULL DEFAULT '',
  `init_command` text NULL DEFAULT '',
  `init_command_delay` integer NULL DEFAULT 0,
  `restart_after_init` numeric NULL DEFAULT false,
  `token_id` text NULL,
  `cert_pem` text NULL,
  `key_pem` text NULL,
  PRIMARY KEY (`id`)
);
-- Copy rows from old table "modules" to new temporary table "new_modules"
INSERT INTO `new_modules` (`id`, `name`, `server_id`, `template_id`, `container_id`, `status`, `env_overrides`, `volume_overrides`, `memory`, `cpu_limit`, `auto_start`, `follow_server_lifecycle`, `detached`, `created_at`, `updated_at`, `last_started`, `ports`, `dependencies`, `health_check_interval`, `health_check_timeout`, `health_check_retries`, `event_hooks`, `metadata`, `cmd_override`, `access_urls`, `created_by`, `uid`, `gid`, `init_command`, `init_command_delay`, `restart_after_init`, `token_id`) SELECT `id`, `name`, `server_id`, `template_id`, `container_id`, IFNULL(`status`, 1) AS `status`, `env_overrides`, `volume_overrides`, `memory`, `cpu_limit`, `auto_start`, `follow_server_lifecycle`, `detached`, `created_at`, `updated_at`, `last_started`, `ports`, `dependencies`, `health_check_interval`, `health_check_timeout`, `health_check_retries`, `event_hooks`, `metadata`, `cmd_override`, `access_urls`, `created_by`, `uid`, `gid`, `init_command`, `init_command_delay`, `restart_after_init`, `token_id` FROM `modules`;
-- Drop "modules" table after copying rows
DROP TABLE `modules`;
-- Rename temporary table "new_modules" to "modules"
ALTER TABLE `new_modules` RENAME TO `modules`;
-- Create index "idx_modules_template_id" to table: "modules"
CREATE INDEX `idx_modules_template_id` ON `modules` (`template_id`);
-- Create index "idx_modules_server_id" to table: "modules"
CREATE INDEX `idx_modules_server_id` ON `modules` (`server_id`);
-- Drop "migrations" table
DROP TABLE `migrations`;
-- Create "finding_dismissals" table
CREATE TABLE `finding_dismissals` (
  `server_id` text NULL,
  `finding_id` text NULL,
  `content_hash` text NOT NULL,
  `dismissed_at` time NOT NULL,
  PRIMARY KEY (`server_id`, `finding_id`)
);
-- Create "metrics_samples" table
CREATE TABLE `metrics_samples` (
  `timestamp` time NOT NULL,
  `tps` real NULL,
  `mspt` real NULL,
  `players` integer NULL,
  `cpu_percent` real NULL,
  `memory_mb` real NULL,
  `disk_bytes` integer NULL,
  `heap_used_mb` real NULL,
  `proxy_active_conns` integer NULL,
  `proxy_bytes_in` integer NULL,
  `proxy_bytes_out` integer NULL,
  `proxy_logins` integer NULL,
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `server_id` text NOT NULL,
  `resolution` integer NOT NULL DEFAULT 0,
  `gc_pause_count` integer NULL,
  `gc_pause_total_ms` real NULL,
  `gc_pause_max_ms` real NULL
);
-- Create index "idx_metrics_lookup" to table: "metrics_samples"
CREATE INDEX `idx_metrics_lookup` ON `metrics_samples` (`server_id`, `resolution`, `timestamp`);
-- Create "server_actions" table
CREATE TABLE `server_actions` (
  `id` integer NULL PRIMARY KEY AUTOINCREMENT,
  `timestamp` time NOT NULL,
  `source` text NOT NULL DEFAULT '',
  `message` text NOT NULL,
  `kind` integer NULL,
  `attrs` text NULL,
  `trace_id` text NULL,
  `server_id` text NOT NULL
);
-- Create index "idx_server_actions_server_id" to table: "server_actions"
CREATE INDEX `idx_server_actions_server_id` ON `server_actions` (`server_id`);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
