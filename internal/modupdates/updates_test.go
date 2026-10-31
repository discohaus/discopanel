package modupdates

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestTTLCacheExpiresAndStaysBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newTTLCache[string, int](time.Minute, 3)
	c.now = func() time.Time { return now }

	c.put("a", 1)
	if v, ok := c.get("a"); !ok || v != 1 {
		t.Fatalf("fresh entry missing")
	}
	now = now.Add(time.Minute)
	if _, ok := c.get("a"); ok {
		t.Fatal("expired entry served")
	}

	for i, k := range []string{"b", "c", "d"} {
		now = now.Add(time.Second)
		c.put(k, i)
	}
	now = now.Add(time.Second)
	c.put("e", 9)
	if c.len() != 3 {
		t.Fatalf("cache grew to %d", c.len())
	}
	if _, ok := c.get("b"); ok {
		t.Error("oldest entry survived eviction")
	}
	if v, ok := c.get("e"); !ok || v != 9 {
		t.Error("newest entry evicted")
	}
}

// Counts lookups and answers from a fixed table
type countingSource struct {
	kind  v1.ModUpdateSource
	known map[string]Status
	err   error
	calls int
	asked int
}

func (c *countingSource) Kind() v1.ModUpdateSource { return c.kind }

func (c *countingSource) Lookup(_ context.Context, jars []*HashedJar, _ Target) (map[string]*Result, error) {
	c.calls++
	c.asked += len(jars)
	if c.err != nil {
		return nil, c.err
	}
	out := map[string]*Result{}
	for _, j := range jars {
		if st, ok := c.known[j.Name]; ok {
			out[j.Path()] = &Result{Jar: j.Jar, Status: st, Source: c.kind}
		}
	}
	return out, nil
}

func TestCheckerFallsThroughSourcesAndCaches(t *testing.T) {
	dir := t.TempDir()
	jars := []Jar{
		writeJar(t, dir, "a.jar", "a"),
		writeJar(t, dir, "b.jar", "b"),
		writeJar(t, dir, "c.jar", "c"),
	}
	mr := &countingSource{kind: v1.ModUpdateSource_MOD_UPDATE_SOURCE_MODRINTH, known: map[string]Status{"a.jar": StatusAvailable}}
	cf := &countingSource{kind: v1.ModUpdateSource_MOD_UPDATE_SOURCE_CURSEFORGE, known: map[string]Status{"b.jar": StatusUpToDate}}
	ch := NewChecker()
	target := Target{McVersion: "1.21.4", Loaders: []string{"paper"}}

	got := ch.Check(t.Context(), []Source{mr, cf}, jars, target)
	if got[0].Status != StatusAvailable || got[0].Source != mr.kind {
		t.Errorf("a: %+v", got[0])
	}
	if got[1].Status != StatusUpToDate || got[1].Source != cf.kind {
		t.Errorf("b: %+v", got[1])
	}
	if got[2].Status != StatusUnknown {
		t.Errorf("c should be unknown source, got %+v", got[2])
	}
	if mr.asked != 3 || cf.asked != 2 {
		t.Errorf("modrinth asked %d, curseforge asked %d", mr.asked, cf.asked)
	}

	// Positive and negative verdicts both come from cache
	again := ch.Check(t.Context(), []Source{mr, cf}, jars, target)
	if mr.calls != 1 || cf.calls != 1 {
		t.Errorf("cache missed: modrinth %d calls, curseforge %d calls", mr.calls, cf.calls)
	}
	if again[0].Status != StatusAvailable || again[2].Status != StatusUnknown {
		t.Errorf("cached verdicts differ: %+v %+v", again[0], again[2])
	}

	// A different channel is a different question
	ch.Check(t.Context(), []Source{mr, cf}, jars, Target{McVersion: "1.21.4", Loaders: []string{"paper"}, IncludeBeta: true})
	if mr.calls != 2 {
		t.Errorf("channel change reused cache")
	}

	// A changed file is hashed and asked about again
	jars[0] = writeJar(t, dir, "a.jar", "a rebuilt")
	jars[0].ModTime = jars[0].ModTime.Add(time.Second)
	ch.Check(t.Context(), []Source{mr, cf}, jars[:1], target)
	if mr.calls != 3 {
		t.Errorf("changed jar served from cache")
	}
}

