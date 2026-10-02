package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/rpc/handlers"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/transfer"
)

func TestCanceledSupportBundle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &SupportService{config: &config.Config{Storage: config.StorageConfig{TempDir: t.TempDir()}}}
	if _, err := s.buildBundle(ctx, false, false, false, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("build error = %v, want cancellation", err)
	}
	if _, err := s.uploadBundleToServer(ctx, "unused", "unused", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("upload error = %v, want cancellation", err)
	}
}

func TestSupportUploadUsesRequestContext(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(stopped)
		case <-time.After(5 * time.Second):
			t.Error("support server did not see the upload cancel")
		}
	}))
	defer server.Close()
	previous := hub.Settings{SupportBase: hub.SupportBase(), IndexBase: hub.IndexBase(), IndexEnabled: hub.IndexEnabled(), InstallID: hub.InstallID()}
	t.Cleanup(func() {
		if err := hub.Configure(previous); err != nil {
			t.Fatal(err)
		}
	})
	if err := hub.Configure(hub.Settings{SupportBase: server.URL, IndexBase: hub.DefaultIndexBase, InstallID: strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(bundle, []byte("test bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := (&SupportService{}).uploadBundleToServer(ctx, bundle, "bundle.tar.gz", nil)
		result <- err
	}()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("upload failed before reaching server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("upload never reached server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("upload error = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload ignored cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("support server kept receiving the canceled upload")
	}
}

// Fills a file with a repeating byte pattern
func writePatternFile(t *testing.T, path string, size int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	chunk := make([]byte, 64<<10)
	for i := range chunk {
		chunk[i] = byte(i*7 + 3)
	}
	for written := 0; written < size; {
		n := len(chunk)
		if size-written < n {
			n = size - written
		}
		if _, err := f.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
}

// Hashes a file on disk
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Counts bytes passing through a reader
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Total bytes allocated so far by this process
func totalAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.TotalAlloc
}

func TestUploadBundleStreamsFromDisk(t *testing.T) {
	const bundleSize = 32 << 20
	type received struct {
		digest        string
		fields        map[string]string
		contentLength int64
		bodyBytes     int64
	}
	got := make(chan received, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
			return
		}
		body := &countingReader{r: r.Body}
		reader := multipart.NewReader(body, params["boundary"])
		res := received{fields: map[string]string{}, contentLength: r.ContentLength}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("next part: %v", err)
				return
			}
			if part.FormName() == "bundle" {
				h := sha256.New()
				if _, err := io.Copy(h, part); err != nil {
					t.Errorf("read bundle part: %v", err)
					return
				}
				res.digest = hex.EncodeToString(h.Sum(nil))
				continue
			}
			value, err := io.ReadAll(part)
			if err != nil {
				t.Errorf("read field: %v", err)
				return
			}
			res.fields[part.FormName()] = string(value)
		}
		res.bodyBytes = body.n
		got <- res
		fmt.Fprint(w, `{"reference_id":"ref-123"}`)
	}))
	defer server.Close()
	previous := hub.Settings{SupportBase: hub.SupportBase(), IndexBase: hub.IndexBase(), IndexEnabled: hub.IndexEnabled(), InstallID: hub.InstallID()}
	t.Cleanup(func() {
		if err := hub.Configure(previous); err != nil {
			t.Fatal(err)
		}
	})
	if err := hub.Configure(hub.Settings{SupportBase: server.URL, IndexBase: hub.DefaultIndexBase, InstallID: strings.Repeat("a", 32)}); err != nil {
		t.Fatal(err)
	}

	bundle := filepath.Join(t.TempDir(), "bundle.tar.gz")
	writePatternFile(t, bundle, bundleSize)
	want := fileDigest(t, bundle)

	info := &v1.UploadSupportBundleRequest{DiscordUsername: "nick", IssueDescription: "panel ate all the ram"}
	before := totalAlloc()
	ref, err := (&SupportService{}).uploadBundleToServer(context.Background(), bundle, "bundle.tar.gz", info)
	allocated := totalAlloc() - before
	if err != nil {
		t.Fatal(err)
	}
	if ref != "ref-123" {
		t.Fatalf("reference = %q", ref)
	}
	res := <-got
	if res.digest != want {
		t.Fatal("uploaded bundle bytes differ from the file on disk")
	}
	if res.fields["size"] != fmt.Sprint(bundleSize) || res.fields["discord_username"] != "nick" || res.fields["issue_description"] != info.IssueDescription || res.fields["timestamp"] == "" {
		t.Fatalf("fields = %v", res.fields)
	}
	if res.contentLength <= 0 || res.contentLength != res.bodyBytes {
		t.Fatalf("content length %d, body bytes %d", res.contentLength, res.bodyBytes)
	}
	if allocated > bundleSize/4 {
		t.Fatalf("upload allocated %d bytes for a %d byte bundle", allocated, bundleSize)
	}
}

