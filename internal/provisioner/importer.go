// Existing server directories imported as-is, never reinstalled
package provisioner

import (
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Imported files ship nothing the runtime can start
var ErrNoImportLaunch = errors.New("no server jar or Forge/NeoForge launch files found, upload the whole server folder including its server jar")

// Archiver debris that never counts as a root entry
var importIgnoredEntries = map[string]bool{"__MACOSX": true, ".DS_Store": true, "Thumbs.db": true}

// Strips one wrapping dir from an extracted server archive
func ImportRoot(dir string) (root, wrapper string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	var only os.DirEntry
	count := 0
	for _, e := range entries {
		if importIgnoredEntries[e.Name()] {
			continue
		}
		count++
		only = e
	}
	if count != 1 || !only.IsDir() {
		return dir, "", nil
	}
	inner := filepath.Join(dir, only.Name())
	// A lone world or content dir is part of the server, not a wrapper
	if serverContentDirs[strings.ToLower(only.Name())] || fileExists(filepath.Join(inner, "level.dat")) {
		return dir, "", nil
	}
	return inner, only.Name(), nil
}

// What an imported server directory testifies about itself
type ImportDetection struct {
	Loader        v1.ModLoader
	LoaderVersion string
	McVersion     string
	Launch        *v1.LaunchSpec // Nil when nothing launchable ships
	LevelName     string
	HasWorld      bool
	Properties    minecraft.PropertiesFile // Nil without server.properties
	ModCount      int
	PluginCount   int
	Warnings      []string
}

// Launch target of the detection, empty when none
func (d *ImportDetection) LaunchTarget() string {
	if d.Launch == nil {
		return ""
	}
	return launchTarget(d.Launch)
}

// Reads loader, versions, launch entry and world from a server dir
func DetectImportedServer(root string) *ImportDetection {
	det := &ImportDetection{}
	if props, err := minecraft.LoadPropertiesFile(root); err == nil {
		det.Properties = props
	}

	det.Loader = detectImportLoader(root)
	det.Launch = ImportLaunch(root, det.Loader)
	version, mc := importLoaderVersion(root, det.Loader, det.Launch)
	det.LoaderVersion = version

	det.LevelName = "world"
	if name := det.Properties["level-name"]; name != "" {
		det.LevelName = name
	}
	var levelVersion string
	if worldDir, err := files.FindWorldDir(root); err == nil {
		det.HasWorld = true
		if info, err := minecraft.ReadLevelDat(filepath.Join(worldDir, "level.dat")); err == nil {
			levelVersion = info.VersionName
		}
	}
	det.McVersion = detectImportMCVersion(root, det.Launch, mc, levelVersion)

	det.ModCount = countJars(filepath.Join(root, "mods"))
	det.PluginCount = countJars(filepath.Join(root, "plugins"))

	if det.Launch == nil {
		det.Warnings = append(det.Warnings, ErrNoImportLaunch.Error())
	}
	if det.McVersion == "" {
		det.Warnings = append(det.Warnings, "Could not detect the Minecraft version, choose it before creating the server")
	}
	if det.Properties == nil {
		det.Warnings = append(det.Warnings, "No server.properties found, panel defaults apply")
	}
	if !det.HasWorld {
		det.Warnings = append(det.Warnings, fmt.Sprintf("No world found at %q, a new world generates on first start", det.LevelName))
	}
	if det.ModCount > 0 && minecraft.LoaderFor(det.Loader).ModsDirectory != "mods" {
		det.Warnings = append(det.Warnings, fmt.Sprintf("mods/ holds %d jars but no mod loader runtime was found, they will not load", det.ModCount))
	}
	return det
}

// Loaders whose server jar carries the loader name
func importJarNamedLoader(loader v1.ModLoader) bool {
	switch minecraft.LoaderFor(loader).Category {
	case optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_BUKKIT,
		optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_PAPER,
		optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_HYBRID,
		optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_SPONGE,
		optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_LIGHTWEIGHT:
		return true
	case optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_OTHER:
		return loader != v1.ModLoader_MOD_LOADER_CUSTOM
	}
	return false
}

// Config files plugin servers write on first boot, most specific first
var importConfigEvidence = []struct {
	rel    string
	loader v1.ModLoader
}{
	{"purpur.yml", v1.ModLoader_MOD_LOADER_PURPUR},
	{"pufferfish.yml", v1.ModLoader_MOD_LOADER_PUFFERFISH},
	{"config/paper-global.yml", v1.ModLoader_MOD_LOADER_PAPER},
	{"paper.yml", v1.ModLoader_MOD_LOADER_PAPER},
	{"spigot.yml", v1.ModLoader_MOD_LOADER_SPIGOT},
	{"bukkit.yml", v1.ModLoader_MOD_LOADER_BUKKIT},
}

// Picks the loader the files run, runtime evidence over guesses
func detectImportLoader(root string) v1.ModLoader {
	// Hybrids bundle forge libraries, their named jar decides first
	for _, row := range minecraft.Loaders() {
		if importJarNamedLoader(row.Loader()) && rootLoaderJar(root, protometa.Name(row.Loader())) != "" {
			return row.Loader()
		}
	}
	if rootLoaderJar(root, "craftbukkit") != "" {
		return v1.ModLoader_MOD_LOADER_BUKKIT
	}
	switch {
	case dirExists(root, "libraries/net/neoforged"), rootLoaderJar(root, "neoforge") != "":
		return v1.ModLoader_MOD_LOADER_NEOFORGE
	case dirExists(root, "libraries/net/minecraftforge/forge"), rootLoaderJar(root, "forge") != "":
		return v1.ModLoader_MOD_LOADER_FORGE
	case dirExists(root, "libraries/org/quiltmc/quilt-loader"), fileExists(filepath.Join(root, "quilt-server-launch.jar")):
		return v1.ModLoader_MOD_LOADER_QUILT
	case dirExists(root, "libraries/net/fabricmc/fabric-loader"), dirExists(root, ".fabric"), rootLoaderJar(root, "fabric") != "":
		return v1.ModLoader_MOD_LOADER_FABRIC
	}
	for _, ev := range importConfigEvidence {
		if fileExists(filepath.Join(root, filepath.FromSlash(ev.rel))) {
			return ev.loader
		}
	}
	// Renamed jars still name their entrypoint
	if jar := plainServerJar(root); jar != "" {
		switch main := jarMainClass(filepath.Join(root, jar)); {
		case strings.HasPrefix(main, "io.papermc.paperclip"):
			return v1.ModLoader_MOD_LOADER_PAPER
		case strings.HasPrefix(main, "org.bukkit.craftbukkit"):
			return v1.ModLoader_MOD_LOADER_SPIGOT
		}
	}
	return v1.ModLoader_MOD_LOADER_VANILLA
}

// server.jar or the lone root jar
func plainServerJar(root string) string {
	if fileExists(filepath.Join(root, "server.jar")) {
		return "server.jar"
	}
	return loneRootJar(root)
}

// Launch entry for imported files, named plugin and hybrid jars first
func ImportLaunch(root string, loader v1.ModLoader) *v1.LaunchSpec {
	if importJarNamedLoader(loader) {
		if jar := rootLoaderJar(root, protometa.Name(loader)); jar != "" {
			return jarLaunch(jar)
		}
		if loader == v1.ModLoader_MOD_LOADER_BUKKIT {
			if jar := rootLoaderJar(root, "craftbukkit"); jar != "" {
				return jarLaunch(jar)
			}
		}
		// Renamed hybrid jars must not lose to bundled forge trees
		if jar := plainServerJar(root); jar != "" {
			return jarLaunch(jar)
		}
	}
	return detectPackLaunch(root, loader)
}

// Loader version plus any MC version the loader layout names
func importLoaderVersion(root string, loader v1.ModLoader, launch *v1.LaunchSpec) (string, string) {
	switch loader {
	case v1.ModLoader_MOD_LOADER_FORGE, v1.ModLoader_MOD_LOADER_NEOFORGE:
		if launch != nil && launch.Kind == v1.LaunchKind_LAUNCH_KIND_ARGS_FILE {
			return argsDirLoaderVersion(launch.ArgsFile)
		}
		if launch != nil && launch.Kind == v1.LaunchKind_LAUNCH_KIND_JAR {
			if mc, version := forgeJarVersion(launch.Jar); version != "" {
				return version, mc
			}
		}
	case v1.ModLoader_MOD_LOADER_FABRIC:
		if mc, version := fabricLauncherJarVersion(root); version != "" {
			return version, mc
		}
		return newestSubdir(filepath.Join(root, "libraries", "net", "fabricmc", "fabric-loader")), ""
	case v1.ModLoader_MOD_LOADER_QUILT:
		return newestSubdir(filepath.Join(root, "libraries", "org", "quiltmc", "quilt-loader")), ""
	}
	if ev := scriptPackEvidence(root, ""); ev.loader == loader {
		return ev.loaderVersion, ev.mcVersion
	}
	return "", ""
}

// Loader version and MC version from a libraries args file path
// Forge and legacy NeoForge dirs read mc-version, modern NeoForge dirs
// carry only their own version, possibly with a -beta suffix
func argsDirLoaderVersion(argsFile string) (string, string) {
	rel := filepath.ToSlash(argsFile)
	dir := path.Base(path.Dir(rel))
	mc, version := splitMCPrefix(dir)
	if mc == "" || !mcEraMajor(mc) {
		return dir, ""
	}
	// Legacy NeoForge 1.20.1 installs pin the full maven version
	if strings.HasPrefix(rel, "libraries/net/neoforged/forge/") {
		return dir, mc
	}
	return version, mc
}

// Splits forge-1.12.2-14.23.5.2860-universal.jar style names
func forgeJarVersion(jar string) (string, string) {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(jar)), ".jar")
	for _, prefix := range []string{"neoforge-", "forge-"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			name = rest
			break
		}
	}
	for _, suffix := range []string{"-universal", "-shim", "-server"} {
		name = strings.TrimSuffix(name, suffix)
	}
	mc, version := splitMCPrefix(name)
	if mc == "" || !mcEraMajor(mc) {
		return "", ""
	}
	return mc, version
}

