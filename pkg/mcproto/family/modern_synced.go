package family

import "github.com/discohaus/discopanel/pkg/mcproto/mcdata"

// One synced registry in vanilla wire order
type syncedRegistry struct {
	Name    string
	Entries []string
}

// Synced set for one protocol with generated tables
func syncedRegistrySet(protocol int32) ([]syncedRegistry, bool) {
	p := mcdata.Lookup(protocol)
	if p == nil {
		return nil, false
	}
	out := make([]syncedRegistry, 0, len(p.Registries))
	for _, reg := range p.Registries {
		out = append(out, syncedRegistry{Name: reg.Name, Entries: reg.Entries})
	}
	return out, true
}

// Wire id of one entry inside one synced registry
func syncedEntryID(protocol int32, registry, entry string) (int32, bool) {
	p := mcdata.Lookup(protocol)
	if p == nil {
		return 0, false
	}
	return p.EntryID(registry, entry)
}

// Tag payload one protocol requires to load
func syncedTagSet(protocol int32) []mcdata.TagRegistry {
	p := mcdata.Lookup(protocol)
	if p == nil {
		return nil
	}
	return p.Tags
}

// Baked biome cell id for one protocol
func plainsBiomeID(protocol int32) int32 {
	if id, ok := syncedEntryID(protocol, "minecraft:worldgen/biome", "plains"); ok {
		return id
	}
	return 0
}
