package modupdates

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Calls the source makes against CurseForge
type curseForgeAPI interface {
	GetFingerprintMatches(ctx context.Context, fingerprints []uint32) ([]fuego.FingerprintMatch, error)
	GetModsByIDs(ctx context.Context, modIDs []int) ([]fuego.Modpack, error)
	GetRecentModFiles(ctx context.Context, modID int, gameVersion string, modLoader fuego.ModLoaderType) ([]fuego.File, error)
	ResolveDownloadURL(ctx context.Context, modID int, file *fuego.File) (string, error)
}

// Identifies jars by murmur2 fingerprint through CurseForge
type CurseForgeSource struct {
	api curseForgeAPI
}

// Needs an api key or the index, the caller decides
func NewCurseForgeSource(apiKey, userAgent string) *CurseForgeSource {
	return &CurseForgeSource{api: fuego.NewClient(apiKey, userAgent)}
}

func (c *CurseForgeSource) Kind() v1.ModUpdateSource {
	return v1.ModUpdateSource_MOD_UPDATE_SOURCE_CURSEFORGE
}

// CurseForge release type codes
var cfChannels = map[int]string{1: "release", 2: "beta", 3: "alpha"}

func (c *CurseForgeSource) Lookup(ctx context.Context, jars []*HashedJar, t Target) (map[string]*Result, error) {
	prints := make([]uint32, 0, len(jars))
	for _, j := range jars {
		prints = append(prints, j.CFPrint)
	}
	matches, err := c.api.GetFingerprintMatches(ctx, prints)
	if err != nil {
		return nil, err
	}
	// A print shared by several files cannot name one of them
	byPrint := make(map[uint32]fuego.File, len(matches))
	ambiguous := map[uint32]bool{}
	for _, m := range matches {
		fp := uint32(m.File.FileFingerprint)
		if prev, ok := byPrint[fp]; ok && prev.ID != m.File.ID {
			ambiguous[fp] = true
		}
		byPrint[fp] = m.File
	}
	for fp := range ambiguous {
		delete(byPrint, fp)
	}
	var modIDs []int
	for _, f := range byPrint {
		if !slices.Contains(modIDs, f.ModID) {
			modIDs = append(modIDs, f.ModID)
		}
	}
	if len(byPrint) == 0 {
		return map[string]*Result{}, nil
	}
	mods := map[int]fuego.Modpack{}
	if list, err := c.api.GetModsByIDs(ctx, modIDs); err == nil {
		for _, m := range list {
			mods[m.ID] = m
		}
	}

	// One file listing per project serves every jar of it
	recent := map[int][]fuego.File{}
	out := map[string]*Result{}
	for _, j := range jars {
		cur, ok := byPrint[j.CFPrint]
		// The 32 bit print only shortlists, size and sha1 must agree too
		if !ok || !cfSameFile(&cur, j) {
			continue
		}
		mod := mods[cur.ModID]
		r := &Result{
			Jar:         j.Jar,
			Source:      c.Kind(),
			ProjectID:   fmt.Sprint(cur.ModID),
			ProjectName: mod.Name,
			ProjectURL:  mod.Links.WebsiteURL,
			Current:     cfRelease(&cur, mod.Links.WebsiteURL),
		}
		out[j.Path()] = r
		if t.McVersion == "" {
			r.Status = StatusNoCompatible
			r.Err = "server Minecraft version is unknown"
			continue
		}
		files, seen := recent[cur.ModID]
		if !seen {
			files, err = c.api.GetRecentModFiles(ctx, cur.ModID, t.McVersion, fuego.ModLoaderAny)
			if err != nil {
				return nil, err
			}
			recent[cur.ModID] = files
		}
		next := pickCFFile(files, t)
		switch {
		case next == nil:
			r.Status = StatusNoCompatible
		case next.ID == cur.ID || !newer(next.FileDate, cur.FileDate):
			r.Status = StatusUpToDate
		default:
			rel := cfRelease(next, mod.Links.WebsiteURL)
			if rel.HashSum == "" {
				r.Status = StatusNoCompatible
				r.Err = "newest file has no verifiable hash"
				continue
			}
			url, err := c.api.ResolveDownloadURL(ctx, cur.ModID, next)
			if err != nil {
				r.Status = StatusError
				r.Err = err.Error()
				continue
			}
			rel.URL = url
			r.Latest = rel
			r.Status = StatusAvailable
		}
	}
	return out, nil
}

// Newest available file the target accepts
func pickCFFile(files []fuego.File, t Target) *fuego.File {
	var best *fuego.File
	for i := range files {
		f := &files[i]
		if !f.IsAvailable || f.IsServerPack || !t.allows(cfChannels[f.ReleaseType]) {
			continue
		}
		if !hasTag(f.GameVersions, t.McVersion) || !cfLoaderOK(f, t) {
			continue
		}
		if best == nil || f.FileDate.After(best.FileDate) {
			best = f
		}
	}
	return best
}

// Exact match on length and the sha1 CurseForge published
func cfSameFile(f *fuego.File, j *HashedJar) bool {
	if f.FileLength != j.Size {
		return false
	}
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			return strings.EqualFold(h.Value, j.SHA1)
		}
	}
	return false
}

func hasTag(tags []string, want string) bool {
	return slices.ContainsFunc(tags, func(v string) bool { return strings.EqualFold(v, want) })
}

// Mod files must carry one of the target's loader tags, plugins carry none
func cfLoaderOK(f *fuego.File, t Target) bool {
	var mapped []string
	for _, l := range t.Loaders {
		if fuego.LoaderTypeOf([]string{l}) != fuego.ModLoaderAny {
			mapped = append(mapped, l)
		}
	}
	if len(mapped) == 0 {
		return true
	}
	for _, l := range mapped {
		if hasTag(f.GameVersions, l) {
			return true
		}
	}
	return false
}

func cfRelease(f *fuego.File, site string) *Release {
	algo, sum := f.BestHash()
	rel := &Release{
		VersionID: fmt.Sprint(f.ID),
		Version:   f.DisplayName,
		FileName:  f.FileName,
		URL:       f.DownloadURL,
		HashAlgo:  algo,
		HashSum:   sum,
		Size:      f.FileLength,
		Channel:   cfChannels[f.ReleaseType],
		Published: f.FileDate,
	}
	if site != "" {
		rel.ChangelogURL = fmt.Sprintf("%s/files/%d", strings.TrimRight(site, "/"), f.ID)
	}
	return rel
}
