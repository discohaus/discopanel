package minecraft

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, dir, name string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for path, content := range files {
		f, err := w.Create(path)
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFindDatapackRefs(t *testing.T) {
	dataPath := t.TempDir()
	writeZip(t, filepath.Join(dataPath, "config", "paxi", "datapacks"), "spacing.zip", map[string]string{
		"data/dungeons_arise/worldgen/structure_set/major.json": `{"structures":[{"structure":"dungeons_arise:aviary"}]}`,
	})
	writeZip(t, filepath.Join(dataPath, "world", "datapacks"), "loot.zip", map[string]string{
		"data/dungeons_arise/loot_tables/chests/aviary/map.json": `{"pools":[]}`,
	})
	writeZip(t, filepath.Join(dataPath, "resourcepacks"), "gui.zip", map[string]string{
		"assets/x.json": `{"ref":"dungeons_arise:aviary"}`,
	})

	zips := FindDatapackZips(dataPath)
	if len(zips) != 2 {
		t.Fatalf("expected two datapack zips, resourcepacks must not count, got %v", zips)
	}

	hits := FindDatapackRefs(dataPath, []string{"dungeons_arise:aviary"})
	if len(hits) != 1 || filepath.Base(hits[0]) != "spacing.zip" {
		t.Fatalf("only the structure set referencer should match, got %v", hits)
	}
}

func TestDisableEnableDatapack(t *testing.T) {
	dataPath := t.TempDir()
	rel := filepath.Join("config", "paxi", "datapacks", "spacing.zip")
	writeZip(t, filepath.Join(dataPath, "config", "paxi", "datapacks"), "spacing.zip", map[string]string{
		"pack.mcmeta": `{}`,
	})

	if err := DisableDatapack(dataPath, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "config", "paxi", "datapacks_disabled", "spacing.zip")); err != nil {
		t.Fatal("zip should move into the disabled sibling dir")
	}
	if err := EnableDatapack(dataPath, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, rel)); err != nil {
		t.Fatal("zip should move back")
	}
}

