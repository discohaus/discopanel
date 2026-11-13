package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Webhook URLs are user supplied, so deliveries must not be usable to reach
// the panel host itself or cloud metadata services (SSRF). LAN addresses stay
// allowed because self-hosters commonly post to services on their own network.
//
// The check runs on the resolved address of every connection the client
// makes, so DNS rebinding and redirects to a blocked address are caught too.

var errBlockedAddress = errors.New("webhook target address is not allowed (loopback, link-local and metadata addresses are blocked)")

// Deliveries dial the target directly. Going through HTTP(S)_PROXY would
// make the dial check see the proxy's address instead of the target's, so
// targets would go unchecked (or every webhook would fail when the proxy
// is on localhost).
var guardedTransport = &http.Transport{
	Proxy: nil,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   dialControl,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          20,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("webhook dial: %w", err)
	}
	if blockedAddr(addr) {
		return errBlockedAddress
	}
	return nil
}

// blockedAddr reports addresses a webhook may never connect to.
func blockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() ||
		a.IsLinkLocalUnicast() || // 169.254.0.0/16 incl. cloud metadata, fe80::/10
		a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() ||
		a.IsMulticast() ||
		a.IsUnspecified() ||
		a == netip.MustParseAddr("fd00:ec2::254") // AWS IPv6 metadata
}

// checkURL rejects malformed or non-http(s) targets before any request.
func checkURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook url must use http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("webhook url has no host")
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return errBlockedAddress
	}
	return nil
}
