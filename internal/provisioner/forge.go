package provisioner

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

const (
	forgePromotionsURL = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	forgeMavenURL      = "https://maven.minecraftforge.net/net/minecraftforge/forge"
	neoforgeMavenURL   = "https://maven.neoforged.net/releases/net/neoforged/neoforge"
	neoforgeVersionAPI = "https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge"
	neoforgeLegacyURL  = "https://maven.neoforged.net/releases/net/neoforged/forge"
	neoforgeLegacyAPI  = "https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/forge"
)

// Runs Forge installer then detects launch layout
func (p *Provisioner) installForge(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, forgeVersion string) (*Result, error) {
	mc := server.McVersion

	installerRel, resolvedVersion, err := p.fetchForgeInstaller(ctx, server, cfg, mc, forgeVersion)
	if err != nil {
		return nil, err
	}

	treeKey := libTreeKey("forge", mc, resolvedVersion)
	p.restoreLibTree(server, treeKey)

	p.progress(server, "running Forge %s installer (this can take a few minutes)...", resolvedVersion)
	cmd := []string{"java", "-jar", filepath.ToSlash(installerRel), "--installServer"}
	if err := p.runInstallerContainer(ctx, server, cfg, cmd); err != nil {
		return nil, fmt.Errorf("Forge installer failed: %w", err)
	}

	spec, err := detectForgeLaunch(server.DataPath, "minecraftforge/forge", resolvedVersion)
	if err != nil {
		return nil, err
	}
	p.saveLibTree(server, treeKey)
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_FORGE, resolvedVersion, mc)
}

// Resolves Forge version and downloads the installer jar
func (p *Provisioner) fetchForgeInstaller(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, mc, forgeVersion string) (string, string, error) {
	installerRel := filepath.Join(".discopanel", "installers", "forge-installer.jar")

	// User-provided installer path (relative to the data dir)
	if local := strVal(cfg.ForgeInstaller); local != "" {
		rel := strings.TrimPrefix(filepath.ToSlash(local), "/data/")
		if !fileExists(joinData(server.DataPath, rel)) {
			return "", "", fmt.Errorf("forge installer %q not found in the server data directory", rel)
		}
		return rel, forgeVersion, nil
	}

	// User-provided installer URL
	if u := strVal(cfg.ForgeInstallerUrl); u != "" {
		p.progress(server, "downloading Forge installer from %s...", u)
		if err := p.download(ctx, u, joinData(server.DataPath, installerRel), nil, nil, p.reporter(server, "forge installer")); err != nil {
			return "", "", err
		}
		return installerRel, forgeVersion, nil
	}

	if forgeVersion == "" {
		var promotions struct {
			Promos map[string]string `json:"promos"`
		}
		if err := p.getJSON(ctx, forgePromotionsURL, &promotions); err != nil {
			return "", "", fmt.Errorf("failed to fetch Forge promotions: %w", err)
		}
		forgeVersion = promotions.Promos[mc+"-recommended"]
		if forgeVersion == "" {
			forgeVersion = promotions.Promos[mc+"-latest"]
		}
		if forgeVersion == "" {
			return "", "", fmt.Errorf("Forge has no builds for MC %s", mc)
		}
	}

	// Old versions carry branch suffixes, resolve via maven metadata
	artifactVersion, err := p.resolveForgeMavenVersion(ctx, mc, forgeVersion)
	if err != nil {
		return "", "", err
	}

	installerURL := fmt.Sprintf("%s/%s/forge-%s-installer.jar", forgeMavenURL, artifactVersion, artifactVersion)
	sum, _ := p.fetchChecksumSidecar(ctx, installerURL, "sha256")

	p.progress(server, "downloading Forge %s installer...", forgeVersion)
	if err := p.download(ctx, installerURL, joinData(server.DataPath, installerRel), sum, nil, p.reporter(server, "forge installer")); err != nil {
		return "", "", err
	}
	return installerRel, forgeVersion, nil
}

