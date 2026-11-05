package webhook

import (
	"net/netip"
	"testing"
)

func TestBlockedAddr(t *testing.T) {
	blocked := []string{"127.0.0.1", "127.8.9.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "::ffff:127.0.0.1", "fd00:ec2::254"}
	allowed := []string{"192.168.1.20", "10.0.0.5", "172.17.0.1", "8.8.8.8", "2606:4700::1111"}
	for _, s := range blocked {
		if !blockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range allowed {
		if blockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestCheckURL(t *testing.T) {
	bad := []string{"file:///etc/passwd", "gopher://x", "http://", "http://localhost:8080/x", "http://LOCALHOST/x", "::nonsense"}
	good := []string{"https://discord.com/api/webhooks/1/abc", "http://192.168.1.4:8123/api/webhook/x"}
	for _, u := range bad {
		if checkURL(u) == nil {
			t.Errorf("%q should be rejected", u)
		}
	}
	for _, u := range good {
		if err := checkURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
}
