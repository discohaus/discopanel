package provisioner

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Opens a throwaway store for sweep tests
func testStore(t *testing.T) *storage.Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "test.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func writeClientJar(t *testing.T, dir, name, manifest string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDisableClientOnlyMods(t *testing.T) {
	dataPath := t.TempDir()
	modsDir := filepath.Join(dataPath, "mods")
	writeClientJar(t, modsDir, "clientmod.jar", `{"id":"clientmod","environment":"client"}`)
	writeClientJar(t, modsDir, "servermod.jar", `{"id":"servermod","environment":"*"}`)
	writeClientJar(t, modsDir, "keepme.jar", `{"id":"keepme","environment":"client"}`)
	// Mirrors supplementaries, flagged client-only yet a real dep
	writeClientJar(t, modsDir, "supplementaries.jar", `{"id":"supplementaries","environment":"client"}`)
	writeClientJar(t, modsDir, "needy.jar", `{"id":"needy","environment":"*","depends":{"supplementaries":"*"}}`)

	p := &Provisioner{log: logger.New(), store: testStore(t)}
	server := &v1.Server{DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_MODRINTH}
	p.disableClientOnlyMods(context.Background(), server, []string{"keepme"}, nil)

	if _, err := os.Stat(filepath.Join(modsDir+"_disabled", "clientmod.jar")); err != nil {
		t.Fatal("client-only jar should be disabled")
	}
	if _, err := os.Stat(filepath.Join(modsDir, "servermod.jar")); err != nil {
		t.Fatal("server-safe jar must stay")
	}
	if _, err := os.Stat(filepath.Join(modsDir, "keepme.jar")); err != nil {
		t.Fatal("force-included jar must stay")
	}
	if _, err := os.Stat(filepath.Join(modsDir, "supplementaries.jar")); err != nil {
		t.Fatal("depended-on jar must survive the sweep")
	}
}

func TestPackDownloadFlagsKnownClientMods(t *testing.T) {
	slugged := &fuego.File{FileName: "some-shaders-1.0.jar", GameVersions: []string{"Client", "Server"}}
	if wanted, flag := cfFileWanted(slugged, &fuego.Modpack{Slug: "oculus"}, 42, nil, nil); !wanted || flag == "" {
		t.Fatal("known client slug must download flagged")
	}
	if wanted, flag := cfFileWanted(slugged, &fuego.Modpack{Slug: "oculus"}, 42, nil, []string{"oculus"}); !wanted || flag != "" {
		t.Fatal("force include must clear the client flag")
	}
	if wanted, _ := cfFileWanted(slugged, &fuego.Modpack{Slug: "oculus"}, 42, []string{"oculus"}, nil); wanted {
		t.Fatal("explicit exclude must skip the download")
	}
	prefixed := &fuego.File{FileName: "rubidium-0.6.5.jar", GameVersions: []string{"Client", "Server"}}
	if wanted, flag := cfFileWanted(prefixed, &fuego.Modpack{}, 7, nil, nil); !wanted || flag == "" {
		t.Fatal("known client file prefix must download flagged")
	}
	envTagged := &fuego.File{FileName: "athena-4.0.0.jar", GameVersions: []string{"Client"}}
	if wanted, flag := cfFileWanted(envTagged, &fuego.Modpack{Slug: "athena"}, 8, nil, nil); !wanted || flag == "" {
		t.Fatal("client env tags must download flagged")
	}
	plain := &fuego.File{FileName: "create-1.0.jar", GameVersions: []string{"Client", "Server"}}
	if wanted, flag := cfFileWanted(plain, &fuego.Modpack{Slug: "create"}, 9, nil, nil); !wanted || flag != "" {
		t.Fatal("server mod must stay wanted unflagged")
	}

	if wanted, flag := mrpackFileWanted(mrpackFile{Path: "mods/embeddium-0.3.jar"}, nil, nil); !wanted || flag == "" {
		t.Fatal("known client jar must download flagged in mrpack")
	}
	if wanted, flag := mrpackFileWanted(mrpackFile{Path: "mods/embeddium-0.3.jar"}, nil, []string{"embeddium"}); !wanted || flag != "" {
		t.Fatal("force include must clear the flag in mrpack")
	}
	if wanted, flag := mrpackFileWanted(mrpackFile{Path: "mods/lithium-0.11.jar"}, nil, nil); !wanted || flag != "" {
		t.Fatal("server jar must stay wanted unflagged in mrpack")
	}

	if wanted, flag := ftbFileWanted(&ftbFile{Name: "shaders.jar", ClientOnly: true}, nil, nil); !wanted || !flag {
		t.Fatal("FTB client flag must download flagged")
	}
	if wanted, flag := ftbFileWanted(&ftbFile{Name: "shaders.jar", ClientOnly: true}, nil, []string{"shaders"}); !wanted || flag {
		t.Fatal("force include must clear the FTB flag")
	}
	if wanted, _ := ftbFileWanted(&ftbFile{Name: "shaders.jar", ClientOnly: true}, []string{"shaders"}, nil); wanted {
		t.Fatal("explicit exclude must skip the download")
	}
}

