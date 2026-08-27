// Pack pipeline, archive content decides installs not source
package provisioner

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Bounds concurrent modpack file downloads
const packDownloadConcurrency = 8

// Extracted pack has nothing runnable
var errNoLaunchTarget = errors.New("could not determine how to launch this server pack: no known server jar, args file, or bundled installer found")

// One downloadable pack archive candidate
type packPayload struct {
	label string
	fetch func(ctx context.Context) (string, error)
}

// Resolves a desired pack into ordered archive candidates
type payloadResolver func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack) ([]packPayload, error)

// Acquisition strategies keyed by pack source
var packPayloadResolvers = map[optionsv1.PackSource]payloadResolver{
	optionsv1.PackSource_PACK_SOURCE_ZIP:        resolveZipPayload,
	optionsv1.PackSource_PACK_SOURCE_CURSEFORGE: resolveCurseForgePayloads,
	optionsv1.PackSource_PACK_SOURCE_MODRINTH:   resolveModrinthPayloads,
}

// Loader facts a pack testifies about itself
type packEvidence struct {
	loaderID      string
	loader        v1.ModLoader
	loaderVersion string
	mcVersion     string
}

// Evidence from a manifest loader id like forge-47.2.0
func loaderIDEvidence(loaderID, mcVersion string) packEvidence {
	ev := packEvidence{loaderID: loaderID, mcVersion: mcVersion}
	if loader, version, ok := minecraft.CutPackLoaderID(loaderID); ok {
		ev.loader, ev.loaderVersion = loader, version
	}
	return ev
}

// Evidence from a loader name and version pin, prefixes stripped
func loaderEvidence(name, version, mcVersion string) packEvidence {
	name = strings.ToLower(name)
	return loaderIDEvidence(name+"-"+cleanLoaderVersion(version, mcVersion, name), mcVersion)
}

// Splits an mc prefix off pins like 1.20.1-47.2.0
func splitMCPrefix(version string) (string, string) {
	if mc, rest, ok := strings.Cut(version, "-"); ok && minecraft.IsReleaseVersion(mc) {
		return mc, rest
	}
	return "", version
}

// Install options threaded through pack format handlers
type packInstallOpts struct {
	force    bool
	depth    int      // Nested pack reference depth
	excludes []string // Extra excludes injected by wrapping formats
}

// Installs the first usable archive the source offers
func (p *Provisioner) installPack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
	resolve, ok := packPayloadResolvers[desired.source]
	if !ok {
		return nil, fmt.Errorf("no modpack configured for this server, set the modpack properties or upload a pack archive")
	}
	payloads, err := resolve(p, ctx, server, cfg, desired)
	if err != nil {
		return nil, err
	}
	if len(payloads) == 0 {
		return nil, fmt.Errorf("modpack %q offers no downloadable archive", desired.id)
	}

	var lastErr error
	for i, payload := range payloads {
		archive, err := payload.fetch(ctx)
		if err != nil {
			// Unfetchable candidates fall through to the next
			if i+1 < len(payloads) && ctx.Err() == nil {
				p.progress(server, "could not fetch %s (%v), trying %s...", payload.label, err, payloads[i+1].label)
				lastErr = err
				continue
			}
			return nil, err
		}
		res, err := p.installPackArchive(ctx, server, cfg, archive, packInstallOpts{force: force})
		if err != nil && errors.Is(err, errNoLaunchTarget) && i+1 < len(payloads) {
			p.progress(server, "%s has no launchable server, trying %s...", payload.label, payloads[i+1].label)
			lastErr = err
			continue
		}
		return res, err
	}
	return nil, lastErr
}

