package modrinth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/discohaus/discopanel/pkg/indexers"
)

// Implements ModSourcer
var _ indexers.ModSourcer = (*ModrinthIndexer)(nil)

// Caps fuzzy search hits tried per query
const maxSourceHits = 3

// Finds candidate jars for a mod id, best first
func (m *ModrinthIndexer) SourceMod(ctx context.Context, q indexers.ModQuery) ([]indexers.ModCandidate, error) {
	direct, _, err := m.projectCandidate(ctx, q.ModID, q)
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	if direct != nil {
		return []indexers.ModCandidate{*direct}, nil
	}

	resp, err := m.client.SearchProjects(ctx, q.ModID, "mod", q.Loaders, []string{q.McVersion}, 0, maxSourceHits)
	if err != nil {
		return nil, err
	}

	var out []indexers.ModCandidate
	var depIDs []string
	seen := map[string]bool{}
	for _, hit := range resp.Hits {
		if hit.ProjectID == "" {
			continue
		}
		seen[hit.ProjectID] = true
		c, deps, err := m.projectCandidate(ctx, hit.ProjectID, q)
		if err == nil && c != nil {
			c.Origin = "modrinth project " + hit.Slug
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
	for _, id := range depIDs {
		c, _, err := m.projectCandidate(ctx, id, q)
		if err != nil || c == nil {
			continue
		}
		out = append(out, *c)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no %s project offers %q for MC %s", strings.Join(q.Loaders, "/"), q.ModID, q.McVersion)
	}
	return out, nil
}

// Builds one candidate from a project's best matching version
func (m *ModrinthIndexer) projectCandidate(ctx context.Context, projectID string, q indexers.ModQuery) (*indexers.ModCandidate, []string, error) {
	versions, err := m.client.GetProjectVersionsFiltered(ctx, projectID, q.Loaders, []string{q.McVersion})
	if err != nil {
		return nil, nil, err
	}
	var pick *Version
	for i := range versions {
		if versions[i].VersionType == "release" {
			pick = &versions[i]
			break
		}
	}
	if pick == nil && len(versions) > 0 {
		pick = &versions[0]
	}
	if pick == nil {
		return nil, nil, nil
	}
	var deps []string
	for _, d := range pick.Dependencies {
		if d.DependencyType == "required" && d.ProjectID != nil && *d.ProjectID != "" {
			deps = append(deps, *d.ProjectID)
		}
	}
	file := primaryVersionFile(pick)
	if file == nil {
		return nil, deps, nil
	}
	algo, sum := "", ""
	switch {
	case file.Hashes.SHA512 != "":
		algo, sum = "sha512", file.Hashes.SHA512
	case file.Hashes.SHA1 != "":
		algo, sum = "sha1", file.Hashes.SHA1
	}
	return &indexers.ModCandidate{
		Origin:   "modrinth project " + projectID,
		FileName: file.Filename,
		URL:      file.URL,
		HashAlgo: algo,
		HashSum:  sum,
	}, deps, nil
}

// Primary file wins, first file is the fallback
func primaryVersionFile(v *Version) *File {
	for i := range v.Files {
		if v.Files[i].Primary {
			return &v.Files[i]
		}
	}
	if len(v.Files) > 0 {
		return &v.Files[0]
	}
	return nil
}

// Reports whether an error is an indexer 404
func isNotFound(err error) bool {
	var ie *indexers.IndexerError
	return errors.As(err, &ie) && ie.Kind == indexers.ErrNotFound
}
