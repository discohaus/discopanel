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
