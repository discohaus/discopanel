// Prepares server data directories, containers never install
package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Receives human-readable provisioning progress lines for a server
type ProgressSink func(serverID string, message string)

type Provisioner struct {
	store  *storage.Store
	docker *docker.Client
	cfg    *config.Config
	log    *logger.Logger
	rec    *metrics.Recorder
	sink   ProgressSink

	mu     sync.Mutex
	active map[string]*sync.Mutex // Per-server provisioning locks
}

// Reports what a successful provision resolved
type Result struct {
	Loader        v1.ModLoader
	LoaderVersion string
	McVersion     string
	JavaMajor     int
}

func New(store *storage.Store, dockerClient *docker.Client, cfg *config.Config, rec *metrics.Recorder, log *logger.Logger) *Provisioner {
	return &Provisioner{
		store:  store,
		docker: dockerClient,
		cfg:    cfg,
		log:    log,
		rec:    rec,
		active: make(map[string]*sync.Mutex),
	}
}

// Registers a sink for progress lines, e.g. log streamer
func (p *Provisioner) SetProgressSink(sink ProgressSink) {
	p.sink = sink
}

func (p *Provisioner) progress(server *v1.Server, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	p.log.Info("provisioner [%s]: %s", server.Name, msg)
	if p.sink != nil {
		p.sink(server.Id, msg)
	}
}

// Progress line that also lands in the activity ledger
func (p *Provisioner) action(ctx context.Context, server *v1.Server, source string, kind v1.ServerActionKind, attrs metrics.Attrs, format string, args ...any) {
	p.progress(server, format, args...)
	if p.rec != nil {
		p.rec.Record(metrics.WithSource(ctx, source), server.Id, kind, attrs, format, args...)
	}
}

func (p *Provisioner) serverLock(serverID string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.active[serverID]; ok {
		return l
	}
	l := &sync.Mutex{}
	p.active[serverID] = l
	return l
}

// Identifies the modpack a server should be provisioned from
type desiredModpack struct {
	source    optionsv1.PackSource
	id        string
	versionID string // Empty means whatever is installed or latest
}

// Idempotently provisions server files and applies config
func (p *Provisioner) Ensure(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) (*Result, error) {
	lock := p.serverLock(server.Id)
	lock.Lock()
	defer lock.Unlock()

	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create server data directory: %w", err)
	}

	// Free gate belongs before any multi gigabyte install
	if !strings.EqualFold(strVal(cfg.Eula), "true") {
		return nil, fmt.Errorf("the Minecraft EULA must be accepted before the server can start")
	}

	force := cfg.ForceProvision != nil && *cfg.ForceProvision
	manifest, err := runtimespec.ReadManifest(server.DataPath)
	if err != nil {
		p.progress(server, "provision manifest unreadable (%v), re-provisioning", err)
		manifest = nil
	}

	desired := p.desiredModpackFor(server, cfg)

	var result *Result
	if p.needsInstall(server, manifest, desired, force, pinnedLoaderVersion(server, cfg)) {
		p.snapshotWorld(server)
		result, err = p.install(ctx, server, cfg, desired, force)
		if err != nil {
			return nil, err
		}

		mref := (*v1.ModpackRef)(nil)
		if desired != nil {
			mref = &v1.ModpackRef{
				Source:    desired.source,
				Id:        desired.id,
				VersionId: desired.versionID,
			}
		}
		if err := runtimespec.WriteManifest(server.DataPath, &v1.Manifest{
			Version:       1,
			Loader:        server.ModLoader,
			LoaderVersion: result.LoaderVersion,
			McVersion:     result.McVersion,
			JavaMajor:     int32(result.JavaMajor),
			Modpack:       mref,
			ProvisionedAt: timestamppb.Now(),
		}); err != nil {
			return nil, fmt.Errorf("failed to write provision manifest: %w", err)
		}
		p.action(ctx, server, "provisioner", v1.ServerActionKind_SERVER_ACTION_KIND_PROVISION_INSTALL,
			metrics.Attrs{"loader": protometa.Name(result.Loader), "loader_version": result.LoaderVersion, "mc_version": result.McVersion},
			"installed server files (%s %s, MC %s, Java %d)",
			protometa.Name(result.Loader), result.LoaderVersion, result.McVersion, result.JavaMajor)
	} else {
		result = &Result{
			Loader:        manifest.Loader,
			LoaderVersion: manifest.LoaderVersion,
			McVersion:     manifest.McVersion,
			JavaMajor:     int(manifest.JavaMajor),
		}
		p.progress(server, "server files verified (%s %s, MC %s, Java %d)",
			protometa.Name(result.Loader), result.LoaderVersion, result.McVersion, result.JavaMajor)
	}

	// Pack-managed mods get the client-only sweep every pass
	if desired != nil {
		p.disableClientOnlyMods(ctx, server, storage.ForceIncludePatterns(cfg), nil)
	}

	// Config files are cheap and authoritative, always applied
	if err := p.applyConfigFiles(ctx, server, cfg, result.McVersion, force); err != nil {
		return nil, fmt.Errorf("failed to apply configuration files: %w", err)
	}

	return result, nil
}

