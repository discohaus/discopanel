package proxy

import (
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestSslipDomain(t *testing.T) {
	if got := sslipDomain("192.168.1.5"); got != "192-168-1-5.sslip.io" {
		t.Fatalf("unexpected domain %q", got)
	}
	if !hostnamePattern.MatchString("survival." + sslipDomain("10.0.0.2")) {
		t.Fatal("derived domain must pass the hostname pattern")
	}
}

func TestDetectOutboundIPv4Override(t *testing.T) {
	if ip, ok := detectOutboundIPv4("203.0.113.7"); !ok || ip != "203.0.113.7" {
		t.Fatalf("override not honored, got %q %v", ip, ok)
	}
	if _, ok := detectOutboundIPv4("not-an-ip"); ok {
		t.Fatal("garbage override must fail")
	}
	if _, ok := detectOutboundIPv4("::1"); ok {
		t.Fatal("ipv6 override must fail")
	}
}

func TestEffectiveBaseDomain(t *testing.T) {
	custom := &Manager{baseURL: "mc.example.com"}
	if base, src := custom.EffectiveBaseDomain(); base != "mc.example.com" || src != v1.BaseUrlSource_BASE_URL_SOURCE_CUSTOM {
		t.Fatalf("custom domain not honored, got %q %v", base, src)
	}

	auto := &Manager{appCfg: &config.Config{Proxy: config.ProxyConfig{PublicIp: "198.51.100.4"}}}
	base, src := auto.EffectiveBaseDomain()
	if base != "198-51-100-4.sslip.io" || src != v1.BaseUrlSource_BASE_URL_SOURCE_AUTO {
		t.Fatalf("instant domain not derived, got %q %v", base, src)
	}

	// Cache serves the second call without re-detection
	auto.appCfg.Proxy.PublicIp = "192.0.2.9"
	if cached, _ := auto.EffectiveBaseDomain(); cached != base {
		t.Fatalf("cache miss, got %q want %q", cached, base)
	}
}
