package minecraft

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	utils "github.com/discohaus/discopanel/pkg/utils"
)

// One row of loader facts keyed by proto enum
type LoaderInfo struct {
	Info *v1.ModLoaderInfo
	*optionsv1.LoaderMeta
}

// Proto enum this row describes
func (r LoaderInfo) Loader() v1.ModLoader {
	return r.Info.Loader
}

// Rows sorted by category then enum number
var registry []LoaderInfo

var (
	loaderIndex  = map[v1.ModLoader]*LoaderInfo{}
	nameIndex    = map[string]*LoaderInfo{}
	dialectIndex = map[string]*LoaderInfo{}
)

func init() {
	for _, l := range protometa.Values[v1.ModLoader]() {
		meta := protometa.Loader(l)
		registry = append(registry, LoaderInfo{
			Info: &v1.ModLoaderInfo{
				Loader:          l,
				Name:            protometa.Name(l),
				DisplayName:     protometa.Label(l),
				Description:     protometa.Desc(l),
				Category:        meta.Category,
				ModsDirectory:   meta.ModsDirectory,
				SupportsMods:    meta.ModsDirectory != "",
				SupportsPlugins: meta.ModsDirectory == "plugins",
			},
			LoaderMeta: meta,
		})
	}
	slices.SortStableFunc(registry, func(a, b LoaderInfo) int {
		if c := cmp.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		return cmp.Compare(a.Loader(), b.Loader())
	})
	for i := range registry {
		row := &registry[i]
		l := row.Loader()
		loaderIndex[l] = row
		nameIndex[protometa.Name(l)] = row
		if len(row.Dialects) > 0 && row.Dialects[0] == protometa.Name(l) {
			dialectIndex[row.Dialects[0]] = row
		}
	}
}

// Row defining a manifest format, nil for unknown formats
func definingLoader(dialect string) *LoaderInfo {
	return dialectIndex[dialect]
}

// Returns every registry row in display order
func Loaders() []LoaderInfo {
	return slices.Clone(registry)
}

// Returns a loader's row, unknown yields bare
func LoaderFor(loader v1.ModLoader) LoaderInfo {
	if row, ok := loaderIndex[loader]; ok {
		return *row
	}
	return LoaderInfo{
		Info: &v1.ModLoaderInfo{
			Loader:      loader,
			Name:        protometa.Name(loader),
			DisplayName: protometa.Label(loader),
			Description: "Unknown mod loader",
			Category:    optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_OTHER,
		},
		LoaderMeta: &optionsv1.LoaderMeta{},
	}
}

// Pack source a loader installs from, unspecified when none
func PackSourceFor(loader v1.ModLoader) optionsv1.PackSource {
	if row, ok := loaderIndex[loader]; ok {
		return row.PackSource
	}
	return optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED
}

// Maps a pack source to its auto installing loader
func LoaderForPackSource(src optionsv1.PackSource) (v1.ModLoader, bool) {
	if src != optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED {
		for i := range registry {
			if registry[i].PackSource == src {
				return registry[i].Loader(), true
			}
		}
	}
	return v1.ModLoader_MOD_LOADER_UNSPECIFIED, false
}

// Splits manifest loader ids like forge-47.2.0
func CutPackLoaderID(loaderID string) (v1.ModLoader, string, bool) {
	name, version, _ := strings.Cut(loaderID, "-")
	row, ok := nameIndex[strings.ToLower(name)]
	if !ok {
		return v1.ModLoader_MOD_LOADER_UNSPECIFIED, "", false
	}
	return row.Loader(), version, true
}

// Loaders that define a manifest format, modpacks build on these
func PackLoaderNames() []string {
	var out []string
	for i := range registry {
		name := protometa.Name(registry[i].Loader())
		if len(registry[i].Dialects) > 0 && registry[i].Dialects[0] == name {
			out = append(out, name)
		}
	}
	return out
}

// Returns the mods storage path for a server
func GetModsPath(serverDataPath string, loader v1.ModLoader) string {
	dir := LoaderFor(loader).ModsDirectory
	if dir == "" {
		return ""
	}
	return filepath.Join(serverDataPath, dir)
}

// Checks if a file is a valid mod for loader
func IsValidModFile(filename string, loader v1.ModLoader) bool {
	if LoaderFor(loader).ModsDirectory == "" {
		return false
	}
	return strings.EqualFold(filepath.Ext(filename), ".jar")
}

// Fuzzy weights for mod loader matches
const (
	modLoaderMatchThreshold     = 0.5
	modpackLoaderMatchThreshold = 0.6
)

func MatchModLoader(input string) (v1.ModLoader, bool) {
	row, score, ok := utils.BestFunc(input, registry, func(r LoaderInfo) string {
		return protometa.Name(r.Loader())
	})
	if !ok || score < modLoaderMatchThreshold {
		return v1.ModLoader_MOD_LOADER_UNSPECIFIED, false
	}
	return row.Loader(), true
}

// Inspects candidate strings for modloader identification
func DetectModpackLoader(candidates ...string) (v1.ModLoader, bool) {
	best := ""
	bestScore := 0.0
	for _, c := range candidates {
		if m, ok := utils.Best(c, PackLoaderNames()); ok && m.Score > bestScore {
			best, bestScore = m.Value, m.Score
		}
	}
	if best == "" || bestScore < modpackLoaderMatchThreshold {
		return v1.ModLoader_MOD_LOADER_UNSPECIFIED, false
	}
	return nameIndex[best].Loader(), true
}
