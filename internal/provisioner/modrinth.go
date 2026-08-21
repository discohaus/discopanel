// Modrinth pack acquisition and mrpack format install
package provisioner

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"golang.org/x/sync/errgroup"
)

// The modrinth.index.json inside a .mrpack archive
type mrpackIndex struct {
	FormatVersion int               `json:"formatVersion"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary"`
	Dependencies  map[string]string `json:"dependencies"`
	Files         []mrpackFile      `json:"files"`
}

type mrpackFile struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
	Env       *struct {
		Client string `json:"client"`
		Server string `json:"server"`
	} `json:"env"`
}

// Resolves a Modrinth pack into ordered archive candidates
func resolveModrinthPayloads(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack) ([]packPayload, error) {
	client := modrinth.NewClient(p.cfg.Server.UserAgent)

	version, err := p.resolveModrinthVersion(ctx, client, cfg, desired)
	if err != nil {
		return nil, err
	}
	desired.versionID = version.ID

	files := orderedPackFiles(version)
	if len(files) == 0 {
		return nil, fmt.Errorf("Modrinth version %s has no files", version.ID)
	}

	var payloads []packPayload
	for _, file := range files {
		payloads = append(payloads, packPayload{
			label: file.Filename,
			fetch: func(ctx context.Context) (string, error) {
				p.progress(server, "downloading modpack %s (%s)...", desired.id, file.Filename)
				dest := stagedArchivePath(server.DataPath, file.Filename)
				if err := p.download(ctx, file.URL, dest, mrChecksum(file.Hashes), nil, p.reporter(server, file.Filename)); err != nil {
					return "", err
				}
				return dest, nil
			},
		})
	}
	return payloads, nil
}

// Orders version files, primary first then server archives
func orderedPackFiles(version *modrinth.Version) []modrinth.File {
	var out []modrinth.File
	seen := map[string]bool{}
	add := func(f modrinth.File) {
		if f.URL == "" || seen[f.URL] {
			return
		}
		seen[f.URL] = true
		out = append(out, f)
	}
	if primary := primaryFile(version); primary != nil && isArchiveFile(primary.Filename) {
		add(*primary)
	}
	for _, f := range version.Files {
		if isArchiveFile(f.Filename) && strings.Contains(strings.ToLower(f.Filename), "server") {
			add(f)
		}
	}
	for _, f := range version.Files {
		if isArchiveFile(f.Filename) {
			add(f)
		}
	}
	// Nothing archive shaped, the primary file still gets a try
	if len(out) == 0 {
		if primary := primaryFile(version); primary != nil {
			add(*primary)
		}
	}
	return out
}

// Reports whether a file name looks like a pack archive
func isArchiveFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".mrpack")
}

// Picks the pack version to install
func (p *Provisioner) resolveModrinthVersion(ctx context.Context, client *modrinth.Client, cfg *v1.ServerProperties, desired *desiredModpack) (*modrinth.Version, error) {
	if desired.id == "" {
		return nil, fmt.Errorf("no Modrinth modpack configured")
	}

	// Explicit version id resolves directly
	if desired.versionID != "" {
		if v, err := client.GetVersion(ctx, desired.versionID); err == nil {
			return v, nil
		}
	}

	versions, err := client.GetProjectVersionsFiltered(ctx, desired.id, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list versions for Modrinth pack %q: %w", desired.id, err)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("Modrinth pack %q has no versions", desired.id)
	}

	// Pinned by version number or id
	if desired.versionID != "" {
		for i := range versions {
			if versions[i].ID == desired.versionID || versions[i].VersionNumber == desired.versionID {
				return &versions[i], nil
			}
		}
		return nil, fmt.Errorf("version %q not found for Modrinth pack %q", desired.versionID, desired.id)
	}

	// Picks latest version within allowed release channel
	channel := strVal(cfg.ModrinthModpackVersionType)
	if channel == "" {
		channel = "release"
	}
	if pick := pickAllowedVersion(versions, channel); pick != nil {
		return pick, nil
	}
	return nil, fmt.Errorf("Modrinth pack %q has no %s versions (available: %s), adjust the Modrinth Modpack Version Type property",
		desired.id, channel, strings.Join(versionTypesOf(versions), ", "))
}

// Picks the newest version inside the allowed release channel
func pickAllowedVersion(versions []modrinth.Version, channel string) *modrinth.Version {
	allowed := map[string]bool{"release": true}
	switch channel {
	case "beta":
		allowed["beta"] = true
	case "alpha":
		allowed["beta"] = true
		allowed["alpha"] = true
	}
	for i := range versions {
		if allowed[versions[i].VersionType] {
			return &versions[i]
		}
	}
	return nil
}

// Names the distinct release channels present, stable first
func versionTypesOf(versions []modrinth.Version) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, want := range []string{"release", "beta", "alpha"} {
		for i := range versions {
			if versions[i].VersionType == want {
				add(want)
			}
		}
	}
	for i := range versions {
		add(versions[i].VersionType)
	}
	return out
}