func TestValidDatapackName(t *testing.T) {
	for name, want := range map[string]bool{
		"pack.zip":        true,
		"My Pack v1.2":    true,
		"":                false,
		".":               false,
		"..":              false,
		".hidden.zip":     false,
		"../escape.zip":   false,
		"nested/pack.zip": false,
		`win\pack.zip`:    false,
	} {
		if got := ValidDatapackName(name) == nil; got != want {
			t.Errorf("ValidDatapackName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParsePackMeta(t *testing.T) {
	cases := []struct {
		name   string
		meta   string
		desc   string
		format int32
	}{
		{"plain string", `{"pack":{"pack_format":48,"description":"Better loot"}}`, "Better loot", 48},
		{"legacy codes", `{"pack":{"pack_format":15,"description":"§aGreen §lbold"}}`, "Green bold", 15},
		{"component", `{"pack":{"pack_format":10,"description":{"text":"Hello ","extra":[{"text":"world","color":"red"}]}}}`, "Hello world", 10},
		{"component list", `{"pack":{"pack_format":10,"description":["a",{"text":"b"},{"translate":"pack.c","fallback":"c"}]}}`, "abc", 10},
		{"min_format pair", `{"pack":{"min_format":[88,0],"max_format":[90,0],"description":"New"}}`, "New", 88},
		{"no description", `{"pack":{"pack_format":5}}`, "", 5},
		{"broken json", `{"pack":`, "", 0},
		{"empty", ``, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc, format := parsePackMeta([]byte(tc.meta))
			if desc != tc.desc || format != tc.format {
				t.Fatalf("got (%q, %d), want (%q, %d)", desc, format, tc.desc, tc.format)
			}
		})
	}
}

func TestListDatapacks(t *testing.T) {
	world := t.TempDir()
	enabled := filepath.Join(world, "datapacks")
	disabled := enabled + "_disabled"
	meta := `{"pack":{"pack_format":48,"description":"Zip pack"}}`
	writeZip(t, enabled, "zipped.zip", map[string]string{"pack.mcmeta": meta, "data/x/a.json": "{}"})
	writeZip(t, enabled, "nometa.zip", map[string]string{"data/x/a.json": "{}"})
	writeZip(t, disabled, "off.zip", map[string]string{"pack.mcmeta": meta})
	// Disabled twin of an enabled pack stays hidden
	writeZip(t, disabled, "zipped.zip", map[string]string{"pack.mcmeta": meta})
	folder := filepath.Join(enabled, "Folder Pack")
	if err := os.MkdirAll(filepath.Join(folder, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(folder, "pack.mcmeta"), []byte(`{"pack":{"pack_format":26,"description":"Folder"}}`), 0644)
	os.WriteFile(filepath.Join(folder, "data", "f.json"), []byte("0123456789"), 0644)
	// Folders without pack.mcmeta, stray files, dot files and symlinks are not packs
	os.MkdirAll(filepath.Join(enabled, "not-a-pack"), 0755)
	os.WriteFile(filepath.Join(enabled, "readme.txt"), []byte("hi"), 0644)
	writeZip(t, enabled, ".pending.zip", map[string]string{"pack.mcmeta": meta})
	if err := os.Symlink(folder, filepath.Join(enabled, "linked")); err != nil {
		t.Fatal(err)
	}

	packs := ListDatapacks(enabled, disabled)
	got := map[string]Datapack{}
	var order []string
	for _, p := range packs {
		got[p.Name] = p
		order = append(order, p.Name)
	}
	want := []string{"Folder Pack", "nometa.zip", "off.zip", "zipped.zip"}
	if len(order) != len(want) {
		t.Fatalf("listed %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("listed %v, want %v", order, want)
		}
	}
	if p := got["zipped.zip"]; !p.Enabled || p.IsDir || p.Description != "Zip pack" || p.PackFormat != 48 || p.Size == 0 {
		t.Errorf("zipped.zip read wrong: %+v", p)
	}
	if p := got["off.zip"]; p.Enabled {
		t.Errorf("off.zip should be disabled: %+v", p)
	}
	if p := got["Folder Pack"]; !p.IsDir || p.Description != "Folder" || p.PackFormat != 26 || p.Size < 10 {
		t.Errorf("folder pack read wrong: %+v", p)
	}
	if p := got["nometa.zip"]; p.Description != "" || p.PackFormat != 0 {
		t.Errorf("zip without meta should list blank: %+v", p)
	}

	if _, _, ok := FindDatapack(enabled, disabled, "../datapacks/zipped.zip"); ok {
		t.Error("FindDatapack must reject path names")
	}
	if p, dir, ok := FindDatapack(enabled, disabled, "off.zip"); !ok || dir != disabled || p.Enabled {
		t.Errorf("FindDatapack(off.zip) = %+v %q %v", p, dir, ok)
	}
	if ListDatapacks(filepath.Join(world, "missing"), filepath.Join(world, "missing_disabled")) != nil {
		t.Error("missing dirs should list nothing")
	}
}

func TestDatapackZipRoot(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		prefix  string
		rewrite bool
		wantErr error
	}{
		{"root", map[string]string{"pack.mcmeta": "{}", "data/a.json": "{}"}, "", false, nil},
		{"dot slash root", map[string]string{"./pack.mcmeta": "{}", "./data/a.json": "{}"}, "", true, nil},
		{"one folder deep", map[string]string{"repo-main/pack.mcmeta": "{}", "repo-main/data/a.json": "{}"}, "repo-main/", true, nil},
		{"dot slash folder", map[string]string{"./repo/pack.mcmeta": "{}"}, "repo/", true, nil},
		{"root beats nested", map[string]string{"x/pack.mcmeta": "{}", "pack.mcmeta": "{}"}, "", false, nil},
		{"macos junk only", map[string]string{"__MACOSX/pack.mcmeta": "{}"}, "", false, ErrDatapackNoMeta},
		{"two deep", map[string]string{"a/b/pack.mcmeta": "{}"}, "", false, ErrDatapackNoMeta},
		{"no meta", map[string]string{"data/a.json": "{}"}, "", false, ErrDatapackNoMeta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeZip(t, dir, "p.zip", tc.files)
			prefix, rewrite, err := DatapackZipRoot(filepath.Join(dir, "p.zip"))
			if err != tc.wantErr || prefix != tc.prefix || rewrite != tc.rewrite {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, %v)", prefix, rewrite, err, tc.prefix, tc.rewrite, tc.wantErr)
			}
		})
	}

	notZip := filepath.Join(t.TempDir(), "fake.zip")
	os.WriteFile(notZip, []byte("not a zip"), 0644)
	if _, _, err := DatapackZipRoot(notZip); err != ErrDatapackNotZip {
		t.Fatalf("plain file should be ErrDatapackNotZip, got %v", err)
	}
}