// Installs one archive, sniffed format picks the path
// Sniff order must stay mirrored in InspectPackArchive
func (p *Provisioner) installPackArchive(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, zipPath string, opts packInstallOpts) (*Result, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open pack archive %q: %w", filepath.Base(zipPath), err)
	}
	defer reader.Close()

	// FTB stubs point at the api holding real server files
	if packID, versionID, ok := ftbInstallerRef(&reader.Reader); ok {
		p.progress(server, "pack is an FTB installer stub, using the FTB api...")
		return p.installFTBPack(ctx, server, cfg, packID, versionID, opts.force)
	}
	if manifest, prefix := readFTBServerManifest(&reader.Reader); manifest != nil {
		p.progress(server, "pack carries an FTB server manifest...")
		return p.installFromFTBServerManifest(ctx, server, cfg, reader, manifest, prefix, opts)
	}
	if manifest, prefix := readCFManifest(&reader.Reader); manifest != nil {
		return p.installFromCFManifest(ctx, server, cfg, reader, manifest, prefix, opts)
	}
	if index, prefix := readMrpackIndex(&reader.Reader); index != nil {
		return p.installFromMrpackIndex(ctx, server, cfg, reader, index, prefix, opts)
	}
	if data, prefix := readZipEntry(&reader.Reader, "instance.json"); data != nil {
		if inst := parseATLInstance(data); inst != nil {
			p.progress(server, "pack is an ATLauncher instance export...")
			return p.installFromInstanceRoot(ctx, server, cfg, reader, prefix, atlEvidence(inst), opts)
		}
		if packID, versionID, ok := parseFTBAppInstance(data); ok {
			p.progress(server, "pack is an FTB app instance, using the FTB api...")
			return p.installFTBPack(ctx, server, cfg, packID, versionID, opts.force)
		}
	}
	if pack, prefix := readMMCPack(&reader.Reader); pack != nil {
		p.progress(server, "pack is a MultiMC style instance export...")
		return p.installFromMMCPack(ctx, server, cfg, reader, pack, prefix, opts)
	}
	if gdl, prefix := readGDLConfig(&reader.Reader); gdl != nil {
		p.progress(server, "pack is a GDLauncher instance export...")
		return p.installFromInstanceRoot(ctx, server, cfg, reader, prefix, gdlEvidence(gdl), opts)
	}
	if ssc, prefix := readServerStarterConfig(&reader.Reader); ssc != nil {
		p.progress(server, "pack uses a ServerStarter config...")
		return p.installFromServerStarter(ctx, server, cfg, reader, ssc, prefix, opts)
	}
	if hasTechnicLayout(&reader.Reader) {
		p.progress(server, "pack looks like a Technic pack, extracting...")
		if err := p.extractServerPack(reader, server.DataPath, !opts.force, p.packRuleExcludes(server, cfg, opts)); err != nil {
			return nil, err
		}
		return p.installPackRuntime(ctx, server, cfg, technicEvidence(server.DataPath))
	}

	// No manifest means ready made server pack, unpack wholesale
	p.progress(server, "extracting server pack...")
	if err := p.extractServerPack(reader, server.DataPath, !opts.force, p.packRuleExcludes(server, cfg, opts)); err != nil {
		return nil, err
	}
	return p.installPackRuntime(ctx, server, cfg, packEvidence{})
}

// User excludes merged with format injected excludes
func (p *Provisioner) packRuleExcludes(server *v1.Server, cfg *v1.ServerProperties, opts packInstallOpts) []string {
	return append(p.packExcludes(server, cfg), opts.excludes...)
}

// Staged archive in the data dir, any format
func resolveZipPayload(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack) ([]packPayload, error) {
	rel := strings.TrimPrefix(filepath.ToSlash(desired.id), "/data/")
	zipPath := joinData(server.DataPath, rel)
	if !fileExists(zipPath) {
		return nil, fmt.Errorf("modpack archive %q not found in the server data directory", rel)
	}
	return []packPayload{{
		label: filepath.Base(rel),
		fetch: func(context.Context) (string, error) { return zipPath, nil },
	}}, nil
}