func TestPackFlaggedClientModsSweepKeepsDeps(t *testing.T) {
	dataPath := t.TempDir()
	modsDir := filepath.Join(dataPath, "mods")
	// Mirrors chipped needing athena, both flagged client by CF tags
	writeClientJar(t, modsDir, "chipped.jar", `{"id":"chipped","version":"4.0.2","environment":"*","depends":{"athena":">=4.0.0"}}`)
	writeClientJar(t, modsDir, "rechiseled.jar", `{"id":"rechiseled","version":"1.2.5","environment":"*","depends":{"fusion":">=1.2.12"}}`)
	writeClientJar(t, modsDir, "athena.jar", `{"id":"athena","version":"4.0.0","environment":"*"}`)
	writeClientJar(t, modsDir, "fusion.jar", `{"id":"fusion","version":"1.2.12","environment":"*"}`)
	writeClientJar(t, modsDir, "shaders.jar", `{"id":"shaders","version":"1.0.0","environment":"*"}`)

	p := &Provisioner{log: logger.New(), store: testStore(t)}
	server := &v1.Server{DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_CURSEFORGE}

	// Required flagged jars stay, nothing moves on disk
	p.disableClientOnlyMods(context.Background(), server, nil, []string{"athena.jar", "fusion.jar"})

	// Flag marking must not poison the shared scan cache
	for _, meta := range minecraft.ScanModsDir(modsDir) {
		if meta.ClientOnly {
			t.Fatalf("%s cache entry must stay unmarked", meta.FileName)
		}
	}

	flagged := []string{"athena.jar", "fusion.jar", "shaders.jar"}
	p.disableClientOnlyMods(context.Background(), server, nil, flagged)

	for _, name := range []string{"chipped.jar", "rechiseled.jar", "athena.jar", "fusion.jar"} {
		if !fileExists(filepath.Join(modsDir, name)) {
			t.Fatalf("%s must stay enabled, mods depend on it", name)
		}
	}
	if fileExists(filepath.Join(modsDir, "shaders.jar")) {
		t.Fatal("flagged jar nothing needs must be disabled")
	}
	if !fileExists(filepath.Join(modsDir+"_disabled", "shaders.jar")) {
		t.Fatal("disabled jar must land in the disabled dir")
	}
}

func TestSweepRespectsUserModChoices(t *testing.T) {
	dataPath := t.TempDir()
	modsDir := filepath.Join(dataPath, "mods")
	writeClientJar(t, modsDir, "clientmod.jar", `{"id":"clientmod","environment":"client"}`)
	writeClientJar(t, modsDir, "unwanted.jar", `{"id":"unwanted","environment":"*"}`)

	ctx := context.Background()
	store := testStore(t)
	server := &v1.Server{Id: "srv1", Name: "srv1", DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_MODRINTH}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("server create failed %v", err)
	}
	rows := []*v1.Mod{
		{Id: "m1", ServerId: "srv1", FileName: "clientmod.jar", DisplayName: "clientmod", Enabled: true},
		{Id: "m2", ServerId: "srv1", FileName: "unwanted.jar", DisplayName: "unwanted", Enabled: false},
	}
	if err := store.CreateMod(ctx, rows...); err != nil {
		t.Fatalf("mod rows failed %v", err)
	}
	// Map update dodges the gorm bool default skip
	if err := store.UpdateModFields(ctx, "m2", map[string]any{"enabled": false}); err != nil {
		t.Fatalf("mod row patch failed %v", err)
	}

	p := &Provisioner{log: logger.New(), store: store}
	p.disableClientOnlyMods(ctx, server, nil, nil)

	if !fileExists(filepath.Join(modsDir, "clientmod.jar")) {
		t.Fatal("user enabled jar must survive the sweep")
	}
	if fileExists(filepath.Join(modsDir, "unwanted.jar")) {
		t.Fatal("user disabled jar must not stay enabled")
	}
	if !fileExists(filepath.Join(modsDir+"_disabled", "unwanted.jar")) {
		t.Fatal("user disabled jar must land in the disabled dir")
	}
}

