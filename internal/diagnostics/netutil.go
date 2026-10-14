package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// Carrier grade NAT block from RFC 6598
var cgnatBlock = mustCIDR("100.64.0.0/10")

// Private and link local blocks
var privateBlocks = []*net.IPNet{
	mustCIDR("10.0.0.0/8"),
	mustCIDR("172.16.0.0/12"),
	mustCIDR("192.168.0.0/16"),
	mustCIDR("169.254.0.0/16"),
	mustCIDR("fc00::/7"),
	mustCIDR("fe80::/10"),
}

// Published Cloudflare proxy ranges, orange cloud traffic
var cloudflareBlocks = []*net.IPNet{
	mustCIDR("173.245.48.0/20"),
	mustCIDR("103.21.244.0/22"),
	mustCIDR("103.22.200.0/22"),
	mustCIDR("103.31.4.0/22"),
	mustCIDR("141.101.64.0/18"),
	mustCIDR("108.162.192.0/18"),
	mustCIDR("190.93.240.0/20"),
	mustCIDR("188.114.96.0/20"),
	mustCIDR("197.234.240.0/22"),
	mustCIDR("198.41.128.0/17"),
	mustCIDR("162.158.0.0/15"),
	mustCIDR("104.16.0.0/13"),
	mustCIDR("104.24.0.0/14"),
	mustCIDR("172.64.0.0/13"),
	mustCIDR("131.0.72.0/22"),
	mustCIDR("2400:cb00::/32"),
	mustCIDR("2606:4700::/32"),
	mustCIDR("2803:f800::/32"),
	mustCIDR("2405:b500::/32"),
	mustCIDR("2405:8100::/32"),
	mustCIDR("2a06:98c0::/29"),
	mustCIDR("2c0f:f248::/32"),
}

func mustCIDR(s string) *net.IPNet {
	_, block, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return block
}

func inBlocks(ip net.IP, blocks []*net.IPNet) bool {
	for _, b := range blocks {
		if b.Contains(ip) {
			return true
		}
	}
	return false
}

// Reports whether ip sits in the carrier grade NAT block
func isCGNAT(ip net.IP) bool {
	return ip != nil && cgnatBlock.Contains(ip)
}

// Reports whether ip is private, loopback, or link local
func isPrivateIP(ip net.IP) bool {
	return ip != nil && (ip.IsLoopback() || inBlocks(ip, privateBlocks))
}

// Reports whether ip belongs to Cloudflare's proxy ranges
func isCloudflare(ip net.IP) bool {
	return ip != nil && inBlocks(ip, cloudflareBlocks)
}

// Reports whether host names a loopback address
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Human reason behind a dial or request failure
func describeNetErr(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "DNS has no record for " + dnsErr.Name
		}
		return "DNS lookup failed for " + dnsErr.Name
	}
	var certErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certInvalid x509.CertificateInvalidError
	var recErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &certInvalid) || errors.As(err, &recErr) {
		return "TLS certificate rejected (interception proxy or wrong clock)"
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return "timed out"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return "network unreachable"
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no such host"):
		return "DNS lookup failed"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline"):
		return "timed out"
	case strings.Contains(lower, "connection refused"):
		return "connection refused"
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return "TLS certificate rejected (interception proxy or wrong clock)"
	case strings.Contains(lower, "connection reset"):
		return "connection reset"
	}
	return msg
}

// One http probe result
type probeResult struct {
	Status  int
	Header  http.Header
	Body    []byte
	Latency time.Duration
	Err     error
}

// Body caps for probes, small by default
const (
	probeBodyCap = 64 << 10
	largeBodyCap = 2 << 20
)

// Performs one request with the default body cap
func (r *Runner) probe(ctx context.Context, method, url string, headers map[string]string) probeResult {
	return r.probeLimit(ctx, method, url, headers, probeBodyCap)
}

// Performs one request, reads a bounded body, never follows far
func (r *Runner) probeLimit(ctx context.Context, method, url string, headers map[string]string, maxBody int64) probeResult {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return probeResult{Err: err}
	}
	req.Header.Set("User-Agent", r.userAgent())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	started := time.Now()
	resp, err := r.http.Do(req)
	if err != nil {
		return probeResult{Err: err, Latency: time.Since(started)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return probeResult{Status: resp.StatusCode, Header: resp.Header, Body: body, Latency: time.Since(started)}
}

// User agent every outbound probe presents
func (r *Runner) userAgent() string {
	if r.cfg.Server.UserAgent != "" {
		return r.cfg.Server.UserAgent
	}
	return "DiscoPanel/" + r.version
}

// Clock skew hinted by a Date header, zero when absent
func headerSkew(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	remote, err := http.ParseTime(h.Get("Date"))
	if err != nil {
		return 0
	}
	return now.Sub(remote)
}

// Formats a latency for summaries
func ms(d time.Duration) string {
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// Nameservers and search domains from resolv.conf
func readResolvConf() (nameservers []string, search []string) {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "nameserver":
			nameservers = append(nameservers, fields[1])
		case "search":
			search = append(search, fields[1:]...)
		}
	}
	return nameservers, search
}

// True when every nameserver is a loopback stub
func loopbackOnly(nameservers []string) bool {
	if len(nameservers) == 0 {
		return false
	}
	for _, ns := range nameservers {
		ip := net.ParseIP(ns)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// Bind probe outcomes
const (
	bindFree   = "free"
	bindInUse  = "in use"
	bindDenied = "denied"
)

// Tries to bind a port and reports why it cannot
func probeBind(host string, port int) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err == nil {
		ln.Close()
		return bindFree, nil
	}
	return classifyBindErr(err), err
}

// Maps a bind error onto a probe outcome
func classifyBindErr(err error) string {
	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, syscall.EADDRINUSE),
		strings.Contains(lower, "address already in use"),
		strings.Contains(lower, "only one usage of each socket address"):
		return bindInUse
	case errors.Is(err, syscall.EACCES),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "access permissions"):
		return bindDenied
	}
	return lower
}
