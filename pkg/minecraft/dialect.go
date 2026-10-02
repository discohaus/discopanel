package minecraft

import (
	"os"
	"path/filepath"
	"slices"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Dialects the server's platform reads, declared else observed
func ResolveDialects(loader v1.ModLoader, dataPath, modsDir string) []string {
	if row, ok := loaderIndex[loader]; ok && len(row.Dialects) > 0 {
		return slices.Clone(row.Dialects)
	}
	return DetectDialects(dataPath, modsDir)
}

// Observes dialects from the install when nothing is declared
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
	if row := inferLoader(ScanModsDir(modsDir)); row != nil {
		return slices.Clone(row.Dialects)
	}
	return nil
}

// Probes disk markers, the longest dialect chain wins
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

// Loader whose dialect chain reads every exclusive jar
func inferLoader(metas []ModJarMeta) *LoaderInfo {
	exclusive := make(map[string]bool)
	present := make(map[string]bool)
	for i := range metas {
		dialects := make(map[string]bool)
		for _, mod := range metas[i].Mods {
			if mod.Declared && mod.Dialect != "" {
				dialects[mod.Dialect] = true
				present[mod.Dialect] = true
			}
		}
		if len(dialects) == 1 {
			for d := range dialects {
				exclusive[d] = true
			}
		}
	}
	// Shortest covering chain wins, ties fall to registry order
	var best *LoaderInfo
	for i := range registry {
		row := &registry[i]
		// Only rows defining a present dialect are candidates
		if len(row.Dialects) == 0 || definingLoader(row.Dialects[0]) != row || !present[row.Dialects[0]] {
			continue
		}
		if !coversDialects(row, exclusive) {
			continue
		}
		if best == nil || len(row.Dialects) < len(best.Dialects) {
			best = row
		}
	}
	return best
}

// Reports whether a loader reads every listed dialect
func coversDialects(row *LoaderInfo, dialects map[string]bool) bool {
	for d := range dialects {
		if !slices.Contains(row.Dialects, d) {
			return false
		}
	}
	return true
}

// Loader the installed jar manifests testify, unspecified when mixed
func InferModsLoader(modsDir string) v1.ModLoader {
	if row := inferLoader(ScanModsDir(modsDir)); row != nil {
		return row.Loader()
	}
	return v1.ModLoader_MOD_LOADER_UNSPECIFIED
}
