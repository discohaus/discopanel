package minecraft

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

// One mod a jar declares or provides
type ModInfo struct {
	ID       string
	Version  string
	Declared bool   // Top-level declaration, nested and provides are false
	Dialect  string // Metadata dialect that declared it
}

// One dependency or incompatibility declared by a mod
type ModDep struct {
	Owner     string // Declaring mod id
	ID        string
	Range     string // Raw version range in loader syntax
	Mandatory bool
	Breaks    bool   // Declared incompatibility, presence is the violation
	Side      string // Lowercase side the dep applies to, empty means both
	Dialect   string // Metadata dialect, fabric quilt forge or neoforge
}

type ModJarMeta struct {
	FileName   string
	Mods       []ModInfo // Top-level and nested declared mods
	Deps       []ModDep  // Top-level mods' dependency declarations
	ClientOnly bool
}

func (m *ModJarMeta) HasModID(id string) bool {
	for i := range m.Mods {
		if m.Mods[i].ID == id {
			return true
		}
	}
	return false
}

// FML forbids hyphens, Connector registers fabric ids folded
func foldModID(id string) string {
	return strings.ReplaceAll(id, "-", "_")
}

// True when a reported id names one of these mods
func (m *ModJarMeta) HasReportedModID(id string) bool {
	if m.HasModID(id) {
		return true
	}
	want := foldModID(id)
	for i := range m.Mods {
		if foldModID(m.Mods[i].ID) == want {
			return true
		}
	}
	return false
}

// True when any declared mod id is required
func (m *ModJarMeta) providesAny(required map[string]bool) bool {
	for i := range m.Mods {
		if required[m.Mods[i].ID] {
			return true
		}
	}
	return false
}

// Server-relevant mandatory dep ids
func requiredModIDs(metas []ModJarMeta) map[string]bool {
	required := map[string]bool{}
	for i := range metas {
		for _, dep := range metas[i].Deps {
			if !dep.Mandatory || dep.Breaks || dep.Side == "client" {
				continue
			}
			required[dep.ID] = true
		}
	}
	return required
}

// Client-only jars safe to drop, jars other mods need stay
func ClientOnlySweep(metas []ModJarMeta, forceIncludes []string) []ModJarMeta {
	keep := make([]ModJarMeta, 0, len(metas))
	var drop []ModJarMeta
	for _, m := range metas {
		if !m.ClientOnly || MatchesPatterns(m.FileName, forceIncludes) {
			keep = append(keep, m)
		} else {
			drop = append(drop, m)
		}
	}
	// Kept jars pull their deps back until stable
	for {
		required := requiredModIDs(keep)
		var next []ModJarMeta
		moved := false
		for _, m := range drop {
			if m.providesAny(required) {
				keep = append(keep, m)
				moved = true
			} else {
				next = append(next, m)
			}
		}
		drop = next
		if !moved {
			return drop
		}
	}
}

// Returns a mod id's declared version, empty unknown
func (m *ModJarMeta) VersionOf(id string) string {
	for i := range m.Mods {
		if m.Mods[i].ID == id {
			return m.Mods[i].Version
		}
	}
	return ""
}

type modScanEntry struct {
	sig   string
	metas []ModJarMeta
}

var modScanCache sync.Map

func ScanModsDir(modsDir string) []ModJarMeta {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return nil
	}

	sig := scanSignature(entries)
	if cached, ok := modScanCache.Load(modsDir); ok {
		if entry := cached.(*modScanEntry); entry.sig == sig {
			return entry.metas
		}
	}

	var metas []ModJarMeta
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			continue
		}
		meta, err := readModJarMeta(filepath.Join(modsDir, e.Name()))
		if err != nil {
			continue
		}
		metas = append(metas, *meta)
	}

	modScanCache.Store(modsDir, &modScanEntry{sig: sig, metas: metas})
	return metas
}

