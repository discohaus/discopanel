package hub

import (
	"strings"
	"testing"
)

// The index's rewrite table, origin to prefix, as the hub publishes it
var indexRewrites = map[string]string{
	"https://api.modrinth.com":         "/modrinth",
	"https://api.curseforge.com":       "/curseforge",
	"https://edge.forgecdn.net":        "/curseforge-cdn",
	"https://piston-meta.mojang.com":   "/mojang/meta",
	"https://piston-data.mojang.com":   "/mojang/data",
	"https://launcher.mojang.com":      "/mojang/launcher",
	"https://api.mojang.com":           "/mojang-api",
	"https://meta.fabricmc.net":        "/fabric",
	"https://meta.quiltmc.org":         "/quilt",
	"https://maven.quiltmc.org":        "/quilt-maven",
	"https://fill.papermc.io":          "/paper",
	"https://fill-data.papermc.io":     "/paper-data",
	"https://api.purpurmc.org":         "/purpur",
	"https://files.minecraftforge.net": "/forge/files",
	"https://maven.minecraftforge.net": "/forge/maven",
	"https://maven.neoforged.net":      "/neoforge",
	"https://api.feed-the-beast.com":   "/ftb",
}

func TestUpstreamTableMatchesIndexRewrites(t *testing.T) {
	if len(Upstreams) != len(indexRewrites) {
		t.Fatalf("table has %d upstreams, the index fronts %d", len(Upstreams), len(indexRewrites))
	}
	for _, u := range Upstreams {
		prefix, ok := indexRewrites[u.Origin]
		if !ok {
			t.Errorf("%s origin %s is not fronted by the index", u.Name, u.Origin)
			continue
		}
		if prefix != u.Prefix {
			t.Errorf("%s prefix = %s, index uses %s", u.Name, u.Prefix, prefix)
		}
	}
}

func TestBasesThroughTheIndex(t *testing.T) {
	configureForTest(t, Settings{SupportBase: DefaultSupportBase, IndexBase: "https://index.example", IndexEnabled: true, InstallID: testInstallID})
	cases := map[string]string{
		Modrinth():       "https://index.example/modrinth",
		CurseForge():     "https://index.example/curseforge",
		CurseForgeCDN():  "https://index.example/curseforge-cdn",
		MojangMeta():     "https://index.example/mojang/meta",
		MojangData():     "https://index.example/mojang/data",
		MojangLauncher(): "https://index.example/mojang/launcher",
		MojangAPI():      "https://index.example/mojang-api",
		Fabric():         "https://index.example/fabric",
		Quilt():          "https://index.example/quilt",
		QuiltMaven():     "https://index.example/quilt-maven",
		Paper():          "https://index.example/paper",
		PaperData():      "https://index.example/paper-data",
		Purpur():         "https://index.example/purpur",
		ForgeFiles():     "https://index.example/forge/files",
		ForgeMaven():     "https://index.example/forge/maven",
		NeoForge():       "https://index.example/neoforge",
		FTB():            "https://index.example/ftb",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("base = %s, want %s", got, want)
		}
	}
	if hosts := UpstreamHosts(); len(hosts) != 1 || hosts[0] != "index.example" {
		t.Errorf("upstream hosts through the index = %v", hosts)
	}
}

func TestBasesIndexOff(t *testing.T) {
	configureForTest(t, Settings{SupportBase: DefaultSupportBase, IndexBase: DefaultIndexBase, InstallID: testInstallID})
	for _, u := range Upstreams {
		if got := Base(u.Name); got != u.Origin {
			t.Errorf("index off %s = %s, want %s", u.Name, got, u.Origin)
		}
	}
	if Modrinth() != "https://api.modrinth.com" || CurseForge() != "https://api.curseforge.com" || MojangMeta() != "https://piston-meta.mojang.com" {
		t.Errorf("index off accessors returned index urls")
	}
	hosts := UpstreamHosts()
	if len(hosts) != len(Upstreams) {
		t.Fatalf("index off hosts = %v", hosts)
	}
	for _, h := range hosts {
		if strings.Contains(h, "/") || strings.Contains(h, "index") {
			t.Errorf("host %q is not a bare upstream host", h)
		}
	}
	for i := 1; i < len(hosts); i++ {
		if hosts[i-1] >= hosts[i] {
			t.Errorf("hosts not sorted and distinct: %v", hosts)
		}
	}
}

func TestBaseUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Base on an unknown name should panic")
		}
	}()
	Base("nowhere")
}