// Finds the exact maven version dir for {mc}-{forge}
func (p *Provisioner) resolveForgeMavenVersion(ctx context.Context, mc, forgeVersion string) (string, error) {
	want := mc + "-" + forgeVersion

	body, err := p.getText(ctx, forgeMavenURL+"/maven-metadata.xml")
	if err != nil {
		// Metadata unavailable, fall back to plain naming scheme
		return want, nil
	}

	var metadata struct {
		Versioning struct {
			Versions struct {
				Version []string `xml:"version"`
			} `xml:"versions"`
		} `xml:"versioning"`
	}
	if err := xml.Unmarshal([]byte(body), &metadata); err != nil {
		return want, nil
	}

	for _, v := range metadata.Versioning.Versions.Version {
		if v == want {
			return v, nil
		}
	}
	for _, v := range metadata.Versioning.Versions.Version {
		if strings.HasPrefix(v, want+"-") {
			return v, nil
		}
	}
	return want, nil
}

// Resolves and runs the NeoForge installer
func (p *Provisioner) installNeoForge(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, neoVersion string) (*Result, error) {
	mc := server.McVersion

	mavenBase := neoforgeMavenURL
	artifact := "neoforge"
	if neoVersion == "" {
		var err error
		neoVersion, mavenBase, artifact, err = p.resolveNeoForgeVersion(ctx, mc)
		if err != nil {
			return nil, err
		}
	}

	installerRel := filepath.Join(".discopanel", "installers", "neoforge-installer.jar")
	installerURL := fmt.Sprintf("%s/%s/%s-%s-installer.jar", mavenBase, neoVersion, artifact, neoVersion)
	sum, _ := p.fetchChecksumSidecar(ctx, installerURL, "sha256")

	p.progress(server, "downloading NeoForge %s installer...", neoVersion)
	if err := p.download(ctx, installerURL, joinData(server.DataPath, installerRel), sum, nil, p.reporter(server, "neoforge installer")); err != nil {
		return nil, err
	}

	treeKey := libTreeKey("neoforge", mc, neoVersion)
	p.restoreLibTree(server, treeKey)

	p.progress(server, "running NeoForge %s installer (this can take a few minutes)...", neoVersion)
	cmd := []string{"java", "-jar", filepath.ToSlash(installerRel), "--installServer"}
	if err := p.runInstallerContainer(ctx, server, cfg, cmd); err != nil {
		return nil, fmt.Errorf("NeoForge installer failed: %w", err)
	}

	spec, err := detectForgeLaunch(server.DataPath, "neoforged/neoforge", neoVersion)
	if err != nil {
		// Legacy artifact installs under neoforged/forge
		spec, err = detectForgeLaunch(server.DataPath, "neoforged/forge", neoVersion)
		if err != nil {
			return nil, err
		}
	}
	p.saveLibTree(server, treeKey)
	return p.finishLaunch(server, spec, v1.ModLoader_MOD_LOADER_NEOFORGE, neoVersion, mc)
}

// Picks the newest NeoForge version for an MC version
func (p *Provisioner) resolveNeoForgeVersion(ctx context.Context, mc string) (string, string, string, error) {
	api, maven, artifact := neoforgeVersionAPI, neoforgeMavenURL, "neoforge"

	prefix := neoforgePrefix(mc)
	if mc == "1.20.1" {
		api, maven, artifact = neoforgeLegacyAPI, neoforgeLegacyURL, "forge"
		prefix = "1.20.1-"
	}

	var listing struct {
		Versions []string `json:"versions"`
	}
	if err := p.getJSON(ctx, api, &listing); err != nil {
		return "", "", "", fmt.Errorf("failed to list NeoForge versions: %w", err)
	}

	var matches []string
	for _, v := range listing.Versions {
		if strings.HasPrefix(v, prefix) {
			matches = append(matches, v)
		}
	}
	if len(matches) == 0 {
		return "", "", "", fmt.Errorf("NeoForge has no builds for MC %s", mc)
	}

	// Listing is ascending, prefer newest stable then newest beta
	pick := ""
	for i := len(matches) - 1; i >= 0; i-- {
		if !strings.Contains(matches[i], "-beta") {
			pick = matches[i]
			break
		}
	}
	if pick == "" {
		pick = matches[len(matches)-1]
	}
	return pick, maven, artifact, nil
}

