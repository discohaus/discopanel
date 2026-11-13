package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/transfer"
)

func TestDownloadStreamContentType(t *testing.T) {
	log := logger.New()
	downloads := transfer.NewDownloadManager(t.TempDir(), time.Minute, log)
	t.Cleanup(downloads.Stop)
	handler := NewDownloadStreamHandler(downloads, log)

	// Html looking bytes under names that would otherwise be sniffed
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, []byte("<html><script>alert(1)</script>"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		filename    string
		contentType string
		want        string
	}{
		{"forced type wins", "pack.mrpack", "application/zip", "application/zip"},
		{"unknown extension", "pack.dpnotatype", "", "application/octet-stream"},
		{"no extension", "README", "", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := downloads.InitTypedSession(path, tt.filename, tt.contentType, 0, false)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/download/"+session.ID, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.want {
				t.Fatalf("content type %q, want %q", got, tt.want)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing nosniff")
			}
		})
	}
}
