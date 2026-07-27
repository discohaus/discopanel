package minecraft

import (
	"os"
	"path/filepath"
	"slices"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// One mod manifest format named for its loader
// The registry declares which formats every loader reads
// Detection exists only for loaders whose row declares nothing

// Dialects the server's platform reads, declared else observed
func ResolveDialects(loader v1.ModLoader, dataPath, modsDir string) []string {
	if row, ok := loaderIndex[loader]; ok && len(row.Dialects) > 0 {
		return slices.Clone(row.Dialects)
	}
	return DetectDialects(dataPath, modsDir)
}

// Dialects observed from the install when nothing is declared
// Launch spec, disk framework, and jars testify
func DetectDialects(dataPath, modsDir string) []string {
	if dataPath != "" {
		if spec, err := runtimespec.ReadLaunchSpec(dataPath); err == nil && spec != nil {
			if row, ok := loaderIndex[spec.Loader]; ok && len(row.Dialects) > 0 {
				return slices.Clone(row.Dialects)
			}
		}
		if row := markerHit(dataPath); row != nil {
			return slices.Clone(row.Dialects)
		}
	}
	if row := definingLoader(inferDialect(ScanModsDir(modsDir))); row != nil {
		return slices.Clone(row.Dialects)
	}
	return nil
}

// Probes disk markers, the longest dialect chain wins
// A fork's chain outranks its base's hit
func markerHit(dataPath string) *LoaderInfo {
	var best *LoaderInfo
	for i := range registry {
		row := &registry[i]
		for _, marker := range row.Markers {
			if _, err := os.Stat(filepath.Join(dataPath, filepath.FromSlash(marker))); err == nil {
				if best == nil || len(row.Dialects) > len(best.Dialects) {
					best = row
				}
				break
			}
		}
	}
	return best
}

// Reports whether the platform supplies a dep id
// The declaring manifest's format names the platform, chain included
func dialectBuiltin(dialect, id string) bool {
	if dialect == "" {
		for d := range dialectIndex {
			if slices.Contains(dialectIndex[d].Builtins, id) {
				return true
			}
		}
		return false
	}
	row := definingLoader(dialect)
	if row == nil {
		return false
	}
	for _, d := range row.Dialects {
		if def := definingLoader(d); def != nil && slices.Contains(def.Builtins, id) {
			return true
		}
	}
	return false
}

// Reports whether a dialect speaks maven ranges over semver
func dialectMavenRanges(dialect string) bool {
	if row := definingLoader(dialect); row != nil {
		return row.MavenRanges
	}
	return false
}

// Indexer loader facets that can source jars for these dialects
func DialectFacets(dialects []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, d := range dialects {
		row := definingLoader(d)
		if row == nil {
			continue
		}
		for _, f := range row.Facets {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// Votes the dialect from installed jar manifests
// A jar carrying only one manifest can only load there
func inferDialect(metas []ModJarMeta) string {
	exclusive := make(map[string]int)
	present := make(map[string]bool)
	for i := range metas {
		dialects := make(map[string]bool)
		for _, mod := range metas[i].Mods {
			if mod.Dialect != "" {
				dialects[mod.Dialect] = true
				present[mod.Dialect] = true
			}
		}
		if len(dialects) == 1 {
			for d := range dialects {
				exclusive[d]++
			}
		}
	}

	winner := ""
	for d, n := range exclusive {
		if n == 0 {
			continue
		}
		if winner != "" && winner != d {
			return dominantFamily(present)
		}
		winner = d
	}
	if winner != "" {
		return winner
	}
	return dominantFamily(present)
}

// Base dialect wins when one family owns the dir
func dominantFamily(present map[string]bool) string {
	families := make(map[string]bool)
	for d := range present {
		base := d
		if row := definingLoader(d); row != nil {
			base = row.Dialects[len(row.Dialects)-1]
		}
		families[base] = true
	}
	if len(families) != 1 {
		return ""
	}
	for base := range families {
		return base
	}
	return ""
}
