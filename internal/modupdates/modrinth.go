package modupdates

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Site the project and changelog links point at
const modrinthSite = "https://modrinth.com"

// Calls the source makes against Modrinth
type modrinthAPI interface {
	VersionsFromHashes(ctx context.Context, algo string, hashes []string) (map[string]modrinth.Version, error)
	LatestVersionsFromHashes(ctx context.Context, algo string, hashes, loaders, gameVersions, versionTypes []string) (map[string]modrinth.Version, error)
	GetProjects(ctx context.Context, ids []string) ([]modrinth.ProjectDetails, error)
}

// Identifies jars by sha512 through Modrinth's version_files api
type ModrinthSource struct {
	api modrinthAPI
}

func NewModrinthSource(userAgent string) *ModrinthSource {
	return &ModrinthSource{api: modrinth.NewClient(userAgent)}
}

func (m *ModrinthSource) Kind() v1.ModUpdateSource {
	return v1.ModUpdateSource_MOD_UPDATE_SOURCE_MODRINTH
}

func (m *ModrinthSource) Lookup(ctx context.Context, jars []*HashedJar, t Target) (map[string]*Result, error) {
	hashes := make([]string, 0, len(jars))
	for _, j := range jars {
		hashes = append(hashes, j.SHA512)
	}
	current, err := m.api.VersionsFromHashes(ctx, "sha512", hashes)
	if err != nil {
		return nil, err
	}
	var known []string
	var projectIDs []string
	for _, h := range hashes {
		if v, ok := current[h]; ok {
			known = append(known, h)
			if !slices.Contains(projectIDs, v.ProjectID) {
				projectIDs = append(projectIDs, v.ProjectID)
			}
		}
	}
	if len(known) == 0 {
		return map[string]*Result{}, nil
	}

	var latest map[string]modrinth.Version
	if t.McVersion != "" && len(t.Loaders) > 0 {
		latest, err = m.api.LatestVersionsFromHashes(ctx, "sha512", known, t.Loaders, []string{t.McVersion}, t.Channels())
		if err != nil {
			return nil, err
		}
	}
	// Names are cosmetic, a failed lookup keeps the ids
	projects := map[string]modrinth.ProjectDetails{}
	if list, err := m.api.GetProjects(ctx, projectIDs); err == nil {
		for _, p := range list {
			projects[p.ID] = p
		}
	}

	out := make(map[string]*Result, len(known))
	for _, j := range jars {
		cur, ok := current[j.SHA512]
		if !ok {
			continue
		}
		r := &Result{
			Jar:       j.Jar,
			Source:    m.Kind(),
			ProjectID: cur.ProjectID,
			Current:   currentRelease(&cur, j.SHA512),
		}
		p := projects[cur.ProjectID]
		r.ProjectName = p.Title
		if p.Slug != "" {
			r.ProjectURL = fmt.Sprintf("%s/%s/%s", modrinthSite, projectPath(p.ProjectType), p.Slug)
		} else {
			r.ProjectURL = fmt.Sprintf("%s/project/%s", modrinthSite, cur.ProjectID)
		}
		r.Current.ChangelogURL = r.ProjectURL + "/version/" + cur.ID

		next, ok := latest[j.SHA512]
		switch {
		case t.McVersion == "" || len(t.Loaders) == 0:
			r.Status = StatusNoCompatible
			r.Err = "server Minecraft version or loader is unknown"
		case !ok || !modrinthCompatible(&next, t):
			r.Status = StatusNoCompatible
		case next.ID == cur.ID || !newer(parseTime(next.DatePublished), parseTime(cur.DatePublished)):
			r.Status = StatusUpToDate
		default:
			rel, err := latestRelease(&next, j.Name)
			if err != nil {
				r.Status = StatusNoCompatible
				r.Err = err.Error()
				break
			}
			if rel.HashSum == "" || rel.URL == "" {
				r.Status = StatusNoCompatible
				r.Err = "newest version has no verifiable file"
				break
			}
			rel.ChangelogURL = r.ProjectURL + "/version/" + next.ID
			r.Latest = rel
			r.Status = StatusAvailable
		}
		out[j.Path()] = r
	}
	return out, nil
}

// Upstream filters are trusted only after a local recheck
func modrinthCompatible(v *modrinth.Version, t Target) bool {
	if !t.allows(v.VersionType) || !slices.Contains(v.GameVersions, t.McVersion) {
		return false
	}
	if v.Status != "" && v.Status != "listed" {
		return false
	}
	for _, l := range v.Loaders {
		if slices.Contains(t.Loaders, strings.ToLower(l)) {
			return true
		}
	}
	return false
}

// Release for the installed file, matched by its sha512
func currentRelease(v *modrinth.Version, sha512 string) *Release {
	rel := versionRelease(v)
	for i := range v.Files {
		if strings.EqualFold(v.Files[i].Hashes.SHA512, sha512) {
			fileRelease(rel, &v.Files[i])
			break
		}
	}
	return rel
}

// Release for the jar of a newer version that replaces installed.
// A version can ship one jar per platform, the one whose name reads like
// the installed file wins; when that stays ambiguous nothing is picked.
func latestRelease(v *modrinth.Version, installed string) (*Release, error) {
	var jars []*modrinth.File
	for i := range v.Files {
		f := &v.Files[i]
		if strings.EqualFold(filepath.Ext(f.Filename), ".jar") && (f.FileType == "" || f.FileType == "unknown") {
			jars = append(jars, f)
		}
	}
	var pick *modrinth.File
	switch len(jars) {
	case 0:
		return nil, errors.New("newest version has no jar file")
	case 1:
		pick = jars[0]
	default:
		want := nameTokens(installed)
		for _, f := range jars {
			if slices.Equal(nameTokens(f.Filename), want) {
				if pick != nil {
					pick = nil
					break
				}
				pick = f
			}
		}
		if pick == nil {
			return nil, errors.New("newest version ships several jars and none clearly replaces this one, update it by hand")
		}
	}
	rel := versionRelease(v)
	fileRelease(rel, pick)
	return rel, nil
}

func versionRelease(v *modrinth.Version) *Release {
	rel := &Release{
		VersionID: v.ID,
		Version:   v.VersionNumber,
		Channel:   v.VersionType,
		Published: parseTime(v.DatePublished),
	}
	if rel.Version == "" {
		rel.Version = v.Name
	}
	return rel
}

func fileRelease(rel *Release, f *modrinth.File) {
	rel.FileName = f.Filename
	rel.URL = f.URL
	rel.Size = f.Size
	switch {
	case f.Hashes.SHA512 != "":
		rel.HashAlgo, rel.HashSum = "sha512", f.Hashes.SHA512
	case f.Hashes.SHA1 != "":
		rel.HashAlgo, rel.HashSum = "sha1", f.Hashes.SHA1
	}
}

// Sorted word tokens of a file name, versions and the extension dropped
func nameTokens(name string) []string {
	name = strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	var out []string
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if strings.ContainsAny(tok, "0123456789") || tok == "v" || tok == "mc" {
			continue
		}
		if !slices.Contains(out, tok) {
			out = append(out, tok)
		}
	}
	slices.Sort(out)
	return out
}

// Site path segment for a project type
func projectPath(projectType string) string {
	switch projectType {
	case "mod", "plugin", "datapack", "shader", "resourcepack", "modpack":
		return projectType
	}
	return "project"
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// A missing date never reads as newer
func newer(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return !a.IsZero() && b.IsZero()
	}
	return a.After(b)
}