// Reads fabric-server-mc.X-loader.Y-launcher.Z.jar style names
func fabricLauncherJarVersion(root string) (string, string) {
	matches, _ := filepath.Glob(filepath.Join(root, "fabric-server-mc.*-loader.*.jar"))
	for _, m := range matches {
		name := strings.TrimPrefix(filepath.Base(m), "fabric-server-mc.")
		mc, rest, ok := strings.Cut(name, "-loader.")
		if !ok {
			continue
		}
		version, _, _ := strings.Cut(strings.TrimSuffix(rest, ".jar"), "-launcher.")
		return mc, version
	}
	return "", ""
}

// MC version evidence, jar metadata over names over the world
func detectImportMCVersion(root string, launch *v1.LaunchSpec, loaderMC, levelVersion string) string {
	if loaderMC != "" {
		return loaderMC
	}
	if launch != nil {
		if v := serverPackMCVersion(root, launch); v != "" {
			return v
		}
	}
	// Fabric launchers point at the vanilla jar they wrap
	if props, err := os.ReadFile(filepath.Join(root, "fabric-server-launcher.properties")); err == nil {
		for _, line := range strings.Split(string(props), "\n") {
			if jar, ok := strings.CutPrefix(strings.TrimSpace(line), "serverJar="); ok {
				if v := jarMCVersion(joinData(root, strings.TrimSpace(jar))); v != "" {
					return v
				}
			}
		}
	}
	var candidates []string
	// Forge installers and paperclip cache the vanilla server by version
	for _, dir := range []string{filepath.Join(root, "libraries", "net", "minecraft", "server"), filepath.Join(root, "versions")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if mc, _, _ := strings.Cut(e.Name(), "-"); e.IsDir() && minecraft.IsReleaseVersion(mc) {
				candidates = append(candidates, mc)
			}
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "cache", "mojang_*.jar")); len(matches) > 0 {
		for _, m := range matches {
			if mc := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "mojang_"), ".jar"); minecraft.IsReleaseVersion(mc) {
				candidates = append(candidates, mc)
			}
		}
	}
	if len(candidates) > 0 {
		return minecraft.FindMostRecentMinecraftVersion(candidates)
	}
	if launch != nil && launch.Kind == v1.LaunchKind_LAUNCH_KIND_JAR {
		if v := jarNameMCVersion(launch.Jar); v != "" {
			return v
		}
	}
	if ev := scriptPackEvidence(root, ""); ev.mcVersion != "" {
		return ev.mcVersion
	}
	return levelVersion
}

