package provisioner

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
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

// Bounds concurrent modpack file downloads
const packDownloadConcurrency = 8

// Extracted pack has nothing runnable
var errNoLaunchTarget = errors.New("could not determine how to launch this server pack: no known server jar, args file, or bundled installer found")

// The manifest.json found inside CurseForge pack zips
type cfManifest struct {
	Minecraft struct {
		Version    string `json:"version"`
		ModLoaders []struct {
			ID      string `json:"id"` // Example "forge-47.2.0"
			Primary bool   `json:"primary"`
		} `json:"modLoaders"`
	} `json:"minecraft"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Overrides string `json:"overrides"`
	Files     []struct {
		ProjectID int  `json:"projectID"`
		FileID    int  `json:"fileID"`
		Required  bool `json:"required"`
	} `json:"files"`
}

// Installs a CurseForge modpack from API or local zip
func (p *Provisioner) installCurseForgePack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
	// Locally uploaded pack zip
	if desired.source == optionsv1.PackSource_PACK_SOURCE_ZIP {
		rel := strings.TrimPrefix(filepath.ToSlash(desired.id), "/data/")
		zipPath := joinData(server.DataPath, rel)
		if !fileExists(zipPath) {
			return nil, fmt.Errorf("modpack zip %q not found in the server data directory", rel)
		}
		return p.installCurseForgeZip(ctx, server, cfg, zipPath, nil, force)
	}

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

	// Prefer author server pack over the client files
	dlFile := p.resolveServerPack(ctx, client, pack, server, file)

	zipPath, err := p.downloadCurseForgeFile(ctx, client, server, pack, dlFile)
	if err != nil {
		return nil, err
	}

	res, err := p.installCurseForgeZip(ctx, server, cfg, zipPath, client, force)
	// Script only server packs fall back to client manifest
	if errors.Is(err, errNoLaunchTarget) && dlFile.ID != file.ID {
		p.progress(server, "server pack is not launchable, installing from client pack manifest...")
		zipPath, err = p.downloadCurseForgeFile(ctx, client, server, pack, file)
		if err != nil {
			return nil, err
		}
		return p.installCurseForgeZip(ctx, server, cfg, zipPath, client, force)
	}
	return res, err
}

// Resolves the url for a pack file and downloads it
func (p *Provisioner) downloadCurseForgeFile(ctx context.Context, client *fuego.Client, server *v1.Server, pack *fuego.Modpack, file *fuego.File) (string, error) {
	dlURL, err := p.resolveModFileURL(ctx, client, pack.ID, file)
	if err != nil {
		return "", err
	}

	p.progress(server, "downloading %s (%s)...", pack.Name, file.FileName)
	zipPath := filepath.Join(installerDir(server.DataPath), "modpack.zip")
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

// Prefers a ready-made server pack over the client file
func (p *Provisioner) resolveServerPack(ctx context.Context, client *fuego.Client, pack *fuego.Modpack, server *v1.Server, file *fuego.File) *fuego.File {
	// Official CurseForge server pack linkage
	if file.ServerPackFileID != nil && *file.ServerPackFileID > 0 {
		if sp, err := client.GetFile(ctx, pack.ID, *file.ServerPackFileID); err == nil {
			p.progress(server, "using server pack %s", sp.FileName)
			return sp
		}
	}
	// Some authors ship the server pack as the alternate file
	if file.AlternateFileID > 0 {
		if alt, err := client.GetFile(ctx, pack.ID, file.AlternateFileID); err == nil && isServerPack(alt) {
			p.progress(server, "using server pack %s", alt.FileName)
			return alt
		}
	}
	return file
}

// Reports whether a file is a ready-made server pack
func isServerPack(f *fuego.File) bool {
	if f.IsServerPack {
		return true
	}
	name := strings.ToLower(f.FileName + " " + f.DisplayName)
	return strings.Contains(name, "server")
}

// Installs a pack zip, manifest driven or wholesale server pack
func (p *Provisioner) installCurseForgeZip(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, zipPath string, client *fuego.Client, force bool) (*Result, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open modpack zip: %w", err)
	}
	defer reader.Close()

	// FTB stubs point at the api holding real server files
	if packID, versionID, ok := ftbInstallerRef(&reader.Reader); ok {
		p.progress(server, "pack is an FTB installer stub, using the FTB api...")
		return p.installFTBPack(ctx, server, cfg, packID, versionID, force)
	}

	manifest, manifestPrefix := readCFManifest(&reader.Reader)
	if manifest != nil {
		return p.installFromCFManifest(ctx, server, cfg, reader, manifest, manifestPrefix, client, force)
	}

	// No manifest means ready-made server pack, unpack wholesale
	p.progress(server, "extracting server pack...")
	if err := p.extractServerPack(reader, server.DataPath, !force, minecraft.SplitPatterns(strVal(cfg.CfExcludeMods))); err != nil {
		return nil, err
	}
	return p.completeServerPack(ctx, server, cfg, force)
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

// Performs manifest driven install of overrides, mods, and loader
func (p *Provisioner) installFromCFManifest(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, manifest *cfManifest, prefix string, client *fuego.Client, force bool) (*Result, error) {
	if client == nil {
		var err error
		client, err = p.curseForgeClient(ctx, cfg)
		if err != nil {
			return nil, err
		}
	}

	p.progress(server, "installing %s %s (MC %s)...", manifest.Name, manifest.Version, manifest.Minecraft.Version)

	// Doctor holds stay out until it re-enables them
	excludes := append(minecraft.SplitPatterns(strVal(cfg.CfExcludeMods)), runtimespec.DoctorExcludes(server.DataPath)...)
	excludes = append(excludes, runtimespec.IncidentHeldFiles(server.DataPath)...)
	forceIncludes := minecraft.SplitPatterns(strVal(cfg.CfForceIncludeMods))

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
	type cfDownload struct {
		projectID int
		fileID    int
		file      fuego.File
		mod       fuego.Modpack
		dest      string
	}
	var pending []cfDownload
	total := 0
	for _, entry := range manifest.Files {
		file, ok := files[entry.FileID]
		if !ok {
			return nil, fmt.Errorf("pack references file %d of project %d which the API did not return", entry.FileID, entry.ProjectID)
		}
		mod := mods[entry.ProjectID]

		if !p.cfFileWanted(server, &file, &mod, entry.ProjectID, excludes, forceIncludes) {
			continue
		}
		total++

		dest := joinData(server.DataPath, filepath.Join(cfClassDir(mod.ClassID), file.FileName))
		if fileExists(dest) && !force {
			continue
		}
		pending = append(pending, cfDownload{
			projectID: entry.ProjectID,
			fileID:    entry.FileID,
			file:      file,
			mod:       mod,
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

	// Install the loader the manifest declares
	loaderID := ""
	for _, ml := range manifest.Minecraft.ModLoaders {
		if ml.Primary || loaderID == "" {
			loaderID = ml.ID
		}
	}
	if loaderID == "" {
		return nil, fmt.Errorf("pack manifest declares no mod loader")
	}

	loader, version, ok := minecraft.CutPackLoaderID(loaderID)
	if !ok {
		return nil, fmt.Errorf("pack declares unknown loader %q", loaderID)
	}
	if _, ok := packLoaderInstallers[loader]; !ok {
		return nil, fmt.Errorf("pack declares unsupported loader %q", loaderID)
	}
	return p.installLoaderForPack(ctx, server, cfg, loader, version, manifest.Minecraft.Version)
}

// Applies exclude and include rules plus client-only heuristic
func (p *Provisioner) cfFileWanted(server *v1.Server, file *fuego.File, mod *fuego.Modpack, projectID int, excludes, forceIncludes []string) bool {
	idStr := strconv.Itoa(projectID)
	slug := strings.ToLower(mod.Slug)
	fileName := strings.ToLower(file.FileName)

	if slices.Contains(forceIncludes, idStr) || (slug != "" && slices.Contains(forceIncludes, slug)) ||
		(fileName != "" && slices.Contains(forceIncludes, fileName)) {
		return true
	}
	if slices.Contains(excludes, idStr) || (slug != "" && slices.Contains(excludes, slug)) ||
		(fileName != "" && slices.Contains(excludes, fileName)) {
		p.progress(server, "skipping excluded mod %s", file.FileName)
		return false
	}

	// Known client mods skip even without API environment flags
	if defaultClientSlug(slug) || defaultClientFile(fileName) {
		p.progress(server, "skipping known client-only mod %s", file.FileName)
		return false
	}

	// CurseForge marks environment support inside gameVersions
	hasClient := slices.Contains(file.GameVersions, "Client")
	hasServer := slices.Contains(file.GameVersions, "Server")
	if hasClient && !hasServer {
		p.progress(server, "skipping client-only mod %s", file.FileName)
		return false
	}
	return true
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

// Extracts server pack zip, strips single wrapping dir
func (p *Provisioner) extractServerPack(reader *zip.ReadCloser, dataPath string, skipExisting bool, excludes []string) error {
	prefix := commonZipRoot(&reader.Reader)
	return p.extractZipPrefix(reader, prefix, dataPath, skipExisting, excludes)
}

// Returns "dir/" when all entries share one wrapping dir
func commonZipRoot(reader *zip.Reader) string {
	contentDirs := map[string]bool{
		"mods": true, "config": true, "overrides": true, "world": true,
		"libraries": true, "plugins": true, "defaultconfigs": true,
		"kubejs": true, "scripts": true, "resourcepacks": true,
	}
	root := ""
	for _, f := range reader.File {
		name := strings.TrimPrefix(f.Name, "./")
		if name == "" {
			continue
		}
		idx := strings.Index(name, "/")
		if idx < 0 {
			return "" // File at the root
		}
		dir := name[:idx]
		if root == "" {
			root = dir
		} else if root != dir {
			return ""
		}
	}
	if root == "" || contentDirs[strings.ToLower(root)] {
		return ""
	}
	return root + "/"
}

// Derives launch spec from an extracted server pack
func (p *Provisioner) completeServerPack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, force bool) (*Result, error) {
	dataPath := server.DataPath

	detect := func() *v1.LaunchSpec {
		if spec, err := detectForgeLaunch(dataPath, "minecraftforge/forge", ""); err == nil {
			return spec
		}
		if spec, err := detectForgeLaunch(dataPath, "neoforged/neoforge", ""); err == nil {
			return spec
		}
		for _, jar := range []string{"fabric-server-launch.jar", "quilt-server-launch.jar", "server.jar"} {
			if fileExists(filepath.Join(dataPath, jar)) {
				return &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: jar}
			}
		}
		return nil
	}

	if spec := detect(); spec != nil {
		p.adoptServerPackVersion(ctx, server, spec)
		return p.finishLaunch(server, spec, server.ModLoader, "", server.McVersion)
	}

	// Some packs ship the loader installer instead
	matches, _ := filepath.Glob(filepath.Join(dataPath, "*installer*.jar"))
	if len(matches) > 0 {
		installer := filepath.Base(matches[0])
		p.progress(server, "running bundled installer %s...", installer)
		cmd := []string{"java", "-jar", installer, "--installServer"}
		if err := p.runInstallerContainer(ctx, server, cfg, cmd); err != nil {
			return nil, fmt.Errorf("bundled installer failed: %w", err)
		}
		if spec := detect(); spec != nil {
			p.adoptServerPackVersion(ctx, server, spec)
			return p.finishLaunch(server, spec, server.ModLoader, "", server.McVersion)
		}
	}

	// Some packs install the loader at first run
	if loader, version := detectServerPackLoader(dataPath, server.McVersion); loader != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		p.progress(server, "server pack ships no loader, installing %s %s", protometa.Name(loader), version)
		return p.installLoaderForPack(ctx, server, cfg, loader, version, server.McVersion)
	}

	return nil, errNoLaunchTarget
}

// Adopts pack MC version over the user guess
func (p *Provisioner) adoptServerPackVersion(ctx context.Context, server *v1.Server, spec *v1.LaunchSpec) {
	evidence := serverPackMCVersion(server.DataPath, spec)
	if evidence == "" || evidence == server.McVersion {
		return
	}
	javaVersion := int32(docker.RequiredJavaMajor(evidence))
	p.action(ctx, server, "provisioner", v1.ServerActionKind_SERVER_ACTION_KIND_PROVISION_MC_VERSION,
		metrics.Attrs{"from": server.McVersion, "to": evidence},
		"server pack ships MC %s, replacing configured %s", evidence, server.McVersion)
	if err := p.store.UpdateServerFields(ctx, server.Id, map[string]any{
		"mc_version":   evidence,
		"java_version": javaVersion,
	}); err != nil {
		p.progress(server, "warning: could not persist detected MC version: %v", err)
	}
	server.McVersion = evidence
	server.JavaVersion = javaVersion
}

// Local MC version evidence inside an extracted server pack
func serverPackMCVersion(dataPath string, spec *v1.LaunchSpec) string {
	switch spec.Kind {
	case v1.LaunchKind_LAUNCH_KIND_JAR:
		if v := jarMCVersion(joinData(dataPath, spec.Jar)); v != "" {
			return v
		}
		if spec.Jar != "server.jar" {
			return jarMCVersion(joinData(dataPath, "server.jar"))
		}
	case v1.LaunchKind_LAUNCH_KIND_ARGS_FILE:
		return forgeArgsMCVersion(spec.ArgsFile)
	}
	return ""
}

// Reads MC version from a vanilla jar version.json
func jarMCVersion(jarPath string) string {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return ""
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "version.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		var v struct {
			ID           string `json:"id"`
			WorldVersion int    `json:"world_version"`
		}
		err = json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&v)
		rc.Close()
		if err != nil || v.WorldVersion <= 0 {
			return ""
		}
		return strings.TrimSpace(v.ID)
	}
	return ""
}

// Parses MC version from a forge libraries args path
func forgeArgsMCVersion(argsFile string) string {
	segs := strings.Split(filepath.ToSlash(argsFile), "/")
	if len(segs) < 2 {
		return ""
	}
	mc, _, ok := strings.Cut(segs[len(segs)-2], "-")
	if !ok || !mcVersionLike(mc) {
		return ""
	}
	return mc
}

// Accepts only the numeric 1.x family shape
func mcVersionLike(v string) bool {
	parts := strings.Split(v, ".")
	if parts[0] != "1" || len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, part := range parts[1:] {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