func TestCheckerReportsErrorsWithoutCaching(t *testing.T) {
	dir := t.TempDir()
	jars := []Jar{writeJar(t, dir, "a.jar", "a")}
	bad := &countingSource{kind: v1.ModUpdateSource_MOD_UPDATE_SOURCE_MODRINTH, err: errors.New("boom")}
	ch := NewChecker()
	got := ch.Check(t.Context(), []Source{bad}, jars, Target{McVersion: "1.21.4"})
	if got[0].Status != StatusError || got[0].Err == "" {
		t.Fatalf("want error verdict, got %+v", got[0])
	}
	ch.Check(t.Context(), []Source{bad}, jars, Target{McVersion: "1.21.4"})
	if bad.calls != 2 {
		t.Errorf("failure was cached")
	}

	// A later source still identifies what the failed one could not
	ok := &countingSource{kind: v1.ModUpdateSource_MOD_UPDATE_SOURCE_CURSEFORGE, known: map[string]Status{"a.jar": StatusUpToDate}}
	got = ch.Check(t.Context(), []Source{bad, ok}, jars, Target{McVersion: "1.21.4"})
	if got[0].Status != StatusUpToDate {
		t.Errorf("fallback source ignored: %+v", got[0])
	}

	missing := Jar{Dir: dir, Name: "gone.jar"}
	got = ch.Check(t.Context(), []Source{ok}, []Jar{missing}, Target{})
	if got[0].Status != StatusError {
		t.Errorf("unreadable jar: %+v", got[0])
	}
}

func cfFile(id int, release int, date string, games ...string) fuego.File {
	d, _ := time.Parse(time.RFC3339, date)
	return fuego.File{ID: id, ModID: 7, IsAvailable: true, ReleaseType: release, FileDate: d, GameVersions: games,
		FileName: "p-" + date[:10] + ".jar", Hashes: []fuego.Hash{{Value: "sha1-" + date[:10], Algo: 1}}}
}

func TestPickCFFile(t *testing.T) {
	files := []fuego.File{
		cfFile(1, 1, "2024-01-01T00:00:00Z", "1.21.4"),
		cfFile(2, 2, "2024-03-01T00:00:00Z", "1.21.4"),
		cfFile(3, 1, "2024-04-01T00:00:00Z", "1.20.1"),
		cfFile(4, 3, "2024-05-01T00:00:00Z", "1.21.4"),
	}
	unavailable := cfFile(5, 1, "2024-06-01T00:00:00Z", "1.21.4")
	unavailable.IsAvailable = false
	files = append(files, unavailable)

	if f := pickCFFile(files, Target{McVersion: "1.21.4"}); f == nil || f.ID != 1 {
		t.Errorf("release channel picked %+v", f)
	}
	if f := pickCFFile(files, Target{McVersion: "1.21.4", IncludeBeta: true}); f == nil || f.ID != 2 {
		t.Errorf("beta channel picked %+v", f)
	}
	if f := pickCFFile(files, Target{McVersion: "1.19.2"}); f != nil {
		t.Errorf("incompatible version picked %+v", f)
	}
}

type fakeCF struct {
	matches []fuego.FingerprintMatch
	files   []fuego.File
	listed  int
}

func (f *fakeCF) GetFingerprintMatches(context.Context, []uint32) ([]fuego.FingerprintMatch, error) {
	return f.matches, nil
}

func (f *fakeCF) GetModsByIDs(context.Context, []int) ([]fuego.Modpack, error) {
	return []fuego.Modpack{{ID: 7, Name: "Plugin", Links: fuego.Links{WebsiteURL: "https://www.curseforge.com/minecraft/bukkit-plugins/plugin"}}}, nil
}

