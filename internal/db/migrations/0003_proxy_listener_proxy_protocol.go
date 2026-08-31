// Scaffolded by protogorm migrate, ops are yours to edit
package migrations

import "github.com/nickheyer/protogorm/migrate"

func init() {
	target := mustSnapshot("0003_proxy_listener_proxy_protocol.snapshot.json")
	Registry.MustAdd(&migrate.Migration{
		Ordinal: 3,
		Name:    "proxy_listener_proxy_protocol",
		Target:  target,
		Ops: []migrate.Op{
			migrate.TableChange{Table: target.Table("proxy_listeners"), Adds: []string{"ingress_proxy_protocol", "trusted_proxies"}},
		},
	})
}
