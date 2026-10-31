package modupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Points the modrinth upstream at a test server through the index prefix
func configureIndex(t *testing.T, indexURL string) {
	t.Helper()
	settings := hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: indexURL, IndexEnabled: true, InstallID: "0123456789abcdef0123456789abcdef"}
	if err := hub.Configure(settings); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(func() {
		hub.Configure(hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: hub.DefaultIndexBase})
	})
}

// Writes a jar with the given body and returns it unhashed
func writeJar(t *testing.T, dir, name, body string) Jar {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return Jar{Root: openRoot(t, dir), Dir: dir, Name: name, Enabled: true, Size: info.Size(), ModTime: info.ModTime()}
}

func TestModrinthLookupRequests(t *testing.T) {
	dir := t.TempDir()
	known := writeJar(t, dir, "LuckPerms-Bukkit-5.4.100.jar", "luckperms old build")
	unknown := writeJar(t, dir, "homemade.jar", "nobody published this")
	kh, err := HashJar(known)
	if err != nil {
		t.Fatal(err)
	}
	uh, _ := HashJar(unknown)

	type seen struct {
		path, ua string
		body     map[string]any
	}
	var mu sync.Mutex
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method == http.MethodPost {
			json.NewDecoder(r.Body).Decode(&body)
		}
		mu.Lock()
		calls = append(calls, seen{r.URL.Path, r.Header.Get("User-Agent"), body})
		mu.Unlock()
		switch r.URL.Path {
		case "/modrinth/v2/version_files":
			fmt.Fprintf(w, `{"%s":{"id":"cur","project_id":"lp","version_number":"5.4.100","version_type":"release",
				"date_published":"2024-01-01T00:00:00Z","loaders":["paper"],"game_versions":["1.21.4"],
				"files":[{"filename":"LuckPerms-Bukkit-5.4.100.jar","url":"https://cdn.example/old.jar","primary":true,"hashes":{"sha512":"%s","sha1":"%s"}}]}}`,
				kh.SHA512, kh.SHA512, kh.SHA1)
		case "/modrinth/v2/version_files/update":
			fmt.Fprintf(w, `{"%s":{"id":"new","project_id":"lp","version_number":"5.4.150","version_type":"release","status":"listed",
				"date_published":"2024-06-01T00:00:00Z","loaders":["bukkit","paper"],"game_versions":["1.21.4"],
				"files":[{"filename":"LuckPerms-Bukkit-5.4.150.jar","url":"https://cdn.example/new.jar","primary":true,"hashes":{"sha512":"abc","sha1":"def"}}]}}`,
				kh.SHA512)
		case "/modrinth/v2/projects":
			fmt.Fprint(w, `[{"id":"lp","slug":"luckperms","title":"LuckPerms","project_type":"plugin"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	configureIndex(t, srv.URL)

	src := NewModrinthSource("discopanel-test/1.0")
	target := Target{McVersion: "1.21.4", Loaders: []string{"paper", "spigot", "bukkit"}}
	got, err := src.Lookup(t.Context(), []*HashedJar{kh, uh}, target)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %+v", calls)
	}
	for _, c := range calls {
		if c.ua != "discopanel-test/1.0" {
			t.Errorf("%s sent user agent %q", c.path, c.ua)
		}
	}
	id := calls[0].body
	if id["algorithm"] != "sha512" || len(id["hashes"].([]any)) != 2 {
		t.Errorf("identify body %v", id)
	}
	if _, ok := id["loaders"]; ok {
		t.Errorf("identify body carries filters: %v", id)
	}
	upd := calls[1].body
	if hs := upd["hashes"].([]any); len(hs) != 1 || hs[0] != kh.SHA512 {
		t.Errorf("update asks for unknown hashes: %v", hs)
	}
	if fmt.Sprint(upd["loaders"]) != "[paper spigot bukkit]" || fmt.Sprint(upd["game_versions"]) != "[1.21.4]" || fmt.Sprint(upd["version_types"]) != "[release]" {
		t.Errorf("update filters %v", upd)
	}

	if _, ok := got[unknown.Path()]; ok {
		t.Error("unknown jar got a verdict from a guess")
	}
	r := got[known.Path()]
	if r == nil || r.Status != StatusAvailable {
		t.Fatalf("want update available, got %+v", r)
	}
	if r.ProjectName != "LuckPerms" || r.ProjectURL != "https://modrinth.com/plugin/luckperms" {
		t.Errorf("project %q %q", r.ProjectName, r.ProjectURL)
	}
	if r.Current.Version != "5.4.100" || r.Latest.Version != "5.4.150" || r.Latest.FileName != "LuckPerms-Bukkit-5.4.150.jar" {
		t.Errorf("versions %+v -> %+v", r.Current, r.Latest)
	}
	if r.Latest.HashAlgo != "sha512" || r.Latest.HashSum != "abc" || r.Latest.URL != "https://cdn.example/new.jar" {
		t.Errorf("latest file %+v", r.Latest)
	}
	if r.Latest.ChangelogURL != "https://modrinth.com/plugin/luckperms/version/new" {
		t.Errorf("changelog %q", r.Latest.ChangelogURL)
	}
}

// Serves canned answers and records the filters it saw
type fakeModrinth struct {
	current map[string]modrinth.Version
	latest  map[string]modrinth.Version
	types   []string
}

func (f *fakeModrinth) VersionsFromHashes(_ context.Context, _ string, hashes []string) (map[string]modrinth.Version, error) {
	return f.current, nil
}

func (f *fakeModrinth) LatestVersionsFromHashes(_ context.Context, _ string, _, _, _, versionTypes []string) (map[string]modrinth.Version, error) {
	f.types = versionTypes
	return f.latest, nil
}

func (f *fakeModrinth) GetProjects(context.Context, []string) ([]modrinth.ProjectDetails, error) {
	return nil, nil
}

func mrVersion(id, typ, published string, loaders, games []string) modrinth.Version {
	return modrinth.Version{
		ID: id, ProjectID: "p", VersionNumber: id, VersionType: typ, DatePublished: published,
		Loaders: loaders, GameVersions: games, Status: "listed",
		Files: []modrinth.File{{Filename: id + ".jar", URL: "https://cdn.example/" + id, Primary: true, Hashes: modrinth.Hashes{SHA512: "h-" + id}}},
	}
}

func TestModrinthCompatibility(t *testing.T) {
	paper := []string{"paper"}
	mc := []string{"1.21.4"}
	cur := mrVersion("cur", "release", "2024-01-01T00:00:00Z", paper, mc)
	beta := mrVersion("beta", "beta", "2024-05-01T00:00:00Z", paper, mc)
	unlisted := mrVersion("gone", "release", "2024-05-01T00:00:00Z", paper, mc)
	unlisted.Status = "unlisted"
	cases := []struct {
		name   string
		latest *modrinth.Version
		beta   bool
		want   Status
	}{
		{"newer release", ptr(mrVersion("new", "release", "2024-05-01T00:00:00Z", paper, mc)), false, StatusAvailable},
		{"same version", &cur, false, StatusUpToDate},
		{"older than installed", ptr(mrVersion("old", "release", "2023-01-01T00:00:00Z", paper, mc)), false, StatusUpToDate},
		{"nothing compatible", nil, false, StatusNoCompatible},
		{"wrong loader", ptr(mrVersion("fab", "release", "2024-05-01T00:00:00Z", []string{"fabric"}, mc)), false, StatusNoCompatible},
		{"wrong game version", ptr(mrVersion("mc", "release", "2024-05-01T00:00:00Z", paper, []string{"1.20.1"})), false, StatusNoCompatible},
		{"beta on release channel", &beta, false, StatusNoCompatible},
		{"beta with beta allowed", &beta, true, StatusAvailable},
		{"alpha never offered", ptr(mrVersion("a", "alpha", "2024-05-01T00:00:00Z", paper, mc)), true, StatusNoCompatible},
		{"unlisted version", &unlisted, false, StatusNoCompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &HashedJar{Jar: Jar{Dir: "/srv/plugins", Name: "x.jar"}, SHA512: "h-cur"}
			api := &fakeModrinth{current: map[string]modrinth.Version{"h-cur": cur}, latest: map[string]modrinth.Version{}}
			if tc.latest != nil {
				api.latest["h-cur"] = *tc.latest
			}
			src := &ModrinthSource{api: api}
			got, err := src.Lookup(t.Context(), []*HashedJar{j}, Target{McVersion: "1.21.4", Loaders: []string{"paper", "spigot"}, IncludeBeta: tc.beta})
			if err != nil {
				t.Fatal(err)
			}
			if r := got[j.Path()]; r == nil || r.Status != tc.want {
				t.Fatalf("want status %d, got %+v", tc.want, r)
			}
			wantTypes := []string{"release"}
			if tc.beta {
				wantTypes = []string{"release", "beta"}
			}
			if !slices.Equal(api.types, wantTypes) {
				t.Errorf("asked for %v", api.types)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestTargetLoaders(t *testing.T) {
	cases := map[v1.ModLoader][]string{
		v1.ModLoader_MOD_LOADER_PAPER:  {"paper", "spigot", "bukkit"},
		v1.ModLoader_MOD_LOADER_PURPUR: {"purpur", "paper", "spigot", "bukkit"},
		v1.ModLoader_MOD_LOADER_FOLIA:  {"folia"},
		v1.ModLoader_MOD_LOADER_SPIGOT: {"spigot", "bukkit"},
		v1.ModLoader_MOD_LOADER_FABRIC: {"fabric"},
		v1.ModLoader_MOD_LOADER_QUILT:  {"quilt", "fabric"},
	}
	for loader, want := range cases {
		if got := TargetLoaders(loader, t.TempDir(), t.TempDir()); !slices.Equal(got, want) {
			t.Errorf("%v: got %v want %v", loader, got, want)
		}
	}
}

func TestNewerNeverTrustsMissingDates(t *testing.T) {
	now := time.Now()
	if newer(time.Time{}, now) || newer(now, now) || !newer(now.Add(time.Second), now) || !newer(now, time.Time{}) {
		t.Fatal("newer misjudges dates")
	}
}

func TestLatestReleasePicksTheMatchingJar(t *testing.T) {
	file := func(name, typ string) modrinth.File {
		return modrinth.File{Filename: name, FileType: typ, URL: "https://cdn.modrinth.com/" + name, Hashes: modrinth.Hashes{SHA512: "h"}, Size: 10}
	}
	v := &modrinth.Version{ID: "v", VersionNumber: "5.4.150", Files: []modrinth.File{
		file("LuckPerms-Bukkit-5.4.150.jar", ""),
		file("LuckPerms-Velocity-5.4.150.jar", ""),
		file("LuckPerms-Bukkit-5.4.150-sources.jar", "sources-jar"),
	}}
	rel, err := latestRelease(v, "LuckPerms-Bukkit-5.4.100.jar")
	if err != nil || rel.FileName != "LuckPerms-Bukkit-5.4.150.jar" || rel.Size != 10 {
		t.Fatalf("got %+v %v", rel, err)
	}
	if _, err := latestRelease(v, "luckperms.jar"); err == nil {
		t.Fatal("ambiguous jars auto picked")
	}
	single := &modrinth.Version{Files: []modrinth.File{file("Other-Name-2.jar", ""), file("readme.txt", "")}}
	if rel, err := latestRelease(single, "plugin-1.jar"); err != nil || rel.FileName != "Other-Name-2.jar" {
		t.Fatalf("single jar: %+v %v", rel, err)
	}
	if _, err := latestRelease(&modrinth.Version{Files: []modrinth.File{file("x.zip", "")}}, "x.jar"); err == nil {
		t.Fatal("version without a jar accepted")
	}
}
