-- Add column "ingress_proxy_protocol" to table: "proxy_listeners"
ALTER TABLE `proxy_listeners` ADD COLUMN `ingress_proxy_protocol` numeric NOT NULL DEFAULT false;
-- Add column "trusted_proxies" to table: "proxy_listeners"
ALTER TABLE `proxy_listeners` ADD COLUMN `trusted_proxies` text NULL;