func scanSignature(entries []os.DirEntry) string {
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s|%d|%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

type fabricModJSON struct {
	ID          string         `json:"id"`
	Version     string         `json:"version"`
	Environment any            `json:"environment"`
	Provides    []string       `json:"provides"`
	Depends     map[string]any `json:"depends"`
	Breaks      map[string]any `json:"breaks"`
	Jars        []struct {
		File string `json:"file"`
	} `json:"jars"`
}

type quiltModJSON struct {
	QuiltLoader struct {
		ID       string   `json:"id"`
		Version  string   `json:"version"`
		Depends  []any    `json:"depends"`
		Breaks   []any    `json:"breaks"`
		Provides []any    `json:"provides"`
		Jars     []string `json:"jars"`
	} `json:"quilt_loader"`
	Minecraft struct {
		Environment string `json:"environment"`
	} `json:"minecraft"`
}

// Nested jars can nest again, mod metadata rarely goes deeper
const maxJarNesting = 3

// Parses one jar outside the mods dir cache
func ReadModJar(jarPath string) (*ModJarMeta, error) {
	return readModJarMeta(jarPath)
}

func readModJarMeta(jarPath string) (*ModJarMeta, error) {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	meta := &ModJarMeta{FileName: filepath.Base(jarPath)}
	parseJarEntries(meta, &r.Reader, maxJarNesting, true)
	return meta, nil
}

// Walks one jar's metadata files, recursing into bundled jars
func parseJarEntries(meta *ModJarMeta, r *zip.Reader, depth int, topLevel bool) {
	var nested []string
	var mcmodData []byte
	manifestVersion := ""
	hasModsToml := false

	for _, f := range r.File {
		switch f.Name {
		case "META-INF/MANIFEST.MF":
			manifestVersion = implementationVersion(f)
		case "fabric.mod.json":
			var fm fabricModJSON
			if readJarJSON(f, &fm) == nil {
				applyFabric(meta, &fm, topLevel)
				for _, j := range fm.Jars {
					nested = append(nested, j.File)
				}
			}
		case "quilt.mod.json":
			var qm quiltModJSON
			if readJarJSON(f, &qm) == nil {
				applyQuilt(meta, &qm, topLevel)
				nested = append(nested, qm.QuiltLoader.Jars...)
			}
		case "META-INF/mods.toml", "META-INF/neoforge.mods.toml":
			data, err := readJarFile(f)
			if err != nil {
				continue
			}
			dialect := "forge"
			if strings.Contains(f.Name, "neoforge") {
				dialect = "neoforge"
			}
			hasModsToml = true
			applyModsToml(meta, data, topLevel, dialect)
		case "mcmod.info":
			if data, err := readJarFile(f); err == nil {
				mcmodData = data
			}
		case "META-INF/jarjar/metadata.json":
			var jj jarJarMetadata
			if readJarJSON(f, &jj) == nil {
				for _, j := range jj.Jars {
					nested = append(nested, j.Path)
				}
			}
		}
		// Service jars nest mods without jarjar metadata
		if strings.HasPrefix(f.Name, "META-INF/jarjar/") && strings.HasSuffix(strings.ToLower(f.Name), ".jar") {
			nested = append(nested, f.Name)
		}
	}

	// Legacy metadata only speaks when no toml is present
	if mcmodData != nil && !hasModsToml {
		applyMcmodInfo(meta, mcmodData, topLevel)
	}

	resolveVersionPlaceholders(meta, manifestVersion)

	if depth <= 0 {
		return
	}
	// Metadata lists and directory scans name the same jars
	seen := map[string]bool{}
	for _, path := range nested {
		if seen[path] {
			continue
		}
		seen[path] = true
		parseNestedJar(meta, r, path, depth-1)
	}
}

type jarJarMetadata struct {
	Jars []struct {
		Path string `json:"path"`
	} `json:"jars"`
}

// Bundled mods count as provided, their deps ship satisfied
func parseNestedJar(meta *ModJarMeta, r *zip.Reader, path string, depth int) {
	for _, f := range r.File {
		if f.Name != path {
			continue
		}
		data, err := readNestedJar(f)
		if err != nil {
			return
		}
		inner, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		parseJarEntries(meta, inner, depth, false)
		return
	}
}

func applyFabric(meta *ModJarMeta, fm *fabricModJSON, topLevel bool) {
	if fm.ID != "" {
		meta.Mods = append(meta.Mods, ModInfo{ID: fm.ID, Version: fm.Version, Declared: topLevel, Dialect: "fabric"})
	}
	for _, p := range fm.Provides {
		if p != "" {
			meta.Mods = append(meta.Mods, ModInfo{ID: p, Version: fm.Version, Dialect: "fabric"})
		}
	}
	if !topLevel {
		return
	}
	if env, ok := fm.Environment.(string); ok && env == "client" {
		meta.ClientOnly = true
	}
	for id, ranges := range fm.Depends {
		meta.Deps = append(meta.Deps, ModDep{
			Owner: fm.ID, ID: id, Range: joinRanges(ranges), Mandatory: true, Dialect: "fabric",
		})
	}
	for id, ranges := range fm.Breaks {
		meta.Deps = append(meta.Deps, ModDep{
			Owner: fm.ID, ID: id, Range: joinRanges(ranges), Breaks: true, Dialect: "fabric",
		})
	}
}

// Fabric ranges are strings or any-of arrays, keep them raw
func joinRanges(v any) string {
	switch r := v.(type) {
	case string:
		return r
	case []any:
		var parts []string
		for _, e := range r {
			if s, ok := e.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " || ")
	}
	return ""
}

func applyQuilt(meta *ModJarMeta, qm *quiltModJSON, topLevel bool) {
	id := qm.QuiltLoader.ID
	if id != "" {
		meta.Mods = append(meta.Mods, ModInfo{ID: id, Version: qm.QuiltLoader.Version, Declared: topLevel, Dialect: "quilt"})
	}
	for _, p := range qm.QuiltLoader.Provides {
		if pid := quiltEntryID(p); pid != "" {
			meta.Mods = append(meta.Mods, ModInfo{ID: pid, Version: qm.QuiltLoader.Version, Dialect: "quilt"})
		}
	}
	if !topLevel {
		return
	}
	if qm.Minecraft.Environment == "client" {
		meta.ClientOnly = true
	}
	for _, d := range qm.QuiltLoader.Depends {
		if dep := quiltDep(id, d, false); dep != nil {
			dep.Dialect = "quilt"
			meta.Deps = append(meta.Deps, *dep)
		}
	}
	for _, d := range qm.QuiltLoader.Breaks {
		if dep := quiltDep(id, d, true); dep != nil {
			dep.Dialect = "quilt"
			meta.Deps = append(meta.Deps, *dep)
		}
	}
}

// Quilt entries are bare id strings or objects
func quiltEntryID(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case map[string]any:
		if id, ok := e["id"].(string); ok {
			return id
		}
	}
	return ""
}