func (f *fakeCF) GetRecentModFiles(context.Context, int, string, fuego.ModLoaderType) ([]fuego.File, error) {
	f.listed++
	return f.files, nil
}

func (f *fakeCF) ResolveDownloadURL(_ context.Context, _ int, file *fuego.File) (string, error) {
	return "https://edge.example/" + file.FileName, nil
}

func TestCurseForgeLookup(t *testing.T) {
	cur := cfFile(1, 1, "2024-01-01T00:00:00Z", "1.21.4")
	cur.FileFingerprint, cur.FileLength = 42, 5
	other := cur
	other.ID, other.FileFingerprint = 9, 43
	clash1, clash2 := cur, cur
	clash1.ID, clash1.FileFingerprint = 20, 45
	clash2.ID, clash2.FileFingerprint = 21, 45
	api := &fakeCF{
		matches: []fuego.FingerprintMatch{{File: cur}, {File: other}, {File: clash1}, {File: clash2}},
		files:   []fuego.File{cfFile(3, 1, "2024-04-01T00:00:00Z", "1.21.4"), cur},
	}
	src := &CurseForgeSource{api: api}
	sha := "sha1-2024-01-01"
	a := &HashedJar{Jar: Jar{Dir: "/p", Name: "a.jar", Size: 5}, CFPrint: 42, SHA1: sha}
	b := &HashedJar{Jar: Jar{Dir: "/p", Name: "b.jar", Size: 5}, CFPrint: 43, SHA1: "different"}
	sz := &HashedJar{Jar: Jar{Dir: "/p", Name: "sz.jar", Size: 6}, CFPrint: 42, SHA1: sha}
	amb := &HashedJar{Jar: Jar{Dir: "/p", Name: "amb.jar", Size: 5}, CFPrint: 45, SHA1: sha}
	u := &HashedJar{Jar: Jar{Dir: "/p", Name: "u.jar", Size: 5}, CFPrint: 44, SHA1: sha}
	got, err := src.Lookup(t.Context(), []*HashedJar{a, b, sz, amb, u}, Target{McVersion: "1.21.4"})
	if err != nil {
		t.Fatal(err)
	}
	r := got[a.Path()]
	if r == nil || r.Status != StatusAvailable || r.Latest.URL != "https://edge.example/p-2024-04-01.jar" || r.Latest.HashAlgo != "sha1" {
		t.Fatalf("a: %+v", r)
	}
	if r.Latest.ChangelogURL != "https://www.curseforge.com/minecraft/bukkit-plugins/plugin/files/3" {
		t.Errorf("changelog %q", r.Latest.ChangelogURL)
	}
	// A bare fingerprint hit never identifies a jar
	for _, j := range []*HashedJar{b, sz, amb, u} {
		if _, ok := got[j.Path()]; ok {
			t.Errorf("%s identified without an exact match", j.Name)
		}
	}
	if api.listed != 1 {
		t.Errorf("listed project files %d times for one project", api.listed)
	}
}

func TestCFLoaderTags(t *testing.T) {
	fabric := cfFile(1, 1, "2024-01-01T00:00:00Z", "1.21.1", "Fabric")
	forge := cfFile(2, 1, "2024-02-01T00:00:00Z", "1.21.1", "Forge")
	plugin := cfFile(3, 1, "2024-03-01T00:00:00Z", "1.21.1")
	cases := []struct {
		loaders []string
		file    fuego.File
		want    bool
	}{
		{[]string{"quilt", "fabric"}, fabric, true},
		{[]string{"quilt", "fabric"}, forge, false},
		{[]string{"neoforge", "forge"}, forge, true},
		{[]string{"neoforge"}, forge, false},
		{[]string{"paper", "spigot", "bukkit"}, plugin, true},
		{[]string{"fabric"}, plugin, false},
	}
	for _, tc := range cases {
		if got := cfLoaderOK(&tc.file, Target{McVersion: "1.21.1", Loaders: tc.loaders}); got != tc.want {
			t.Errorf("%v on %v: got %v", tc.loaders, tc.file.GameVersions, got)
		}
	}
}
