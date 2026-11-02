package services

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func zipTree(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for rel, body := range files {
		w, err := zw.Create(rel)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return path
}

func readFile(t *testing.T, p string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func TestRestoreReplacesWorldWholesale(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		"world/level.dat":           "new-level",
		"world/region/r.0.0.mca":    "new-region",
		"world/region/r.9.9.mca":    "made-after-backup", // must disappear
		"mods/keep.jar":             "mod",
		"server.properties":         "motd=current",
		".discopanel/manifest.json": "internal",
	})
	archive := zipTree(t, map[string]string{
		"world/level.dat":        "old-level",
		"world/region/r.0.0.mca": "old-region",
		".discopanel/evil.json":  "must not land",
	})

	if err := restoreArchiveExact(context.Background(), archive, data); err != nil {
		t.Fatal(err)
	}

	if got, _ := readFile(t, filepath.Join(data, "world/level.dat")); got != "old-level" {
		t.Errorf("level.dat = %q", got)
	}
	if got, _ := readFile(t, filepath.Join(data, "world/region/r.0.0.mca")); got != "old-region" {
		t.Errorf("r.0.0 = %q", got)
	}
	if _, ok := readFile(t, filepath.Join(data, "world/region/r.9.9.mca")); ok {
		t.Error("stale region file newer than the backup survived the restore")
	}
	if _, ok := readFile(t, filepath.Join(data, "mods/keep.jar")); !ok {
		t.Error("restore removed a path the backup does not contain")
	}
	if got, _ := readFile(t, filepath.Join(data, "server.properties")); got != "motd=current" {
		t.Error("restore touched server.properties")
	}
	if _, ok := readFile(t, filepath.Join(data, ".discopanel/evil.json")); ok {
		t.Error("backup wrote into panel internal state")
	}
	assertNoStaging(t, data)
}

func TestRestoreMergesPartialPaths(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{
		"world/level.dat":        "keep-level",
		"world/region/r.0.0.mca": "new",
		"config/a.toml":          "new-a",
		"config/b.toml":          "only-live",
	})
	// A backup of just world/region and config/a.toml: no level.dat, so
	// world must be merged, not replaced (replacing would delete level.dat).
	archive := zipTree(t, map[string]string{
		"world/region/r.0.0.mca": "old",
		"config/a.toml":          "old-a",
	})
	if err := restoreArchiveExact(context.Background(), archive, data); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"world/level.dat":        "keep-level",
		"world/region/r.0.0.mca": "old",
		"config/a.toml":          "old-a",
		"config/b.toml":          "only-live",
	}
	for rel, want := range checks {
		if got, _ := readFile(t, filepath.Join(data, rel)); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	assertNoStaging(t, data)
}

func TestRestoreCorruptArchiveChangesNothing(t *testing.T) {
	data := t.TempDir()
	writeTree(t, data, map[string]string{"world/level.dat": "live"})
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, []byte("not a zip at all"), 0644)

	if err := restoreArchiveExact(context.Background(), bad, data); err == nil {
		t.Fatal("expected an error for a corrupt archive")
	}
	if got, _ := readFile(t, filepath.Join(data, "world/level.dat")); got != "live" {
		t.Errorf("world changed after failed restore: %q", got)
	}
	assertNoStaging(t, data)
}

func assertNoStaging(t *testing.T, data string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(data, ".restore-*"))
	if len(matches) > 0 {
		t.Errorf("staging dirs left behind: %v", matches)
	}
}

func TestPrunePreRestoreSnapshots(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		name := preRestorePrefix + base.Add(time.Duration(i)*time.Hour).Format("20060102-150405") + ".zip"
		os.WriteFile(filepath.Join(dir, name), nil, 0644)
	}
	os.WriteFile(filepath.Join(dir, "nightly_20250101-000000.zip"), nil, 0644)

	if err := prunePreRestoreSnapshots(dir, 3); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, preRestorePrefix+"*"))
	if len(left) != 3 {
		t.Fatalf("kept %d snapshots, want 3: %v", len(left), left)
	}
	for _, oldest := range []string{"20260101-000000", "20260101-010000"} {
		if _, err := os.Stat(filepath.Join(dir, preRestorePrefix+oldest+".zip")); err == nil {
			t.Errorf("oldest snapshot %s kept", oldest)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "nightly_20250101-000000.zip")); err != nil {
		t.Error("pruned a scheduled backup that is not a pre-restore snapshot")
	}
}

// Mount-point fallback: dst itself is kept, its contents replaced exactly
func TestReplaceContentsKeepsDirReplacesChildren(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTree(t, src, map[string]string{"level.dat": "old", "region/r.0.0.mca": "old-r"})
	writeTree(t, dst, map[string]string{"level.dat": "new", "region/r.9.9.mca": "stale"})
	before, _ := os.Stat(dst)
	if err := replaceContents(src, dst); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(dst)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("destination directory itself was replaced")
	}
	if got, _ := readFile(t, filepath.Join(dst, "level.dat")); got != "old" {
		t.Errorf("level.dat = %q", got)
	}
	if _, ok := readFile(t, filepath.Join(dst, "region/r.9.9.mca")); ok {
		t.Error("stale region survived")
	}
}
