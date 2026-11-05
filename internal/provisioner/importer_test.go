package provisioner

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Writes data dir relative files, empty content means a placeholder
func writeTree(t *testing.T, root string, entries map[string]string) {
	t.Helper()
	for rel, content := range entries {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if content == "" {
			content = "x"
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// Writes a jar holding the given entries
func writeJar(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestImportRootUnwrap(t *testing.T) {
	cases := []struct {
		name        string
		tree        map[string]string
		wantWrapper string
	}{
		{"flat", map[string]string{"server.jar": "", "server.properties": ""}, ""},
		{"wrapped", map[string]string{"My Server/server.jar": "", "My Server/mods/a.jar": ""}, "My Server"},
		{"wrapped with mac debris", map[string]string{"srv/server.jar": "", "__MACOSX/srv/._server.jar": "", ".DS_Store": ""}, "srv"},
		{"lone content dir", map[string]string{"mods/a.jar": ""}, ""},
		{"lone world dir", map[string]string{"survival/level.dat": ""}, ""},
		{"two dirs", map[string]string{"a/server.jar": "", "b/server.jar": ""}, ""},
		{"lone file", map[string]string{"server.jar": ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, tc.tree)
			root, wrapper, err := ImportRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			if wrapper != tc.wantWrapper {
				t.Fatalf("wrapper %q, want %q", wrapper, tc.wantWrapper)
			}
			want := dir
			if tc.wantWrapper != "" {
				want = filepath.Join(dir, tc.wantWrapper)
			}
			if root != want {
				t.Fatalf("root %q, want %q", root, want)
			}
		})
	}
}

func TestDetectImportedServer(t *testing.T) {
	vanillaJSON := func(id string) map[string]string {
		return map[string]string{"version.json": `{"id":"` + id + `","world_version":3700}`}
	}
	cases := []struct {
		name          string
		tree          map[string]string
		jars          map[string]map[string]string
		loader        v1.ModLoader
		loaderVersion string
		mc            string
		target        string
	}{
		{
			name:   "vanilla server jar",
			jars:   map[string]map[string]string{"server.jar": vanillaJSON("1.21.1")},
			loader: v1.ModLoader_MOD_LOADER_VANILLA, mc: "1.21.1", target: "server.jar",
		},
		{
			name: "modern forge args file",
			tree: map[string]string{
				"libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt": "",
				"run.sh":            "java @user_jvm_args.txt @libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt",
				"user_jvm_args.txt": "",
				"mods/create.jar":   "",
			},
			loader: v1.ModLoader_MOD_LOADER_FORGE, loaderVersion: "47.2.0", mc: "1.20.1",
			target: "libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt",
		},
		{
			name: "legacy forge universal jar",
			tree: map[string]string{
				"forge-1.12.2-14.23.5.2860-universal.jar": "",
				"forge-1.12.2-14.23.5.2860-installer.jar": "",
				"minecraft_server.1.12.2.jar":             "",
			},
			loader: v1.ModLoader_MOD_LOADER_FORGE, loaderVersion: "14.23.5.2860", mc: "1.12.2",
			target: "forge-1.12.2-14.23.5.2860-universal.jar",
		},
		{
			name: "neoforge takes mc from the vanilla library",
			tree: map[string]string{
				"libraries/net/neoforged/neoforge/21.1.77/unix_args.txt":  "",
				"libraries/net/minecraft/server/1.21.1-20240808.144430/x": "",
			},
			loader: v1.ModLoader_MOD_LOADER_NEOFORGE, loaderVersion: "21.1.77", mc: "1.21.1",
			target: "libraries/net/neoforged/neoforge/21.1.77/unix_args.txt",
		},
		{
			name: "fabric launcher wraps the vanilla jar",
			tree: map[string]string{
				"fabric-server-launch.jar":                      "",
				"fabric-server-launcher.properties":             "serverJar=vanilla.jar\n",
				"libraries/net/fabricmc/fabric-loader/0.16.5/x": "",
			},
			jars:   map[string]map[string]string{"vanilla.jar": vanillaJSON("1.21.1")},
			loader: v1.ModLoader_MOD_LOADER_FABRIC, loaderVersion: "0.16.5", mc: "1.21.1", target: "fabric-server-launch.jar",
		},
		{
			name:   "fabric executable server jar",
			tree:   map[string]string{"fabric-server-mc.1.20.4-loader.0.15.7-launcher.1.0.0.jar": ""},
			loader: v1.ModLoader_MOD_LOADER_FABRIC, loaderVersion: "0.15.7", mc: "1.20.4",
			target: "fabric-server-mc.1.20.4-loader.0.15.7-launcher.1.0.0.jar",
		},
		{
			name: "quilt",
			tree: map[string]string{
				"quilt-server-launch.jar":                          "",
				"libraries/org/quiltmc/quilt-loader/0.26.4/x":      "",
				"libraries/net/minecraft/server/1.20.1/server.jar": "",
			},
			loader: v1.ModLoader_MOD_LOADER_QUILT, loaderVersion: "0.26.4", mc: "1.20.1", target: "quilt-server-launch.jar",
		},
		{
			name:   "paper named jar",
			tree:   map[string]string{"paper-1.20.4-496.jar": "", "plugins/EssentialsX.jar": ""},
			loader: v1.ModLoader_MOD_LOADER_PAPER, mc: "1.20.4", target: "paper-1.20.4-496.jar",
		},
		{
			name:   "purpur config beside renamed jar",
			tree:   map[string]string{"server.jar": "", "purpur.yml": "", "versions/1.21.1/purpur-1.21.1.jar": ""},
			loader: v1.ModLoader_MOD_LOADER_PURPUR, mc: "1.21.1", target: "server.jar",
		},
		{
			name:   "renamed paperclip jar",
			jars:   map[string]map[string]string{"server.jar": {"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\nMain-Class: io.papermc.paperclip.Main\n"}},
			tree:   map[string]string{"cache/mojang_1.20.4.jar": ""},
			loader: v1.ModLoader_MOD_LOADER_PAPER, mc: "1.20.4", target: "server.jar",
		},
		{
			name:   "spigot config",
			tree:   map[string]string{"spigot-1.20.4.jar": "", "spigot.yml": "", "bukkit.yml": ""},
			loader: v1.ModLoader_MOD_LOADER_SPIGOT, mc: "1.20.4", target: "spigot-1.20.4.jar",
		},
		{
			name: "hybrid jar beats its bundled forge tree",
			tree: map[string]string{
				"mohist-1.20.1-500-server.jar":                                   "",
				"libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt": "",
			},
			loader: v1.ModLoader_MOD_LOADER_MOHIST, mc: "1.20.1", target: "mohist-1.20.1-500-server.jar",
		},
		{
			name:   "unknown version",
			tree:   map[string]string{"custom.jar": "", "server.properties": "level-name=survival\n"},
			loader: v1.ModLoader_MOD_LOADER_VANILLA, mc: "", target: "custom.jar",
		},
		{
			name:   "nothing launchable",
			tree:   map[string]string{"world/region/r.0.0.mca": "", "server.properties": ""},
			loader: v1.ModLoader_MOD_LOADER_VANILLA,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.tree)
			for name, entries := range tc.jars {
				writeJar(t, filepath.Join(root, name), entries)
			}
			det := DetectImportedServer(root)
			if det.Loader != tc.loader {
				t.Fatalf("loader %v, want %v", det.Loader, tc.loader)
			}
			if det.LoaderVersion != tc.loaderVersion {
				t.Fatalf("loader version %q, want %q", det.LoaderVersion, tc.loaderVersion)
			}
			if det.McVersion != tc.mc {
				t.Fatalf("mc version %q, want %q", det.McVersion, tc.mc)
			}
			if got := det.LaunchTarget(); got != tc.target {
				t.Fatalf("launch target %q, want %q", got, tc.target)
			}
			if tc.target == "" && len(det.Warnings) == 0 {
				t.Fatal("unlaunchable imports must warn")
			}
		})
	}
}

func TestDetectImportedServerWorld(t *testing.T) {
	root := t.TempDir()
	writeJar(t, filepath.Join(root, "server.jar"), map[string]string{"x": ""})
	writeTree(t, root, map[string]string{
		"server.properties":  "level-name=survival\nmax-players=40\n",
		"survival/level.dat": "",
		"mods/stray.jar":     "",
		"plugins/a.jar":      "",
		"plugins/b.jar":      "",
		"plugins/notes.txt":  "",
	})
	det := DetectImportedServer(root)
	if !det.HasWorld || det.LevelName != "survival" {
		t.Fatalf("world not found, has %v level %q", det.HasWorld, det.LevelName)
	}
	if det.PluginCount != 2 || det.ModCount != 1 {
		t.Fatalf("counts mods %d plugins %d", det.ModCount, det.PluginCount)
	}
	if det.Properties["max-players"] != "40" {
		t.Fatalf("properties not read %+v", det.Properties)
	}
	// Vanilla cannot load the stray mod and must say so
	found := false
	for _, w := range det.Warnings {
		if bytes.Contains([]byte(w), []byte("mods/")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a stray mods warning, got %v", det.Warnings)
	}
}

func TestApplyImportedProperties(t *testing.T) {
	props := minecraft.PropertiesFile{
		"motd":                      `§bHello world`,
		"difficulty":                "2",
		"gamemode":                  "Creative",
		"max-players":               "64",
		"level-name":                "survival",
		"level-seed":                "",
		"pvp":                       "false",
		"view-distance":             "12",
		"spawn-protection":          "not-a-number",
		"resource-pack":             `https\://example.com/pack.zip`,
		"server-port":               "25570",
		"rcon.password":             "hunter2",
		"enable-rcon":               "false",
		"query.port":                "25570",
		"management-server-enabled": "true",
		"some-mod-key":              "kept in file only",
	}
	motd, seed := "A DiscoPanel Server", "inherited"
	cfg := &v1.ServerProperties{Motd: &motd, Seed: &seed}
	adopted := ApplyImportedProperties(cfg, props)

	if cfg.GetMotd() != `§bHello world` {
		t.Fatalf("motd must round trip verbatim, got %q", cfg.GetMotd())
	}
	if cfg.GetDifficulty() != "normal" || cfg.GetMode() != "creative" {
		t.Fatalf("select values wrong difficulty %q mode %q", cfg.GetDifficulty(), cfg.GetMode())
	}
	if cfg.GetLevel() != "survival" || cfg.Pvp == nil || cfg.GetPvp() || cfg.GetViewDistance() != 12 {
		t.Fatalf("plain values wrong %+v", cfg)
	}
	if cfg.Seed != nil {
		t.Fatalf("empty seed must clear the inherited value, got %q", cfg.GetSeed())
	}
	if cfg.GetResourcePack() != "https://example.com/pack.zip" {
		t.Fatalf("separator escapes must unescape, got %q", cfg.GetResourcePack())
	}
	if cfg.SpawnProtection != nil {
		t.Fatal("unparsable numbers must be skipped")
	}
	// Panel owned networking, RCON and system keys stay untouched
	if cfg.ServerPort != nil || cfg.RconPassword != nil || cfg.EnableRcon != nil || cfg.QueryPort != nil || cfg.MaxPlayers != nil || cfg.ManagementServerEnabled != nil {
		t.Fatalf("panel owned keys adopted %+v", cfg)
	}
	for _, key := range []string{"server-port", "rcon.password", "max-players", "some-mod-key", "spawn-protection"} {
		if _, ok := adopted[key]; ok {
			t.Fatalf("key %s must not be reported adopted", key)
		}
	}
	if adopted["difficulty"] != "normal" || adopted["motd"] == "" {
		t.Fatalf("adopted summary wrong %+v", adopted)
	}

	// Values outside a select's options are ignored
	bad := &v1.ServerProperties{}
	ApplyImportedProperties(bad, minecraft.PropertiesFile{"difficulty": "nightmare"})
	if bad.Difficulty != nil {
		t.Fatalf("unknown select value adopted %q", bad.GetDifficulty())
	}
}

func TestUnescapeProperty(t *testing.T) {
	cases := map[string]string{
		`plain`:         `plain`,
		`a\:b\=c\#d\!e`: `a:b=c#d!e`,
		`keep\\:slash`:  `keep\\:slash`,
		`§cRed`:         `§cRed`,
		`trailing\`:     `trailing\`,
	}
	for in, want := range cases {
		if got := unescapeProperty(in); got != want {
			t.Errorf("unescapeProperty(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAdoptedImportSkipsInstall(t *testing.T) {
	dataPath := t.TempDir()
	args := "libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt"
	writeTree(t, dataPath, map[string]string{
		args: "",
		// Another panel's state must not survive the import
		".discopanel/agent.json":  `{"token":"foreign"}`,
		".discopanel/launch.json": `{"kind":"LAUNCH_KIND_JAR","jar":"gone.jar"}`,
	})
	server := &v1.Server{Id: "s1", Name: "s1", DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_FORGE, McVersion: "1.20.1"}
	if err := adoptImportedFiles(server, "47.2.0", 17); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runtimespec.AgentPath(dataPath)); !os.IsNotExist(err) {
		t.Fatal("foreign agent spec must be removed")
	}
	spec, err := runtimespec.ReadLaunchSpec(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kind != v1.LaunchKind_LAUNCH_KIND_ARGS_FILE || spec.ArgsFile != args || spec.JavaMajor != 17 || spec.McVersion != "1.20.1" {
		t.Fatalf("launch spec wrong %+v", spec)
	}
	manifest, err := runtimespec.ReadManifest(dataPath)
	if err != nil || manifest == nil {
		t.Fatalf("manifest missing %v", err)
	}

	p := &Provisioner{}
	cfg := &v1.ServerProperties{}
	if p.needsInstall(server, manifest, p.desiredModpackFor(server, cfg), false, pinnedLoaderVersion(server, cfg)) {
		t.Fatal("adopted import must not reinstall")
	}
	pin := "47.2.0"
	cfg.ForgeVersion = &pin
	if p.needsInstall(server, manifest, p.desiredModpackFor(server, cfg), false, pinnedLoaderVersion(server, cfg)) {
		t.Fatal("pinning the shipped version must not reinstall")
	}
	// Explicit version changes still reprovision as usual
	server.McVersion = "1.20.4"
	if !p.needsInstall(server, manifest, nil, false, "") {
		t.Fatal("changed MC version must reinstall")
	}

	empty := &v1.Server{DataPath: t.TempDir(), ModLoader: v1.ModLoader_MOD_LOADER_VANILLA, McVersion: "1.21.1"}
	if err := adoptImportedFiles(empty, "", 21); err == nil {
		t.Fatal("unlaunchable imports must not adopt")
	}
}

func TestArgsDirLoaderVersion(t *testing.T) {
	cases := []struct {
		args, version, mc string
	}{
		{"libraries/net/minecraftforge/forge/1.20.1-47.2.0/unix_args.txt", "47.2.0", "1.20.1"},
		{"libraries/net/neoforged/neoforge/21.1.143/unix_args.txt", "21.1.143", ""},
		// Beta suffixes are not an mc prefix split
		{"libraries/net/neoforged/neoforge/21.4.0-beta/unix_args.txt", "21.4.0-beta", ""},
		// Legacy NeoForge keeps the full maven version as its pin
		{"libraries/net/neoforged/forge/1.20.1-47.1.106/unix_args.txt", "1.20.1-47.1.106", "1.20.1"},
	}
	for _, tc := range cases {
		version, mc := argsDirLoaderVersion(tc.args)
		if version != tc.version || mc != tc.mc {
			t.Errorf("argsDirLoaderVersion(%q) = %q, %q, want %q, %q", tc.args, version, mc, tc.version, tc.mc)
		}
	}
}

func TestDetectImportedNeoForgeVersions(t *testing.T) {
	cases := []struct {
		args, version, mc string
	}{
		{"libraries/net/neoforged/neoforge/21.1.143/unix_args.txt", "21.1.143", "1.21.1"},
		{"libraries/net/neoforged/neoforge/21.4.0-beta/unix_args.txt", "21.4.0-beta", "1.21.4"},
		{"libraries/net/neoforged/forge/1.20.1-47.1.106/unix_args.txt", "1.20.1-47.1.106", "1.20.1"},
	}
	for _, tc := range cases {
		root := t.TempDir()
		tree := map[string]string{tc.args: ""}
		if tc.mc != "1.20.1" {
			tree["libraries/net/minecraft/server/"+tc.mc+"/server.jar"] = ""
		}
		writeTree(t, root, tree)
		det := DetectImportedServer(root)
		if det.Loader != v1.ModLoader_MOD_LOADER_NEOFORGE || det.LoaderVersion != tc.version || det.McVersion != tc.mc {
			t.Errorf("%s: loader %v version %q mc %q", tc.args, det.Loader, det.LoaderVersion, det.McVersion)
		}
	}
}

func TestNeoForgePinnedArtifact(t *testing.T) {
	cases := []struct {
		mc, pin, artifact, version string
	}{
		{"1.21.1", "21.1.143", "neoforge", "21.1.143"},
		{"1.21.4", "21.4.0-beta", "neoforge", "21.4.0-beta"},
		// Imported 1.20.1 pins resolve under the legacy forge artifact
		{"1.20.1", "1.20.1-47.1.106", "forge", "1.20.1-47.1.106"},
		{"1.20.1", "47.1.106", "forge", "1.20.1-47.1.106"},
	}
	for _, tc := range cases {
		base, artifact, version := neoforgePinnedArtifact(tc.mc, tc.pin)
		if artifact != tc.artifact || version != tc.version {
			t.Errorf("neoforgePinnedArtifact(%q, %q) = %q, %q", tc.mc, tc.pin, artifact, version)
		}
		wantBase := neoforgeMavenURL()
		if tc.artifact == "forge" {
			wantBase = neoforgeLegacyURL()
		}
		if base != wantBase {
			t.Errorf("neoforgePinnedArtifact(%q, %q) base %q, want %q", tc.mc, tc.pin, base, wantBase)
		}
	}

	// The legacy pin the import writes satisfies needsInstall after adoption
	dataPath := t.TempDir()
	writeTree(t, dataPath, map[string]string{"libraries/net/neoforged/forge/1.20.1-47.1.106/unix_args.txt": ""})
	server := &v1.Server{Id: "n", Name: "n", DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_NEOFORGE, McVersion: "1.20.1"}
	det := DetectImportedServer(dataPath)
	if err := adoptImportedFiles(server, det.LoaderVersion, 17); err != nil {
		t.Fatal(err)
	}
	manifest, _ := runtimespec.ReadManifest(dataPath)
	pin := det.LoaderVersion
	cfg := &v1.ServerProperties{ForgeVersion: &pin}
	p := &Provisioner{}
	if p.needsInstall(server, manifest, nil, false, pinnedLoaderVersion(server, cfg)) {
		t.Fatal("legacy NeoForge import must not reinstall")
	}
}
