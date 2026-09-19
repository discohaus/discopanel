package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstallIDCreatesAndReuses(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadInstallID(dir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if !ValidInstallID(first) {
		t.Fatalf("minted id %q is not 32 lowercase hex characters", first)
	}
	path := filepath.Join(dir, InstallIDFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != first+"\n" {
		t.Errorf("file holds %q", raw)
	}
	second, err := LoadInstallID(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second != first {
		t.Errorf("second load minted %q, want the persisted %q", second, first)
	}
}

func TestLoadInstallIDRegeneratesMalformed(t *testing.T) {
	for _, bad := range []string{"", "not-hex", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef", "0123456789abcdef0123456789abcdef00"} {
		dir := t.TempDir()
		path := filepath.Join(dir, InstallIDFile)
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		id, err := LoadInstallID(dir)
		if err != nil {
			t.Fatalf("load over %q: %v", bad, err)
		}
		if !ValidInstallID(id) || id == bad {
			t.Errorf("load over %q produced %q", bad, id)
		}
		raw, _ := os.ReadFile(path)
		if string(raw) != id+"\n" {
			t.Errorf("file not rewritten: %q", raw)
		}
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm after rewrite = %o", perm)
		}
	}
}

func TestLoadInstallIDTightensPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, InstallIDFile)
	const id = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte("  "+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadInstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Errorf("got %q", got)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

func TestLoadInstallIDCreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	id, err := LoadInstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidInstallID(id) {
		t.Errorf("id = %q", id)
	}
}

func TestNewInstallIDsDiffer(t *testing.T) {
	a, err := NewInstallID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two draws produced the same id %q", a)
	}
}