func quiltDep(owner string, v any, breaks bool) *ModDep {
	switch e := v.(type) {
	case string:
		return &ModDep{Owner: owner, ID: e, Mandatory: !breaks, Breaks: breaks}
	case map[string]any:
		id, _ := e["id"].(string)
		if id == "" {
			return nil
		}
		optional, _ := e["optional"].(bool)
		return &ModDep{
			Owner:     owner,
			ID:        id,
			Range:     joinRanges(e["versions"]),
			Mandatory: !breaks && !optional,
			Breaks:    breaks,
		}
	}
	return nil
}

type forgeModsToml struct {
	ClientSideOnly bool `toml:"clientSideOnly"`
	Mods           []struct {
		ModID   string `toml:"modId"`
		Version string `toml:"version"`
	} `toml:"mods"`
	Dependencies map[string][]struct {
		ModID        string `toml:"modId"`
		Mandatory    *bool  `toml:"mandatory"`
		Type         string `toml:"type"`
		VersionRange string `toml:"versionRange"`
		Side         string `toml:"side"`
	} `toml:"dependencies"`
}

func applyModsToml(meta *ModJarMeta, data []byte, topLevel bool, dialect string) {
	var ft forgeModsToml
	if err := toml.Unmarshal(data, &ft); err != nil {
		return
	}

	for _, m := range ft.Mods {
		if m.ModID != "" {
			meta.Mods = append(meta.Mods, ModInfo{ID: m.ModID, Version: m.Version, Declared: topLevel, Dialect: dialect})
		}
	}
	if !topLevel {
		return
	}
	if ft.ClientSideOnly {
		meta.ClientOnly = true
	}
	for owner, deps := range ft.Dependencies {
		for _, dep := range deps {
			if dep.ModID == "" {
				continue
			}
			if dep.ModID == "minecraft" && strings.EqualFold(dep.Side, "CLIENT") {
				meta.ClientOnly = true
			}
			// Forge speaks mandatory, NeoForge speaks type
			mandatory := dep.Mandatory != nil && *dep.Mandatory
			if dep.Mandatory == nil {
				mandatory = dep.Type == "" || strings.EqualFold(dep.Type, "required")
			}
			meta.Deps = append(meta.Deps, ModDep{
				Owner:     owner,
				ID:        dep.ModID,
				Range:     dep.VersionRange,
				Mandatory: mandatory,
				Breaks:    strings.EqualFold(dep.Type, "incompatible"),
				Side:      strings.ToLower(dep.Side),
				Dialect:   dialect,
			})
		}
	}
}

