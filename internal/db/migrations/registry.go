// Migration chain assembled from committed snapshots
package migrations

import (
	"embed"

	"github.com/nickheyer/protogorm/migrate"
)

//go:embed *.snapshot.json
var snapshots embed.FS

// Chain every migration file registers into
var Registry = &migrate.Registry{Genesis: mustSnapshot("genesis.snapshot.json")}

// Desired schema this build ships
func Head() *migrate.Spec {
	return mustSnapshot("head.snapshot.json")
}

// Reads one committed snapshot or panics
func mustSnapshot(name string) *migrate.Spec {
	data, err := snapshots.ReadFile(name)
	if err != nil {
		panic("missing snapshot " + name + ", run make gen")
	}
	return migrate.MustParseSpec(data)
}
