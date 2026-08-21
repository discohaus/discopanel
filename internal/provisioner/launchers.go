// Launcher instance exports and FTB server file formats
package provisioner

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Finds one entry at the zip root or one dir deep
func findZipEntry(reader *zip.Reader, name string) (*zip.File, string) {
	for _, f := range reader.File {
		entry := f.Name
		prefix := ""
		if idx := strings.Index(entry, "/"); idx >= 0 && strings.Count(entry, "/") == 1 {
			prefix = entry[:idx+1]
			entry = entry[idx+1:]
		}
		if entry == name {
			return f, prefix
		}
	}
	return nil, ""
}

// Reads one zip entry fully up to a byte cap
func readZipFileLimit(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

// Reads a named root or one deep entry's bytes
func readZipEntry(reader *zip.Reader, name string) ([]byte, string) {
	// ATLauncher instance files embed whole mod listings
	const capBytes = 16 << 20
	f, prefix := findZipEntry(reader, name)
	if f == nil || f.UncompressedSize64 > capBytes {
		return nil, ""
	}
	data, err := readZipFileLimit(f, capBytes)
	if err != nil {
		return nil, ""
	}
	return data, prefix
}

// Reports whether any entry lives under prefix
func zipHasPrefix(reader *zip.Reader, prefix string) bool {
	for _, f := range reader.File {
		if strings.HasPrefix(f.Name, prefix) && f.Name != prefix {
			return true
		}
	}
	return false
}

// Launcher metadata and client junk skipped from extracts
var instanceJunkDirs = []string{
	"logs/", "screenshots/", "crash-reports/", "natives/",
	"cache/", ".fabric/", ".mixin.out/", "assets/", "icons/",
}

// Skips launcher metadata and client junk during extraction
func instanceJunk(rel string) bool {
	lower := strings.ToLower(rel)
	for _, dir := range instanceJunkDirs {
		if strings.HasPrefix(lower, dir) {
			return true
		}
	}
	switch lower {
	case "instance.json", "instance.cfg", "mmc-pack.json", "config.json", ".packignore":
		return true
	}
	return false
}

// Extracts instance game files then provisions the runtime
func (p *Provisioner) installFromInstanceRoot(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, prefix string, ev packEvidence, opts packInstallOpts) (*Result, error) {
	excludes := p.packRuleExcludes(server, cfg, opts)
	if err := p.extractZipFiltered(reader, prefix, server.DataPath, !opts.force, excludes, instanceJunk); err != nil {
		return nil, err
	}
	return p.installPackRuntime(ctx, server, cfg, ev)
}

// Strips mc and loader name prefixes from version pins
func cleanLoaderVersion(version, mc, loaderName string) string {
	if mc != "" {
		version = strings.TrimPrefix(version, mc+"-")
	}
	if loaderName != "" {
		version = strings.TrimPrefix(version, strings.ToLower(loaderName)+"-")
	}
	return version
}

// ATLauncher serializes its instance under a launcher key
type atlInstance struct {
	ID       string `json:"id"`
	Launcher *struct {
		Name          string `json:"name"`
		Pack          string `json:"pack"`
		Version       string `json:"version"`
		LoaderVersion *struct {
			Type    string `json:"type"`
			Version string `json:"version"`
		} `json:"loaderVersion"`
	} `json:"launcher"`
}

// Parses instance.json bytes as an ATLauncher instance
func parseATLInstance(data []byte) *atlInstance {
	var inst atlInstance
	if json.Unmarshal(data, &inst) != nil || inst.Launcher == nil {
		return nil
	}
	return &inst
}

// Loader facts an ATLauncher instance declares
func atlEvidence(inst *atlInstance) packEvidence {
	ev := packEvidence{}
	if mcVersionLike(inst.ID) {
		ev.mcVersion = inst.ID
	}
	if l := inst.Launcher.LoaderVersion; l != nil && l.Type != "" {
		version := cleanLoaderVersion(l.Version, ev.mcVersion, l.Type)
		ev.loaderID = strings.ToLower(l.Type) + "-" + version
		if loader, v, ok := minecraft.CutPackLoaderID(ev.loaderID); ok {
			ev.loader, ev.loaderVersion = loader, v
		}
	}
	return ev
}

// FTB app instances carry numeric pack and version ids
func parseFTBAppInstance(data []byte) (int, int, bool) {
	var inst struct {
		ID        int             `json:"id"`
		VersionID int             `json:"versionId"`
		Launcher  json.RawMessage `json:"launcher"`
	}
	if json.Unmarshal(data, &inst) != nil || inst.Launcher != nil {
		return 0, 0, false
	}
	if inst.ID <= 0 || inst.VersionID <= 0 {
		return 0, 0, false
	}
	return inst.ID, inst.VersionID, true
}

// Manifest shipped inside FTB new source server files
type ftbServerManifest struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	VersionName    string `json:"versionName"`
	VersionID      int    `json:"versionId"`
	ModPackTargets *struct {
		ModLoader struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"modLoader"`
		McVersion string `json:"mcVersion"`
	} `json:"modPackTargets"`
}

// Finds .manifest.json at zip root or one dir deep
func readFTBServerManifest(reader *zip.Reader) (*ftbServerManifest, string) {
	data, prefix := readZipEntry(reader, ".manifest.json")
	if data == nil {
		return nil, ""
	}
	var m ftbServerManifest
	if json.Unmarshal(data, &m) != nil || m.ModPackTargets == nil {
		return nil, ""
	}
	return &m, prefix
}

// Loader facts an FTB server manifest declares
func ftbServerManifestEvidence(m *ftbServerManifest) packEvidence {
	ev := packEvidence{}
	if m.ModPackTargets == nil {
		return ev
	}
	ev.mcVersion = m.ModPackTargets.McVersion
	if name := m.ModPackTargets.ModLoader.Name; name != "" {
		version := cleanLoaderVersion(m.ModPackTargets.ModLoader.Version, ev.mcVersion, name)
		ev.loaderID = strings.ToLower(name) + "-" + version
		if loader, v, ok := minecraft.CutPackLoaderID(ev.loaderID); ok {
			ev.loader, ev.loaderVersion = loader, v
		}
	}
	return ev
}

// Extracts FTB server files then provisions the declared loader
func (p *Provisioner) installFromFTBServerManifest(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, manifest *ftbServerManifest, prefix string, opts packInstallOpts) (*Result, error) {
	excludes := p.packRuleExcludes(server, cfg, opts)
	if err := p.extractZipFiltered(reader, prefix, server.DataPath, !opts.force, excludes, nil); err != nil {
		return nil, err
	}
	ev := ftbServerManifestEvidence(manifest)
	p.adoptMCVersion(ctx, server, ev.mcVersion)
	return p.installPackRuntime(ctx, server, cfg, ev)
}

// Components pin loader and game versions in MultiMC packs
type mmcPack struct {
	Components []struct {
		UID     string `json:"uid"`
		Version string `json:"version"`
	} `json:"components"`
}

// Loader owning each known MultiMC component uid
var mmcComponentLoaders = map[string]v1.ModLoader{
	"net.minecraftforge":         v1.ModLoader_MOD_LOADER_FORGE,
	"net.neoforged":              v1.ModLoader_MOD_LOADER_NEOFORGE,
	"net.fabricmc.fabric-loader": v1.ModLoader_MOD_LOADER_FABRIC,
	"org.quiltmc.quilt-loader":   v1.ModLoader_MOD_LOADER_QUILT,
}

// Finds mmc-pack.json at zip root or one dir deep
func readMMCPack(reader *zip.Reader) (*mmcPack, string) {
	data, prefix := readZipEntry(reader, "mmc-pack.json")
	if data == nil {
		return nil, ""
	}
	var pack mmcPack
	if json.Unmarshal(data, &pack) != nil || len(pack.Components) == 0 {
		return nil, ""
	}
	return &pack, prefix
}

// Loader facts MultiMC components declare
func mmcEvidence(pack *mmcPack) packEvidence {
	ev := packEvidence{}
	for _, c := range pack.Components {
		if c.UID == "net.minecraft" {
			ev.mcVersion = c.Version
			continue
		}
		if loader, ok := mmcComponentLoaders[c.UID]; ok {
			ev.loader = loader
			ev.loaderVersion = c.Version
		}
	}
	if ev.loader != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		ev.loaderVersion = cleanLoaderVersion(ev.loaderVersion, ev.mcVersion, "")
		ev.loaderID = protometa.Name(ev.loader) + "-" + ev.loaderVersion
	}
	return ev
}

// Reads the instance display name from instance.cfg
func mmcInstanceName(reader *zip.Reader, prefix string) string {
	f, cfgPrefix := findZipEntry(reader, "instance.cfg")
	if f == nil || cfgPrefix != prefix || f.UncompressedSize64 > 1<<20 {
		return ""
	}
	data, err := readZipFileLimit(f, 1<<20)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "name="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Extracts the instance game dir then provisions the runtime
func (p *Provisioner) installFromMMCPack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, reader *zip.ReadCloser, pack *mmcPack, prefix string, opts packInstallOpts) (*Result, error) {
	content := ""
	for _, sub := range []string{".minecraft/", "minecraft/"} {
		if zipHasPrefix(&reader.Reader, prefix+sub) {
			content = prefix + sub
			break
		}
	}
	if content != "" {
		excludes := p.packRuleExcludes(server, cfg, opts)
		if err := p.extractZipFiltered(reader, content, server.DataPath, !opts.force, excludes, instanceJunk); err != nil {
			return nil, err
		}
	}
	return p.installPackRuntime(ctx, server, cfg, mmcEvidence(pack))
}

// Legacy GDLauncher instances describe their loader in config.json
type gdlConfig struct {
	Loader *struct {
		LoaderType    string `json:"loaderType"`
		McVersion     string `json:"mcVersion"`
		LoaderVersion string `json:"loaderVersion"`
	} `json:"loader"`
}

// Finds a GDLauncher config.json at root or one dir deep
func readGDLConfig(reader *zip.Reader) (*gdlConfig, string) {
	data, prefix := readZipEntry(reader, "config.json")
	if data == nil {
		return nil, ""
	}
	var gdl gdlConfig
	if json.Unmarshal(data, &gdl) != nil || gdl.Loader == nil || gdl.Loader.LoaderType == "" {
		return nil, ""
	}
	return &gdl, prefix
}

// Loader facts a GDLauncher config declares
func gdlEvidence(gdl *gdlConfig) packEvidence {
	ev := packEvidence{mcVersion: gdl.Loader.McVersion}
	version := cleanLoaderVersion(gdl.Loader.LoaderVersion, ev.mcVersion, gdl.Loader.LoaderType)
	ev.loaderID = strings.ToLower(gdl.Loader.LoaderType) + "-" + version
	if loader, v, ok := minecraft.CutPackLoaderID(ev.loaderID); ok {
		ev.loader, ev.loaderVersion = loader, v
	}
	return ev
}

// Reports whether the zip carries a Technic pack layout
func hasTechnicLayout(reader *zip.Reader) bool {
	for _, entry := range reader.File {
		name := strings.TrimPrefix(entry.Name, "./")
		if name == "bin/modpack.jar" {
			return true
		}
		// One wrapping dir deep still counts
		if strings.Count(name, "/") == 2 && strings.HasSuffix(name, "/bin/modpack.jar") {
			return true
		}
	}
	return false
}

// Reads forge facts from an extracted Technic modpack.jar
func technicEvidence(dataPath string) packEvidence {
	jarPath := filepath.Join(dataPath, "bin", "modpack.jar")
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return packEvidence{}
	}
	defer r.Close()
	for _, f := range r.File {
		if path.Base(f.Name) != "version.json" {
			continue
		}
		data, err := readZipFileLimit(f, 1<<20)
		if err != nil {
			return packEvidence{}
		}
		var profile struct {
			Libraries []struct {
				Name string `json:"name"`
			} `json:"libraries"`
		}
		if json.Unmarshal(data, &profile) != nil {
			return packEvidence{}
		}
		for _, lib := range profile.Libraries {
			version, ok := strings.CutPrefix(lib.Name, "net.minecraftforge:forge:")
			if !ok {
				continue
			}
			// Versions read mc dash forge with optional suffix
			mc, forgeVersion, ok := strings.Cut(version, "-")
			if !ok || !mcVersionLike(mc) {
				continue
			}
			forgeVersion = strings.TrimSuffix(forgeVersion, "-"+mc)
			return packEvidence{
				loaderID:      "forge-" + forgeVersion,
				loader:        v1.ModLoader_MOD_LOADER_FORGE,
				loaderVersion: forgeVersion,
				mcVersion:     mc,
			}
		}
	}
	return packEvidence{}
}