func TestDownloadSupportBundleStreamsThroughSession(t *testing.T) {
	const bundleSize = 32 << 20
	tempDir := t.TempDir()
	log := logger.New()
	downloads := transfer.NewDownloadManager(tempDir, time.Minute, log)
	t.Cleanup(downloads.Stop)
	s := &SupportService{
		config:    &config.Config{Storage: config.StorageConfig{TempDir: tempDir}},
		downloads: downloads,
		log:       log,
		bundles:   map[string]*bundleJob{},
	}
	writePatternFile(t, s.bundlePath("bundle.tar.gz"), bundleSize)
	want := fileDigest(t, s.bundlePath("bundle.tar.gz"))
	s.bundles["ready"] = &bundleJob{state: v1.SupportBundleState_SUPPORT_BUNDLE_STATE_READY, filename: "bundle.tar.gz", size: bundleSize}
	s.bundles["running"] = &bundleJob{state: v1.SupportBundleState_SUPPORT_BUNDLE_STATE_RUNNING}
	s.bundles["uploaded"] = &bundleJob{state: v1.SupportBundleState_SUPPORT_BUNDLE_STATE_READY, referenceID: "ref"}

	ctx := context.Background()
	var cerr *connect.Error
	for _, id := range []string{"running", "uploaded"} {
		_, err := s.DownloadSupportBundle(ctx, connect.NewRequest(&v1.DownloadSupportBundleRequest{BundleId: id}))
		if !errors.As(err, &cerr) || cerr.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("%s bundle error = %v, want failed precondition", id, err)
		}
	}

	resp, err := s.DownloadSupportBundle(ctx, connect.NewRequest(&v1.DownloadSupportBundleRequest{BundleId: "ready"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.SessionId == "" || resp.Msg.Filename != "bundle.tar.gz" || resp.Msg.TotalSize != bundleSize {
		t.Fatalf("response = %v", resp.Msg)
	}
	if _, err := os.Stat(s.bundlePath("bundle.tar.gz")); err != nil {
		t.Fatalf("bundle removed before the session served it: %v", err)
	}
	_, err = s.DownloadSupportBundle(ctx, connect.NewRequest(&v1.DownloadSupportBundleRequest{BundleId: "ready"}))
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound {
		t.Fatalf("second download error = %v, want not found", err)
	}
	s.cleanupBundle("ready")
	if _, err := os.Stat(s.bundlePath("bundle.tar.gz")); err != nil {
		t.Fatalf("retention cleanup removed a claimed bundle: %v", err)
	}

	server := httptest.NewServer(handlers.NewDownloadStreamHandler(downloads, log))
	defer server.Close()
	before := totalAlloc()
	res, err := http.Get(server.URL + "/api/v1/download/" + resp.Msg.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, res.Body)
	allocated := totalAlloc() - before
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || n != bundleSize || hex.EncodeToString(h.Sum(nil)) != want {
		t.Fatalf("status %d, %d bytes, digest mismatch", res.StatusCode, n)
	}
	if !strings.Contains(res.Header.Get("Content-Disposition"), `filename="bundle.tar.gz"`) {
		t.Fatalf("content disposition = %q", res.Header.Get("Content-Disposition"))
	}
	if allocated > bundleSize/4 {
		t.Fatalf("download allocated %d bytes for a %d byte bundle", allocated, bundleSize)
	}

	downloads.CleanupSession(resp.Msg.SessionId)
	if _, err := os.Stat(s.bundlePath("bundle.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("session cleanup left the bundle behind: %v", err)
	}
}