// Maps an MC version to its NeoForge prefix
func neoforgePrefix(mc string) string {
	parts := strings.Split(mc, ".")
	if strings.HasPrefix(mc, "1.") {
		// 1.21 -> 21.0., 1.21.1 -> 21.1
		if len(parts) == 2 {
			return parts[1] + ".0."
		}
		return parts[1] + "." + parts[2] + "."
	}
	// Date-based versions get four dot components like 26.2.0
	if len(parts) == 2 {
		return mc + ".0."
	}
	return mc + "."
}

// Fetches a maven checksum sidecar (e.g. .sha256) if present
func (p *Provisioner) fetchChecksumSidecar(ctx context.Context, artifactURL, algo string) (*checksum, error) {
	body, err := p.getText(ctx, artifactURL+"."+algo)
	if err != nil {
		return nil, err
	}
	value := strings.Fields(strings.TrimSpace(body))
	if len(value) == 0 {
		return nil, fmt.Errorf("empty checksum sidecar")
	}
	return &checksum{algo: algo, value: value[0]}, nil
}

// Reads a pack start script for its loader version
func detectServerPackLoader(dataPath, mc string) (v1.ModLoader, string) {
	for _, name := range []string{"startserver.sh", "startserver.bat", "start.sh", "run.sh"} {
		data, err := os.ReadFile(filepath.Join(dataPath, name))
		if err != nil {
			continue
		}
		s := string(data)
		if v := scriptVar(s, "NEOFORGE_VERSION"); v != "" {
			return v1.ModLoader_MOD_LOADER_NEOFORGE, v
		}
		if v := scriptVar(s, "FORGE_VERSION"); v != "" {
			return v1.ModLoader_MOD_LOADER_FORGE, strings.TrimPrefix(v, mc+"-")
		}
	}
	return v1.ModLoader_MOD_LOADER_UNSPECIFIED, ""
}

// Returns the value assigned to a shell variable
func scriptVar(s, key string) string {
	i := strings.Index(s, key+"=")
	if i < 0 {
		return ""
	}
	rest := s[i+len(key)+1:]
	if end := strings.IndexAny(rest, " \t\r\n\"'"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// Locates the launch entry produced by a Forge installer
func detectForgeLaunch(dataPath, vendorPath, version string) (*v1.LaunchSpec, error) {
	pattern := filepath.Join(dataPath, "libraries", "net", filepath.FromSlash(vendorPath), "*", "unix_args.txt")
	if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
		rel, err := filepath.Rel(dataPath, pickForgeArgs(matches, version))
		if err == nil {
			return &v1.LaunchSpec{
				Kind:     v1.LaunchKind_LAUNCH_KIND_ARGS_FILE,
				ArgsFile: filepath.ToSlash(rel),
			}, nil
		}
	}

	// Legacy layout is a runnable forge jar in root
	for _, glob := range []string{"forge-*-universal.jar", "forge-*.jar", "neoforge-*-universal.jar"} {
		matches, err := filepath.Glob(filepath.Join(dataPath, glob))
		if err != nil {
			continue
		}
		for _, m := range matches {
			name := filepath.Base(m)
			if strings.Contains(name, "installer") {
				continue
			}
			return &v1.LaunchSpec{Kind: v1.LaunchKind_LAUNCH_KIND_JAR, Jar: name}, nil
		}
	}

	return nil, fmt.Errorf("installer completed but no launchable server was found (expected libraries/net/%s/*/unix_args.txt or a forge server jar)", vendorPath)
}

// Prefers the requested version, else the newest install
func pickForgeArgs(matches []string, version string) string {
	if version != "" {
		for _, m := range matches {
			dir := filepath.Base(filepath.Dir(m))
			if dir == version || strings.HasSuffix(dir, "-"+version) {
				return m
			}
		}
	}
	// Old versions linger in libraries, lexical order lies
	best := matches[0]
	var bestTime time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			best = m
		}
	}
	return best
}
