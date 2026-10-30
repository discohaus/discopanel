package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/discohaus/discopanel/pkg/mcproto/mcdata"
)

// Writes one protocol document with one line per table row
func writeProtocol(path string, p *mcdata.Protocol) error {
	var b bytes.Buffer
	b.WriteString("{\n")
	fmt.Fprintf(&b, " \"protocol\": %d,\n", p.Protocol)
	fmt.Fprintf(&b, " \"versions\": %s,\n", compact(p.Versions))
	b.WriteString(" \"packets\": {\n")
	states := sortedKeys(p.Packets)
	for i, state := range states {
		fmt.Fprintf(&b, "  %q: {\n", state)
		dirs := sortedKeys(p.Packets[state])
		for j, dir := range dirs {
			fmt.Fprintf(&b, "   %q: %s", dir, compact(p.Packets[state][dir]))
			b.WriteString(sep(j, len(dirs)))
		}
		b.WriteString("  }" + sep(i, len(states)))
	}
	b.WriteString(" },\n")
	fmt.Fprintf(&b, " \"entity_types\": %s,\n", compact(p.EntityTypes))
	fmt.Fprintf(&b, " \"block_entity_types\": %s,\n", compact(p.BlockEntityTypes))
	fmt.Fprintf(&b, " \"block_states\": %s,\n", compact(p.BlockStates))
	fmt.Fprintf(&b, " \"feature_flags\": %s,\n", compact(p.FeatureFlags))
	b.WriteString(" \"registries\": [\n")
	for i, reg := range p.Registries {
		fmt.Fprintf(&b, "  {\"name\": %q, \"entries\": %s}", reg.Name, compact(reg.Entries))
		b.WriteString(sep(i, len(p.Registries)))
	}
	b.WriteString(" ],\n")
	b.WriteString(" \"tags\": [\n")
	for i, reg := range p.Tags {
		fmt.Fprintf(&b, "  {\"name\": %q, \"tags\": [\n", reg.Name)
		for j, tag := range reg.Tags {
			fmt.Fprintf(&b, "   {\"name\": %q, \"ids\": %s}", tag.Name, compact(tag.IDs))
			b.WriteString(sep(j, len(reg.Tags)))
		}
		b.WriteString("  ]}" + sep(i, len(p.Tags)))
	}
	b.WriteString(" ]\n}\n")
	if _, err := mcdata.Parse(b.Bytes()); err != nil {
		return fmt.Errorf("emitted document rejected: %w", err)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func compact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	if string(raw) == "null" {
		return "[]"
	}
	return string(raw)
}

func sep(i, n int) string {
	if i == n-1 {
		return "\n"
	}
	return ",\n"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
