package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUnderContainment(t *testing.T) {
	base := t.TempDir()

	good := []string{".", "", "foo", "foo/bar", "foo/../baz"}
	for _, p := range good {
		if _, err := ResolveUnder(base, p); err != nil {
			t.Errorf("path %q rejected: %v", p, err)
		}
	}

	bad := []string{"..", "../x", "foo/../../x", "../" + filepath.Base(base) + "bar"}
	for _, p := range bad {
		if got, err := ResolveUnder(base, p); err == nil {
			t.Errorf("path %q escaped to %q", p, got)
		}
	}
}

func TestResolveUnderSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(base, "leak")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, err := ResolveUnder(base, "leak/secret"); err == nil {
		t.Errorf("symlink escape resolved to %q", got)
	}
	if got, err := ResolveUnder(base, "leak"); err == nil {
		t.Errorf("symlink target escape resolved to %q", got)
	}

	// Links staying inside the jail keep working
	if err := os.MkdirAll(filepath.Join(base, "real"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "inlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveUnder(base, "inlink/newfile"); err != nil {
		t.Errorf("internal symlink rejected: %v", err)
	}
	if _, err := ResolveUnder(base, "brand/new/tree"); err != nil {
		t.Errorf("unborn path rejected: %v", err)
	}
}

func TestFindWorldDirReadsLevelName(t *testing.T) {
	dir := t.TempDir()
	worldPath := filepath.Join(dir, "myrealm")
	if err := os.MkdirAll(worldPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldPath, "level.dat"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	props := "#Minecraft server properties\nmotd=hi\nlevel-name=myrealm\n"
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte(props), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := FindWorldDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != worldPath {
		t.Errorf("got %q want %q", got, worldPath)
	}
}

func TestFindWorldDirDefaultsToWorld(t *testing.T) {
	dir := t.TempDir()
	worldPath := filepath.Join(dir, "world")
	if err := os.MkdirAll(worldPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldPath, "level.dat"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := FindWorldDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != worldPath {
		t.Errorf("got %q want %q", got, worldPath)
	}
}

func TestFindWorldDirMissingWorldErrors(t *testing.T) {
	if _, err := FindWorldDir(t.TempDir()); err == nil {
		t.Error("expected error for missing world")
	}
}

func TestDatapacksDirs(t *testing.T) {
	data := t.TempDir()
	enabled, disabled, err := DatapacksDirs(data)
	if err != nil || enabled != filepath.Join(data, "world", "datapacks") || disabled != enabled+"_disabled" {
		t.Fatalf("default world: got (%q, %q, %v)", enabled, disabled, err)
	}

	os.WriteFile(filepath.Join(data, "server.properties"), []byte("level-name=survival\n"), 0644)
	if enabled, _, err = DatapacksDirs(data); err != nil || enabled != filepath.Join(data, "survival", "datapacks") {
		t.Fatalf("level-name world: got (%q, %v)", enabled, err)
	}

	os.WriteFile(filepath.Join(data, "server.properties"), []byte("level-name=../../etc\n"), 0644)
	if _, _, err = DatapacksDirs(data); err == nil {
		t.Fatal("level-name escaping the data dir must be rejected")
	}
}
