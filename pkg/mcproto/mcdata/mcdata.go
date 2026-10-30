// Package mcdata embeds wire tables generated from official server jars
package mcdata

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed data
var files embed.FS

// One synced registry with entries in vanilla wire order
type Registry struct {
	Name    string   `json:"name"`
	Entries []string `json:"entries"`
}

// One tag with member ids in the registry wire space
type Tag struct {
	Name string  `json:"name"`
	IDs  []int32 `json:"ids"`
}

// Tags for one registry in vanilla wire order
type TagRegistry struct {
	Name string `json:"name"`
	Tags []Tag  `json:"tags"`
}

// Wire facts for one protocol number
type Protocol struct {
	Protocol         int32                                  `json:"protocol"`
	Versions         []string                               `json:"versions"`
	Packets          map[string]map[string]map[string]int32 `json:"packets"`
	EntityTypes      map[string]int32                       `json:"entity_types"`
	BlockEntityTypes map[string]int32                       `json:"block_entity_types"`
	BlockStates      map[string]int32                       `json:"block_states"`
	FeatureFlags     []string                               `json:"feature_flags"`
	Registries       []Registry                             `json:"registries"`
	Tags             []TagRegistry                          `json:"tags"`
}

var (
	byProtocol = map[int32]*Protocol{}
	byVersion  = map[string]int32{}
	ordered    []*Protocol
)

// Embedded files load once at package init
func init() {
	entries, err := fs.ReadDir(files, "data")
	if err != nil {
		panic(fmt.Sprintf("mcdata: %v", err))
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := files.ReadFile("data/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("mcdata: %v", err))
		}
		p, err := Parse(raw)
		if err != nil {
			panic(fmt.Sprintf("mcdata: %s: %v", e.Name(), err))
		}
		register(p)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Protocol < ordered[j].Protocol })
}

// Decodes and validates one protocol document
func Parse(raw []byte) (*Protocol, error) {
	var p Protocol
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.Protocol <= 0 {
		return nil, fmt.Errorf("protocol number missing")
	}
	if len(p.Versions) == 0 {
		return nil, fmt.Errorf("protocol %d names no versions", p.Protocol)
	}
	if len(p.Packets) != 0 {
		for _, state := range []string{"login", "configuration", "play"} {
			for _, dir := range []string{"clientbound", "serverbound"} {
				if len(p.Packets[state][dir]) == 0 {
					return nil, fmt.Errorf("protocol %d lacks %s/%s packets", p.Protocol, state, dir)
				}
			}
		}
	}
	if len(p.Registries) == 0 {
		return nil, fmt.Errorf("protocol %d lacks synced registries", p.Protocol)
	}
	return &p, nil
}

func register(p *Protocol) {
	byProtocol[p.Protocol] = p
	for _, v := range p.Versions {
		byVersion[v] = p.Protocol
	}
	ordered = append(ordered, p)
}

// Every embedded protocol, oldest first
func All() []*Protocol {
	return ordered
}

// Facts for one protocol number, nil when not embedded
func Lookup(protocol int32) *Protocol {
	return byProtocol[protocol]
}

// Highest embedded protocol number
func Newest() int32 {
	if len(ordered) == 0 {
		return 0
	}
	return ordered[len(ordered)-1].Protocol
}

// Lowest embedded protocol number
func Oldest() int32 {
	if len(ordered) == 0 {
		return 0
	}
	return ordered[0].Protocol
}

// Protocol number for one release name
func ProtocolForVersion(version string) (int32, bool) {
	p, ok := byVersion[strings.TrimSpace(version)]
	return p, ok
}

// Reports whether the jar shipped a packet report
func (p *Protocol) HasPackets() bool {
	return len(p.Packets) != 0
}

// Packet id for one state, direction, and resource name
func (p *Protocol) PacketID(state, direction, name string) (int32, bool) {
	id, ok := p.Packets[state][direction][qualify(name)]
	return id, ok
}

// Block state id for one hub palette spelling
func (p *Protocol) StateID(block string) (int32, bool) {
	id, ok := p.BlockStates[block]
	return id, ok
}

// Wire id of one entry inside one synced registry
func (p *Protocol) EntryID(registry, entry string) (int32, bool) {
	registry, entry = qualify(registry), qualify(entry)
	for _, reg := range p.Registries {
		if reg.Name != registry {
			continue
		}
		for i, name := range reg.Entries {
			if name == entry {
				return int32(i), true
			}
		}
	}
	return 0, false
}

// Adds the vanilla namespace to bare names
func qualify(name string) string {
	if strings.Contains(name, ":") {
		return name
	}
	return "minecraft:" + name
}
