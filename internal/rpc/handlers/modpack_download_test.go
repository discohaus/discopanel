package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/pkg/logger"
)

var errNotPublic = errors.New("not public")

// Serves one public pack id, everything else reads private
type fakePublicSource struct {
	id, path, filename string
}

func (f fakePublicSource) PublicModpackArchive(_ context.Context, id string) (string, string, error) {
	if id != f.id {
		return "", "", errNotPublic
	}
	return f.path, f.filename, nil
}

func publicMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.zip")
	if err := os.WriteFile(path, []byte("<html>0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	src := fakePublicSource{id: "pub", path: path, filename: "My \"Pack\"\r\nX-Evil: 1.zip"}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/modpacks/{id}/download", NewPublicModpackDownloadHandler(src, errNotPublic, logger.New()))
	return mux, path
}

func TestPublicModpackDownloadGating(t *testing.T) {
	mux, _ := publicMux(t)
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"public pack", http.MethodGet, "/api/v1/modpacks/pub/download", http.StatusOK},
		{"head public pack", http.MethodHead, "/api/v1/modpacks/pub/download", http.StatusOK},
		{"private pack", http.MethodGet, "/api/v1/modpacks/private/download", http.StatusNotFound},
		{"post rejected", http.MethodPost, "/api/v1/modpacks/pub/download", http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status %d, want %d", rec.Code, tt.status)
			}
		})
	}
}

func TestPublicModpackDownloadHeaders(t *testing.T) {
	mux, _ := publicMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/modpacks/pub/download", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	h := rec.Header()
	// Html looking bytes must not be sniffed into a page
	if got := h.Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type %q", got)
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if h.Get("X-Evil") != "" {
		t.Fatal("filename injected a header")
	}
	if got := h.Get("Content-Disposition"); got == "" || got[:len("attachment;")] != "attachment;" {
		t.Fatalf("disposition %q", got)
	}
	if h.Get("Accept-Ranges") != "bytes" {
		t.Fatal("ranges not advertised")
	}
}

func TestPublicModpackDownloadRange(t *testing.T) {
	mux, _ := publicMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/modpacks/pub/download", nil)
	req.Header.Set("Range", "bytes=6-9")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != "0123" {
		t.Fatalf("range body %q", body)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 6-9/16" {
		t.Fatalf("content range %q", got)
	}
}

func TestPublicModpackDownloadMissingFile(t *testing.T) {
	mux, path := publicMux(t)
	os.Remove(path)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/modpacks/pub/download", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}
