package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

const (
	fabricMetaURL = "https://meta.fabricmc.net/v2"
	quiltMetaURL  = "https://meta.quiltmc.org/v3"
	paperFillURL  = "https://fill.papermc.io/v3"
	purpurAPIURL  = "https://api.purpurmc.org/v2/purpur"
)

// Downloads the Mojang server jar for the MC version
func (p *Provisioner) installVanilla(ctx context.Context, server *v1.Server) (*Result, error) {
	if err := p.downloadVanillaJar(ctx, server, server.McVersion, "server.jar"); err != nil {
		return nil, err
	}
	spec := &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "server.jar"}
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_VANILLA, "", server.McVersion)
}

// Fetches SHA1 verified official server jar to relPath
func (p *Provisioner) downloadVanillaJar(ctx context.Context, server *v1.Server, mcVersion, relPath string) error {
	meta, err := minecraft.GetVersionMetadata(mcVersion)
	if err != nil {
		return err
	}
	if meta.Downloads.Server.URL == "" {
		return fmt.Errorf("Mojang publishes no server jar for MC %s", mcVersion)
	}
	p.progress(server, "downloading vanilla server jar for MC %s...", mcVersion)
	return p.download(ctx, meta.Downloads.Server.URL, joinData(server.DataPath, relPath),
		&checksum{algo: "sha1", value: meta.Downloads.Server.SHA1}, nil, p.reporter(server, relPath))
}