// Provisions the runtime, declared loader first then disk evidence
func (p *Provisioner) installPackRuntime(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, ev packEvidence) (*Result, error) {
	// Pack MC evidence beats the configured guess from here on
	p.adoptMCVersion(ctx, server, ev.mcVersion)

	// Declared loader installs natively when supported
	if ev.loader != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		if _, ok := packLoaderInstallers[ev.loader]; ok {
			return p.installLoaderForPack(ctx, server, cfg, ev.loader, ev.loaderVersion)
		}
		p.progress(server, "pack declares loader %s with no native installer, checking shipped server files...", ev.loaderID)
	}

	// Explicit custom launch settings beat disk guessing
	if strVal(cfg.CustomServer) != "" || strVal(cfg.CustomJarExec) != "" {
		p.progress(server, "using the configured custom launch settings...")
		return p.installCustom(ctx, server, cfg)
	}

	// Pack may ship its runtime ready to launch
	if spec := detectPackLaunch(server.DataPath, server.ModLoader); spec != nil {
		p.adoptServerPackVersion(ctx, server, spec)
		return p.finishLaunch(server, spec, server.ModLoader, ev.loaderVersion, server.McVersion)
	}

	// Bundled installer jars produce the runtime on demand
	if installer := bundledInstaller(server.DataPath); installer != "" {
		p.progress(server, "running bundled installer %s...", installer)
		cmd := []string{"java", "-jar", installer, "--installServer"}
		if err := p.runInstallerContainer(ctx, server, cfg, cmd); err != nil {
			return nil, fmt.Errorf("bundled installer failed: %w", err)
		}
		if spec := detectPackLaunch(server.DataPath, server.ModLoader); spec != nil {
			p.adoptServerPackVersion(ctx, server, spec)
			return p.finishLaunch(server, spec, server.ModLoader, ev.loaderVersion, server.McVersion)
		}
	}

	// Start scripts and vars files often pin a loader
	if sev := scriptPackEvidence(server.DataPath, server.McVersion); sev.loader != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		if _, ok := packLoaderInstallers[sev.loader]; ok {
			p.adoptMCVersion(ctx, server, sev.mcVersion)
			p.progress(server, "pack scripts pin %s %s, installing...", protometa.Name(sev.loader), sev.loaderVersion)
			return p.installLoaderForPack(ctx, server, cfg, sev.loader, sev.loaderVersion)
		}
	}

	// Mod jars themselves testify a loader family last
	if ev.loaderID == "" {
		loader := minecraft.InferModsLoader(minecraft.GetModsPath(server.DataPath, server.ModLoader))
		if _, ok := packLoaderInstallers[loader]; ok {
			p.progress(server, "mods declare %s, installing the latest for MC %s...", protometa.Name(loader), server.McVersion)
			return p.installLoaderForPack(ctx, server, cfg, loader, "")
		}
	}

	if ev.loaderID != "" {
		return nil, fmt.Errorf("pack needs loader %q which has no native DiscoPanel installer and the pack ships no launchable server: upload the loader's server files or set the Custom Server JAR or Custom JAR Execution properties (%w)", ev.loaderID, errNoLaunchTarget)
	}
	return nil, errNoLaunchTarget
}

// Finds a launchable server the data dir already ships
func detectPackLaunch(dataPath string, loader v1.ModLoader) *v1.LaunchSpec {
	// A jar named after the declared loader outranks bundled trees
	if jar := rootLoaderJar(dataPath, protometa.Name(loader)); jar != "" {
		return jarLaunch(jar)
	}
	rows := minecraft.Loaders()
	for _, row := range rows {
		if spec := markerLaunch(dataPath, row.Markers); spec != nil {
			return spec
		}
	}
	for _, row := range rows {
		if jar := rootLoaderJar(dataPath, protometa.Name(row.Loader())); jar != "" {
			return jarLaunch(jar)
		}
	}
	if fileExists(filepath.Join(dataPath, "server.jar")) {
		return jarLaunch("server.jar")
	}
	if jar := loneRootJar(dataPath); jar != "" {
		return jarLaunch(jar)
	}
	return nil
}

// Launch entry the registry markers of one loader point at
func markerLaunch(dataPath string, markers []string) *v1.LaunchSpec {
	for _, marker := range markers {
		if strings.HasSuffix(marker, ".jar") {
			if fileExists(filepath.Join(dataPath, filepath.FromSlash(marker))) {
				return jarLaunch(marker)
			}
			continue
		}
		// Library markers hold artifact/version/unix_args.txt
		if spec := argsFileLaunch(dataPath, filepath.Join(filepath.FromSlash(marker), "*"), ""); spec != nil {
			return spec
		}
	}
	return nil
}

