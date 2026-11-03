package services

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Builds a mod service over a real store, uploads and one server
func datapackRig(t *testing.T) (*ModService, string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "test.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	dataPath := t.TempDir()
	server := &v1.Server{Id: "srv1", Name: "srv1", DataPath: dataPath, ModLoader: v1.ModLoader_MOD_LOADER_VANILLA}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatalf("server create failed %v", err)
	}
	log := logger.New()
	uploads := transfer.NewUploadManager(t.TempDir(), time.Hour, 64<<20, log)
	t.Cleanup(uploads.Stop)
	return NewModService(store, nil, cfg, uploads, nil, metrics.NewRecorder(store, log), log), dataPath
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Runs a whole chunked upload and returns its session id
func uploadBytes(t *testing.T, s *ModService, name string, data []byte) string {
	t.Helper()
	session, err := s.uploadManager.InitSession(name, int64(len(data)), int32(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := s.uploadManager.WriteChunk(session.ID, 0, data); err != nil || !done {
		t.Fatalf("chunk write: done=%v err=%v", done, err)
	}
	return session.ID
}

func importPack(s *ModService, session string) (*v1.ImportUploadedDatapackResponse, error) {
	res, err := s.ImportUploadedDatapack(context.Background(), connect.NewRequest(&v1.ImportUploadedDatapackRequest{ServerId: "srv1", UploadSessionId: session}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func TestImportUploadedDatapack(t *testing.T) {
	s, dataPath := datapackRig(t)
	os.WriteFile(filepath.Join(dataPath, "server.properties"), []byte("level-name=survival\n"), 0644)
	meta := `{"pack":{"pack_format":48,"description":"Loot tweaks"}}`

	res, err := importPack(s, uploadBytes(t, s, "loot.zip", zipBytes(t, map[string]string{"pack.mcmeta": meta})))
	if err != nil {
		t.Fatal(err)
	}
	if res.Datapack.Name != "loot.zip" || !res.Datapack.Enabled || res.Datapack.Description != "Loot tweaks" || res.Replaced {
		t.Fatalf("import result wrong: %+v replaced=%v", res.Datapack, res.Replaced)
	}
	// No level.dat yet, the packs wait for world generation
	if res.WorldExists {
		t.Fatal("world without level.dat should report world_exists false")
	}
	dpDir := filepath.Join(dataPath, "survival", "datapacks")
	if _, err := os.Stat(filepath.Join(dpDir, "loot.zip")); err != nil {
		t.Fatal("zip should land in <level-name>/datapacks")
	}

	// A folder wrapped zip is rebased so the game can load it
	res, err = importPack(s, uploadBytes(t, s, "repo-main.zip", zipBytes(t, map[string]string{"repo-main/pack.mcmeta": meta, "repo-main/data/a.json": "{}"})))
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(filepath.Join(dpDir, "repo-main.zip"))
	if err != nil {
		t.Fatal(err)
	}
	var rooted bool
	for _, f := range r.File {
		rooted = rooted || f.Name == "pack.mcmeta"
	}
	r.Close()
	if !rooted {
		t.Fatal("nested pack.mcmeta should be moved to the zip root")
	}

	// ./ prefixed entries are normalised so the game finds pack.mcmeta
	if _, err := importPack(s, uploadBytes(t, s, "dotted.zip", zipBytes(t, map[string]string{"./pack.mcmeta": meta, "./data/a.json": "{}"}))); err != nil {
		t.Fatal(err)
	}
	r, err = zip.OpenReader(filepath.Join(dpDir, "dotted.zip"))
	if err != nil {
		t.Fatal(err)
	}
	rooted = false
	for _, f := range r.File {
		rooted = rooted || f.Name == "pack.mcmeta"
	}
	r.Close()
	if !rooted {
		t.Fatal("./pack.mcmeta should be stored as pack.mcmeta")
	}

	// Same name replaces and says so
	if res, err = importPack(s, uploadBytes(t, s, "loot.zip", zipBytes(t, map[string]string{"pack.mcmeta": meta}))); err != nil || !res.Replaced {
		t.Fatalf("reupload should report replaced, got %+v %v", res, err)
	}

	bad := []struct {
		name string
		data []byte
	}{
		{"nometa.zip", zipBytes(t, map[string]string{"data/a.json": "{}"})},
		{"notzip.zip", []byte("plain text")},
		{"pack.jar", zipBytes(t, map[string]string{"pack.mcmeta": meta})},
		{".hidden.zip", zipBytes(t, map[string]string{"pack.mcmeta": meta})},
	}
	for _, tc := range bad {
		if _, err := importPack(s, uploadBytes(t, s, tc.name, tc.data)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: want InvalidArgument, got %v", tc.name, err)
		}
	}
	entries, _ := os.ReadDir(dpDir)
	if len(entries) != 3 {
		t.Fatalf("rejected uploads must leave nothing behind, dir holds %d entries", len(entries))
	}
}

func TestToggleAndDeleteDatapack(t *testing.T) {
	s, dataPath := datapackRig(t)
	ctx := context.Background()
	if _, err := importPack(s, uploadBytes(t, s, "a.zip", zipBytes(t, map[string]string{"pack.mcmeta": "{}"}))); err != nil {
		t.Fatal(err)
	}
	dpDir := filepath.Join(dataPath, "world", "datapacks")

	toggle := func(name string, enabled bool) (*v1.Datapack, error) {
		res, err := s.ToggleDatapack(ctx, connect.NewRequest(&v1.ToggleDatapackRequest{ServerId: "srv1", Name: name, Enabled: enabled}))
		if err != nil {
			return nil, err
		}
		return res.Msg.Datapack, nil
	}
	if p, err := toggle("a.zip", false); err != nil || p.Enabled {
		t.Fatalf("disable: %+v %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(dpDir+"_disabled", "a.zip")); err != nil {
		t.Fatal("disabled pack should sit in datapacks_disabled")
	}
	// Repeating the current state is a no-op
	if p, err := toggle("a.zip", false); err != nil || p.Enabled {
		t.Fatalf("repeat disable: %+v %v", p, err)
	}
	os.WriteFile(filepath.Join(dataPath, "world", "level.dat"), []byte("x"), 0644)
	list, err := s.ListDatapacks(ctx, connect.NewRequest(&v1.ListDatapacksRequest{ServerId: "srv1"}))
	if err != nil || len(list.Msg.Datapacks) != 1 || list.Msg.Datapacks[0].Enabled || list.Msg.Directory != "world/datapacks" || !list.Msg.WorldExists {
		t.Fatalf("list after disable: %+v %v", list, err)
	}
	if p, err := toggle("a.zip", true); err != nil || !p.Enabled {
		t.Fatalf("enable: %+v %v", p, err)
	}
	if _, err := toggle("../a.zip", true); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("path names must not resolve, got %v", err)
	}

	if _, err := s.DeleteDatapack(ctx, connect.NewRequest(&v1.DeleteDatapackRequest{ServerId: "srv1", Name: "a.zip"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dpDir, "a.zip")); !os.IsNotExist(err) {
		t.Fatal("deleted pack should be gone")
	}
	if _, err := s.DeleteDatapack(ctx, connect.NewRequest(&v1.DeleteDatapackRequest{ServerId: "srv1", Name: "a.zip"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("second delete should be NotFound, got %v", err)
	}

	// Folder packs delete whole
	folder := filepath.Join(dpDir, "folder")
	os.MkdirAll(filepath.Join(folder, "data"), 0755)
	os.WriteFile(filepath.Join(folder, "pack.mcmeta"), []byte("{}"), 0644)
	if _, err := s.DeleteDatapack(ctx, connect.NewRequest(&v1.DeleteDatapackRequest{ServerId: "srv1", Name: "folder"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatal("folder pack should be removed")
	}
}
