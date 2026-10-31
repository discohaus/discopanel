package modupdates

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Downloader trusting the test server's certificate and host
func testDownloader(t *testing.T, srv *httptest.Server, extra ...string) *Downloader {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	hosts := append([]string{strings.ToLower(u.Host)}, extra...)
	return newDownloader("discopanel-test", srv.Client(), func() []string { return hosts })
}

func TestDownloaderFetchesVerifiedFiles(t *testing.T) {
	var ua string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		switch r.URL.Path {
		case "/good.jar", "/big.jar":
			fmt.Fprint(w, "jar body")
		case "/huge.jar":
			w.Write(make([]byte, sizeSlack+100))
		case "/hop":
			http.Redirect(w, r, "/good.jar", http.StatusFound)
		case "/away":
			http.Redirect(w, r, "https://evil.example/x.jar", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d := testDownloader(t, srv)
	sum := sum512("jar body")

	cases := []struct {
		name, path, sum string
		size            int64
		ok              bool
	}{
		{"verified", "/good.jar", sum, 8, true},
		{"same host redirect", "/hop", sum, 0, true},
		{"cross host redirect", "/away", sum, 0, false},
		{"checksum mismatch", "/good.jar", sum512("other"), 0, false},
		{"size mismatch", "/good.jar", sum, 9, false},
		{"over the cap", "/huge.jar", sum, 10, false},
		{"not found", "/missing.jar", sum, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "x.jar")
			err := d.Fetch(t.Context(), srv.URL+tc.path, dest, "sha512", tc.sum, tc.size)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				if _, statErr := os.Stat(dest); statErr == nil {
					t.Error("partial file left behind")
				}
				if strings.Contains(err.Error(), sum) {
					t.Errorf("error echoes the received digest: %v", err)
				}
			}
		})
	}
	if ua != "discopanel-test" {
		t.Errorf("user agent %q", ua)
	}
}

func TestDownloaderRefusesUnknownHostsAndPlainHTTP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the server")
	}))
	defer srv.Close()
	d := newDownloader("ua", srv.Client(), func() []string { return downloadHosts })
	for _, raw := range []string{srv.URL + "/x.jar", "http://cdn.modrinth.com/x.jar", "file:///etc/passwd", "https://cdn.modrinth.com.evil.example/x.jar"} {
		if err := d.Fetch(t.Context(), raw, filepath.Join(t.TempDir(), "x"), "sha512", sum512("x"), 0); err == nil {
			t.Errorf("%s fetched", raw)
		}
	}
	if err := d.Fetch(t.Context(), "https://cdn.modrinth.com/x.jar", filepath.Join(t.TempDir(), "x"), "crc32", "00", 0); err == nil {
		t.Error("fetched without a usable checksum")
	}
}