// Downloads Fabric launcher and pre-seeds the vanilla jar
func (p *Provisioner) installFabric(ctx context.Context, server *v1.Server, loaderVersion string) (*Result, error) {
	mc := server.McVersion

	if loaderVersion == "" {
		var loaders []struct {
			Loader struct {
				Version string `json:"version"`
				Stable  bool   `json:"stable"`
			} `json:"loader"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/versions/loader/%s", fabricMetaURL, mc), &loaders); err != nil {
			return nil, fmt.Errorf("failed to resolve Fabric loader versions: %w", err)
		}
		if len(loaders) == 0 {
			return nil, fmt.Errorf("Fabric has no loader builds for MC %s", mc)
		}
		for _, l := range loaders {
			if l.Loader.Stable {
				loaderVersion = l.Loader.Version
				break
			}
		}
		if loaderVersion == "" {
			loaderVersion = loaders[0].Loader.Version
		}
	}

	var installers []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := p.getJSON(ctx, fabricMetaURL+"/versions/installer", &installers); err != nil {
		return nil, fmt.Errorf("failed to resolve Fabric installer versions: %w", err)
	}
	installerVersion := ""
	for _, i := range installers {
		if i.Stable {
			installerVersion = i.Version
			break
		}
	}
	if installerVersion == "" && len(installers) > 0 {
		installerVersion = installers[0].Version
	}
	if installerVersion == "" {
		return nil, fmt.Errorf("no Fabric installer versions available")
	}

	p.progress(server, "downloading Fabric server launcher (loader %s)...", loaderVersion)
	launcherURL := fmt.Sprintf("%s/versions/loader/%s/%s/%s/server/jar", fabricMetaURL, mc, loaderVersion, installerVersion)
	if err := p.download(ctx, launcherURL, joinData(server.DataPath, "fabric-server-launch.jar"), nil, nil, p.reporter(server, "fabric-server-launch.jar")); err != nil {
		return nil, err
	}

	// Pre-seed vanilla jar so launcher skips boot download
	if err := p.downloadVanillaJar(ctx, server, mc, "server.jar"); err != nil {
		return nil, err
	}
	launcherProps := "serverJar=server.jar\n"
	if err := os.WriteFile(joinData(server.DataPath, "fabric-server-launcher.properties"), []byte(launcherProps), 0644); err != nil {
		return nil, err
	}

	spec := &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "fabric-server-launch.jar"}
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_FABRIC, loaderVersion, mc)
}

// Runs the Quilt installer in a one-shot container
func (p *Provisioner) installQuilt(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, loaderVersion string) (*Result, error) {
	mc := server.McVersion

	if loaderVersion == "" {
		var loaders []struct {
			Loader struct {
				Version string `json:"version"`
			} `json:"loader"`
		}
		if err := p.getJSON(ctx, fmt.Sprintf("%s/versions/loader/%s", quiltMetaURL, mc), &loaders); err != nil {
			return nil, fmt.Errorf("failed to resolve Quilt loader versions: %w", err)
		}
		if len(loaders) == 0 {
			return nil, fmt.Errorf("Quilt has no loader builds for MC %s", mc)
		}
		// Newest first, prefer newest non-prerelease version
		for _, l := range loaders {
			if !strings.Contains(l.Loader.Version, "-") {
				loaderVersion = l.Loader.Version
				break
			}
		}
		if loaderVersion == "" {
			loaderVersion = loaders[0].Loader.Version
		}
	}

	var installers []struct {
		Version string `json:"version"`
		URL     string `json:"url"`
	}
	if err := p.getJSON(ctx, quiltMetaURL+"/versions/installer", &installers); err != nil {
		return nil, fmt.Errorf("failed to resolve Quilt installer versions: %w", err)
	}
	if len(installers) == 0 {
		return nil, fmt.Errorf("no Quilt installer versions available")
	}
	installer := installers[0]

	installerRel := filepath.Join(".discopanel", "installers", "quilt-installer.jar")
	p.progress(server, "downloading Quilt installer %s...", installer.Version)
	if err := p.download(ctx, installer.URL, joinData(server.DataPath, installerRel), nil, nil, p.reporter(server, "quilt installer")); err != nil {
		return nil, err
	}

	p.progress(server, "running Quilt installer (loader %s)...", loaderVersion)
	cmd := []string{"java", "-jar", filepath.ToSlash(installerRel), "install", "server", mc, loaderVersion, "--install-dir=.", "--download-server"}
	if err := p.runInstallerContainer(ctx, server, cfg, cmd); err != nil {
		return nil, fmt.Errorf("Quilt installer failed: %w", err)
	}

	spec := &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "quilt-server-launch.jar"}
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_QUILT, loaderVersion, mc)
}

// Provisions paper or folia from the Fill v3 API
func (p *Provisioner) installPaperMC(ctx context.Context, server *v1.Server, project string) (*Result, error) {
	mc := server.McVersion

	var build struct {
		ID        int    `json:"id"`
		Channel   string `json:"channel"`
		Downloads map[string]struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			Checksums struct {
				SHA256 string `json:"sha256"`
			} `json:"checksums"`
		} `json:"downloads"`
	}
	buildURL := fmt.Sprintf("%s/projects/%s/versions/%s/builds/latest", paperFillURL, project, mc)
	if err := p.getJSON(ctx, buildURL, &build); err != nil {
		return nil, fmt.Errorf("failed to resolve %s build for MC %s (is this version supported?): %w", project, mc, err)
	}

	dl, ok := build.Downloads["server:default"]
	if !ok {
		return nil, fmt.Errorf("%s build %d for MC %s has no server download", project, build.ID, mc)
	}

	p.progress(server, "downloading %s build %d for MC %s...", project, build.ID, mc)
	if err := p.download(ctx, dl.URL, joinData(server.DataPath, "server.jar"),
		&checksum{algo: "sha256", value: dl.Checksums.SHA256}, nil, p.reporter(server, "server.jar")); err != nil {
		return nil, err
	}

	loader := v1.ModLoader_MOD_LOADER_PAPER
	if project == "folia" {
		loader = v1.ModLoader_MOD_LOADER_FOLIA
	}
	spec := &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "server.jar"}
	return p.finishLaunch(server, spec, loader, fmt.Sprintf("%d", build.ID), mc)
}

// Provisions Purpur from the purpurmc api
func (p *Provisioner) installPurpur(ctx context.Context, server *v1.Server) (*Result, error) {
	mc := server.McVersion

	var versionInfo struct {
		Builds struct {
			Latest string `json:"latest"`
		} `json:"builds"`
	}
	if err := p.getJSON(ctx, fmt.Sprintf("%s/%s", purpurAPIURL, mc), &versionInfo); err != nil {
		return nil, fmt.Errorf("failed to resolve Purpur builds for MC %s (is this version supported?): %w", mc, err)
	}
	buildNum := versionInfo.Builds.Latest
	if buildNum == "" {
		return nil, fmt.Errorf("Purpur has no builds for MC %s", mc)
	}

	var buildInfo struct {
		Build  string `json:"build"`
		Result string `json:"result"`
		MD5    string `json:"md5"`
	}
	if err := p.getJSON(ctx, fmt.Sprintf("%s/%s/%s", purpurAPIURL, mc, buildNum), &buildInfo); err != nil {
		return nil, err
	}
	if buildInfo.Result != "SUCCESS" {
		return nil, fmt.Errorf("latest Purpur build %s for MC %s is not a successful build", buildNum, mc)
	}

	p.progress(server, "downloading Purpur build %s for MC %s...", buildNum, mc)
	if err := p.download(ctx, fmt.Sprintf("%s/%s/%s/download", purpurAPIURL, mc, buildNum),
		joinData(server.DataPath, "server.jar"), &checksum{algo: "md5", value: buildInfo.MD5}, nil, p.reporter(server, "server.jar")); err != nil {
		return nil, err
	}

	spec := &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "server.jar"}
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_PURPUR, buildNum, mc)
}

// Provisions from a user supplied jar or exec string
func (p *Provisioner) installCustom(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) (*Result, error) {
	customServer := strVal(cfg.CustomServer)
	customExec := strVal(cfg.CustomJarExec)

	jarRel := ""
	if customServer != "" {
		if strings.Contains(customServer, "://") {
			jarRel = filepath.Base(customServer)
			if !strings.HasSuffix(jarRel, ".jar") {
				jarRel = "server.jar"
			}
			p.progress(server, "downloading custom server jar from %s...", customServer)
			if err := p.download(ctx, customServer, joinData(server.DataPath, jarRel), nil, nil, p.reporter(server, jarRel)); err != nil {
				return nil, err
			}
		} else {
			jarRel = strings.TrimPrefix(filepath.ToSlash(customServer), "/data/")
			if !fileExists(joinData(server.DataPath, jarRel)) {
				return nil, fmt.Errorf("custom server jar %q not found in the server data directory", jarRel)
			}
		}
	}

	var spec *v1.LaunchSpec
	switch {
	case customExec != "":
		spec = &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_CUSTOM, Exec: customExec}
	case jarRel != "":
		spec = &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: filepath.ToSlash(jarRel)}
	case fileExists(joinData(server.DataPath, "server.jar")):
		spec = &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: "server.jar"}
	default:
		return nil, fmt.Errorf("custom server requires a Custom Server JAR (URL or data-dir path) or a Custom JAR Execution command")
	}

	return p.finishLaunch(server, spec, server.ModLoader, "", server.McVersion)
}
