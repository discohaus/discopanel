package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Completes an upload session holding the given archive bytes
func uploadArchive(t *testing.T, m *transfer.UploadManager, name string, data []byte) string {
	t.Helper()
	sess, err := m.InitSession(name, int64(len(data)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, done, err := m.WriteStream(sess.ID, bytes.NewReader(data), 0); err != nil || !done {
		t.Fatalf("upload incomplete done=%v err=%v", done, err)
	}
	return sess.ID
}

func tarGzTree(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for rel, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func importTestService(t *testing.T) *ServerService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Storage.DataDir = t.TempDir()
	m := transfer.NewUploadManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(m.Stop)
	return &ServerService{config: cfg, uploadManager: m, log: logger.New()}
}

func TestStageServerImportUnwrapsAndPlaces(t *testing.T) {
	s := importTestService(t)
	zipPath := zipTree(t, map[string]string{
		"Survival SMP/server.jar":         "jar",
		"Survival SMP/server.properties":  "motd=Hi\n",
		"Survival SMP/world/level.dat":    "lvl",
		"Survival SMP/.discopanel/a.json": "{}",
	})
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	id := uploadArchive(t, s.uploadManager, "export.zip", data)

	staged, err := s.stageServerImport(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if staged.wrapper != "Survival SMP" || staged.suggestedName() != "Survival SMP" {
		t.Fatalf("wrapper %q name %q", staged.wrapper, staged.suggestedName())
	}
	if staged.meta.FileCount != 4 || staged.meta.ArchiveName != "export.zip" {
		t.Fatalf("meta wrong %+v", staged.meta)
	}
	// The upload is consumed, a second analyze reuses the staged tree
	if _, _, err := s.uploadManager.GetTempPath(id); err == nil {
		t.Fatal("upload session must be consumed after staging")
	}
	again, err := s.stageServerImport(context.Background(), id)
	if err != nil || again.root != staged.root {
		t.Fatalf("restage must reuse the tree, got %v %v", again, err)
	}

	dataPath := filepath.Join(s.config.Storage.DataDir, "servers", "survival_x")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := placeServerImport(staged, dataPath); err != nil {
		t.Fatal(err)
	}
	if body, ok := readFile(t, filepath.Join(dataPath, "world", "level.dat")); !ok || body != "lvl" {
		t.Fatal("world must land in the data dir as-is")
	}
	if _, err := os.Stat(staged.dir); !os.IsNotExist(err) {
		t.Fatal("staging dir must be removed after placement")
	}
}

func TestStageServerImportTarGzFlat(t *testing.T) {
	s := importTestService(t)
	id := uploadArchive(t, s.uploadManager, "backup.tar.gz", tarGzTree(t, map[string]string{
		"server.jar":        "jar",
		"plugins/a.jar":     "p",
		"server.properties": "max-players=50\n",
	}))
	staged, err := s.stageServerImport(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if staged.wrapper != "" || staged.suggestedName() != "backup" {
		t.Fatalf("flat archive wrapper %q name %q", staged.wrapper, staged.suggestedName())
	}
	if _, ok := readFile(t, filepath.Join(staged.root, "plugins", "a.jar")); !ok {
		t.Fatal("tar.gz contents missing")
	}
}

func TestStageServerImportRejects(t *testing.T) {
	s := importTestService(t)
	if _, err := s.stageServerImport(context.Background(), "../../etc"); err == nil {
		t.Fatal("non uuid session ids must be rejected")
	}
	id := uploadArchive(t, s.uploadManager, "server.rar", []byte("nope"))
	if _, err := s.stageServerImport(context.Background(), id); err == nil {
		t.Fatal("unsupported archive types must be rejected")
	}
	id = uploadArchive(t, s.uploadManager, "server.zip", []byte("not a zip"))
	if _, err := s.stageServerImport(context.Background(), id); err == nil {
		t.Fatal("corrupt archives must be rejected")
	}
	entries, _ := os.ReadDir(s.importStagingRoot())
	if len(entries) != 0 {
		t.Fatalf("failed imports must leave no staging, got %d entries", len(entries))
	}

	release, err := s.claimServerImport(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimServerImport(id); err == nil {
		t.Fatal("a session must not be claimed twice")
	}
	release()
}

func TestDiscardServerImport(t *testing.T) {
	s := importTestService(t)
	data, err := os.ReadFile(zipTree(t, map[string]string{"server.jar": "jar"}))
	if err != nil {
		t.Fatal(err)
	}
	id := uploadArchive(t, s.uploadManager, "srv.zip", data)
	staged, err := s.stageServerImport(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&v1.DiscardServerImportRequest{UploadSessionId: id})
	if _, err := s.DiscardServerImport(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged.dir); !os.IsNotExist(err) {
		t.Fatal("discard must delete the staged tree")
	}
	// Discarding twice, or after a create consumed it, is harmless
	if _, err := s.DiscardServerImport(context.Background(), req); err != nil {
		t.Fatalf("repeat discard failed %v", err)
	}

	// An import in use by analyze or create is never discarded
	release, err := s.claimServerImport(id)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := s.DiscardServerImport(context.Background(), req); err == nil {
		t.Fatal("discard must refuse a claimed import")
	}
}

func TestSweepStaleImportsSparesClaimed(t *testing.T) {
	s := importTestService(t)
	root := s.importStagingRoot()
	old := time.Now().Add(-2 * importStagingTTL)
	mk := func(name string, complete bool, mod time.Time) string {
		dir := filepath.Join(root, name)
		sub := "partial"
		if complete {
			sub = "files"
		}
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	stale := mk("11111111-1111-1111-1111-111111111111", true, old)
	claimed := mk("22222222-2222-2222-2222-222222222222", true, old)
	fresh := mk("33333333-3333-3333-3333-333333333333", true, time.Now())
	crashed := mk("44444444-4444-4444-4444-444444444444", false, time.Now())
	busyPartial := mk("55555555-5555-5555-5555-555555555555", false, time.Now())

	for _, id := range []string{filepath.Base(claimed), filepath.Base(busyPartial)} {
		release, err := s.claimServerImport(id)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	s.SweepImportsAtStartup()

	for dir, keep := range map[string]bool{stale: false, claimed: true, fresh: true, crashed: false, busyPartial: true} {
		_, err := os.Stat(dir)
		if exists := err == nil; exists != keep {
			t.Errorf("%s exists=%v want %v", filepath.Base(dir), exists, keep)
		}
	}
	// The sweep releases what it claimed
	if _, busy := s.importsBusy.Load(filepath.Base(stale)); busy {
		t.Fatal("sweep left a claim behind")
	}
}
