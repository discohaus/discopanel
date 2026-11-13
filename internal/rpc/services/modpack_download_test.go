package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Modpack service on a real store with a temp data dir
func testModpackService(t *testing.T) (*ModpackService, *storage.Store, string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "modpack.db")
	cfg.Database.AutoMigrate = true
	cfg.Storage.DataDir = t.TempDir()
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	log := logger.New()
	downloads := transfer.NewDownloadManager(t.TempDir(), time.Minute, log)
	t.Cleanup(downloads.Stop)
	return NewModpackService(store, cfg, nil, downloads, log), store, cfg.Storage.DataDir
}

// Pack row plus one file row pointing at downloadURL
func seedModpack(t *testing.T, store *storage.Store, id, indexer, downloadURL string) {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertIndexedModpack(ctx, &v1.IndexedModpack{Id: id, IndexerId: id, Indexer: indexer, Name: id, LatestFileId: id}); err != nil {
		t.Fatalf("seed modpack failed %v", err)
	}
	if err := store.UpsertIndexedModpackFile(ctx, &v1.IndexedModpackFile{Id: id, ModpackId: id, FileName: "Pack 1.0.zip", DownloadUrl: downloadURL, FileLength: 4}); err != nil {
		t.Fatalf("seed modpack file failed %v", err)
	}
}

