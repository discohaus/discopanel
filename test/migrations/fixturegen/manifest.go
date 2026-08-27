// Records what every captured fixture contains
package main

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/discohaus/discopanel/test/migrations/seed"
)

// One captured release
type VersionEntry struct {
	Tag string `json:"tag"`
	// Either rest or connect
	Era string `json:"era,omitempty"`
	// Where the binary came from
	Source string `json:"source,omitempty"`
	// Reused from an earlier run
	Cached     bool             `json:"cached,omitempty"`
	Fixture    string           `json:"fixture"`
	HopFixture string           `json:"hop_fixture,omitempty"`
	Seed       *seed.Report     `json:"seed,omitempty"`
	Tables     map[string]int64 `json:"tables,omitempty"`
	HopTables  map[string]int64 `json:"hop_tables,omitempty"`
	DurationMs int64            `json:"duration_ms,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// Every fixture the generator produced
type Manifest struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Hop         string          `json:"hop,omitempty"`
	Versions    []*VersionEntry `json:"versions"`
}

// Reads an earlier manifest or starts empty
func loadManifest(path string) *Manifest {
	m := &Manifest{}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, m); err != nil {
		return &Manifest{}
	}
	return m
}

// Entry for one tag when present
func (m *Manifest) find(tag string) *VersionEntry {
	for _, v := range m.Versions {
		if v.Tag == tag {
			return v
		}
	}
	return nil
}

// Replaces or appends one entry keeping version order
func (m *Manifest) put(entry *VersionEntry) {
	for i, v := range m.Versions {
		if v.Tag == entry.Tag {
			m.Versions[i] = entry
			return
		}
	}
	m.Versions = append(m.Versions, entry)
	sort.Slice(m.Versions, func(i, j int) bool { return versionLess(m.Versions[i].Tag, m.Versions[j].Tag) })
}

// Writes the manifest as indented json
func (m *Manifest) save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
