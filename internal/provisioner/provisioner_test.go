package provisioner

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

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

	p := &Provisioner{log: logger.New()}
	server := &v1.Server{DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_MODRINTH}
	p.disableClientOnlyMods(context.Background(), server, []string{"keepme"})

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

func TestPackDownloadSkipsKnownClientMods(t *testing.T) {
	p := &Provisioner{log: logger.New()}
	server := &v1.Server{}

	slugged := &fuego.File{FileName: "some-shaders-1.0.jar", GameVersions: []string{"Client", "Server"}}
	if p.cfFileWanted(server, slugged, &fuego.Modpack{Slug: "oculus"}, 42, nil, nil) {
		t.Fatal("known client slug must be skipped")
	}
	if !p.cfFileWanted(server, slugged, &fuego.Modpack{Slug: "oculus"}, 42, nil, []string{"oculus"}) {
		t.Fatal("force include must override the client list")
	}
	prefixed := &fuego.File{FileName: "rubidium-0.6.5.jar", GameVersions: []string{"Client", "Server"}}
	if p.cfFileWanted(server, prefixed, &fuego.Modpack{}, 7, nil, nil) {
		t.Fatal("known client file prefix must be skipped")
	}
	wanted := &fuego.File{FileName: "create-1.0.jar", GameVersions: []string{"Client", "Server"}}
	if !p.cfFileWanted(server, wanted, &fuego.Modpack{Slug: "create"}, 9, nil, nil) {
		t.Fatal("server mod must stay wanted")
	}

	if p.mrpackFileWanted(server, mrpackFile{Path: "mods/embeddium-0.3.jar"}, nil, nil) {
		t.Fatal("known client jar must be skipped in mrpack")
	}
	if !p.mrpackFileWanted(server, mrpackFile{Path: "mods/embeddium-0.3.jar"}, nil, []string{"embeddium"}) {
		t.Fatal("force include must override in mrpack")
	}
	if !p.mrpackFileWanted(server, mrpackFile{Path: "mods/lithium-0.11.jar"}, nil, nil) {
		t.Fatal("server jar must stay wanted in mrpack")
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
