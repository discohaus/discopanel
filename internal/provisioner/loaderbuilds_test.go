package provisioner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Trimmed copies of real upstream responses
const (
	fabricLoadersFixture = `[
		{"loader":{"separator":".","build":12,"maven":"net.fabricmc:fabric-loader:0.17.0-beta.1","version":"0.17.0-beta.1","stable":false},"intermediary":{"version":"1.21.1","stable":true}},
		{"loader":{"separator":".","build":11,"maven":"net.fabricmc:fabric-loader:0.16.10","version":"0.16.10","stable":true},"intermediary":{"version":"1.21.1","stable":true}},
		{"loader":{"separator":".","build":10,"maven":"net.fabricmc:fabric-loader:0.16.9","version":"0.16.9","stable":true},"intermediary":{"version":"1.21.1","stable":true}}
	]`
	quiltLoadersFixture = `[
		{"loader":{"separator":".","build":1,"maven":"org.quiltmc:quilt-loader:0.28.0-beta.2","version":"0.28.0-beta.2"},"hashed":{"version":"1.21.1"}},
		{"loader":{"separator":".","build":2,"maven":"org.quiltmc:quilt-loader:0.27.1","version":"0.27.1"},"hashed":{"version":"1.21.1"}},
		{"loader":{"separator":".","build":3,"maven":"org.quiltmc:quilt-loader:0.27.0","version":"0.27.0"},"hashed":{"version":"1.21.1"}}
	]`
	neoforgeVersionsFixture = `{"isSnapshot":false,"versions":[
		"21.0.167","21.1.1-beta","21.1.9","21.1.99","21.1.143","21.1.144-beta","21.2.0-beta","21.10.1"
	]}`
	neoforgeLegacyFixture = `{"isSnapshot":false,"versions":["1.20.1-47.1.3","1.20.1-47.1.106","1.20.1-47.1.79"]}`
	forgeMetadataFixture  = `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>net.minecraftforge</groupId>
  <artifactId>forge</artifactId>
  <versioning>
    <latest>1.21.10-60.0.1</latest>
    <release>1.21.10-60.0.1</release>
    <versions>
      <version>1.21.10-60.0.1</version>
      <version>1.21.1-52.0.9</version>
      <version>1.21.1-52.0.10</version>
      <version>1.21.1-52.1.0</version>
      <version>1.21-51.0.33</version>
      <version>1.7.10-10.13.4.1614-1.7.10</version>
      <version>1.7.10-10.13.4.1558-1.7.10</version>
    </versions>
  </versioning>
</metadata>`
	forgePromotionsFixture = `{"homepage":"https://files.minecraftforge.net/","promos":{
		"1.21.1-latest":"52.1.0","1.21.1-recommended":"52.0.10","1.21.10-latest":"60.0.1"
	}}`
)

func versionsOf(builds []LoaderBuild) []string {
	out := make([]string, len(builds))
	for i, b := range builds {
		out[i] = b.Version
	}
	return out
}

func checkBuilds(t *testing.T, builds []LoaderBuild, wantVersions []string, wantRecommended string, wantUnstable []string) {
	t.Helper()
	if got := strings.Join(versionsOf(builds), ","); got != strings.Join(wantVersions, ",") {
		t.Fatalf("versions = %s, want %s", got, strings.Join(wantVersions, ","))
	}
	if got := recommendedBuild(builds); got != wantRecommended {
		t.Fatalf("recommended = %q, want %q", got, wantRecommended)
	}
	recommended := 0
	for _, b := range builds {
		if b.Recommended {
			recommended++
		}
		unstable := false
		for _, u := range wantUnstable {
			unstable = unstable || u == b.Version
		}
		if b.Stable == unstable {
			t.Fatalf("%s stable = %v, want %v", b.Version, b.Stable, !unstable)
		}
	}
	if wantRecommended != "" && recommended != 1 {
		t.Fatalf("%d builds flagged recommended, want 1", recommended)
	}
}