// Pinned loader version for loaders that honor pins
func pinnedLoaderVersion(server *v1.Server, cfg *v1.ServerProperties) string {
	switch server.ModLoader {
	case v1.ModLoader_MOD_LOADER_FORGE, v1.ModLoader_MOD_LOADER_NEOFORGE:
		return strVal(cfg.ForgeVersion)
	}
	return ""
}

func (p *Provisioner) needsInstall(server *v1.Server, manifest *v1.Manifest, desired *desiredModpack, force bool, pinned string) bool {
	if force || manifest == nil {
		return true
	}
	if manifest.Loader != server.ModLoader {
		return true
	}
	// Pinned loader version changes force a reinstall
	if pinned != "" && manifest.LoaderVersion != pinned {
		return true
	}
	// Modpack servers derive MC version from the pack
	if desired == nil && manifest.McVersion != server.McVersion {
		return true
	}

	var installed *v1.ModpackRef = manifest.Modpack
	switch {
	case desired == nil && installed != nil,
		desired != nil && installed == nil:
		return true
	case desired != nil && installed != nil:
		if desired.source != installed.Source || desired.id != installed.Id {
			return true
		}
		if desired.versionID != "" && desired.versionID != installed.VersionId {
			return true
		}
	}

	// The launch target must still exist on disk
	spec, err := runtimespec.ReadLaunchSpec(server.DataPath)
	if err != nil {
		return true
	}
	return !launchTargetExists(server.DataPath, spec)
}

// Archives world dirs before a reinstall rewrites the tree
func (p *Provisioner) snapshotWorld(server *v1.Server) {
	if p.cfg == nil || p.cfg.Storage.BackupDir == "" {
		return
	}
	worldDirs, err := files.FindWorldDirs(server.DataPath)
	if err != nil || len(worldDirs) == 0 {
		return
	}
	rels := make([]string, 0, len(worldDirs))
	for _, w := range worldDirs {
		if rel, err := filepath.Rel(server.DataPath, w); err == nil {
			rels = append(rels, rel)
		}
	}
	destDir := filepath.Join(p.cfg.Storage.BackupDir, filepath.Base(server.DataPath))
	if err := os.MkdirAll(destDir, 0755); err != nil {
		p.log.Warn("Pre-provision snapshot skipped: %v", err)
		return
	}
	dest := filepath.Join(destDir, fmt.Sprintf("pre-provision_%s.zip", time.Now().UTC().Format("20060102-150405")))
	if _, err := files.CreateZipArchive(rels, server.DataPath, dest, true); err != nil {
		p.log.Warn("Pre-provision snapshot failed: %v", err)
		return
	}
	p.progress(server, "world snapshot saved (%s)", filepath.Base(dest))
}

// Derives the modpack identity from server and config
func (p *Provisioner) desiredModpackFor(server *v1.Server, cfg *v1.ServerProperties) *desiredModpack {
	source := minecraft.PackSourceFor(server.ModLoader)
	if source == optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED {
		return nil
	}
	// Staged archives win on every pack platform
	if v := strVal(cfg.CfModpackZip); v != "" {
		return &desiredModpack{source: optionsv1.PackSource_PACK_SOURCE_ZIP, id: v}
	}
	switch source {
	case optionsv1.PackSource_PACK_SOURCE_CURSEFORGE:
		slug, fileID := parseCurseForgeRef(strVal(cfg.CfPageUrl), strVal(cfg.CfSlug), strVal(cfg.CfFileId))
		return &desiredModpack{source: optionsv1.PackSource_PACK_SOURCE_CURSEFORGE, id: slug, versionID: fileID}
	case optionsv1.PackSource_PACK_SOURCE_MODRINTH:
		project, version := parseModrinthRef(strVal(cfg.ModrinthModpack), strVal(cfg.ModrinthVersion))
		return &desiredModpack{source: optionsv1.PackSource_PACK_SOURCE_MODRINTH, id: project, versionID: version}
	default:
		return &desiredModpack{source: source}
	}
}