// Launch spec for a jar relative to the data dir
func jarLaunch(jar string) *v1.LaunchSpec {
	return &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: jar}
}

// Lone non installer root jar is the server
func loneRootJar(dataPath string) string {
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		return ""
	}
	jar := ""
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(name), ".jar") {
			continue
		}
		if strings.Contains(strings.ToLower(name), "installer") {
			continue
		}
		// Two candidates is ambiguous, never guess
		if jar != "" {
			return ""
		}
		jar = name
	}
	return jar
}

// First bundled installer jar at the data dir root
func bundledInstaller(dataPath string) string {
	matches, _ := filepath.Glob(filepath.Join(dataPath, "*installer*.jar"))
	if len(matches) == 0 {
		return ""
	}
	return filepath.Base(matches[0])
}

// Union of user excludes and doctor holds, lowercased
func (p *Provisioner) packExcludes(server *v1.Server, cfg *v1.ServerProperties) []string {
	out := minecraft.SplitPatterns(strVal(cfg.CfExcludeMods))
	out = append(out, minecraft.SplitPatterns(strVal(cfg.ModrinthExcludeFiles))...)
	for _, held := range append(runtimespec.DoctorExcludes(server.DataPath), runtimespec.IncidentHeldFiles(server.DataPath)...) {
		out = append(out, strings.ToLower(held))
	}
	return out
}

// Union of user force include patterns, lowercased
func packForceIncludes(cfg *v1.ServerProperties) []string {
	out := minecraft.SplitPatterns(strVal(cfg.CfForceIncludeMods))
	return append(out, minecraft.SplitPatterns(strVal(cfg.ModrinthForceIncludeFiles))...)
}

