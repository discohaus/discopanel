package services

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
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