type installFunc func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error)

// One native install path per loader, absence means user files
var loaderInstallers = map[v1.ModLoader]installFunc{
	v1.ModLoader_MOD_LOADER_VANILLA: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installVanilla(ctx, server)
	},
	v1.ModLoader_MOD_LOADER_FABRIC: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installFabric(ctx, server, "")
	},
	v1.ModLoader_MOD_LOADER_QUILT: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installQuilt(ctx, server, cfg, "")
	},
	v1.ModLoader_MOD_LOADER_FORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installForge(ctx, server, cfg, strVal(cfg.ForgeVersion))
	},
	v1.ModLoader_MOD_LOADER_NEOFORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installNeoForge(ctx, server, cfg, strVal(cfg.ForgeVersion))
	},
	v1.ModLoader_MOD_LOADER_PAPER: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPaperMC(ctx, server, "paper")
	},
	v1.ModLoader_MOD_LOADER_FOLIA: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPaperMC(ctx, server, "folia")
	},
	v1.ModLoader_MOD_LOADER_PURPUR: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPurpur(ctx, server)
	},
	v1.ModLoader_MOD_LOADER_AUTO_CURSEFORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPack(ctx, server, cfg, desired, force)
	},
	v1.ModLoader_MOD_LOADER_CURSEFORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPack(ctx, server, cfg, desired, force)
	},
	v1.ModLoader_MOD_LOADER_MODRINTH: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installPack(ctx, server, cfg, desired, force)
	},
	v1.ModLoader_MOD_LOADER_CUSTOM: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
		return p.installCustom(ctx, server, cfg)
	},
}

// Reports whether the loader installs without user files
func HasNativeInstaller(loader v1.ModLoader) bool {
	_, ok := loaderInstallers[loader]
	return ok
}

// Loader installs that take a pinned version, packs use these
var packLoaderInstallers = map[v1.ModLoader]func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, version string) (*Result, error){
	v1.ModLoader_MOD_LOADER_FABRIC: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, version string) (*Result, error) {
		return p.installFabric(ctx, server, version)
	},
	v1.ModLoader_MOD_LOADER_QUILT: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, version string) (*Result, error) {
		return p.installQuilt(ctx, server, cfg, version)
	},
	v1.ModLoader_MOD_LOADER_FORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, version string) (*Result, error) {
		return p.installForge(ctx, server, cfg, version)
	},
	v1.ModLoader_MOD_LOADER_NEOFORGE: func(p *Provisioner, ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, version string) (*Result, error) {
		return p.installNeoForge(ctx, server, cfg, version)
	},
}

// Installs a pack's pinned loader, the pack MC version wins
func (p *Provisioner) installLoaderForPack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, loader v1.ModLoader, version, mcVersion string) (*Result, error) {
	fn, ok := packLoaderInstallers[loader]
	if !ok {
		return nil, fmt.Errorf("packs cannot install loader %q", protometa.Name(loader))
	}
	packServer, _ := proto.Clone(server).(*v1.Server)
	if mcVersion != "" {
		packServer.McVersion = mcVersion
	}
	result, err := fn(p, ctx, packServer, cfg, version)
	if err != nil {
		return nil, err
	}
	// Reports pack platform as loader, keeps resolved version
	result.Loader = server.ModLoader
	return result, nil
}

// Performs the actual installation for the server's loader
func (p *Provisioner) install(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, desired *desiredModpack, force bool) (*Result, error) {
	p.progress(server, "provisioning %s server (MC %s)...", protometa.Name(server.ModLoader), server.McVersion)
	p.pruneCaches()

	if fn, ok := loaderInstallers[server.ModLoader]; ok {
		return fn(p, ctx, server, cfg, desired, force)
	}
	// Loaders without a native installer still use custom-jar
	if strVal(cfg.CustomServer) != "" || strVal(cfg.CustomJarExec) != "" {
		return p.installCustom(ctx, server, cfg)
	}
	// Uploaded FTB server files testify their pack
	if packID, versionID, ok := ftbInstallerRefDir(server.DataPath); ok {
		p.progress(server, "found an FTB installer stub, using the FTB api...")
		return p.installFTBPack(ctx, server, cfg, packID, versionID, force)
	}
	// User staged server files can still testify a launch
	if spec := detectPackLaunch(server.DataPath); spec != nil {
		p.progress(server, "found launchable server files for %s", protometa.Name(server.ModLoader))
		p.adoptServerPackVersion(ctx, server, spec)
		return p.finishLaunch(server, spec, server.ModLoader, "", server.McVersion)
	}
	return nil, fmt.Errorf(
		"mod loader %q has no native DiscoPanel installer: upload your server files to the data directory and set the Custom Server JAR or Custom JAR Execution fields",
		protometa.Name(server.ModLoader))
}