// Deterministic scratch path, extension follows the upstream name
func stagedArchivePath(dataPath, upstreamName string) string {
	ext := strings.ToLower(path.Ext(upstreamName))
	if ext == "" || len(ext) > 8 {
		ext = ".zip"
	}
	return filepath.Join(installerDir(dataPath), "modpack"+ext)
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

// Extracts entries under prefix from an open zip into destDir
func (p *Provisioner) extractZipPrefix(reader *zip.ReadCloser, prefix, destDir string, skipExisting bool, excludes []string) error {
	return p.extractZipFiltered(reader, prefix, destDir, skipExisting, excludes, nil)
}

// Extracts entries under prefix, an optional skip gates each
func (p *Provisioner) extractZipFiltered(reader *zip.ReadCloser, prefix, destDir string, skipExisting bool, excludes []string, skip func(rel string) bool) error {
	for _, f := range reader.File {
		if !strings.HasPrefix(f.Name, prefix) || f.Name == prefix {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		if skip != nil && skip(rel) {
			continue
		}
		if !f.FileInfo().IsDir() && minecraft.MatchesPatterns(path.Base(f.Name), excludes) {
			continue
		}
		target := joinData(destDir, rel)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if skipExisting && fileExists(target) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Adopts launch spec MC evidence over the user guess
func (p *Provisioner) adoptServerPackVersion(ctx context.Context, server *v1.Server, spec *v1.LaunchSpec) {
	p.adoptMCVersion(ctx, server, serverPackMCVersion(server.DataPath, spec))
}

// Persists an evidenced MC version onto the server
func (p *Provisioner) adoptMCVersion(ctx context.Context, server *v1.Server, evidence string) {
	if evidence == "" || evidence == server.McVersion {
		return
	}
	javaVersion := int32(docker.RequiredJavaMajor(evidence))
	p.action(ctx, server, "provisioner", v1.ServerActionKind_SERVER_ACTION_KIND_PROVISION_MC_VERSION,
		metrics.Attrs{"from": server.McVersion, "to": evidence},
		"pack ships MC %s, replacing configured %s", evidence, server.McVersion)
	if p.store != nil {
		if err := p.store.UpdateServerFields(ctx, server.Id, map[string]any{
			"mc_version":   evidence,
			"java_version": javaVersion,
		}); err != nil {
			p.progress(server, "warning: could not persist detected MC version: %v", err)
		}
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
	mc, _ := splitMCPrefix(segs[len(segs)-2])
	return mc
}

// Pack format names inspection reports
const (
	PackFormatFTB           = "ftb"
	PackFormatCurseForge    = "curseforge"
	PackFormatModrinth      = "modrinth"
	PackFormatATLauncher    = "atlauncher"
	PackFormatMultiMC       = "multimc"
	PackFormatGDLauncher    = "gdlauncher"
	PackFormatServerStarter = "serverstarter"
	PackFormatTechnic       = "technic"
	PackFormatServerPack    = "server-pack"
)

// Identity facts sniffed from a pack archive
type PackInspection struct {
	Format        string
	Name          string
	Version       string
	Author        string
	Summary       string
	McVersion     string
	LoaderID      string
	Loader        v1.ModLoader
	LoaderVersion string
}

// Builds an inspection from a format name and evidence
func evidenceInspection(format, name, version string, ev packEvidence) *PackInspection {
	return &PackInspection{
		Format:        format,
		Name:          name,
		Version:       version,
		McVersion:     ev.mcVersion,
		LoaderID:      ev.loaderID,
		Loader:        ev.loader,
		LoaderVersion: ev.loaderVersion,
	}
}

// Sniffs a pack archive without installing anything
// Mirrors the installPackArchive sniff order
func InspectPackArchive(archivePath string) (*PackInspection, error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("not a readable pack archive: %w", err)
	}
	defer reader.Close()

	if packID, versionID, ok := ftbInstallerRef(&reader.Reader); ok {
		return &PackInspection{
			Format:  PackFormatFTB,
			Name:    fmt.Sprintf("FTB pack %d", packID),
			Version: strconv.Itoa(versionID),
		}, nil
	}
	if manifest, _ := readFTBServerManifest(&reader.Reader); manifest != nil {
		return evidenceInspection(PackFormatFTB, manifest.Name, manifest.VersionName, ftbServerManifestEvidence(manifest)), nil
	}
	if manifest, _ := readCFManifest(&reader.Reader); manifest != nil {
		insp := evidenceInspection(PackFormatCurseForge, manifest.Name, manifest.Version, cfManifestEvidence(manifest))
		insp.Author = manifest.Author
		insp.Summary = manifest.Description
		return insp, nil
	}
	if index, _ := readMrpackIndex(&reader.Reader); index != nil {
		insp := evidenceInspection(PackFormatModrinth, index.Name, index.VersionID, mrpackEvidence(index))
		insp.Summary = index.Summary
		return insp, nil
	}
	if data, _ := readZipEntry(&reader.Reader, "instance.json"); data != nil {
		if inst := parseATLInstance(data); inst != nil {
			name := inst.Launcher.Pack
			if name == "" {
				name = inst.Launcher.Name
			}
			return evidenceInspection(PackFormatATLauncher, name, inst.Launcher.Version, atlEvidence(inst)), nil
		}
		if packID, versionID, ok := parseFTBAppInstance(data); ok {
			return &PackInspection{
				Format:  PackFormatFTB,
				Name:    fmt.Sprintf("FTB pack %d", packID),
				Version: strconv.Itoa(versionID),
			}, nil
		}
	}
	if pack, prefix := readMMCPack(&reader.Reader); pack != nil {
		return evidenceInspection(PackFormatMultiMC, mmcInstanceName(&reader.Reader, prefix), "", mmcEvidence(pack)), nil
	}
	if gdl, _ := readGDLConfig(&reader.Reader); gdl != nil {
		return evidenceInspection(PackFormatGDLauncher, "", "", gdlEvidence(gdl)), nil
	}
	if ssc, _ := readServerStarterConfig(&reader.Reader); ssc != nil {
		return evidenceInspection(PackFormatServerStarter, ssc.Modpack.Name, "", ssEvidence(ssc)), nil
	}
	if hasTechnicLayout(&reader.Reader) {
		return &PackInspection{Format: PackFormatTechnic}, nil
	}

	insp := &PackInspection{Format: PackFormatServerPack}
	ev := zipScriptEvidence(&reader.Reader, "")
	if ev.mcVersion == "" {
		ev.mcVersion = zipForgeArgsMC(&reader.Reader)
	}
	insp.McVersion = ev.mcVersion
	insp.LoaderID, insp.Loader, insp.LoaderVersion = ev.loaderID, ev.loader, ev.loaderVersion
	return insp, nil
}

// Script and vars files that may pin loader versions
var packScriptNames = map[string]bool{
	"startserver.sh": true, "startserver.bat": true,
	"serverstart.sh": true, "serverstart.bat": true,
	"start.sh": true, "run.sh": true,
	"variables.txt": true,
	"settings.cfg":  true, "settings.bat": true, "settings.sh": true,
}

// Returns the value assigned to a shell style variable
func scriptVar(s, key string) string {
	for from := 0; ; {
		i := strings.Index(s[from:], key+"=")
		if i < 0 {
			return ""
		}
		i += from
		from = i + len(key) + 1
		// Longer variable names also contain the key
		if i > 0 && isShellWordChar(s[i-1]) {
			continue
		}
		rest := s[from:]
		// Quoted assignments open with a quote
		rest = strings.TrimLeft(rest, "\"'")
		if end := strings.IndexAny(rest, " \t\r\n\"'"); end >= 0 {
			rest = rest[:end]
		}
		val := strings.TrimSpace(rest)
		// Unexpanded references carry no literal value
		if val == "" || strings.HasPrefix(val, "$") {
			continue
		}
		return val
	}
}

// Reports whether a byte can appear in a variable name
func isShellWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Loader pins named inside one script's content
func scriptEvidence(content, mcHint string) packEvidence {
	for _, name := range minecraft.PackLoaderNames() {
		if v := scriptVar(content, strings.ToUpper(name)+"_VERSION"); v != "" {
			mc, v := splitMCPrefix(cleanLoaderVersion(v, mcHint, name))
			return loaderEvidence(name, v, mc)
		}
	}
	// ServerPackCreator vars carry loader name and versions
	if ml := scriptVar(content, "MODLOADER"); ml != "" {
		return loaderEvidence(ml, scriptVar(content, "MODLOADER_VERSION"), scriptVar(content, "MINECRAFT_VERSION"))
	}
	// Legacy FTB settings name forge and mc directly
	if v := scriptVar(content, "FORGEVER"); v != "" {
		return loaderEvidence("forge", v, scriptVar(content, "MCVER"))
	}
	return packEvidence{}
}

// Reads data dir scripts for loader pins
func scriptPackEvidence(dataPath, mcHint string) packEvidence {
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		return packEvidence{}
	}
	for _, e := range entries {
		if e.IsDir() || !packScriptNames[strings.ToLower(e.Name())] {
			continue
		}
		if info, err := e.Info(); err != nil || info.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataPath, e.Name()))
		if err != nil {
			continue
		}
		if ev := scriptEvidence(string(data), mcHint); ev.loaderID != "" {
			return ev
		}
	}
	return packEvidence{}
}

// Reads start scripts inside a zip for loader pins
func zipScriptEvidence(reader *zip.Reader, mcHint string) packEvidence {
	for _, f := range reader.File {
		if !packScriptNames[strings.ToLower(path.Base(f.Name))] || f.UncompressedSize64 > 1<<20 {
			continue
		}
		data, err := readZipFileLimit(f, 1<<20)
		if err != nil {
			continue
		}
		if ev := scriptEvidence(string(data), mcHint); ev.loaderID != "" {
			return ev
		}
	}
	return packEvidence{}
}

// Reads MC evidence from forge library paths in a zip
func zipForgeArgsMC(reader *zip.Reader) string {
	for _, f := range reader.File {
		name := filepath.ToSlash(f.Name)
		if !strings.HasSuffix(name, "unix_args.txt") {
			continue
		}
		if v := forgeArgsMCVersion(name); v != "" {
			return v
		}
	}
	return ""
}
