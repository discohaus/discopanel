// ServerStarter style server packs driven by a yaml config
package provisioner

import (
	"archive/zip"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"gopkg.in/yaml.v3"
)

// The server-setup-config.yaml ServerStarter packs ship
type serverStarterConfig struct {
	Modpack struct {
		Name string `yaml:"name"`
	} `yaml:"modpack"`
	Install struct {
		McVersion      string `yaml:"mcVersion"`
		LoaderVersion  string `yaml:"loaderVersion"`
		ForgeVersion   string `yaml:"forgeVersion"` // Spec v1 name
		InstallerUrl   string `yaml:"installerUrl"`
		ModpackUrl     string `yaml:"modpackUrl"`
		ModpackFormat  string `yaml:"modpackFormat"`
		FormatSpecific struct {
			IgnoreProject []int `yaml:"ignoreProject"`
		} `yaml:"formatSpecific"`
	} `yaml:"install"`
}

// Finds server-setup-config.yaml at root or one dir deep
func readServerStarterConfig(reader *zip.Reader) (*serverStarterConfig, string) {
	data, prefix := readZipEntry(reader, "server-setup-config.yaml")
	if data == nil {
		return nil, ""
	}
	var ssc serverStarterConfig
	if yaml.Unmarshal(data, &ssc) != nil {
		return nil, ""
	}
	if ssc.Install.McVersion == "" && ssc.Install.ModpackUrl == "" {
		return nil, ""
	}
	return &ssc, prefix
}

// Loader facts a ServerStarter config declares
func ssEvidence(ssc *serverStarterConfig) packEvidence {
	ev := packEvidence{mcVersion: ssc.Install.McVersion}
	version := ssc.Install.LoaderVersion
	if version == "" {
		version = ssc.Install.ForgeVersion
	}

	// The installer url names the loader family
	loader := v1.ModLoader_MOD_LOADER_UNSPECIFIED
	url := strings.ToLower(ssc.Install.InstallerUrl)
	switch {
	case strings.Contains(url, "neoforged"):
		loader = v1.ModLoader_MOD_LOADER_NEOFORGE
	case strings.Contains(url, "minecraftforge"):
		loader = v1.ModLoader_MOD_LOADER_FORGE
	case strings.Contains(url, "fabricmc"):
		loader = v1.ModLoader_MOD_LOADER_FABRIC
	case strings.Contains(url, "quiltmc"):
		loader = v1.ModLoader_MOD_LOADER_QUILT
	case ssc.Install.ForgeVersion != "":
		loader = v1.ModLoader_MOD_LOADER_FORGE
	}
	if loader == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		return ev
	}
	return loaderEvidence(protometa.Name(loader), version, ev.mcVersion)
}

// Extracts server files, installs the referenced pack, then the loader
func (p *Provisioner) installFromServerStarter(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, ssc *serverStarterConfig, prefix string, opts packInstallOpts) (*Result, error) {
	excludes := p.packRuleExcludes(server, cfg, opts)
	if err := p.extractZipFiltered(reader, prefix, server.DataPath, !opts.force, excludes, nil); err != nil {
		return nil, err
	}

	url := strings.TrimSpace(ssc.Install.ModpackUrl)
	if url != "" && !strings.HasPrefix(strings.ToLower(url), "file:") && opts.depth < 2 {
		p.progress(server, "downloading the modpack this server config references...")
		dest := filepath.Join(installerDir(server.DataPath), "referenced-modpack.zip")
		if err := p.download(ctx, url, dest, nil, nil, p.reporter(server, "referenced modpack")); err != nil {
			return nil, err
		}

		next := opts
		next.depth++
		// Author project ignores guard the server from client mods
		for _, id := range ssc.Install.FormatSpecific.IgnoreProject {
			next.excludes = append(next.excludes, strconv.Itoa(id))
		}
		res, err := p.installPackArchive(ctx, server, cfg, dest, next)
		if err == nil || !errors.Is(err, errNoLaunchTarget) {
			return res, err
		}
		p.progress(server, "referenced pack ships no runtime, using the config's loader pin...")
	}

	return p.installPackRuntime(ctx, server, cfg, ssEvidence(ssc))
}
