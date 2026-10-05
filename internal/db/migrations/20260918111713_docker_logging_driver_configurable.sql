-- Add column "log_driver" to table: "modules"
ALTER TABLE `modules` ADD COLUMN `log_driver` text NULL DEFAULT '';
