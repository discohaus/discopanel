package diagnostics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestModrinthProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   v1.DiagnosticSeverity
	}{
		{"loaders", http.StatusOK, `[{"name":"fabric"}]`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_PASS},
		{"documentation page", http.StatusOK, `<html>Modrinth docs</html>`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN},
		{"root metadata", http.StatusOK, `{"version":"2.7.0"}`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN},
		{"empty list", http.StatusOK, `[]`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN},
		{"missing loader name", http.StatusOK, `[{}]`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN},
		{"index error", http.StatusBadGateway, `{"message":"modrinth supplied a redirect target outside its known hosts"}`, v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{cfg: &config.Config{}, http: &http.Client{Transport: probeTransport(func(req *http.Request) (*http.Response, error) {
				if want := modrinth.BaseURL() + "/tag/loader"; req.URL.String() != want {
					t.Fatalf("probe URL = %q, want %q", req.URL, want)
				}
				if req.Method != http.MethodGet || req.Header.Get("Accept") != "application/json" {
					t.Fatalf("unexpected probe: %s, Accept: %q", req.Method, req.Header.Get("Accept"))
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			})}}
			c := &check{facts: make(map[string]string)}
			r.checkModrinth(context.Background(), c)
			if c.severity != tc.want {
				t.Fatalf("severity = %s, want %s: %s", c.severity, tc.want, c.summary)
			}
			if c.facts["url"] != hub.Modrinth()+"/v2/tag/loader" {
				t.Fatalf("reported URL does not match the probe: %q", c.facts["url"])
			}
			if tc.status == http.StatusBadGateway && (len(c.detail) != 1 || !strings.Contains(c.detail[0], "redirect target")) {
				t.Fatalf("index error detail was lost: %v", c.detail)
			}
		})
	}
}