func TestParseFabricLoaders(t *testing.T) {
	builds, err := parseFabricLoaders([]byte(fabricLoadersFixture))
	if err != nil {
		t.Fatal(err)
	}
	checkBuilds(t, builds, []string{"0.17.0-beta.1", "0.16.10", "0.16.9"}, "0.16.10", []string{"0.17.0-beta.1"})

	if _, err := parseFabricLoaders([]byte(`{"oops":true}`)); err == nil {
		t.Fatal("expected an error for a non-array body")
	}
	empty, err := parseFabricLoaders([]byte(`[]`))
	if err != nil || len(empty) != 0 || recommendedBuild(empty) != "" {
		t.Fatalf("empty listing = %v, %v", empty, err)
	}
}

func TestParseQuiltLoaders(t *testing.T) {
	builds, err := parseQuiltLoaders([]byte(quiltLoadersFixture))
	if err != nil {
		t.Fatal(err)
	}
	checkBuilds(t, builds, []string{"0.28.0-beta.2", "0.27.1", "0.27.0"}, "0.27.1", []string{"0.28.0-beta.2"})

	// Only prereleases still yields a default
	onlyBeta, err := parseQuiltLoaders([]byte(`[{"loader":{"version":"0.30.0-beta.1"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	checkBuilds(t, onlyBeta, []string{"0.30.0-beta.1"}, "0.30.0-beta.1", []string{"0.30.0-beta.1"})
}

func TestParseNeoForgeVersions(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		mc          string
		want        []string
		recommended string
		unstable    []string
	}{
		{
			name:        "modern prefix filter and numeric order",
			body:        neoforgeVersionsFixture,
			mc:          "1.21.1",
			want:        []string{"21.1.144-beta", "21.1.143", "21.1.99", "21.1.9", "21.1.1-beta"},
			recommended: "21.1.143",
			unstable:    []string{"21.1.144-beta", "21.1.1-beta"},
		},
		{
			name:        "two part MC version",
			body:        neoforgeVersionsFixture,
			mc:          "1.21",
			want:        []string{"21.0.167"},
			recommended: "21.0.167",
		},
		{
			name:        "betas only",
			body:        neoforgeVersionsFixture,
			mc:          "1.21.2",
			want:        []string{"21.2.0-beta"},
			recommended: "21.2.0-beta",
			unstable:    []string{"21.2.0-beta"},
		},
		{
			name:        "legacy artifact keeps the MC prefix",
			body:        neoforgeLegacyFixture,
			mc:          "1.20.1",
			want:        []string{"1.20.1-47.1.106", "1.20.1-47.1.79", "1.20.1-47.1.3"},
			recommended: "1.20.1-47.1.106",
		},
		{
			name: "no builds",
			body: neoforgeVersionsFixture,
			mc:   "1.19.2",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builds, err := parseNeoForgeVersions([]byte(tt.body), neoforgeSourceFor(tt.mc).prefix)
			if err != nil {
				t.Fatal(err)
			}
			checkBuilds(t, builds, tt.want, tt.recommended, tt.unstable)
		})
	}
}

func TestNeoForgeSourceFor(t *testing.T) {
	legacy := neoforgeSourceFor("1.20.1")
	if legacy.artifact != "forge" || legacy.prefix != "1.20.1-" || !strings.HasSuffix(legacy.maven, "/net/neoforged/forge") {
		t.Fatalf("legacy source = %+v", legacy)
	}
	modern := neoforgeSourceFor("1.21.1")
	if modern.artifact != "neoforge" || modern.prefix != "21.1." || !strings.HasSuffix(modern.maven, "/net/neoforged/neoforge") {
		t.Fatalf("modern source = %+v", modern)
	}
}

func TestParseForgeMetadata(t *testing.T) {
	tests := []struct {
		mc   string
		want []string
	}{
		// 1.21.10 and 1.21 entries must not leak into 1.21.1
		{mc: "1.21.1", want: []string{"52.1.0", "52.0.10", "52.0.9"}},
		{mc: "1.21", want: []string{"51.0.33"}},
		// Branch suffixes are stripped from the pin
		{mc: "1.7.10", want: []string{"10.13.4.1614", "10.13.4.1558"}},
		{mc: "1.12.2", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.mc, func(t *testing.T) {
			builds, err := parseForgeMetadata([]byte(forgeMetadataFixture), tt.mc)
			if err != nil {
				t.Fatal(err)
			}
			// Promotions are applied separately
			checkBuilds(t, builds, tt.want, "", nil)
		})
	}

	if _, err := parseForgeMetadata([]byte("not xml <"), "1.21.1"); err == nil {
		t.Fatal("expected an error for malformed metadata")
	}
}

func TestForgePromotions(t *testing.T) {
	tests := []struct {
		mc   string
		want string
	}{
		{mc: "1.21.1", want: "52.0.10"}, // Recommended wins
		{mc: "1.21.10", want: "60.0.1"}, // Latest without a recommendation
		{mc: "1.12.2", want: ""},        // Not promoted at all
	}
	for _, tt := range tests {
		if got := forgePromotedVersion([]byte(forgePromotionsFixture), tt.mc); got != tt.want {
			t.Fatalf("promoted %s = %q, want %q", tt.mc, got, tt.want)
		}
	}
	if got := forgePromotedVersion([]byte("{"), "1.21.1"); got != "" {
		t.Fatalf("malformed promotions = %q", got)
	}

	builds, err := parseForgeMetadata([]byte(forgeMetadataFixture), "1.21.1")
	if err != nil {
		t.Fatal(err)
	}
	markForgePromotion(builds, forgePromotedVersion([]byte(forgePromotionsFixture), "1.21.1"))
	checkBuilds(t, builds, []string{"52.1.0", "52.0.10", "52.0.9"}, "52.0.10", nil)
}

func TestPinnedLoaderVersion(t *testing.T) {
	tests := []struct {
		loader v1.ModLoader
		pin    string
		want   string
	}{
		{v1.ModLoader_MOD_LOADER_FABRIC, " 0.16.9 ", "0.16.9"},
		{v1.ModLoader_MOD_LOADER_QUILT, "0.27.0-beta.1", "0.27.0-beta.1"},
		{v1.ModLoader_MOD_LOADER_FORGE, "47.2.0", "47.2.0"},
		{v1.ModLoader_MOD_LOADER_NEOFORGE, "21.1.143", "21.1.143"},
		{v1.ModLoader_MOD_LOADER_NEOFORGE, "1.20.1-47.1.106", "1.20.1-47.1.106"},
		// Pins from the other loader family are ignored
		{v1.ModLoader_MOD_LOADER_FABRIC, "47.2.0", ""},
		{v1.ModLoader_MOD_LOADER_QUILT, "21.1.143", ""},
		{v1.ModLoader_MOD_LOADER_FORGE, "0.16.9", ""},
		{v1.ModLoader_MOD_LOADER_PAPER, "0.16.9", ""},
		{v1.ModLoader_MOD_LOADER_VANILLA, "47.2.0", ""},
		{v1.ModLoader_MOD_LOADER_MODRINTH, "47.2.0", ""},
	}
	for _, tt := range tests {
		pin := tt.pin
		cfg := &v1.ServerProperties{ForgeVersion: &pin}
		if got := pinnedLoaderVersion(&v1.Server{ModLoader: tt.loader}, cfg); got != tt.want {
			t.Fatalf("%v pin %q = %q, want %q", tt.loader, tt.pin, got, tt.want)
		}
	}
}

func TestDropForeignPinClearsStaleForgePin(t *testing.T) {
	store := testStore(t)
	p := &Provisioner{log: logger.New(), store: store}
	ctx := t.Context()

	cfg := store.CreateDefaultServerProperties("srv")
	stale := "47.2.0"
	cfg.ForgeVersion = &stale
	if err := store.UpdateServerProperties(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	// Server switched Forge to Fabric before pins were cleared
	server := &v1.Server{Id: "srv", Name: "test", ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.21.1"}
	p.dropForeignPin(ctx, server, cfg)
	if cfg.ForgeVersion != nil {
		t.Fatalf("in-memory pin = %q, want cleared", *cfg.ForgeVersion)
	}
	saved, err := store.GetServerProperties(ctx, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ForgeVersion != nil {
		t.Fatalf("stored pin = %q, want cleared", *saved.ForgeVersion)
	}

	// A matching pin is left alone
	keep := "0.16.9"
	cfg.ForgeVersion = &keep
	p.dropForeignPin(ctx, server, cfg)
	if strVal(cfg.ForgeVersion) != "0.16.9" {
		t.Fatalf("matching pin dropped")
	}
}

func TestGlobalLoaderBuildDoesNotInherit(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	global := store.CreateDefaultServerProperties(storage.GlobalSettingsID)
	pin, difficulty := "47.2.0", "hard"
	global.ForgeVersion = &pin
	global.Difficulty = &difficulty
	if err := store.UpdateGlobalSettings(ctx, global); err != nil {
		t.Fatal(err)
	}
	cfg := store.CreateDefaultServerProperties("srv")
	if cfg.ForgeVersion != nil {
		t.Fatalf("global loader build inherited: %q", *cfg.ForgeVersion)
	}
	if strVal(cfg.Difficulty) != "hard" {
		t.Fatalf("other globals must still inherit, difficulty = %q", strVal(cfg.Difficulty))
	}
}

func TestPinnedBuildRejectsUnsafePins(t *testing.T) {
	p := testProvisioner(t)
	for _, pin := range []string{"0.16.9/../../x", "0.16.9?a=b", "0.16.9 --evil"} {
		pin := pin
		cfg := &v1.ServerProperties{ForgeVersion: &pin}
		server := &v1.Server{Name: "test", ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.21.1"}
		if _, err := p.pinnedBuild(t.Context(), server, cfg); err == nil || !strings.Contains(err.Error(), "not a valid version") {
			t.Fatalf("pin %q err = %v", pin, err)
		}
	}
}

func TestPinChangeForcesFabricReinstall(t *testing.T) {
	p := &Provisioner{}
	server := &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_QUILT, McVersion: "1.21.1", DataPath: t.TempDir()}
	manifest := &v1.Manifest{Loader: v1.ModLoader_MOD_LOADER_QUILT, LoaderVersion: "0.27.1", McVersion: "1.21.1"}
	if !p.needsInstall(server, manifest, nil, false, "0.27.0") {
		t.Fatal("a different pinned Quilt build must reinstall")
	}
}

// Serves loader listings the way the index fronts the meta hosts
func loaderMetaServer(t *testing.T) map[string]*atomic.Int32 {
	t.Helper()
	paths := []string{
		"/fabric/v2/versions/loader/1.21.1",
		"/quilt/v3/versions/loader/1.21.1",
		"/fabric/v2/versions/loader/1.99",
		"/quilt/v3/versions/loader/1.99",
		"/forge/maven/net/minecraftforge/forge/maven-metadata.xml",
		"/forge/files/net/minecraftforge/forge/promotions_slim.json",
	}
	calls := make(map[string]*atomic.Int32, len(paths))
	for _, path := range paths {
		calls[path] = &atomic.Int32{}
	}
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := calls[r.URL.Path]; ok {
			c.Add(1)
		}
		switch r.URL.Path {
		case "/fabric/v2/versions/loader/1.21.1":
			w.Write([]byte(fabricLoadersFixture))
		case "/quilt/v3/versions/loader/1.21.1":
			w.Write([]byte(quiltLoadersFixture))
		case "/fabric/v2/versions/loader/1.99", "/quilt/v3/versions/loader/1.99":
			w.Write([]byte(`[]`))
		case "/forge/maven/net/minecraftforge/forge/maven-metadata.xml":
			w.Write([]byte(forgeMetadataFixture))
		default:
			// Promotions stay down to exercise the failure cache
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(index.Close)
	configureIndexForTest(t, index.URL)
	return calls
}

func TestPinnedBuildFabricAndQuilt(t *testing.T) {
	loaderMetaServer(t)

	tests := []struct {
		name    string
		loader  v1.ModLoader
		mc      string
		pin     string
		want    string
		wantErr string
	}{
		{name: "fabric no pin", loader: v1.ModLoader_MOD_LOADER_FABRIC, mc: "1.21.1"},
		{name: "fabric stable pin", loader: v1.ModLoader_MOD_LOADER_FABRIC, mc: "1.21.1", pin: "0.16.9", want: "0.16.9"},
		{name: "fabric beta pin", loader: v1.ModLoader_MOD_LOADER_FABRIC, mc: "1.21.1", pin: "0.17.0-beta.1", want: "0.17.0-beta.1"},
		{name: "fabric unknown pin", loader: v1.ModLoader_MOD_LOADER_FABRIC, mc: "1.21.1", pin: "0.1.0", wantErr: `Fabric build "0.1.0" does not exist for MC 1.21.1 (newest is 0.17.0-beta.1)`},
		{name: "fabric no builds", loader: v1.ModLoader_MOD_LOADER_FABRIC, mc: "1.99", pin: "0.16.9", wantErr: "Fabric has no builds for MC 1.99"},
		{name: "quilt pin", loader: v1.ModLoader_MOD_LOADER_QUILT, mc: "1.21.1", pin: "0.27.0", want: "0.27.0"},
		{name: "quilt unknown pin", loader: v1.ModLoader_MOD_LOADER_QUILT, mc: "1.21.1", pin: "0.16.9", wantErr: `Quilt build "0.16.9" does not exist for MC 1.21.1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProvisioner(t)
			pin := tt.pin
			cfg := &v1.ServerProperties{ForgeVersion: &pin}
			server := &v1.Server{Name: "test", ModLoader: tt.loader, McVersion: tt.mc}
			got, err := p.pinnedBuild(t.Context(), server, cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("pinned = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoaderBuildsCaches(t *testing.T) {
	calls := loaderMetaServer(t)
	p := testProvisioner(t)

	for range 3 {
		builds, err := p.LoaderBuilds(t.Context(), v1.ModLoader_MOD_LOADER_QUILT, "1.21.1")
		if err != nil {
			t.Fatal(err)
		}
		if recommendedBuild(builds) != "0.27.1" {
			t.Fatalf("recommended = %q", recommendedBuild(builds))
		}
	}
	if n := calls["/quilt/v3/versions/loader/1.21.1"].Load(); n != 1 {
		t.Fatalf("quilt upstream hit %d times, want 1", n)
	}

	// One Forge document serves every MC version
	for _, mc := range []string{"1.21.1", "1.21", "1.7.10", "1.21.1"} {
		builds, err := p.LoaderBuilds(t.Context(), v1.ModLoader_MOD_LOADER_FORGE, mc)
		if err != nil {
			t.Fatal(err)
		}
		if len(builds) == 0 {
			t.Fatalf("no Forge builds for %s", mc)
		}
		// Promotions are down, nothing is recommended
		if recommendedBuild(builds) != "" {
			t.Fatalf("recommended without promotions = %q", recommendedBuild(builds))
		}
	}
	if n := calls["/forge/maven/net/minecraftforge/forge/maven-metadata.xml"].Load(); n != 1 {
		t.Fatalf("forge metadata hit %d times, want 1", n)
	}
	// Failed promotions are cached too, but only briefly
	if n := calls["/forge/files/net/minecraftforge/forge/promotions_slim.json"].Load(); n != 1 {
		t.Fatalf("forge promotions hit %d times, want 1", n)
	}
	for key, e := range p.metaDocs.entries {
		if !strings.Contains(key, "promotions_slim.json") {
			continue
		}
		if e.err == nil || e.expires.Sub(e.added) != metaFailureTTL {
			t.Fatalf("promotions failure entry = %+v, want a %v failure", e, metaFailureTTL)
		}
	}

	if _, err := p.LoaderBuilds(t.Context(), v1.ModLoader_MOD_LOADER_PAPER, "1.21.1"); err == nil {
		t.Fatal("paper has no selectable builds")
	}
}

func TestMetaDocCacheBounded(t *testing.T) {
	var c metaDocCache
	for i := range metaDocMax * 3 {
		c.put(fmt.Sprintf("https://meta.example/%d", i), []byte("x"), nil)
	}
	if n := c.len(); n > metaDocMax {
		t.Fatalf("cache holds %d entries, cap is %d", n, metaDocMax)
	}
	// Newest entries survive eviction
	if _, ok := c.get(fmt.Sprintf("https://meta.example/%d", metaDocMax*3-1)); !ok {
		t.Fatal("newest entry evicted")
	}
	if _, ok := c.get("https://meta.example/0"); ok {
		t.Fatal("oldest entry kept past the cap")
	}
	c.put("https://meta.example/fail", nil, fmt.Errorf("boom"))
	e, ok := c.get("https://meta.example/fail")
	if !ok || e.err == nil || e.expires.Sub(e.added) != metaFailureTTL {
		t.Fatalf("failure entry = %+v, %v", e, ok)
	}
}
