package modupdates

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum512(s string) string {
	h := sha512.Sum512([]byte(s))
	return hex.EncodeToString(h[:])
}

// Serves body for any url, ignoring the checksum like a careless fetcher
func fetchBody(body string) Fetcher {
	return func(_ context.Context, _, dest, _, _ string, _ int64) error {
		return os.WriteFile(dest, []byte(body), 0600)
	}
}

type applyEnv struct {
	data, plugins, disabled string
	pluginsRoot, disRoot    *os.Root
	backups                 Backups
	stage                   string
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func newApplyEnv(t *testing.T) applyEnv {
	t.Helper()
	data := t.TempDir()
	e := applyEnv{
		data:     data,
		plugins:  filepath.Join(data, "plugins"),
		disabled: filepath.Join(data, "plugins_disabled"),
		stage:    t.TempDir(),
	}
	os.MkdirAll(e.plugins, 0755)
	os.MkdirAll(e.disabled, 0755)
	e.pluginsRoot = openRoot(t, e.plugins)
	e.disRoot = openRoot(t, e.disabled)
	b, err := BackupsFor(t.TempDir(), "server-1", e.plugins)
	if err != nil {
		t.Fatal(err)
	}
	e.backups = b
	return e
}

func (e applyEnv) dirs() []*os.Root { return []*os.Root{e.pluginsRoot, e.disRoot} }

func (e applyEnv) jar(t *testing.T, name, body string) Jar {
	t.Helper()
	j := writeJar(t, e.plugins, name, body)
	j.Root = e.pluginsRoot
	return j
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

func release(name, body string) *Release {
	return &Release{Version: "2.0", FileName: name, URL: "https://cdn.modrinth.com/" + name, HashAlgo: "sha512", HashSum: sum512(body)}
}

func TestApplyBacksUpAndReplaces(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin-1.0.jar", "old build")

	newName, err := Apply(t.Context(), e.pluginsRoot, jar, "1.0", release("Plugin-2.0.jar", "new build"), e.backups, e.stage, e.dirs(), fetchBody("new build"))
	if err != nil || newName != "Plugin-2.0.jar" {
		t.Fatalf("apply: %q %v", newName, err)
	}
	if got := listNames(t, e.plugins); len(got) != 1 || got[0] != "Plugin-2.0.jar" {
		t.Fatalf("plugins dir holds %v", got)
	}
	info, _ := os.Stat(filepath.Join(e.plugins, "Plugin-2.0.jar"))
	if info.Mode().Perm() != 0644 {
		t.Errorf("new jar mode %v", info.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(e.plugins, "Plugin-2.0.jar")); string(b) != "new build" {
		t.Fatalf("new jar body %q", b)
	}
	// Nothing but the jar is written under the server data dir
	if got := listNames(t, e.data); len(got) != 2 {
		t.Fatalf("data dir gained %v", got)
	}
	rec, ok := e.backups.Lookup("Plugin-2.0.jar")
	if !ok || rec.Previous != "Plugin-1.0.jar" || rec.FromVersion != "1.0" || rec.ToVersion != "2.0" {
		t.Fatalf("backup record %+v %v", rec, ok)
	}
	if b, _ := os.ReadFile(filepath.Join(e.backups.Root, rec.Backup)); string(b) != "old build" {
		t.Fatalf("backup body %q", b)
	}

	cur := writeJarInfo(t, e.plugins, "Plugin-2.0.jar")
	restored, err := Rollback(e.pluginsRoot, cur, e.backups, e.dirs())
	if err != nil || restored != "Plugin-1.0.jar" {
		t.Fatalf("rollback: %q %v", restored, err)
	}
	if got := listNames(t, e.plugins); len(got) != 1 || got[0] != "Plugin-1.0.jar" {
		t.Fatalf("after rollback plugins dir holds %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.plugins, "Plugin-1.0.jar")); string(b) != "old build" {
		t.Fatalf("restored body %q", b)
	}
	if _, ok := e.backups.Lookup("Plugin-2.0.jar"); ok {
		t.Error("record outlived its rollback")
	}
}

// Stats an existing jar into a Jar
func writeJarInfo(t *testing.T, dir, name string) Jar {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return Jar{Dir: dir, Name: name, Size: info.Size(), ModTime: info.ModTime()}
}

func TestApplySameNameAndChainedBackups(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin.jar", "v1")
	if _, err := Apply(t.Context(), e.pluginsRoot, jar, "1", release("Plugin.jar", "v2"), e.backups, e.stage, e.dirs(), fetchBody("v2")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e.plugins, "Plugin.jar")); string(b) != "v2" {
		t.Fatalf("same name swap left %q", b)
	}
	jar = writeJarInfo(t, e.plugins, "Plugin.jar")
	if _, err := Apply(t.Context(), e.pluginsRoot, jar, "2", release("Plugin.jar", "v3"), e.backups, t.TempDir(), e.dirs(), fetchBody("v3")); err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, n := range listNames(t, e.backups.Root) {
		if n != "index.json" {
			backups = append(backups, n)
		}
	}
	if len(backups) != 1 {
		t.Fatalf("want one kept generation, got %v", backups)
	}
	if b, _ := os.ReadFile(filepath.Join(e.backups.Root, backups[0])); string(b) != "v2" {
		t.Fatalf("kept generation %q", b)
	}
	if got := listNames(t, e.plugins); len(got) != 1 {
		t.Fatalf("leftovers in plugins: %v", got)
	}
}

func TestApplyRejectsBadInput(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin-1.0.jar", "old build")
	writeJar(t, e.disabled, "Taken.jar", "someone else")
	os.Symlink("/etc/hostname", filepath.Join(e.plugins, "Link.jar"))
	cases := []struct {
		name string
		rel  *Release
		body string
	}{
		{"escapes folder", release("../../other/plugins/x.jar", "new"), "new"},
		{"absolute path", release("/etc/x.jar", "new"), "new"},
		{"hidden name", release(".x.jar", "new"), "new"},
		{"not a jar", release("x.zip", "new"), "new"},
		{"clashes with a disabled jar", release("Taken.jar", "new"), "new"},
		{"clashes with a planted link", release("Link.jar", "new"), "new"},
		{"checksum mismatch", release("Plugin-2.0.jar", "new"), "tampered"},
		{"no checksum", &Release{FileName: "Plugin-2.0.jar", URL: "https://cdn.modrinth.com/x"}, "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, err := Apply(t.Context(), e.pluginsRoot, jar, "1.0", tc.rel, e.backups, t.TempDir(), e.dirs(), fetchBody(tc.body))
			if err == nil || name != "" {
				t.Fatalf("accepted: %q", name)
			}
			if strings.Contains(err.Error(), sum512(tc.body)) {
				t.Fatalf("error echoes the received digest: %v", err)
			}
			if got := listNames(t, e.plugins); len(got) != 2 {
				t.Fatalf("plugins dir changed to %v", got)
			}
			if b, _ := os.ReadFile(jar.Path()); string(b) != "old build" {
				t.Fatalf("old jar touched: %q", b)
			}
			if _, err := os.Stat(filepath.Join(e.data, "other")); err == nil {
				t.Fatal("wrote outside the server")
			}
		})
	}
}

func TestApplyRefusesSymlinkedJar(t *testing.T) {
	e := newApplyEnv(t)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("host secret"), 0600)
	// Plugin code swaps its own jar for a link to a host file
	if err := os.Symlink(secret, filepath.Join(e.plugins, "Plugin-1.0.jar")); err != nil {
		t.Fatal(err)
	}
	jar := Jar{Root: e.pluginsRoot, Dir: e.plugins, Name: "Plugin-1.0.jar"}
	if _, err := HashJar(jar); err == nil {
		t.Fatal("hashed a symlinked jar")
	}
	_, err := Apply(t.Context(), e.pluginsRoot, jar, "", release("Plugin-2.0.jar", "new"), e.backups, e.stage, e.dirs(), fetchBody("new"))
	if err == nil {
		t.Fatal("updated through a symlinked jar")
	}
	if entries := listNames(t, e.backups.Root); len(entries) != 0 {
		t.Fatalf("host file copied into backups: %v", entries)
	}

	// A link to a jar inside the folder is still not a regular file
	writeJar(t, e.plugins, "Real.jar", "real")
	os.Symlink("Real.jar", filepath.Join(e.plugins, "Inner.jar"))
	if _, err := HashJar(Jar{Root: e.pluginsRoot, Dir: e.plugins, Name: "Inner.jar"}); err == nil {
		t.Fatal("hashed an in-folder link")
	}
}

func TestRollbackIgnoresPlantedBackups(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin.jar", "v1")
	if _, err := Apply(t.Context(), e.pluginsRoot, jar, "1", release("Plugin-2.jar", "v2"), e.backups, e.stage, e.dirs(), fetchBody("v2")); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.backups.Lookup("Plugin-2.jar")
	// Even if the store were reached, a link in place of the backup is refused
	secret := filepath.Join(t.TempDir(), "shadow")
	os.WriteFile(secret, []byte("root:x"), 0600)
	os.Remove(filepath.Join(e.backups.Root, rec.Backup))
	os.Symlink(secret, filepath.Join(e.backups.Root, rec.Backup))
	cur := writeJarInfo(t, e.plugins, "Plugin-2.jar")
	if _, err := Rollback(e.pluginsRoot, cur, e.backups, e.dirs()); err == nil {
		t.Fatal("rolled back from a symlinked backup")
	}
	for _, n := range listNames(t, e.plugins) {
		if b, _ := os.ReadFile(filepath.Join(e.plugins, n)); string(b) == "root:x" {
			t.Fatal("host file copied into plugins")
		}
	}
	// The store sits outside the server data dir
	if strings.HasPrefix(e.backups.Root, e.data) {
		t.Fatalf("backups under the data dir: %s", e.backups.Root)
	}
}

func TestBackupBaseRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "..", "../x", "a/b", ".hidden"} {
		if _, err := BackupBase("/panel", id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
	if got, err := BackupBase("/panel", "abc"); err != nil || got != filepath.Join("/panel", "mod-updates", "abc") {
		t.Errorf("got %q %v", got, err)
	}
}

func TestApplyFetchFailureKeepsOldJar(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin-1.0.jar", "old build")
	fail := func(context.Context, string, string, string, string, int64) error { return errors.New("offline") }
	if _, err := Apply(t.Context(), e.pluginsRoot, jar, "", release("Plugin-2.0.jar", "new"), e.backups, e.stage, e.dirs(), fail); err == nil {
		t.Fatal("want error")
	}
	if got := listNames(t, e.plugins); len(got) != 1 || got[0] != "Plugin-1.0.jar" {
		t.Fatalf("plugins dir changed to %v", got)
	}
}

func TestApplyKeepsDisabledJarDisabled(t *testing.T) {
	e := newApplyEnv(t)
	jar := writeJar(t, e.disabled, "Plugin-1.0.jar", "old")
	jar.Root, jar.Enabled = e.disRoot, false
	name, err := Apply(t.Context(), e.disRoot, jar, "", release("Plugin-2.0.jar", "new"), e.backups, e.stage, e.dirs(), fetchBody("new"))
	if err != nil || name != "Plugin-2.0.jar" {
		t.Fatalf("apply: %q %v", name, err)
	}
	if got := listNames(t, e.disabled); len(got) != 1 || got[0] != "Plugin-2.0.jar" {
		t.Fatalf("disabled dir holds %v", got)
	}
	if got := listNames(t, e.plugins); len(got) != 0 {
		t.Fatalf("update leaked into plugins: %v", got)
	}
}

func TestRollbackWithoutBackup(t *testing.T) {
	e := newApplyEnv(t)
	jar := e.jar(t, "Plugin.jar", "x")
	if _, err := Rollback(e.pluginsRoot, jar, e.backups, e.dirs()); err == nil {
		t.Fatal("rollback without backup succeeded")
	}
}
