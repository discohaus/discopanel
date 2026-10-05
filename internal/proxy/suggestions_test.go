package proxy

import (
	"slices"
	"testing"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestInstantBase(t *testing.T) {
	if got := instantBase("sslip.io", "192.168.1.5"); got != "192-168-1-5.sslip.io" {
		t.Fatalf("unexpected base %q", got)
	}
	if !hostnamePattern.MatchString("survival." + instantBase("sslip.io", "10.0.0.2")) {
		t.Fatal("derived base must pass the hostname pattern")
	}
	if got := instantBase("sslip.io", ""); got != "" {
		t.Fatalf("empty ip must derive nothing, got %q", got)
	}
}

func TestNormalizeHostnames(t *testing.T) {
	got, err := NormalizeHostnames([]string{" MC.Example.Com ", "mc.example.com", "", "a.sslip.io"})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	// Sorted output proves order carries no meaning
	if !slices.Equal(got, []string{"a.sslip.io", "mc.example.com"}) {
		t.Fatalf("unexpected hostnames %v", got)
	}
	if _, err := NormalizeHostnames([]string{"bad_name!"}); err == nil {
		t.Fatal("invalid hostname must error")
	}
}

func TestRoutedHostnames(t *testing.T) {
	fallback := []string{"smp.example.com", "smp.10-0-0-2.sslip.io"}
	mc := &v1.NetworkPort{Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT}
	if got := routedHostnames(mc, fallback); !slices.Equal(got, fallback) {
		t.Fatalf("fallback not inherited, got %v", got)
	}
	mc.Hostnames = []string{"Map.Example.Com", "map.example.com"}
	if got := routedHostnames(mc, fallback); !slices.Equal(got, []string{"map.example.com"}) {
		t.Fatalf("override not deduped, got %v", got)
	}
	if got := routedHostnames(&v1.NetworkPort{Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT}, nil); got != nil {
		t.Fatalf("minecraft without hostname must skip, got %v", got)
	}
	http := &v1.NetworkPort{Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP}
	if got := routedHostnames(http, nil); got != nil {
		t.Fatalf("http without the flag must stay dark, got %v", got)
	}
	http.CatchAll = true
	if got := routedHostnames(http, nil); !slices.Equal(got, []string{""}) {
		t.Fatalf("flagged http must catch all, got %v", got)
	}
	http.Hostnames = []string{"map.example.com"}
	if got := routedHostnames(http, nil); !slices.Equal(got, []string{"map.example.com", ""}) {
		t.Fatalf("names and catch all must combine, got %v", got)
	}
}

func TestEffectiveBaseURL(t *testing.T) {
	m := &Manager{
		baseURL:    "mc.example.com",
		detectedIP: "192.168.1.5",
		detectedAt: time.Now(),
	}
	if got := m.EffectiveBaseURL(); got != "mc.example.com" {
		t.Fatalf("saved base must win, got %q", got)
	}

	// Unset base falls back to the detected wildcard name
	m.baseURL = ""
	if got := m.EffectiveBaseURL(); got != "192-168-1-5.sslip.io" {
		t.Fatalf("fallback base wrong, got %q", got)
	}
	if !hostnamePattern.MatchString(m.EffectiveBaseURL()) {
		t.Fatal("fallback base must pass the hostname pattern")
	}
}

func TestPanelHostname(t *testing.T) {
	m := &Manager{panelNames: []string{"panel.example.com", "10-0-0-2.sslip.io"}}
	if got := m.PanelHostname(); got != "panel.example.com" {
		t.Fatalf("first panel name must win, got %q", got)
	}
}
