package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func testProvisioner(t *testing.T) *Provisioner {
	t.Helper()
	cfg := &config.Config{}
	cfg.Storage.DataDir = t.TempDir()
	cfg.Server.UserAgent = "discobench-test"
	return &Provisioner{cfg: cfg, log: logger.New()}
}

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestCASRoundTrip(t *testing.T) {
	p := testProvisioner(t)
	content := []byte("mod jar bytes")
	sum := &checksum{algo: "sha256", value: sha256Of(content)}

	src := filepath.Join(t.TempDir(), "src.jar")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "dest.jar")
	if p.casGet(dest, sum) {
		t.Fatal("empty cache should miss")
	}

	p.casPut(src, sum)
	if !p.casGet(dest, sum) {
		t.Fatal("admitted entry should hit")
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(content) {
		t.Fatalf("cache hit content mismatch: %q %v", got, err)
	}

	// Weak and missing checksums never cache
	p.casPut(src, &checksum{algo: "md5", value: "d41d8cd98f00b204e9800998ecf8427e"})
	if p.casGet(dest, &checksum{algo: "md5", value: "d41d8cd98f00b204e9800998ecf8427e"}) {
		t.Fatal("md5 must not be a cache identity")
	}
	if p.casGet(dest, nil) {
		t.Fatal("nil checksum must miss")
	}
}

func TestDownloadUsesCache(t *testing.T) {
	p := testProvisioner(t)
	content := []byte("artifact payload")
	sum := &checksum{algo: "sha256", value: sha256Of(content)}

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	ctx := context.Background()
	dest1 := filepath.Join(t.TempDir(), "a.jar")
	if err := p.download(ctx, srv.URL, dest1, sum, nil, nil); err != nil {
		t.Fatal(err)
	}
	dest2 := filepath.Join(t.TempDir(), "b.jar")
	if err := p.download(ctx, srv.URL, dest2, sum, nil, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("second download should come from cache, upstream saw %d requests", hits.Load())
	}
	got, err := os.ReadFile(dest2)
	if err != nil || string(got) != string(content) {
		t.Fatalf("cached download content mismatch: %q %v", got, err)
	}

	// Corrupt upstream still fails checksum and admits nothing
	bad := &checksum{algo: "sha256", value: sha256Of([]byte("other"))}
	if err := p.download(ctx, srv.URL, filepath.Join(t.TempDir(), "c.jar"), bad, nil, nil); err == nil {
		t.Fatal("checksum mismatch must fail")
	}
	if p.casGet(filepath.Join(t.TempDir(), "d.jar"), bad) {
		t.Fatal("failed download must not be admitted")
	}
}

func TestLibTreeRoundTrip(t *testing.T) {
	p := testProvisioner(t)
	server := &v1.Server{Id: "s1", Name: "s1", DataPath: t.TempDir()}

	libDir := filepath.Join(server.DataPath, "libraries", "net", "minecraftforge")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "forge.jar"), []byte("lib"), 0644); err != nil {
		t.Fatal(err)
	}

	key := libTreeKey("forge", "1.20.1", "47.2.0")
	p.saveLibTree(server, key)
	if _, err := os.Stat(p.libTreePath(key)); err != nil {
		t.Fatalf("archive missing after save: %v", err)
	}

	fresh := &v1.Server{Id: "s2", Name: "s2", DataPath: t.TempDir()}
	p.restoreLibTree(fresh, key)
	restored := filepath.Join(fresh.DataPath, "libraries", "net", "minecraftforge", "forge.jar")
	if data, err := os.ReadFile(restored); err != nil || string(data) != "lib" {
		t.Fatalf("restore mismatch: %q %v", data, err)
	}
}

func TestCASGetDropsRottenEntries(t *testing.T) {
	p := testProvisioner(t)
	content := []byte("pristine artifact")
	sum := &checksum{algo: "sha256", value: sha256Of(content)}

	src := filepath.Join(t.TempDir(), "src.jar")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	p.casPut(src, sum)

	entry := casPath(p.cacheRoot(), sum.algo, sum.value)
	if err := os.WriteFile(entry, []byte("bit rot"), 0644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "dest.jar")
	if p.casGet(dest, sum) {
		t.Fatal("rotten entry must miss")
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("rotten entry must be dropped")
	}
}

func TestPruneCaches(t *testing.T) {
	p := testProvisioner(t)
	content := []byte("old artifact")
	sum := &checksum{algo: "sha256", value: sha256Of(content)}
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	p.casPut(src, sum)

	entry := casPath(p.cacheRoot(), sum.algo, sum.value)
	old := time.Now().Add(-2 * casMaxAge)
	if err := os.Chtimes(entry, old, old); err != nil {
		t.Fatal(err)
	}

	pruneGate.Store(0)
	p.pruneCaches()
	shard := filepath.Dir(entry)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, entryErr := os.Stat(entry)
		_, shardErr := os.Stat(shard)
		if os.IsNotExist(entryErr) && os.IsNotExist(shardErr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatal("stale entry survived pruning")
	}
	t.Fatal("empty shard dir survived pruning")
}