// One mod entry in a 1.12 era mcmod.info file
type mcmodInfoEntry struct {
	ModID                    string   `json:"modid"`
	Version                  string   `json:"version"`
	RequiredMods             []string `json:"requiredMods"`
	UseDependencyInformation bool     `json:"useDependencyInformation"`
}

// Accepts the bare array and the wrapped modList forms
func parseMcmodInfo(data []byte) []mcmodInfoEntry {
	var list []mcmodInfoEntry
	if json.Unmarshal(data, &list) == nil {
		return list
	}
	var wrapped struct {
		ModList []mcmodInfoEntry `json:"modList"`
	}
	if json.Unmarshal(data, &wrapped) == nil {
		return wrapped.ModList
	}
	return nil
}

// Ids lowercase like legacy forge treats them
func applyMcmodInfo(meta *ModJarMeta, data []byte, topLevel bool) {
	for _, m := range parseMcmodInfo(data) {
		id := strings.ToLower(strings.TrimSpace(m.ModID))
		if id == "" {
			continue
		}
		meta.Mods = append(meta.Mods, ModInfo{ID: id, Version: m.Version, Declared: topLevel, Dialect: "forge"})
		if !topLevel || !m.UseDependencyInformation {
			continue
		}
		for _, dep := range m.RequiredMods {
			depID, depRange := splitMcmodDep(dep)
			if depID == "" {
				continue
			}
			meta.Deps = append(meta.Deps, ModDep{
				Owner: id, ID: depID, Range: depRange, Mandatory: true, Dialect: "forge",
			})
		}
	}
}

// Entries look like modid or modid@[1.0,)
func splitMcmodDep(s string) (string, string) {
	id, rng, _ := strings.Cut(strings.TrimSpace(s), "@")
	return strings.ToLower(strings.TrimSpace(id)), strings.TrimSpace(rng)
}

// Gradle stamps real versions into the manifest, toml keeps ${...}
func resolveVersionPlaceholders(meta *ModJarMeta, manifestVersion string) {
	for i := range meta.Mods {
		if strings.Contains(meta.Mods[i].Version, "${") {
			meta.Mods[i].Version = manifestVersion
		}
	}
}

func implementationVersion(f *zip.File) string {
	data, err := readJarFile(f)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "Implementation-Version:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

const maxManifestBytes = 1 << 20

// Bundled jars carry whole libraries, cap stays generous
const maxNestedJarBytes = 64 << 20

func readJarFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxManifestBytes))
}

func readNestedJar(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxNestedJarBytes))
}

func readJarJSON(f *zip.File, v any) error {
	data, err := readJarFile(f)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func DisableModJar(modsDir, fileName string) error {
	disabledDir := modsDir + "_disabled"
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(modsDir, fileName), filepath.Join(disabledDir, fileName))
}

// Moves a previously disabled jar back into the mods dir
func EnableModJar(modsDir, fileName string) error {
	return os.Rename(filepath.Join(modsDir+"_disabled", fileName), filepath.Join(modsDir, fileName))
}

func SplitPatterns(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, strings.ToLower(part))
		}
	}
	return out
}

func MatchesPatterns(fileName string, patterns []string) bool {
	name := strings.ToLower(fileName)
	for _, p := range patterns {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}
