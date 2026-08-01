package proxy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Bounds one external dial or echo roundtrip
const probeTimeout = 3 * time.Second

// Bounds the local bind precheck
const localProbeTimeout = 500 * time.Millisecond

// Throttles failed public detection retries
const publicRetryDelay = time.Minute

// Background cache refresh cadence
const addressRefreshInterval = 10 * time.Minute

// Echo services answering with the caller's address
var publicIPEndpoints = []string{
	"https://api.ipify.org",
	"https://checkip.amazonaws.com",
	"https://icanhazip.com",
}

// Path prefix the http lane reflects for probes
const echoPathPrefix = "/.discopanel/echo/"

// First wan verdict waits for sockets to bind
const wanVerdictDelay = 10 * time.Second

// One address the panel might be reached on
type AddressCandidate struct {
	IP        string
	Domain    string
	Source    v1.AddressSource
	Checked   bool
	Reachable bool
	Confirmed bool
}

// Probe result for one bound host port
type PortProbe struct {
	Port      int
	Transport v1.NetworkTransport
	Checked   bool
	Reachable bool
	Confirmed bool
	Detail    string
	Note      v1.ProbeNote
}

// Asks internet echo services for the router address
func detectPublicIPv4(ctx context.Context) (string, bool) {
	client := &http.Client{Timeout: probeTimeout + time.Second}
	for _, endpoint := range publicIPEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if rerr != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip == nil || ip.To4() == nil {
			continue
		}
		return ip.To4().String(), true
	}
	return "", false
}

// Refreshes the public address cache, never under the lock
func (m *Manager) RefreshAddresses(ctx context.Context) {
	m.mu.Lock()
	if time.Since(m.publicTried) < publicRetryDelay {
		m.mu.Unlock()
		return
	}
	m.publicTried = time.Now()
	m.mu.Unlock()

	ip, ok := detectPublicIPv4(ctx)

	m.mu.Lock()
	if ok {
		m.publicIP = ip
		m.publicAt = time.Now()
	} else if time.Since(m.publicAt) > publicIPTTL {
		m.publicIP = ""
	}
	m.mu.Unlock()
}

// Keeps the address cache warm for the process lifetime
func (m *Manager) startAddressRefresh() {
	m.refreshOnce.Do(func() {
		go func() {
			m.RefreshAddresses(context.Background())
			time.Sleep(wanVerdictDelay)
			m.refreshWANVerdict(context.Background())
			ticker := time.NewTicker(addressRefreshInterval)
			defer ticker.Stop()
			for range ticker.C {
				m.RefreshAddresses(context.Background())
				m.refreshWANVerdict(context.Background())
			}
		}()
	})
}

// Wan address the verdict applies to right now
func (m *Manager) wanTargetLocked() string {
	if ip := m.overrideIPLocked(); ip != "" {
		return ip
	}
	return m.publicIP
}

// Refreshes the wan verdict through one echo sweep
func (m *Manager) refreshWANVerdict(ctx context.Context) {
	m.mu.Lock()
	target := m.wanTargetLocked()
	m.mu.Unlock()
	if target == "" {
		return
	}
	probes := m.ProbeReachability(ctx, target)
	m.RecordWANVerdict(target, probes)
}