func TestEnsureGatesEULABeforeInstall(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.DataDir = t.TempDir()
	p := New(nil, nil, cfg, nil, logger.New())
	server := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir(), ModLoader: v1.ModLoader_MOD_LOADER_VANILLA, McVersion: "1.21.1"}

	_, err := p.Ensure(context.Background(), server, &v1.ServerProperties{})
	if err == nil || !strings.Contains(err.Error(), "EULA") {
		t.Fatalf("expected EULA gate before install, got %v", err)
	}
}

func TestOverrideWhitelistTruncates(t *testing.T) {
	p := testProvisioner(t)
	server := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir()}
	path := filepath.Join(server.DataPath, "whitelist.json")
	seed := `[{"uuid":"0-0-0-0-0","name":"steve"}]`
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfgRow := &v1.ServerProperties{}

	// Empty list without override leaves the file alone
	if err := p.writePlayerListFile(ctx, server, cfgRow, "whitelist.json", "", false, false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != seed {
		t.Fatalf("merge mode must not touch the file, got %s", data)
	}

	// Explicit override with an empty list truncates
	if err := p.writePlayerListFile(ctx, server, cfgRow, "whitelist.json", "", false, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("override with empty list must truncate, got %s", data)
	}
}

func TestManagementSecretPersists(t *testing.T) {
	p := testProvisioner(t)
	server := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir(), Port: 25565}
	cfgRow := &v1.ServerProperties{}

	if err := p.writeServerProperties(server, cfgRow, "1.21.9"); err != nil {
		t.Fatal(err)
	}
	readSecret := func() string {
		props, err := minecraft.LoadPropertiesFile(server.DataPath)
		if err != nil {
			t.Fatal(err)
		}
		return props["management-server-secret"]
	}
	first := readSecret()
	if len(first) != 40 {
		t.Fatalf("expected a 40 char secret, got %q", first)
	}
	if err := p.writeServerProperties(server, cfgRow, "1.21.9"); err != nil {
		t.Fatal(err)
	}
	if again := readSecret(); again != first {
		t.Fatalf("secret must persist across Ensure, %q became %q", first, again)
	}
}

