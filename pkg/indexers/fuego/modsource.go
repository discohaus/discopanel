package fuego

import (
	"context"
	"fmt"

	"github.com/discohaus/discopanel/pkg/indexers"
)

// Implements ModSourcer
var _ indexers.ModSourcer = (*FuegoIndexer)(nil)

// Caps fuzzy search hits tried per query
const maxSourceHits = 3

// Finds candidate jars for a mod id, best first
func (f *FuegoIndexer) SourceMod(ctx context.Context, q indexers.ModQuery) ([]indexers.ModCandidate, error) {
	lt := queryLoaderType(q.Loaders)
	if lt == ModLoaderAny {
		return nil, fmt.Errorf("no curseforge loader filter matches %v", q.Loaders)
	}

	if mod, err := f.client.GetModBySlug(ctx, q.ModID, ModsClassID); err == nil {
		if c, _, err := f.modCandidate(ctx, mod, q.McVersion, lt); err == nil && c != nil {
			return []indexers.ModCandidate{*c}, nil
		}
	}

	resp, err := f.client.SearchProjects(ctx, q.ModID, ModsClassID, q.McVersion, lt, 0, maxSourceHits)
	if err != nil {
		return nil, err
	}

	var out []indexers.ModCandidate
	var depIDs []int
	seen := map[int]bool{}
	for i := range resp.Data {
		hit := &resp.Data[i]
		seen[hit.ID] = true
		c, deps, err := f.modCandidate(ctx, hit, q.McVersion, lt)
		if err == nil && c != nil {
			out = append(out, *c)
		}
		for _, id := range deps {
			if !seen[id] && len(depIDs) < maxSourceHits {
				seen[id] = true
				depIDs = append(depIDs, id)
			}
		}
	}

	// Addons point at the wanted mod as a dependency
	if len(depIDs) > 0 {
		if mods, err := f.client.GetModsByIDs(ctx, depIDs); err == nil {
			for i := range mods {
				c, _, err := f.modCandidate(ctx, &mods[i], q.McVersion, lt)
				if err != nil || c == nil {
					continue
				}
				out = append(out, *c)
			}
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no curseforge mod offers %q for MC %s", q.ModID, q.McVersion)
	}
	return out, nil
}

// First mappable loader facet wins
func queryLoaderType(loaders []string) ModLoaderType {
	for _, l := range loaders {
		if lt := loaderType(l); lt != ModLoaderAny {
			return lt
		}
	}
	return ModLoaderAny
}

// Builds one candidate from a mod's newest matching file
func (f *FuegoIndexer) modCandidate(ctx context.Context, mod *Modpack, mcVersion string, lt ModLoaderType) (*indexers.ModCandidate, []int, error) {
	files, err := f.client.GetModpackFiles(ctx, mod.ID, mcVersion, lt)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	newest := &files[0]
	for i := range files {
		if files[i].FileDate.After(newest.FileDate) {
			newest = &files[i]
		}
	}
	var deps []int
	for _, d := range newest.Dependencies {
		if d.RelationType == RelationRequiredDependency && d.ModID > 0 {
			deps = append(deps, d.ModID)
		}
	}
	dlURL, err := f.client.ResolveDownloadURL(ctx, mod.ID, newest)
	if err != nil {
		return nil, deps, err
	}
	algo, sum := newest.BestHash()
	return &indexers.ModCandidate{
		Origin:   "curseforge mod " + mod.Slug,
		FileName: newest.FileName,
		URL:      dlURL,
		HashAlgo: algo,
		HashSum:  sum,
	}, deps, nil
}
