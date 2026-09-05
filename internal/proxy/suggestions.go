package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Detection cache lifetime, addresses move on dhcp
const ipCacheTTL = 5 * time.Minute

// Router addresses move rarely, cache generously
const publicIPTTL = 30 * time.Minute

// Throttles failed public detection retries
const publicRetryDelay = time.Minute

// Wildcard dns suffix backing the auto base domain
const defaultBaseSuffix = "sslip.io"

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

// Reads the default gateway from the kernel route table
func detectGatewayIPv4() (string, bool) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		raw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil || raw == 0 {
			continue
		}
		// Route table stores addresses little endian
		ip := net.IPv4(byte(raw), byte(raw>>8), byte(raw>>16), byte(raw>>24))
		return ip.String(), true
	}
	return "", false
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

// Saved base domain snapshot
func (m *Manager) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// Saved base domain else a wildcard dns fallback
// Fallback derives live so address changes never stale
func (m *Manager) EffectiveBaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseURL != "" {
		return m.baseURL
	}
	if ip, ok := m.lanIPLocked(); ok {
		return instantBase(defaultBaseSuffix, ip)
	}
	return ""
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

// Snapshot of detected lan public and gateway addresses
func (m *Manager) NetworkAddresses() (string, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lan, _ := m.lanIPLocked()
	public := m.publicIPLocked()
	if m.gatewayIP == "" || time.Since(m.gatewayAt) > ipCacheTTL {
		if ip, ok := detectGatewayIPv4(); ok {
			m.gatewayIP = ip
			m.gatewayAt = time.Now()
		}
	}
	return lan, public, m.gatewayIP
}

// Panel hostnames snapshot for reservation claims
func (m *Manager) PanelHostnames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.panelNames...)
}

// Alias host source, any panel name works alike
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
