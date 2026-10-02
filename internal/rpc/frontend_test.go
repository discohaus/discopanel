package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/discohaus/discopanel/pkg/logger"
)

func TestFrontendHandlerAndHealth(t *testing.T) {
	mockFS := fstest.MapFS{
		"index.html":                &fstest.MapFile{Data: []byte("<!doctype html><html><body>DiscoPanel</body></html>")},
		"service-worker.js":         &fstest.MapFile{Data: []byte("// sw content")},
		"manifest.webmanifest":      &fstest.MapFile{Data: []byte(`{"name":"DiscoPanel"}`)},
		"_app/immutable/chunk.1.js": &fstest.MapFile{Data: []byte("console.log('immutable');")},
		"favicon.svg":               &fstest.MapFile{Data: []byte("<svg></svg>")},
	}

	server := &Server{
		log: logger.New(),
	}
	handler := server.createFrontendHandler(http.FS(mockFS))

	tests := []struct {
		name               string
		path               string
		expectedStatus     int
		expectedBodySubstr string
	}{
		{
			name:               "Root path serves index.html with no-cache",
			path:               "/",
			expectedStatus:     http.StatusOK,
			expectedBodySubstr: "DiscoPanel",
		},
		{
			name:               "service-worker.js with no-cache",
			path:               "/service-worker.js",
			expectedStatus:     http.StatusOK,
			expectedBodySubstr: "sw content",
		},
		{
			name:               "manifest.webmanifest with no-cache",
			path:               "/manifest.webmanifest",
			expectedStatus:     http.StatusOK,
			expectedBodySubstr: "DiscoPanel",
		},
		{
			name:               "Frontend assets are not cached",
			path:               "/_app/immutable/chunk.1.js",
			expectedStatus:     http.StatusOK,
			expectedBodySubstr: "immutable",
		},
		{
			name:               "Client SPA route falls back to index.html",
			path:               "/servers/srv-12345/settings",
			expectedStatus:     http.StatusOK,
			expectedBodySubstr: "DiscoPanel",
		},
		{
			name:               "Unmatched /api/ path returns 404 instead of SPA fallback",
			path:               "/api/unknown-endpoint",
			expectedStatus:     http.StatusNotFound,
			expectedBodySubstr: "404 page not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d for %s", tc.expectedStatus, w.Code, tc.path)
			}

			if tc.expectedBodySubstr != "" && !strings.Contains(w.Body.String(), tc.expectedBodySubstr) {
				t.Errorf("expected body to contain %q, got %q", tc.expectedBodySubstr, w.Body.String())
			}
		})
	}
}

// Browsers only honour the web app manifest when it is served as JSON.
// A text/plain manifest gets installed as a plain shortcut with an address
// bar instead of a standalone PWA window.
func TestFrontendHandlerContentTypes(t *testing.T) {
	mockFS := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<!doctype html>")},
		"service-worker.js":    &fstest.MapFile{Data: []byte("// sw")},
		"manifest.webmanifest": &fstest.MapFile{Data: []byte(`{"name":"DiscoPanel"}`)},
		"app.css":              &fstest.MapFile{Data: []byte("body{}")},
		"favicon.svg":          &fstest.MapFile{Data: []byte("<svg></svg>")},
	}

	server := &Server{log: logger.New()}
	handler := server.createFrontendHandler(http.FS(mockFS))

	tests := []struct {
		path            string
		expectedType    string
		expectedSWAllow string
	}{
		{path: "/manifest.webmanifest", expectedType: "application/manifest+json"},
		{path: "/service-worker.js", expectedType: "text/javascript; charset=utf-8", expectedSWAllow: "/"},
		{path: "/app.css", expectedType: "text/css; charset=utf-8"},
		{path: "/favicon.svg", expectedType: "image/svg+xml"},
		{path: "/", expectedType: "text/html; charset=utf-8"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if got := w.Header().Get("Content-Type"); got != tc.expectedType {
				t.Errorf("expected Content-Type %q, got %q", tc.expectedType, got)
			}

			if got := w.Header().Get("Service-Worker-Allowed"); got != tc.expectedSWAllow {
				t.Errorf("expected Service-Worker-Allowed %q, got %q", tc.expectedSWAllow, got)
			}
		})
	}
}