// Computes java requirement, writes launch spec, builds Result
func (p *Provisioner) finishLaunch(server *v1.Server, spec *v1.LaunchSpec, loader v1.ModLoader, loaderVersion, mcVersion string) (*Result, error) {
	javaMajor := docker.RequiredJavaMajor(mcVersion)
	spec.Version = 1
	spec.Loader = loader
	spec.McVersion = mcVersion
	spec.JavaMajor = int32(javaMajor)

	if !launchTargetExists(server.DataPath, spec) {
		return nil, fmt.Errorf("launch target %q missing after installation", launchTarget(spec))
	}
	if err := runtimespec.WriteLaunchSpec(server.DataPath, spec); err != nil {
		return nil, fmt.Errorf("failed to write launch spec: %w", err)
	}

	return &Result{
		Loader:        loader,
		LoaderVersion: loaderVersion,
		McVersion:     mcVersion,
		JavaMajor:     javaMajor,
	}, nil
}

func launchTarget(spec *v1.LaunchSpec) string {
	switch spec.Kind {
	case v1.LaunchKind_LAUNCH_KIND_JAR:
		return spec.Jar
	case v1.LaunchKind_LAUNCH_KIND_ARGS_FILE:
		return spec.ArgsFile
	default:
		return spec.Exec
	}
}

func launchTargetExists(dataPath string, spec *v1.LaunchSpec) bool {
	switch spec.Kind {
	case v1.LaunchKind_LAUNCH_KIND_JAR:
		return fileExists(joinData(dataPath, spec.Jar))
	case v1.LaunchKind_LAUNCH_KIND_ARGS_FILE:
		return fileExists(joinData(dataPath, spec.ArgsFile))
	case v1.LaunchKind_LAUNCH_KIND_CUSTOM:
		return spec.Exec != ""
	default:
		return false
	}
}

// Pack metadata client flags add evidence beside jar metadata
func (p *Provisioner) disableClientOnlyMods(ctx context.Context, server *v1.Server, forceIncludes, packFlagged []string) {
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return
	}
	metas := slices.Clone(minecraft.ScanModsDir(modsDir))
	for i := range metas {
		if !metas[i].ClientOnly && minecraft.MatchesPatterns(metas[i].FileName, packFlagged) {
			metas[i].ClientOnly = true
		}
	}
	for _, meta := range minecraft.ClientOnlySweep(metas, forceIncludes) {
		if err := minecraft.DisableModJar(modsDir, meta.FileName); err != nil {
			p.progress(server, "could not disable client-only mod %s (%v)", meta.FileName, err)
			continue
		}
		p.action(ctx, server, "mod check", v1.ServerActionKind_SERVER_ACTION_KIND_MOD_DISABLE, metrics.Attrs{"file": meta.FileName, "reason": "client-only"}, "disabled client-only mod %s", meta.FileName)
	}
}

func (p *Provisioner) runInstallerContainer(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, cmd []string) error {
	javaMajor := docker.RequiredJavaMajor(server.McVersion)
	image := p.docker.RuntimeImage(javaMajor)

	uid := 1000
	gid := 1000
	if cfg.Uid != nil {
		uid = int(*cfg.Uid)
	}
	if cfg.Gid != nil {
		gid = int(*cfg.Gid)
	}

	opts := docker.OneShotOptions{
		Image:      image,
		Cmd:        cmd,
		DataPath:   server.DataPath,
		WorkingDir: "/data",
		User:       fmt.Sprintf("%d:%d", uid, gid),
		Name:       fmt.Sprintf("discopanel-install-%s", server.Id),
		Labels: map[string]string{
			"discopanel.server.id": server.Id,
			"discopanel.managed":   "true",
			"discopanel.oneshot":   "true",
		},
	}

	return p.docker.RunOneShot(ctx, opts, func(line string) {
		p.progress(server, "[installer] %s", line)
	})
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func boolVal(b *bool) bool {
	return b != nil && *b
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