// Writes a stored archive under the manual pack dir
func writeArchive(t *testing.T, dataDir, name string) string {
	t.Helper()
	dir := filepath.Join(dataDir, "modpacks", "manual")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("PK\x03\x04"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalModpackArchive(t *testing.T) {
	dataDir := t.TempDir()
	inside := writeArchive(t, dataDir, "a.zip")
	outside := filepath.Join(t.TempDir(), "secret.zip")
	if err := os.WriteFile(outside, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Prefix sibling must not pass as inside
	sibling := filepath.Join(dataDir, "modpacks-evil", "b.zip")
	os.MkdirAll(filepath.Dir(sibling), 0755)
	os.WriteFile(sibling, []byte("x"), 0644)
	link := filepath.Join(dataDir, "modpacks", "manual", "link.zip")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	relInside, err := filepath.Rel(cwd, inside)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		stored  string
		wantErr bool
	}{
		{"absolute inside", inside, false},
		{"relative inside", relInside, false},
		{"absolute outside", outside, true},
		{"traversal", filepath.Join(dataDir, "modpacks", "manual", "..", "..", "..", filepath.Base(filepath.Dir(outside)), "secret.zip"), true},
		{"prefix sibling", sibling, true},
		{"symlink escape", link, true},
		{"archive dir itself", filepath.Join(dataDir, "modpacks"), true},
		{"subdirectory", filepath.Join(dataDir, "modpacks", "manual"), true},
		{"missing", filepath.Join(dataDir, "modpacks", "manual", "gone.zip"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := localModpackArchive(dataDir, tt.stored)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, resolved %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got != inside {
				t.Fatalf("resolved %s, want %s", got, inside)
			}
		})
	}
}

func TestPickModpackFile(t *testing.T) {
	packFiles := []*v1.IndexedModpackFile{{Id: "new"}, {Id: "latest"}, {Id: "old"}}
	tests := []struct {
		name     string
		fileID   string
		latestID string
		want     string
	}{
		{"explicit file", "old", "latest", "old"},
		{"unknown explicit file", "nope", "latest", ""},
		{"latest pointer", "", "latest", "latest"},
		{"stale latest falls back to newest", "", "gone", "new"},
		{"no latest uses newest", "", "", "new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickModpackFile(packFiles, tt.fileID, tt.latestID)
			gotID := ""
			if got != nil {
				gotID = got.Id
			}
			if gotID != tt.want {
				t.Fatalf("picked %q, want %q", gotID, tt.want)
			}
		})
	}
	if pickModpackFile(nil, "", "x") != nil {
		t.Fatal("empty list picked a file")
	}
}

func TestModpackArchiveFilename(t *testing.T) {
	tests := []struct{ name, fileName, fallback, want string }{
		{"plain", "Pack 1.0.zip", "/x/id.zip", "Pack 1.0.zip"},
		{"path stripped", "../../etc/passwd", "/x/id.zip", "passwd"},
		{"windows path stripped", `C:\\packs\\pack.mrpack`, "/x/id.zip", "pack.mrpack"},
		{"empty uses fallback", "", "/x/id.zip", "id.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modpackArchiveFilename(&v1.IndexedModpackFile{FileName: tt.fileName}, tt.fallback)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInitModpackDownload(t *testing.T) {
	svc, store, dataDir := testModpackService(t)
	ctx := context.Background()
	local := writeArchive(t, dataDir, "local.zip")
	seedModpack(t, store, "local", indexers.ManualIndexer, local)
	seedModpack(t, store, "remote", "modrinth", "https://cdn.modrinth.com/data/x/pack.mrpack")
	seedModpack(t, store, "restricted", "fuego", "")
	seedModpack(t, store, "badscheme", "fuego", "file:///etc/passwd")
	seedModpack(t, store, "escaped", indexers.ManualIndexer, "/etc/passwd")
	seedModpack(t, store, "missing", indexers.ManualIndexer, filepath.Join(dataDir, "modpacks", "manual", "gone.zip"))

	resp, err := svc.InitModpackDownload(ctx, connect.NewRequest(&v1.InitModpackDownloadRequest{Id: "local"}))
	if err != nil {
		t.Fatalf("local download failed %v", err)
	}
	if resp.Msg.Remote || resp.Msg.SessionId == "" || resp.Msg.DownloadUrl != "/api/v1/download/"+resp.Msg.SessionId {
		t.Fatalf("unexpected local response %+v", resp.Msg)
	}
	if resp.Msg.Filename != "Pack 1.0.zip" || resp.Msg.TotalSize != 4 {
		t.Fatalf("unexpected local metadata %+v", resp.Msg)
	}
	session, err := svc.downloadManager.GetSession(resp.Msg.SessionId)
	if err != nil || session.FilePath != local || session.DeleteAfter || session.ContentType != "application/zip" {
		t.Fatalf("session not registered for stored archive %+v %v", session, err)
	}

	resp, err = svc.InitModpackDownload(ctx, connect.NewRequest(&v1.InitModpackDownloadRequest{Id: "remote"}))
	if err != nil {
		t.Fatalf("remote download failed %v", err)
	}
	if !resp.Msg.Remote || resp.Msg.SessionId != "" || resp.Msg.DownloadUrl != "https://cdn.modrinth.com/data/x/pack.mrpack" {
		t.Fatalf("unexpected remote response %+v", resp.Msg)
	}

	failures := []struct {
		id, fileID string
		code       connect.Code
	}{
		{"restricted", "", connect.CodeFailedPrecondition},
		{"badscheme", "", connect.CodeFailedPrecondition},
		{"escaped", "", connect.CodeFailedPrecondition},
		{"missing", "", connect.CodeNotFound},
		{"local", "other-file", connect.CodeNotFound},
		{"nope", "", connect.CodeNotFound},
	}
	for _, f := range failures {
		_, err := svc.InitModpackDownload(ctx, connect.NewRequest(&v1.InitModpackDownloadRequest{Id: f.id, FileId: f.fileID}))
		if connect.CodeOf(err) != f.code {
			t.Errorf("%s/%s: got %v, want %v", f.id, f.fileID, err, f.code)
		}
	}
}

func TestPublicModpackGating(t *testing.T) {
	svc, store, dataDir := testModpackService(t)
	ctx := context.Background()
	local := writeArchive(t, dataDir, "shared.zip")
	seedModpack(t, store, "shared", indexers.ManualIndexer, local)
	seedModpack(t, store, "indexed", "modrinth", "https://cdn.modrinth.com/data/x/pack.mrpack")
	seedModpack(t, store, "escaped", indexers.ManualIndexer, "/etc/passwd")

	// Private by default
	if _, _, err := svc.PublicModpackArchive(ctx, "shared"); !errors.Is(err, ErrModpackNotPublic) {
		t.Fatalf("private pack served, err %v", err)
	}

	resp, err := svc.SetModpackPublicDownload(ctx, connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "shared", Enabled: true}))
	if err != nil {
		t.Fatalf("enable failed %v", err)
	}
	if !resp.Msg.PublicDownload || resp.Msg.PublicPath != "/api/v1/modpacks/shared/download" {
		t.Fatalf("unexpected enable response %+v", resp.Msg)
	}
	path, filename, err := svc.PublicModpackArchive(ctx, "shared")
	if err != nil || path != local || filename != "Pack 1.0.zip" {
		t.Fatalf("public pack not served %s %s %v", path, filename, err)
	}

	// Listings carry the flag
	got, err := svc.GetModpack(ctx, connect.NewRequest(&v1.GetModpackRequest{Id: "shared"}))
	if err != nil || !got.Msg.Modpack.PublicDownload {
		t.Fatalf("GetModpack missing public flag %v", err)
	}
	search, err := svc.SearchModpacks(ctx, connect.NewRequest(&v1.SearchModpacksRequest{Indexer: indexers.ManualIndexer}))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range search.Msg.Modpacks {
		if m.PublicDownload != (m.Id == "shared") {
			t.Fatalf("search flag wrong for %s", m.Id)
		}
	}

	// Indexed and unservable packs cannot be shared
	for _, id := range []string{"indexed", "escaped", "nope"} {
		if _, err := svc.SetModpackPublicDownload(ctx, connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: id, Enabled: true})); err == nil {
			t.Fatalf("%s enabled public download", id)
		}
	}
	// Flags set behind the rpc still never serve them
	for _, id := range []string{"indexed", "escaped", "nope"} {
		if err := store.SetModpackPublicDownload(ctx, id, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.PublicModpackArchive(ctx, id); !errors.Is(err, ErrModpackNotPublic) {
			t.Fatalf("%s served publicly, err %v", id, err)
		}
	}

	// Turning it off revokes the link
	if _, err := svc.SetModpackPublicDownload(ctx, connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "shared", Enabled: false})); err != nil {
		t.Fatalf("disable failed %v", err)
	}
	if _, _, err := svc.PublicModpackArchive(ctx, "shared"); !errors.Is(err, ErrModpackNotPublic) {
		t.Fatalf("disabled pack served, err %v", err)
	}
	// Disabling twice is harmless
	if _, err := svc.SetModpackPublicDownload(ctx, connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "shared", Enabled: false})); err != nil {
		t.Fatalf("second disable failed %v", err)
	}

	// Deleting a pack clears its flag
	if err := store.SetModpackPublicDownload(ctx, "shared", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteModpack(ctx, connect.NewRequest(&v1.DeleteModpackRequest{Id: "shared"})); err != nil {
		t.Fatalf("delete failed %v", err)
	}
	if public, _ := store.IsModpackPublicDownload(ctx, "shared"); public {
		t.Fatal("deleted pack kept its public flag")
	}
}

func TestPublicDownloadModpackIDsIgnoresOtherSettings(t *testing.T) {
	_, store, _ := testModpackService(t)
	ctx := context.Background()
	// LIKE wildcard lookalike must not match
	if err := store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: "modpackXpublic_download:x", Value: "true"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetModpackPublicDownload(ctx, "a", true); err != nil {
		t.Fatal(err)
	}
	ids, err := store.PublicDownloadModpackIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || !ids["a"] {
		t.Fatalf("unexpected ids %v", ids)
	}
}