// Rewritten zip entries keyed by name
func zipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	names := map[string]string{}
	for _, f := range r.File {
		data, err := readZipEntry(f)
		if err != nil {
			t.Fatalf("entry %s unreadable after raw copy: %v", f.Name, err)
		}
		names[f.Name] = string(data)
	}
	return names
}

func TestRebaseDatapackZip(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, dir, "nested.zip", map[string]string{
		"repo-main/pack.mcmeta":      `{"pack":{"pack_format":48,"description":"Nested"}}`,
		"repo-main/data/x/loot.json": `{"pools":[]}`,
		"README.md":                  "outside the pack",
	})
	dst := filepath.Join(dir, "fixed.zip")
	if err := RebaseDatapackZip(filepath.Join(dir, "nested.zip"), dst, "repo-main/"); err != nil {
		t.Fatal(err)
	}
	if prefix, rewrite, err := DatapackZipRoot(dst); err != nil || prefix != "" || rewrite {
		t.Fatalf("rebased zip should carry a clean root pack.mcmeta, got (%q, %v, %v)", prefix, rewrite, err)
	}
	if names := zipEntries(t, dst); len(names) != 2 || names["data/x/loot.json"] != `{"pools":[]}` {
		t.Fatalf("rebased entries wrong: %v", names)
	}

	// ./ entries at the root lose the dot segment
	writeZip(t, dir, "dotted.zip", map[string]string{
		"./pack.mcmeta":        `{"pack":{"pack_format":48}}`,
		"././data/x/loot.json": `{}`,
	})
	dst = filepath.Join(dir, "undotted.zip")
	if err := RebaseDatapackZip(filepath.Join(dir, "dotted.zip"), dst, ""); err != nil {
		t.Fatal(err)
	}
	names := zipEntries(t, dst)
	if _, ok := names["pack.mcmeta"]; !ok || len(names) != 2 || names["data/x/loot.json"] != "{}" {
		t.Fatalf("dotted entries should be normalised, got %v", names)
	}
	if _, rewrite, _ := DatapackZipRoot(dst); rewrite {
		t.Fatal("normalised zip should need no further rewrite")
	}
}

func TestSetDatapackEnabled(t *testing.T) {
	enabled := filepath.Join(t.TempDir(), "datapacks")
	disabled := enabled + "_disabled"
	writeZip(t, enabled, "a.zip", map[string]string{"pack.mcmeta": "{}"})
	folder := filepath.Join(enabled, "folder")
	os.MkdirAll(folder, 0755)
	os.WriteFile(filepath.Join(folder, "pack.mcmeta"), []byte("{}"), 0644)

	for _, name := range []string{"a.zip", "folder"} {
		if err := SetDatapackEnabled(enabled, name, false); err != nil {
			t.Fatalf("disable %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(disabled, name)); err != nil {
			t.Fatalf("%s should sit in the disabled dir", name)
		}
		if err := SetDatapackEnabled(enabled, name, true); err != nil {
			t.Fatalf("enable %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(enabled, name)); err != nil {
			t.Fatalf("%s should be back in the datapacks dir", name)
		}
	}

	// A twin in the destination is never overwritten
	writeZip(t, disabled, "a.zip", map[string]string{"pack.mcmeta": `{"twin":true}`})
	if err := SetDatapackEnabled(enabled, "a.zip", false); err != ErrDatapackExists {
		t.Fatalf("disable over a twin should be ErrDatapackExists, got %v", err)
	}
	if err := SetDatapackEnabled(enabled, "missing.zip", true); err != ErrDatapackNotFound {
		t.Fatalf("missing pack should be ErrDatapackNotFound, got %v", err)
	}
	if err := SetDatapackEnabled(enabled, "../a.zip", false); err != ErrDatapackName {
		t.Fatalf("path name should be ErrDatapackName, got %v", err)
	}
}
