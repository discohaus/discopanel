package proxy

import (
	"net"
	"os"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Detection cache lifetime, addresses move on dhcp
const ipCacheTTL = 5 * time.Minute

// Public address cache lifetime before falling back
const publicIPTTL = 30 * time.Minute

// Finds the address other machines reach this host on
func detectOutboundIPv4(override string) (string, bool) {
	if override != "" {
		if ip := net.ParseIP(override); ip != nil && ip.To4() != nil {
			return ip.To4().String(), true
		}
		return "", false
	}

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

// Turns an address into a resolvable sslip.io domain
func sslipDomain(ip string) string {
	return strings.ReplaceAll(ip, ".", "-") + ".sslip.io"
}

// Base domain in effect right now with its source
func (m *Manager) EffectiveBaseDomain() (string, v1.BaseUrlSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effectiveBaseDomainLocked()
}

// Effective domain lookup, caller must hold the lock
func (m *Manager) effectiveBaseDomainLocked() (string, v1.BaseUrlSource) {
	if m.baseURL != "" {
		return m.baseURL, v1.BaseUrlSource_BASE_URL_SOURCE_CUSTOM
	}
	if ip, ok := m.bestAutoIPLocked(); ok {
		return sslipDomain(ip), v1.BaseUrlSource_BASE_URL_SOURCE_AUTO
	}
	return "", v1.BaseUrlSource_BASE_URL_SOURCE_UNSPECIFIED
}

// Config override read live for tests and reloads
func (m *Manager) overrideIPLocked() string {
	if m.appCfg == nil {
		return ""
	}
	if ip := net.ParseIP(m.appCfg.Proxy.PublicIp); ip != nil && ip.To4() != nil {
		return ip.To4().String()
	}
	return ""
}

// Best instant address, override then confirmed wan then lan
func (m *Manager) bestAutoIPLocked() (string, bool) {
	if ip := m.overrideIPLocked(); ip != "" {
		return ip, true
	}
	if ip, ok := m.confirmedWANLocked(); ok {
		return ip, true
	}
	return m.lanIPLocked()
}

// Wan address only when an echo proved it works
func (m *Manager) confirmedWANLocked() (string, bool) {
	if m.publicIP == "" || time.Since(m.publicAt) > publicIPTTL {
		return "", false
	}
	if m.wanIP != m.publicIP || !m.wanConfirmed {
		return "", false
	}
	return m.publicIP, true
}

// Automatic resolution with its structured reason
func (m *Manager) AutoDomain() (string, v1.AutoDomainReason) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ip := m.overrideIPLocked(); ip != "" {
		return sslipDomain(ip), v1.AutoDomainReason_AUTO_DOMAIN_REASON_OVERRIDE
	}
	if ip, ok := m.confirmedWANLocked(); ok {
		return sslipDomain(ip), v1.AutoDomainReason_AUTO_DOMAIN_REASON_WAN_CONFIRMED
	}
	wanLive := m.publicIP != "" && time.Since(m.publicAt) <= publicIPTTL
	ip, ok := m.lanIPLocked()
	if !ok {
		return "", v1.AutoDomainReason_AUTO_DOMAIN_REASON_NO_ADDRESS
	}
	if !wanLive {
		return sslipDomain(ip), v1.AutoDomainReason_AUTO_DOMAIN_REASON_LAN_NO_WAN
	}
	if m.wanIP == m.publicIP && m.wanChecked {
		return sslipDomain(ip), v1.AutoDomainReason_AUTO_DOMAIN_REASON_LAN_WAN_UNREACHABLE
	}
	return sslipDomain(ip), v1.AutoDomainReason_AUTO_DOMAIN_REASON_LAN_WAN_UNVERIFIED
}

// Detected lan address for verdict comparisons
func (m *Manager) LanIP() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ip, _ := m.lanIPLocked()
	return ip
}

// Best reachable name for urls, never loopback
func (m *Manager) PanelHostname() string {
	if domain, _ := m.EffectiveBaseDomain(); domain != "" {
		return domain
	}
	if ip, ok := detectOutboundIPv4(""); ok {
		return ip
	}
	if name, err := os.Hostname(); err == nil {
		return name
	}
	return ""
}

// Cached lan address, detection is cheap and local
func (m *Manager) lanIPLocked() (string, bool) {
	if m.detectedIP == "" || time.Since(m.detectedAt) > ipCacheTTL {
		ip, ok := detectOutboundIPv4("")
		if !ok {
			return "", false
		}
		m.detectedIP = ip
		m.detectedAt = time.Now()
	}
	return m.detectedIP, true
}
