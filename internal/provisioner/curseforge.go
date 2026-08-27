// CurseForge pack acquisition and manifest format install
package provisioner

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"golang.org/x/sync/errgroup"
)

// CurseForge file release channels
const (
	cfChannelRelease = 1
	cfChannelBeta    = 2
	cfChannelAlpha   = 3
)

// Names the release channels present in a file list
func cfChannelsOf(files []fuego.File) []string {
	names := []struct {
		id   int
		name string
	}{{cfChannelRelease, "release"}, {cfChannelBeta, "beta"}, {cfChannelAlpha, "alpha"}}
	var out []string
	for _, ch := range names {
		for i := range files {
			if files[i].ReleaseType == ch.id {
				out = append(out, ch.name)
				break
			}
		}
	}
	return out
}

// The manifest.json found inside CurseForge pack zips
type cfManifest struct {
	Minecraft struct {
		Version    string `json:"version"`
		ModLoaders []struct {
			ID      string `json:"id"` // Example "forge-47.2.0"
			Primary bool   `json:"primary"`
		} `json:"modLoaders"`
	} `json:"minecraft"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Overrides   string `json:"overrides"`
	Files       []struct {
		ProjectID int  `json:"projectID"`
		FileID    int  `json:"fileID"`
		Required  bool `json:"required"`
	} `json:"files"`
}

// Resolves a CurseForge pack into ordered archive candidates
func resolveCurseForgePayloads(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack) ([]packPayload, error) {
	client, err := p.curseForgeClient(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if desired.id == "" {
		return nil, fmt.Errorf("no CurseForge modpack configured (set the page URL or slug)")
	}

	pack, err := client.GetModBySlug(ctx, desired.id, fuego.ModpackClassID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve CurseForge pack %q: %w", desired.id, err)
	}

	file, err := p.resolveCurseForgeFile(ctx, client, pack, desired.versionID)
	if err != nil {
		return nil, err
	}
	desired.versionID = strconv.Itoa(file.ID)

	// Author server packs go first, client file backs them
	files := append(p.serverPackCandidates(ctx, client, pack, server, file), file)
	seen := map[int]bool{}
	var payloads []packPayload
	for _, f := range files {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		payloads = append(payloads, packPayload{
			label: f.FileName,
			fetch: func(ctx context.Context) (string, error) {
				return p.downloadCurseForgeFile(ctx, client, server, pack, f)
			},
		})
	}
	return payloads, nil
}

// Resolves the url for a pack file and downloads it
func (p *Provisioner) downloadCurseForgeFile(ctx context.Context, client *fuego.Client, server *v1.Server, pack *fuego.Modpack, file *fuego.File) (string, error) {
	dlURL, err := p.resolveModFileURL(ctx, client, pack.ID, file)
	if err != nil {
		return "", err
	}

	p.progress(server, "downloading %s (%s)...", pack.Name, file.FileName)
	zipPath := stagedArchivePath(server.DataPath, file.FileName)
	if err := p.download(ctx, dlURL, zipPath, cfChecksum(file), nil, p.reporter(server, file.FileName)); err != nil {
		return "", err
	}
	return zipPath, nil
}

// Builds a fuego client from server or global API key
func (p *Provisioner) curseForgeClient(ctx context.Context, cfg *v1.ServerProperties) (*fuego.Client, error) {
	apiKey := strVal(cfg.CfApiKey)
	if apiKey == "" {
		if global, _, err := p.store.GetGlobalSettings(ctx); err == nil && global != nil {
			apiKey = strVal(global.CfApiKey)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("a CurseForge API key is required for CurseForge modpacks (set it in the server or global settings)")
	}
	return fuego.NewClient(apiKey, p.cfg.Server.UserAgent), nil
}

// Picks pinned id, main file, or newest release file
func (p *Provisioner) resolveCurseForgeFile(ctx context.Context, client *fuego.Client, pack *fuego.Modpack, fileID string) (*fuego.File, error) {
	if fileID != "" {
		id, err := strconv.Atoi(fileID)
		if err != nil {
			return nil, fmt.Errorf("invalid CurseForge file id %q", fileID)
		}
		return client.GetFile(ctx, pack.ID, id)
	}
	if pack.MainFileID > 0 {
		if f, err := client.GetFile(ctx, pack.ID, pack.MainFileID); err == nil {
			return f, nil
		}
	}
	files, err := client.GetModpackFiles(ctx, pack.ID, "", fuego.ModLoaderAny)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("CurseForge pack %q has no files", pack.Slug)
	}
	var newest *fuego.File
	for i := range files {
		if files[i].ReleaseType != cfChannelRelease {
			continue
		}
		if newest == nil || files[i].FileDate.After(newest.FileDate) {
			newest = &files[i]
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("CurseForge pack %q has no release files (available: %s), pin a CurseForge File ID to install one",
			pack.Slug, strings.Join(cfChannelsOf(files), ", "))
	}
	return newest, nil
}

// Ready made server packs linked from the client file
func (p *Provisioner) serverPackCandidates(ctx context.Context, client *fuego.Client, pack *fuego.Modpack, server *v1.Server, file *fuego.File) []*fuego.File {
	var out []*fuego.File
	// Official CurseForge server pack linkage
	if file.ServerPackFileID != nil && *file.ServerPackFileID > 0 {
		if sp, err := client.GetFile(ctx, pack.ID, *file.ServerPackFileID); err == nil {
			p.progress(server, "using server pack %s", sp.FileName)
			out = append(out, sp)
		}
	}
	// Some authors ship the server pack as the alternate file
	if file.AlternateFileID > 0 {
		if alt, err := client.GetFile(ctx, pack.ID, file.AlternateFileID); err == nil && isServerPack(alt) {
			out = append(out, alt)
		}
	}
	return out
}

// Reports whether a file is a ready-made server pack
func isServerPack(f *fuego.File) bool {
	if f.IsServerPack {
		return true
	}
	name := strings.ToLower(f.FileName + " " + f.DisplayName)
	return strings.Contains(name, "server")
}

// Finds manifest.json at zip root or one dir deep
func readCFManifest(reader *zip.Reader) (*cfManifest, string) {
	for _, f := range reader.File {
		name := f.Name
		prefix := ""
		if idx := strings.Index(name, "/"); idx >= 0 && strings.Count(name, "/") == 1 {
			prefix = name[:idx+1]
			name = name[idx+1:]
		}
		if name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		var m cfManifest
		err = json.NewDecoder(rc).Decode(&m)
		rc.Close()
		if err == nil && m.Minecraft.Version != "" {
			return &m, prefix
		}
	}
	return nil, ""
}

// Loader facts a CurseForge manifest declares
func cfManifestEvidence(manifest *cfManifest) packEvidence {
	loaderID := ""
	for _, ml := range manifest.Minecraft.ModLoaders {
		if ml.Primary || loaderID == "" {
			loaderID = ml.ID
		}
	}
	return loaderIDEvidence(loaderID, manifest.Minecraft.Version)
}

// Performs manifest driven install of overrides, mods, and loader
func (p *Provisioner) installFromCFManifest(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, manifest *cfManifest, prefix string, opts packInstallOpts) (*Result, error) {
	client, err := p.curseForgeClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	force := opts.force

	p.progress(server, "installing %s %s (MC %s)...", manifest.Name, manifest.Version, manifest.Minecraft.Version)

	excludes := p.packRuleExcludes(server, cfg, opts)
	forceIncludes := packForceIncludes(cfg)

	// Apply overrides
	overrides := manifest.Overrides
	if overrides == "" {
		overrides = "overrides"
	}
	if err := p.extractZipPrefix(reader, prefix+overrides+"/", server.DataPath, !force, excludes); err != nil {
		return nil, fmt.Errorf("failed to apply pack overrides: %w", err)
	}

	// Bulk-fetch file and mod metadata
	fileIDs := make([]int, 0, len(manifest.Files))
	modIDs := make([]int, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		fileIDs = append(fileIDs, f.FileID)
		modIDs = append(modIDs, f.ProjectID)
	}

	files := map[int]fuego.File{}
	if len(fileIDs) > 0 {
		fetched, err := client.GetFilesByIDs(ctx, fileIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch pack file metadata: %w", err)
		}
		for _, f := range fetched {
			files[f.ID] = f
		}
	}

	mods := map[int]fuego.Modpack{}
	if len(modIDs) > 0 {
		fetched, err := client.GetModsByIDs(ctx, modIDs)
		if err != nil {
			p.progress(server, "warning: could not fetch mod metadata (%v); using defaults", err)
		} else {
			for _, m := range fetched {
				mods[m.ID] = m
			}
		}
	}

	// Resolve wanted files up front then download concurrently
	// Client flagged mods still download, the sweep decides
	type cfDownload struct {
		projectID int
		file      fuego.File
		dest      string
	}
	var pending []cfDownload
	var packFlagged []string
	total := 0
	for _, entry := range manifest.Files {
		file, ok := files[entry.FileID]
		if !ok {
			return nil, fmt.Errorf("pack references file %d of project %d which the API did not return", entry.FileID, entry.ProjectID)
		}
		mod := mods[entry.ProjectID]

		wanted, clientReason := cfFileWanted(&file, &mod, entry.ProjectID, excludes, forceIncludes)
		if !wanted {
			p.progress(server, "skipping excluded mod %s", file.FileName)
			continue
		}
		classDir := cfClassDir(mod.ClassID)
		if clientReason != "" {
			// Non-mod client files cannot be loader deps
			if classDir != "mods" {
				p.progress(server, "skipping %s %s", clientReason, file.FileName)
				continue
			}
			packFlagged = append(packFlagged, strings.ToLower(file.FileName))
		}
		total++

		dest := joinData(server.DataPath, filepath.Join(classDir, file.FileName))
		if fileExists(dest) && !force {
			continue
		}
		pending = append(pending, cfDownload{
			projectID: entry.ProjectID,
			file:      file,
			dest:      dest,
		})
	}

	var done atomic.Int64
	done.Store(int64(total - len(pending)))
	if len(pending) > 0 {
		p.progress(server, "downloading %d mods (%d already present)...", len(pending), total-len(pending))
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(packDownloadConcurrency)
	for _, dl := range pending {
		g.Go(func() error {
			dlURL, err := p.resolveModFileURL(gctx, client, dl.projectID, &dl.file)
			if err != nil {
				return err
			}

			err = p.download(gctx, dlURL, dl.dest, cfChecksum(&dl.file), nil, nil)
			if err != nil {
				return fmt.Errorf("failed to download %s: %w", dl.file.FileName, err)
			}
			if n := done.Add(1); n%25 == 0 {
				p.progress(server, "downloaded %d/%d mods...", n, total)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		p.progress(server, "mod downloads complete (%d/%d)", done.Load(), total)
	}

	p.disableClientOnlyMods(ctx, server, forceIncludes, packFlagged)

	return p.installPackRuntime(ctx, server, cfg, cfManifestEvidence(manifest))
}

// Applies exclude and include rules plus client-only heuristic
// Client heuristics flag for the sweep instead of skipping
func cfFileWanted(file *fuego.File, mod *fuego.Modpack, projectID int, excludes, forceIncludes []string) (bool, string) {
	idStr := strconv.Itoa(projectID)
	slug := strings.ToLower(mod.Slug)
	fileName := strings.ToLower(file.FileName)

	if slices.Contains(forceIncludes, idStr) || (slug != "" && slices.Contains(forceIncludes, slug)) ||
		(fileName != "" && slices.Contains(forceIncludes, fileName)) {
		return true, ""
	}
	if slices.Contains(excludes, idStr) || (slug != "" && slices.Contains(excludes, slug)) ||
		(fileName != "" && slices.Contains(excludes, fileName)) {
		return false, ""
	}

	// Known client mods flag even without API environment flags
	if knownClientMod(slug) || knownClientMod(fileName) {
		return true, "known client-only mod"
	}

	// CurseForge marks environment support inside gameVersions
	hasClient := slices.Contains(file.GameVersions, "Client")
	hasServer := slices.Contains(file.GameVersions, "Server")
	if hasClient && !hasServer {
		return true, "client-only mod"
	}
	return true, ""
}

// Maps a CurseForge class to its install directory
func cfClassDir(classID int) string {
	switch classID {
	case 12: // Resource packs
		return "resourcepacks"
	case 6552: // Shader packs
		return "shaderpacks"
	case 5: // Bukkit plugins
		return "plugins"
	case 6945: // Data packs
		return "datapacks"
	default:
		return "mods"
	}
}

func cfChecksum(file *fuego.File) *checksum {
	for _, h := range file.Hashes {
		if h.Algo == 1 {
			return &checksum{algo: "sha1", value: h.Value}
		}
	}
	for _, h := range file.Hashes {
		if h.Algo == 2 {
			return &checksum{algo: "md5", value: h.Value}
		}
	}
	return nil
}
