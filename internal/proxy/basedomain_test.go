package proxy

import (
	"testing"
	"time"

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

	// Override beats a detected public address
	auto.publicIP = "203.0.113.7"
	auto.publicAt = time.Now()
	if got, _ := auto.EffectiveBaseDomain(); got != base {
		t.Fatalf("override lost to public ip, got %q want %q", got, base)
	}

	// Unconfirmed wan never wins automatic
	auto.appCfg.Proxy.PublicIp = ""
	if got, _ := auto.EffectiveBaseDomain(); got == "203-0-113-7.sslip.io" {
		t.Fatalf("unconfirmed public ip preferred, got %q", got)
	}

	// Echo confirmed wan wins automatic
	auto.wanIP = "203.0.113.7"
	auto.wanChecked = true
	auto.wanConfirmed = true
	if got, _ := auto.EffectiveBaseDomain(); got != "203-0-113-7.sslip.io" {
		t.Fatalf("confirmed public ip not preferred, got %q", got)
	}

	// Verdict for a stale address never carries over
	auto.publicIP = "198.51.100.9"
	if got, _ := auto.EffectiveBaseDomain(); got == "198-51-100-9.sslip.io" {
		t.Fatalf("verdict leaked onto a fresh address, got %q", got)
	}
}