// First release looking token in a jar name like paper-1.20.4-496.jar
func jarNameMCVersion(jar string) string {
	name := strings.TrimSuffix(filepath.Base(jar), ".jar")
	name = strings.TrimPrefix(name, "minecraft_server.")
	for _, token := range strings.Split(name, "-") {
		if minecraft.IsReleaseVersion(token) && mcEraMajor(token) {
			return token
		}
	}
	return ""
}

// Classic 1.x versions or year based ones like 26.1
func mcEraMajor(v string) bool {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	return err == nil && (n == 1 || n >= 25 && n < 100)
}

// Reads Main-Class from a jar manifest
func jarMainClass(jarPath string) string {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return ""
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "META-INF/MANIFEST.MF" {
			continue
		}
		data, err := readZipFileLimit(f, 1<<20)
		if err != nil {
			return ""
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			if main, ok := strings.CutPrefix(scanner.Text(), "Main-Class:"); ok {
				return strings.TrimSpace(main)
			}
		}
	}
	return ""
}

// Most recently written subdir name, empty when none
func newestSubdir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	var bestTime int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		if t := info.ModTime().UnixNano(); best == "" || t > bestTime {
			best, bestTime = e.Name(), t
		}
	}
	return best
}

func dirExists(root, rel string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.IsDir()
}

