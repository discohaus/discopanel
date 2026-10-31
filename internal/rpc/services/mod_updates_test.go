package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestWebLinkKeepsOnlyWebURLs(t *testing.T) {
	for raw, want := range map[string]string{
		"https://modrinth.com/plugin/x": "https://modrinth.com/plugin/x",
		"http://example.com/files/1":    "http://example.com/files/1",
		"javascript:alert(1)":           "",
		"//evil.example/x":              "",
		"":                              "",
		"data:text/html,<script>":       "",
	} {
		if got := webLink(raw); got != want {
			t.Errorf("webLink(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestUniqueIDs(t *testing.T) {
	got := uniqueIDs([]string{"a", "b", "a", "", "c", "b"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("got %v", got)
	}
}

func TestJarDirsRefuseLinks(t *testing.T) {
	data := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "victim.jar"), []byte("other server"), 0644)
	server := &v1.Server{Id: "s1", DataPath: data, ModLoader: v1.ModLoader_MOD_LOADER_PAPER}

	// A plugins folder swapped for a link to another server is refused
	if err := os.Symlink(outside, filepath.Join(data, "plugins")); err != nil {
		t.Fatal(err)
	}
	if d, err := openJarDirs(server); err == nil {
		d.Close()
		t.Fatal("opened a symlinked plugins folder")
	}
	os.Remove(filepath.Join(data, "plugins"))

	plugins := filepath.Join(data, "plugins")
	os.MkdirAll(plugins, 0755)
	os.WriteFile(filepath.Join(plugins, "Real.jar"), []byte("real"), 0644)
	os.Symlink("/etc/passwd", filepath.Join(plugins, "Host.jar"))
	os.Symlink("Real.jar", filepath.Join(plugins, "Inner.jar"))
	d, err := openJarDirs(server)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.disabled != nil {
		t.Error("missing disabled folder opened")
	}
	jars, order := serverJars(server.Id, d, server.ModLoader)
	if len(order) != 1 || jars[order[0]].Name != "Real.jar" {
		t.Fatalf("listed %v", jars)
	}
}

func TestPackManagedJars(t *testing.T) {
	data := t.TempDir()
	state := filepath.Join(data, ".discopanel")
	os.MkdirAll(state, 0755)
	os.WriteFile(filepath.Join(state, "manifest.json"), []byte(`{"version":1,"modpack":{"id":"p"},"packFiles":["mods/pack.jar","mods/sub/deep.jar","config/x.toml"]}`), 0644)
	os.WriteFile(filepath.Join(state, "modrinth-projects.json"), []byte(`{"version":1,"projects":{"sodium":{"file_name":"sodium.jar"}}}`), 0644)
	server := &v1.Server{Id: "s1", DataPath: data, ModLoader: v1.ModLoader_MOD_LOADER_FABRIC}
	managed, modpack := packManagedJars(server, filepath.Join(data, "mods"))
	if !modpack {
		t.Error("modpack not detected")
	}
	if managed["pack.jar"] == "" || managed["sodium.jar"] == "" || managed["deep.jar"] != "" || len(managed) != 2 {
		t.Errorf("managed %v", managed)
	}
}