// Finds modrinth.index.json at zip root or one dir deep
func readMrpackIndex(reader *zip.Reader) (*mrpackIndex, string) {
	for _, f := range reader.File {
		name := f.Name
		prefix := ""
		if idx := strings.Index(name, "/"); idx >= 0 && strings.Count(name, "/") == 1 {
			prefix = name[:idx+1]
			name = name[idx+1:]
		}
		if name != "modrinth.index.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		var index mrpackIndex
		err = json.NewDecoder(rc).Decode(&index)
		rc.Close()
		if err == nil {
			return &index, prefix
		}
	}
	return nil, ""
}

// Loader facts a Modrinth index declares
func mrpackEvidence(index *mrpackIndex) packEvidence {
	ev := packEvidence{mcVersion: index.Dependencies["minecraft"]}
	for _, entry := range mrpackLoaderKeys {
		if version := index.Dependencies[entry.key]; version != "" {
			ev.loaderID = entry.key + "-" + version
			ev.loader = entry.loader
			ev.loaderVersion = version
			return ev
		}
	}
	// Unknown dependency keys still name themselves in errors
	for key, version := range index.Dependencies {
		if key != "minecraft" {
			ev.loaderID = key + "-" + version
		}
	}
	return ev
}

// Downloads index files, applies overrides, provisions the runtime
func (p *Provisioner) installFromMrpackIndex(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, index *mrpackIndex, prefix string, opts packInstallOpts) (*Result, error) {
	force := opts.force
	excludes := p.packRuleExcludes(server, cfg, opts)
	forceIncludes := packForceIncludes(cfg)

	// Resolves wanted files, then downloads concurrently, bounded
	var pending []mrpackFile
	total := 0
	for _, file := range index.Files {
		if !p.mrpackFileWanted(server, file, excludes, forceIncludes) {
			continue
		}
		total++
		if len(file.Downloads) == 0 {
			return nil, fmt.Errorf("mrpack file %q has no download URLs", file.Path)
		}
		if !force && fileExists(joinData(server.DataPath, file.Path)) {
			continue
		}
		pending = append(pending, file)
	}
	p.progress(server, "installing %s: downloading %d files (%d already present)...",
		index.Name, len(pending), total-len(pending))

	var done atomic.Int64
	done.Store(int64(total - len(pending)))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(packDownloadConcurrency)
	for _, file := range pending {
		g.Go(func() error {
			dest := joinData(server.DataPath, file.Path)
			sum := mrpackChecksum(file.Hashes)

			var err error
			for _, u := range file.Downloads {
				if err = p.download(gctx, u, dest, sum, nil, nil); err == nil {
					break
				}
			}
			if err != nil {
				return fmt.Errorf("failed to download %q: %w", file.Path, err)
			}
			if n := done.Add(1); n%25 == 0 {
				p.progress(server, "downloaded %d/%d files...", n, total)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		p.progress(server, "pack downloads complete (%d/%d)", done.Load(), total)
	}

	// Apply overrides then server-overrides on top
	for _, dir := range []string{"overrides/", "server-overrides/"} {
		if err := p.extractZipPrefix(reader, prefix+dir, server.DataPath, !force, excludes); err != nil {
			return nil, fmt.Errorf("failed to apply %s: %w", strings.TrimSuffix(dir, "/"), err)
		}
	}

	return p.installPackRuntime(ctx, server, cfg, mrpackEvidence(index))
}

// Applies env.server and user include/exclude rules
func (p *Provisioner) mrpackFileWanted(server *v1.Server, file mrpackFile, excludes, forceIncludes []string) bool {
	name := strings.ToLower(filepath.Base(file.Path))
	for _, pattern := range forceIncludes {
		if strings.Contains(name, pattern) {
			return true
		}
	}
	for _, pattern := range excludes {
		if strings.Contains(name, pattern) {
			p.progress(server, "skipping excluded file %s", file.Path)
			return false
		}
	}
	// Known client jars skip even without env metadata
	if defaultClientFile(name) {
		p.progress(server, "skipping known client-only file %s", file.Path)
		return false
	}
	if file.Env != nil && file.Env.Server == "unsupported" {
		p.progress(server, "skipping client-only file %s", file.Path)
		return false
	}
	return true
}

// Mrpack dependency keys and the loaders they pin
var mrpackLoaderKeys = []struct {
	key    string
	loader v1.ModLoader
}{
	{"fabric-loader", v1.ModLoader_MOD_LOADER_FABRIC},
	{"quilt-loader", v1.ModLoader_MOD_LOADER_QUILT},
	{"forge", v1.ModLoader_MOD_LOADER_FORGE},
	{"neoforge", v1.ModLoader_MOD_LOADER_NEOFORGE},
}

// Remembers what a project resolved to on a past boot
type modrinthProjectState struct {
	VersionID    string   `json:"version_id"`
	FileName     string   `json:"file_name"`
	McVersion    string   `json:"mc_version"`
	Loader       string   `json:"loader"`
	RequiredDeps []string `json:"required_deps,omitempty"`
	OptionalDeps []string `json:"optional_deps,omitempty"`
}

type modrinthInstallState struct {
	Version  int                             `json:"version"`
	Projects map[string]modrinthProjectState `json:"projects"`
}

func modrinthStatePath(dataPath string) string {
	return filepath.Join(dataPath, ".discopanel", "modrinth-projects.json")
}

func readModrinthState(dataPath string) *modrinthInstallState {
	empty := &modrinthInstallState{Version: 1, Projects: map[string]modrinthProjectState{}}
	data, err := os.ReadFile(modrinthStatePath(dataPath))
	if err != nil {
		return empty
	}
	var state modrinthInstallState
	if json.Unmarshal(data, &state) != nil || state.Projects == nil {
		return empty
	}
	return &state
}

func writeModrinthState(dataPath string, state *modrinthInstallState) error {
	path := modrinthStatePath(dataPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Installs Modrinth mods, skips ones already present
func (p *Provisioner) installModrinthProjects(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, mcVersion string, force bool) error {
	projects := minecraft.SplitPatterns(strVal(cfg.ModrinthProjects))
	if len(projects) == 0 {
		return nil
	}
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return fmt.Errorf("modrinth projects need a server type with a mods directory")
	}

	// The property override wins, else the install itself testifies
	facets := []string{strVal(cfg.ModrinthLoader)}
	if facets[0] == "" {
		facets = minecraft.DialectFacets(minecraft.ResolveDialects(server.ModLoader, server.DataPath, modsDir))
	}
	if len(facets) == 0 {
		return fmt.Errorf("modrinth projects need the Modrinth Loader property set for this server")
	}
	loaderName := facets[0]

	depMode := strVal(cfg.ModrinthDownloadDependencies)
	versionType := strVal(cfg.ModrinthProjectsDefaultVersionType)
	if versionType == "" {
		versionType = "release"
	}

	state := readModrinthState(server.DataPath)
	stateDirty := false
	var client *modrinth.Client
	visited := map[string]bool{}
	queue := append([]string{}, projects...)
	var installErr error

	for len(queue) > 0 {
		project := queue[0]
		queue = queue[1:]
		if project == "" || visited[project] {
			continue
		}
		visited[project] = true

		// Recorded installs with jars on disk need no network
		if !force {
			if entry, ok := state.Projects[project]; ok &&
				entry.McVersion == mcVersion && entry.Loader == loaderName &&
				fileExists(filepath.Join(modsDir, entry.FileName)) {
				if depMode == "required" || depMode == "optional" {
					queue = append(queue, entry.RequiredDeps...)
					if depMode == "optional" {
						queue = append(queue, entry.OptionalDeps...)
					}
				}
				continue
			}
		}

		if client == nil {
			client = modrinth.NewClient(p.cfg.Server.UserAgent)
		}
		versions, err := client.GetProjectVersionsFiltered(ctx, project, facets, []string{mcVersion})
		if err != nil {
			installErr = fmt.Errorf("failed to resolve Modrinth project %q: %w", project, err)
			break
		}
		if len(versions) == 0 {
			installErr = fmt.Errorf("Modrinth project %q has no version for %s %s", project, loaderName, mcVersion)
			break
		}
		pick := pickAllowedVersion(versions, versionType)
		if pick == nil {
			installErr = fmt.Errorf("Modrinth project %q has no %s versions for %s %s (available: %s), adjust the Modrinth Default Version Type property",
				project, versionType, loaderName, mcVersion, strings.Join(versionTypesOf(versions), ", "))
			break
		}

		file := primaryFile(pick)
		if file == nil {
			continue
		}

		dest := filepath.Join(modsDir, file.Filename)
		if force || !fileExists(dest) {
			p.progress(server, "installing mod %s (%s)...", project, pick.VersionNumber)
			if err := p.download(ctx, file.URL, dest, mrChecksum(file.Hashes), nil, nil); err != nil {
				installErr = fmt.Errorf("failed to download Modrinth project %q: %w", project, err)
				break
			}
		}

		var requiredDeps, optionalDeps []string
		for _, dep := range pick.Dependencies {
			if dep.ProjectID == nil {
				continue
			}
			switch dep.DependencyType {
			case "required":
				requiredDeps = append(requiredDeps, *dep.ProjectID)
			case "optional":
				optionalDeps = append(optionalDeps, *dep.ProjectID)
			}
		}
		state.Projects[project] = modrinthProjectState{
			VersionID:    pick.ID,
			FileName:     file.Filename,
			McVersion:    mcVersion,
			Loader:       loaderName,
			RequiredDeps: requiredDeps,
			OptionalDeps: optionalDeps,
		}
		stateDirty = true

		if depMode == "required" || depMode == "optional" {
			queue = append(queue, requiredDeps...)
			if depMode == "optional" {
				queue = append(queue, optionalDeps...)
			}
		}
	}

	if stateDirty {
		if err := writeModrinthState(server.DataPath, state); err != nil && installErr == nil {
			installErr = fmt.Errorf("failed to record installed Modrinth projects: %w", err)
		}
	}
	return installErr
}
