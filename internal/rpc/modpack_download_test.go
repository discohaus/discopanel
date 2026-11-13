package rpc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/pkg/indexers"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/transfer"
)

// Fetches a path with an optional range header
func fetch(t *testing.T, url, rangeHeader string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// Pack downloads through the real service and route wiring
func TestModpackDownloadRoutes(t *testing.T) {
	s := testRig(t)
	ctx := context.Background()
	s.config.Storage.DataDir = t.TempDir()
	s.downloadManager = transfer.NewDownloadManager(t.TempDir(), time.Minute, s.log)
	t.Cleanup(s.downloadManager.Stop)
	s.uploadManager = transfer.NewUploadManager(t.TempDir(), time.Minute, 0, s.log)
	t.Cleanup(s.uploadManager.Stop)

	mux := http.NewServeMux()
	s.registerServices(mux, s.handlerOptions())
	s.registerTransferRoutes(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	// Html looking bytes must still go out as a zip download
	archive := filepath.Join(s.config.Storage.DataDir, "modpacks", "manual", "pack-1.mrpack")
	if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
		t.Fatal(err)
	}
	payload := "<html><script>alert(1)</script></html>"
	if err := os.WriteFile(archive, []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.store.UpsertIndexedModpack(ctx, &v1.IndexedModpack{Id: "pack-1", IndexerId: "pack-1", Indexer: indexers.ManualIndexer, Name: "Pack", LatestFileId: "pack-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.UpsertIndexedModpackFile(ctx, &v1.IndexedModpackFile{Id: "pack-1", ModpackId: "pack-1", FileName: "Pack.mrpack", DownloadUrl: archive}); err != nil {
		t.Fatal(err)
	}

	adminToken := seedUser(t, s, "boss", "admin")
	client := discopanelv1connect.NewModpackServiceClient(http.DefaultClient, ts.URL)
	authed := func(req connect.AnyRequest) {
		req.Header().Set("Authorization", "Bearer "+adminToken)
	}
	publicURL := ts.URL + "/api/v1/modpacks/pack-1/download"

	// Private until opted in, and the rpcs still demand auth
	if resp, _ := fetch(t, publicURL, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("private pack status %d", resp.StatusCode)
	}
	if _, err := client.InitModpackDownload(ctx, connect.NewRequest(&v1.InitModpackDownloadRequest{Id: "pack-1"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous init download got %v", err)
	}
	if _, err := client.SetModpackPublicDownload(ctx, connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "pack-1", Enabled: true})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous public toggle got %v", err)
	}

	// Session download through the shared download route
	initReq := connect.NewRequest(&v1.InitModpackDownloadRequest{Id: "pack-1"})
	authed(initReq)
	initResp, err := client.InitModpackDownload(ctx, initReq)
	if err != nil {
		t.Fatalf("init download failed %v", err)
	}
	resp, body := fetch(t, ts.URL+initResp.Msg.DownloadUrl, "")
	if resp.StatusCode != http.StatusOK || body != payload {
		t.Fatalf("session download status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("session download headers %v", resp.Header)
	}

	// Opt in, then the public route serves without a token
	setReq := connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "pack-1", Enabled: true})
	authed(setReq)
	setResp, err := client.SetModpackPublicDownload(ctx, setReq)
	if err != nil {
		t.Fatalf("enable public failed %v", err)
	}
	if ts.URL+setResp.Msg.PublicPath != publicURL {
		t.Fatalf("public path %q not routed", setResp.Msg.PublicPath)
	}
	resp, body = fetch(t, publicURL, "")
	if resp.StatusCode != http.StatusOK || body != payload {
		t.Fatalf("public download status %d body %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "application/zip" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("public download headers %v", resp.Header)
	}
	if resp.Header.Get("Content-Disposition") != `attachment; filename=Pack.mrpack` {
		t.Fatalf("disposition %q", resp.Header.Get("Content-Disposition"))
	}
	resp, body = fetch(t, publicURL, "bytes=0-5")
	if resp.StatusCode != http.StatusPartialContent || body != payload[:6] {
		t.Fatalf("range status %d body %q", resp.StatusCode, body)
	}
	if resp, _ := fetch(t, ts.URL+"/api/v1/modpacks/other/download", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown pack status %d", resp.StatusCode)
	}

	// Opting out revokes the link at once
	offReq := connect.NewRequest(&v1.SetModpackPublicDownloadRequest{Id: "pack-1", Enabled: false})
	authed(offReq)
	if _, err := client.SetModpackPublicDownload(ctx, offReq); err != nil {
		t.Fatalf("disable public failed %v", err)
	}
	if resp, _ := fetch(t, publicURL, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked pack status %d", resp.StatusCode)
	}
}
