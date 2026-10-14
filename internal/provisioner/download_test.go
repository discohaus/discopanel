package provisioner

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/discohaus/discopanel/pkg/hub"
)

// Points the hub at an index test server, restoring the defaults afterwards
func configureIndexForTest(t *testing.T, indexURL string) {
	t.Helper()
	settings := hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: indexURL, IndexEnabled: true, InstallID: "0123456789abcdef0123456789abcdef"}
	if err := hub.Configure(settings); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(func() {
		hub.Configure(hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: hub.DefaultIndexBase})
	})
}

func TestDownloadFallsBackToTheOriginTheIndexNames(t *testing.T) {
	payload := []byte("server jar bytes")
	var originCalls, indexCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		if r.URL.Path != "/v1/objects/abc/server.jar" || r.Header.Get("User-Agent") != "discobench-test" || r.Header.Get(hub.InstallIDHeader) != "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(payload)
	}))
	defer origin.Close()
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"code":"permission_denied","message":"mojang refused the request","origin":"%s/v1/objects/abc/server.jar"}`, origin.URL)
	}))
	defer index.Close()
	configureIndexForTest(t, index.URL)

	p := testProvisioner(t)
	dest := filepath.Join(t.TempDir(), "server.jar")
	sum := &checksum{algo: "sha256", value: sha256Of(payload)}
	if err := p.download(t.Context(), index.URL+"/mojang/data/v1/objects/abc/server.jar", dest, sum, nil, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("dest = %q, %v", got, err)
	}
	if indexCalls.Load() != 1 || originCalls.Load() != 1 {
		t.Fatalf("index calls=%d origin calls=%d", indexCalls.Load(), originCalls.Load())
	}
}

func TestDownloadIndexErrorWithoutOriginFails(t *testing.T) {
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"permission_denied","message":"nope"}`)
	}))
	defer index.Close()
	configureIndexForTest(t, index.URL)

	p := testProvisioner(t)
	dest := filepath.Join(t.TempDir(), "server.jar")
	err := p.download(t.Context(), index.URL+"/mojang/data/v1/objects/abc/server.jar", dest, nil, nil, nil)
	var se *httpStatusError
	if !errors.As(err, &se) || se.status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Fatal("dest must not exist after a failed download")
	}
}
