package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Detection cache lifetime, addresses move on dhcp
const ipCacheTTL = 5 * time.Minute

// Router addresses move rarely, cache generously
const publicIPTTL = 30 * time.Minute

// Throttles failed public detection retries
const publicRetryDelay = time.Minute

// Wildcard dns providers resolving ip-shaped names
var instantSuffixes = []string{"sslip.io", "traefik.me"}

// Echo services answering with the caller's address
var publicIPEndpoints = []string{
	"https://api.ipify.org",
	"https://checkip.amazonaws.com",
	"https://icanhazip.com",
}

// Asks internet echo services for the router address
func detectPublicIPv4(ctx context.Context) (string, bool) {
	client := &http.Client{Timeout: 3 * time.Second}
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

// Turns an address into a provider base name
func instantBase(suffix, ip string) string {
	if suffix == "" || ip == "" {
		return ""
	}
	return strings.ReplaceAll(ip, ".", "-") + "." + suffix
}

// Reachability a base name implies for players
func hostnameScope(name string) v1.HostnameScope {
	for _, suffix := range instantSuffixes {
		if !strings.HasSuffix(name, "."+suffix) {
			continue
		}
		trimmed := strings.TrimSuffix(name, "."+suffix)
		if i := strings.LastIndexByte(trimmed, '.'); i >= 0 {
			trimmed = trimmed[i+1:]
		}
		ip := net.ParseIP(strings.ReplaceAll(trimmed, "-", "."))
		if ip == nil {
			continue
		}
		if ip.IsPrivate() || ip.IsLoopback() {
			return v1.HostnameScope_HOSTNAME_SCOPE_LAN
		}
		return v1.HostnameScope_HOSTNAME_SCOPE_PUBLIC
	}
	return v1.HostnameScope_HOSTNAME_SCOPE_PUBLIC
}

// Finds the address other machines reach this host on
func detectOutboundIPv4() (string, bool) {
	// Route lookup only, no packet leaves the host
	if conn, err := net.Dial("udp4", "1.1.1.1:53"); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP.To4() != nil {
			return addr.IP.To4().String(), true
		}
	}

	// Interface scan covers hosts without a default route
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || !ip.IsGlobalUnicast() {
			continue
		}
		return ip.String(), true
	}
	return "", false
}

// Computes hostname suggestions for one optional label
func (m *Manager) HostnameSuggestions(label string) []*v1.HostnameSuggestion {
	label = NormalizeHostname(label)
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []*v1.HostnameSuggestion
	seen := make(map[string]bool)
	add := func(base string, scope v1.HostnameScope) {
		name := base
		if label != "" {
			name = label + "." + base
		}
		if name == "" || seen[name] || !ValidHostname(name) {
			return
		}
		seen[name] = true
		out = append(out, &v1.HostnameSuggestion{Hostname: name, Scope: scope})
	}

	// Configured names first, they are deliberate
	for _, name := range m.panelNames {
		add(name, hostnameScope(name))
	}
	if ip, ok := m.lanIPLocked(); ok {
		for _, suffix := range instantSuffixes {
			add(instantBase(suffix, ip), v1.HostnameScope_HOSTNAME_SCOPE_LAN)
		}
	}
	if ip := m.publicIPLocked(); ip != "" {
		for _, suffix := range instantSuffixes {
			add(instantBase(suffix, ip), v1.HostnameScope_HOSTNAME_SCOPE_PUBLIC)
		}
	}
	return out
}

// Configured override else the cached detected address
func (m *Manager) publicIPLocked() string {
	if m.appCfg != nil {
		if ip := net.ParseIP(m.appCfg.Proxy.PublicIp); ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	// Stale cache kicks a refresh without blocking
	if time.Since(m.publicAt) > publicIPTTL && time.Since(m.publicTried) > publicRetryDelay {
		m.publicTried = time.Now()
		go m.refreshPublicIP()
	}
	return m.publicIP
}

// Echo lookup runs off the lock and fills the cache
func (m *Manager) refreshPublicIP() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ip, ok := detectPublicIPv4(ctx)
	if !ok {
		return
	}
	m.mu.Lock()
	m.publicIP = ip
	m.publicAt = time.Now()
	m.mu.Unlock()
}

// Cached lan address, detection is cheap and local
func (m *Manager) lanIPLocked() (string, bool) {
	if m.detectedIP == "" || time.Since(m.detectedAt) > ipCacheTTL {
		ip, ok := detectOutboundIPv4()
		if !ok {
			return "", false
		}
		m.detectedIP = ip
		m.detectedAt = time.Now()
	}
	return m.detectedIP, true
}

// Panel hostnames snapshot for reservation claims
func (m *Manager) PanelHostnames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.panelNames...)
}

// Single url host, any panel name works alike
func (m *Manager) PanelHostname() string {
	m.mu.Lock()
	names := m.panelNames
	m.mu.Unlock()
	if len(names) > 0 {
		return names[0]
	}
	if ip, ok := detectOutboundIPv4(); ok {
		return ip
	}
	if name, err := os.Hostname(); err == nil {
		return name
	}
	return ""
}