func countJars(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jar") {
			n++
		}
	}
	return n
}

// Records imported files as provisioned so Ensure only applies config
func AdoptImportedServer(server *v1.Server, loaderVersion string) error {
	javaMajor, err := docker.RequiredJavaMajor(server.McVersion)
	if err != nil {
		return err
	}
	return adoptImportedFiles(server, loaderVersion, javaMajor)
}

func adoptImportedFiles(server *v1.Server, loaderVersion string, javaMajor int) error {
	// Archives from another panel carry its state and agent token
	if err := os.RemoveAll(filepath.Join(server.DataPath, runtimespec.StateDir)); err != nil {
		return fmt.Errorf("failed to clear imported panel state: %w", err)
	}
	spec := ImportLaunch(server.DataPath, server.ModLoader)
	if spec == nil {
		return ErrNoImportLaunch
	}
	if _, err := writeLaunch(server, spec, server.ModLoader, loaderVersion, server.McVersion, javaMajor); err != nil {
		return err
	}
	return runtimespec.WriteManifest(server.DataPath, &v1.Manifest{
		Version:       1,
		Loader:        server.ModLoader,
		LoaderVersion: loaderVersion,
		McVersion:     server.McVersion,
		JavaMajor:     int32(javaMajor),
		ProvisionedAt: timestamppb.Now(),
	})
}

// Legacy numeric select values older servers wrote
var legacyPropValues = map[string][]string{
	"difficulty": {"peaceful", "easy", "normal", "hard"},
	"gamemode":   {"survival", "creative", "adventure", "spectator"},
}

// Keys the panel owns for networking and RCON, imports never adopt them
func importSkippedProp(key string) bool {
	switch key {
	case "server-port", "query.port", "enable-rcon", "rcon.port", "rcon.password":
		return true
	}
	return strings.HasPrefix(key, "management-server-")
}

// Adopts server.properties values into settings fields, returns adopted pairs
func ApplyImportedProperties(cfg *v1.ServerProperties, props minecraft.PropertiesFile) map[string]string {
	adopted := map[string]string{}
	m := cfg.ProtoReflect()
	for _, pr := range protometa.Props(m.Descriptor()) {
		key := pr.Meta.Prop
		if key == "" || pr.Meta.System || importSkippedProp(key) {
			continue
		}
		raw, ok := props[key]
		if !ok {
			continue
		}
		value := unescapeProperty(raw)
		if names, ok := legacyPropValues[key]; ok {
			if i, err := strconv.Atoi(value); err == nil && i >= 0 && i < len(names) {
				value = names[i]
			}
		}
		if pr.Meta.Input == "select" && len(pr.Meta.Options) > 0 {
			value = strings.ToLower(value)
			if !slices.Contains(pr.Meta.Options, value) {
				continue
			}
		}
		if value == "" && pr.Field.Kind() != protoreflect.StringKind {
			continue
		}
		if err := protometa.SetScalarString(m, pr.Field, value); err != nil {
			continue
		}
		adopted[key] = value
	}
	return adopted
}

// Undoes separator escapes, unicode and backslash escapes stay verbatim
// since the panel writes values back unescaped
func unescapeProperty(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '\\' && i+1 < len(v) {
			switch n := v[i+1]; n {
			case ':', '=', '#', '!', ' ':
				b.WriteByte(n)
				i++
				continue
			case '\\':
				b.WriteString(`\\`)
				i++
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}
