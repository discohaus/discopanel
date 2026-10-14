-- Add column "modpack_id" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_id` text NULL;
-- Add column "modpack_version_id" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_version_id` text NULL;
-- Add column "modpack_update_version_id" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_update_version_id` text NULL;
-- Add column "modpack_update_version_name" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_update_version_name` text NULL;
-- Add column "modpack_update_file_date" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_update_file_date` time NULL;
-- Add column "modpack_update_dismissed_id" to table: "servers"
ALTER TABLE `servers` ADD COLUMN `modpack_update_dismissed_id` text NULL;
-- Create index "idx_servers_modpack_id" to table: "servers"
CREATE INDEX `idx_servers_modpack_id` ON `servers` (`modpack_id`);
-- Add column "is_server_pack" to table: "indexed_modpack_files"
ALTER TABLE `indexed_modpack_files` ADD COLUMN `is_server_pack` numeric NULL;
