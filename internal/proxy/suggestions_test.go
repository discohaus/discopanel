package proxy

import (
	"slices"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestInstantBase(t *testing.T) {
	if got := instantBase("sslip.io", "192.168.1.5"); got != "192-168-1-5.sslip.io" {
		t.Fatalf("unexpected base %q", got)
	}
	if !hostnamePattern.MatchString("survival." + instantBase("sslip.io", "10.0.0.2")) {
		t.Fatal("derived base must pass the hostname pattern")
	}
	if got := instantBase("traefik.me", ""); got != "" {
		t.Fatalf("empty ip must derive nothing, got %q", got)
	}
}

func TestHostnameScope(t *testing.T) {
	lan := v1.HostnameScope_HOSTNAME_SCOPE_LAN
	public := v1.HostnameScope_HOSTNAME_SCOPE_PUBLIC
	cases := map[string]v1.HostnameScope{
		"192-168-1-5.sslip.io":     lan,
		"smp.10-0-0-2.traefik.me":  lan,
		"127-0-0-1.traefik.me":     lan,
		"198-51-100-4.sslip.io":    public,
		"mc.198-51-100-4.sslip.io": public,
		"mc.example.com":           public,
		// Dropped providers read as ordinary public names
		"10-0-0-2.nip.io": public,
	}
	for name, want := range cases {
		if got := hostnameScope(name); got != want {
			t.Fatalf("scope of %s = %v, want %v", name, got, want)
		}
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
	if got := routedHostnames(http, nil); !slices.Equal(got, []string{""}) {
		t.Fatalf("http without hostname must catch all, got %v", got)
	}
}

func TestHostnameSuggestions(t *testing.T) {
	m := &Manager{
		appCfg:     &config.Config{Proxy: config.ProxyConfig{PublicIp: "198.51.100.4"}},
		panelNames: []string{"mc.example.com"},
		detectedIP: "192.168.1.5",
		detectedAt: time.Now(),
	}

	names := func(label string) []string {
		var out []string
		for _, s := range m.HostnameSuggestions(label) {
			out = append(out, s.Hostname)
		}
		return out
	}

	bare := names("")
	if bare[0] != "mc.example.com" {
		t.Fatalf("configured name must lead, got %v", bare)
	}
	if !slices.Contains(bare, "192-168-1-5.sslip.io") || !slices.Contains(bare, "198-51-100-4.traefik.me") {
		t.Fatalf("instant bases missing, got %v", bare)
	}

	labeled := names("smp")
	if labeled[0] != "smp.mc.example.com" || !slices.Contains(labeled, "smp.192-168-1-5.sslip.io") {
		t.Fatalf("label not prefixed, got %v", labeled)
	}

	// Scopes tag lan and public addresses apart
	for _, s := range m.HostnameSuggestions("") {
		want := v1.HostnameScope_HOSTNAME_SCOPE_PUBLIC
		if s.Hostname == "192-168-1-5.sslip.io" || s.Hostname == "192-168-1-5.traefik.me" {
			want = v1.HostnameScope_HOSTNAME_SCOPE_LAN
		}
		if s.Scope != want {
			t.Fatalf("scope of %s = %v, want %v", s.Hostname, s.Scope, want)
		}
	}
}

func TestPanelHostname(t *testing.T) {
	m := &Manager{panelNames: []string{"panel.example.com", "10-0-0-2.sslip.io"}}
	if got := m.PanelHostname(); got != "panel.example.com" {
		t.Fatalf("first panel name must win, got %q", got)
	}
}