// Stores a wan probe verdict for automatic resolution
func (m *Manager) RecordWANVerdict(ip string, probes []PortProbe) {
	var checked, reachable, confirmed bool
	for _, p := range probes {
		checked = checked || p.Checked
		reachable = reachable || p.Reachable
		confirmed = confirmed || p.Confirmed
	}
	if !checked {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ip == "" || ip != m.wanTargetLocked() {
		return
	}
	m.wanIP = ip
	m.wanChecked = true
	m.wanReachable = reachable
	m.wanConfirmed = confirmed
}

// Candidate addresses ordered most viable first
func (m *Manager) AddressCandidates(ctx context.Context) []AddressCandidate {
	m.mu.Lock()
	cold := m.publicIP == "" && m.overrideIPLocked() == ""
	m.mu.Unlock()
	if cold {
		m.RefreshAddresses(ctx)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var out []AddressCandidate
	seen := make(map[string]bool)
	add := func(ip string, source v1.AddressSource) {
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		c := AddressCandidate{IP: ip, Domain: sslipDomain(ip), Source: source}
		// Wan verdict rides on its matching candidate
		if ip == m.wanIP && m.wanChecked {
			c.Checked = true
			c.Reachable = m.wanReachable
			c.Confirmed = m.wanConfirmed
		}
		out = append(out, c)
	}

	add(m.overrideIPLocked(), v1.AddressSource_ADDRESS_SOURCE_PUBLIC)
	if time.Since(m.publicAt) <= publicIPTTL {
		add(m.publicIP, v1.AddressSource_ADDRESS_SOURCE_PUBLIC)
	}
	if ip, ok := m.lanIPLocked(); ok {
		add(ip, v1.AddressSource_ADDRESS_SOURCE_LAN)
	}
	return out
}

// One probe target snapshotted from the registry
type probeTarget struct {
	port      int
	transport v1.NetworkTransport
	echo      bool
	detail    string
}

// Probes every bound host port through one address
func (m *Manager) ProbeReachability(ctx context.Context, ip string) []PortProbe {
	// Snapshot targets, dials never run under the lock
	m.mu.Lock()
	reservations, err := m.reservationsLocked(ctx)
	if err != nil {
		reservations = nil
	}
	listenerUp := make(map[int]bool, len(m.tcpSockets))
	for port, sock := range m.tcpSockets {
		listenerUp[port] = sock.IsRunning()
	}
	m.mu.Unlock()

	targets := make(map[string]probeTarget)
	add := func(t probeTarget) {
		key := fmt.Sprintf("%d/%d", t.port, t.transport)
		if have, ok := targets[key]; !ok || (!have.echo && t.echo) {
			targets[key] = t
		}
	}
	for _, r := range reservations {
		switch r.Kind {
		case kindSocket:
			add(probeTarget{port: r.Port, transport: r.Transport, echo: listenerUp[r.Port], detail: r.Detail})
		case kindExclusive:
			// Panel answers echoes just like listener sockets
			add(probeTarget{port: r.Port, transport: r.Transport, echo: r.OwnerKind == OwnerPanel, detail: r.Detail})
		}
	}

	ordered := make([]probeTarget, 0, len(targets))
	for _, t := range targets {
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].port < ordered[j].port })

	// Specific binds never answer on loopback
	localHosts := []string{"127.0.0.1"}
	if lan := m.LanIP(); lan != "" {
		localHosts = append(localHosts, lan)
	}

	results := make([]PortProbe, len(ordered))
	var wg sync.WaitGroup
	for i, target := range ordered {
		wg.Add(1)
		go func(i int, t probeTarget) {
			defer wg.Done()
			results[i] = probeOne(ctx, ip, localHosts, t)
		}(i, target)
	}
	wg.Wait()
	return results
}

// Runs local precheck then the external roundtrip
func probeOne(ctx context.Context, ip string, localHosts []string, t probeTarget) PortProbe {
	probe := PortProbe{Port: t.port, Transport: t.transport, Detail: t.detail}

	if t.transport == v1.NetworkTransport_NETWORK_TRANSPORT_UDP {
		probe.Note = v1.ProbeNote_PROBE_NOTE_UNPROBEABLE
		return probe
	}

	// Closed local ports prove nothing about the router
	bound := false
	for _, host := range localHosts {
		local := net.JoinHostPort(host, fmt.Sprintf("%d", t.port))
		if conn, err := net.DialTimeout("tcp", local, localProbeTimeout); err == nil {
			conn.Close()
			bound = true
			break
		}
	}
	if !bound {
		probe.Note = v1.ProbeNote_PROBE_NOTE_UNBOUND
		return probe
	}

	probe.Checked = true
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", t.port))
	if t.echo {
		reachable, confirmed := echoProbe(ctx, addr)
		// Tls fronted sockets still echo through a handshake
		if reachable && !confirmed && tlsEchoProbe(ctx, addr) {
			confirmed = true
		}
		probe.Reachable = reachable
		probe.Confirmed = confirmed
		if reachable && !confirmed {
			probe.Note = v1.ProbeNote_PROBE_NOTE_INTERCEPTED
		}
		return probe
	}

	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return probe
	}
	conn.Close()
	probe.Reachable = true
	return probe
}