func TestProxiedServersAcceptTransfers(t *testing.T) {
	p := testProvisioner(t)
	readProp := func(server *v1.Server) string {
		props, err := minecraft.LoadPropertiesFile(server.DataPath)
		if err != nil {
			t.Fatal(err)
		}
		return props["accepts-transfers"]
	}

	proxied := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir(), Port: 25565,
		ProxyHostnames: []string{"play.example.com"}, ProxyListenerId: "l1"}
	if err := p.writeServerProperties(proxied, &v1.ServerProperties{}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(proxied); got != "true" {
		t.Fatalf("proxied server must accept transfers, got %q", got)
	}

	direct := &v1.Server{Id: "s2", Name: "s2", DataPath: t.TempDir(), Port: 25565}
	if err := p.writeServerProperties(direct, &v1.ServerProperties{}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(direct); got != "" {
		t.Fatalf("unproxied server must stay untouched, got %q", got)
	}

	refused := false
	optOut := &v1.Server{Id: "s3", Name: "s3", DataPath: t.TempDir(), Port: 25565,
		ProxyHostnames: []string{"play.example.com"}, ProxyListenerId: "l1"}
	if err := p.writeServerProperties(optOut, &v1.ServerProperties{AcceptsTransfers: &refused}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(optOut); got != "false" {
		t.Fatalf("explicit opt out must survive, got %q", got)
	}
}

func TestAutopauseDisablesWatchdog(t *testing.T) {
	p := testProvisioner(t)
	readProp := func(server *v1.Server) string {
		props, err := minecraft.LoadPropertiesFile(server.DataPath)
		if err != nil {
			t.Fatal(err)
		}
		return props["max-tick-time"]
	}

	on := true
	paused := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir(), Port: 25565}
	if err := p.writeServerProperties(paused, &v1.ServerProperties{EnableAutopause: &on}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(paused); got != "-1" {
		t.Fatalf("autopause must disable the watchdog, got %q", got)
	}

	plain := &v1.Server{Id: "s2", Name: "s2", DataPath: t.TempDir(), Port: 25565}
	if err := p.writeServerProperties(plain, &v1.ServerProperties{}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(plain); got != "" {
		t.Fatalf("no autopause must leave the watchdog alone, got %q", got)
	}

	custom := "max-tick-time=60000"
	overridden := &v1.Server{Id: "s3", Name: "s3", DataPath: t.TempDir(), Port: 25565}
	if err := p.writeServerProperties(overridden, &v1.ServerProperties{EnableAutopause: &on, CustomServerProperties: &custom}, "1.21.1"); err != nil {
		t.Fatal(err)
	}
	if got := readProp(overridden); got != "60000" {
		t.Fatalf("custom properties must win, got %q", got)
	}
}

func TestModrinthStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := readModrinthState(dir)
	if len(state.Projects) != 0 {
		t.Fatalf("fresh state should be empty, got %+v", state.Projects)
	}
	state.Projects["sodium"] = modrinthProjectState{
		VersionID:    "abc",
		FileName:     "sodium-0.5.jar",
		McVersion:    "1.20.1",
		Loader:       "fabric",
		RequiredDeps: []string{"fabric-api"},
	}
	if err := writeModrinthState(dir, state); err != nil {
		t.Fatal(err)
	}
	again := readModrinthState(dir)
	entry, ok := again.Projects["sodium"]
	if !ok || entry.FileName != "sodium-0.5.jar" || entry.McVersion != "1.20.1" ||
		entry.Loader != "fabric" || len(entry.RequiredDeps) != 1 {
		t.Fatalf("state round trip mismatch, got %+v", again.Projects)
	}
}

func TestPickAllowedVersion(t *testing.T) {
	versions := []modrinth.Version{
		{ID: "b1", VersionType: "beta"},
		{ID: "a1", VersionType: "alpha"},
	}
	if pick := pickAllowedVersion(versions, "release"); pick != nil {
		t.Fatalf("release channel must reject beta and alpha, got %+v", pick)
	}
	if pick := pickAllowedVersion(versions, "beta"); pick == nil || pick.ID != "b1" {
		t.Fatalf("beta channel should pick b1, got %+v", pick)
	}
	if pick := pickAllowedVersion(versions, "alpha"); pick == nil || pick.ID != "b1" {
		t.Fatalf("alpha channel allows beta too, got %+v", pick)
	}
	got := strings.Join(versionTypesOf(versions), ",")
	if got != "beta,alpha" {
		t.Fatalf("expected beta,alpha, got %s", got)
	}
}

func writeVersionJar(t *testing.T, path, versionJSON string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("version.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(versionJSON)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestServerPackMCVersionEvidence(t *testing.T) {
	if v := forgeArgsMCVersion("libraries/net/minecraftforge/forge/1.20.1-47.2.20/unix_args.txt"); v != "1.20.1" {
		t.Fatalf("forge args path should testify 1.20.1, got %q", v)
	}
	if v := forgeArgsMCVersion("libraries/net/neoforged/neoforge/20.4.237/unix_args.txt"); v != "" {
		t.Fatalf("neoforge version dirs must not testify, got %q", v)
	}

	dataPath := t.TempDir()
	writeVersionJar(t, filepath.Join(dataPath, "server.jar"), `{"id":"1.12.2","name":"1.12.2","world_version":1343}`)
	if v := jarMCVersion(filepath.Join(dataPath, "server.jar")); v != "1.12.2" {
		t.Fatalf("vanilla version.json should testify 1.12.2, got %q", v)
	}

	// Forge launch profiles lack world_version and never testify
	writeVersionJar(t, filepath.Join(dataPath, "forge.jar"), `{"id":"1.12.2-forge1.12.2-14.23.5.2860","inheritsFrom":"1.12.2"}`)
	if v := jarMCVersion(filepath.Join(dataPath, "forge.jar")); v != "" {
		t.Fatalf("forge profile must not testify, got %q", v)
	}
}