// Proves a roundtrip lands on this panel's own socket
func echoProbe(ctx context.Context, addr string) (bool, bool) {
	nonce, ok := echoNonce()
	if !ok {
		return false, false
	}
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, false
	}
	defer conn.Close()
	return true, echoRoundtrip(conn, nonce)
}

// Echo roundtrip through a tls handshake
func tlsEchoProbe(ctx context.Context, addr string) bool {
	nonce, ok := echoNonce()
	if !ok {
		return false
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: probeTimeout},
		Config:    &tls.Config{InsecureSkipVerify: true},
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer conn.Close()
	return echoRoundtrip(conn, nonce)
}

// Random nonce for one echo attempt
func echoNonce() (string, bool) {
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", false
	}
	return hex.EncodeToString(nonceBytes), true
}

// Sends the echo request and checks the reflection
func echoRoundtrip(conn net.Conn, nonce string) bool {
	conn.SetDeadline(time.Now().Add(probeTimeout))
	fmt.Fprintf(conn, "GET %s%s HTTP/1.1\r\nHost: echo\r\nConnection: close\r\n\r\n", echoPathPrefix, nonce)
	body, _ := io.ReadAll(io.LimitReader(conn, 4096))
	return strings.Contains(string(body), nonce)
}

// Ports a public authority validates through
var acmeProbePorts = []int{80, 443}

// Probes the literal acme challenge path for a domain
func (m *Manager) ProbeChallengePath(ctx context.Context, domain string) (string, []PortProbe, bool, error) {
	ip, err := ResolveProbeTarget(ctx, domain)
	if err != nil {
		return "", nil, false, err
	}
	probes := make([]PortProbe, len(acmeProbePorts))
	var wg sync.WaitGroup
	for i, port := range acmeProbePorts {
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			probes[i] = challengeProbe(ctx, ip, port)
		}(i, port)
	}
	wg.Wait()
	ok := false
	for _, p := range probes {
		ok = ok || p.Confirmed
	}
	return ip, probes, ok, nil
}

// One validation port roundtrip, plain then tls
func challengeProbe(ctx context.Context, ip string, port int) PortProbe {
	probe := PortProbe{
		Port:      port,
		Transport: v1.NetworkTransport_NETWORK_TRANSPORT_TCP,
		Checked:   true,
		Detail:    "authority validation port",
	}
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	reachable, confirmed := echoProbe(ctx, addr)
	if reachable && !confirmed && tlsEchoProbe(ctx, addr) {
		confirmed = true
	}
	probe.Reachable = reachable
	probe.Confirmed = confirmed
	if reachable && !confirmed {
		probe.Note = v1.ProbeNote_PROBE_NOTE_INTERCEPTED
	}
	return probe
}

// Answers a reachability echo request when addressed
func HandleEcho(w http.ResponseWriter, r *http.Request) bool {
	nonce, ok := strings.CutPrefix(r.URL.Path, echoPathPrefix)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Discopanel-Echo", "1")
	fmt.Fprint(w, nonce)
	return true
}

// Resolves a probe target into one usable ipv4
func ResolveProbeTarget(ctx context.Context, target string) (string, error) {
	if ip := net.ParseIP(target); ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("only ipv4 targets can be probed")
		}
		return ip.To4().String(), nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, target)
	if err != nil {
		return "", fmt.Errorf("%s does not resolve yet", target)
	}
	for _, addr := range addrs {
		if v4 := addr.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("%s has no ipv4 address", target)
}
