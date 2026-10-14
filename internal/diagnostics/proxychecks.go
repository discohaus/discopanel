package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Certificate expiry warning window
const certWarnWindow = 14 * 24 * time.Hour

// Summarizes the proxy state for the report
func (r *Runner) checkProxyConfig(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	enabled := r.proxy.Enabled()
	c.fact("enabled", enabled)
	c.fact("running", r.proxy.IsRunning())
	c.fact("panel_hostnames", strings.Join(r.proxy.PanelHostnames(), ", "))
	c.fact("base_url", r.proxy.BaseURL())
	c.fact("effective_base_url", r.proxy.EffectiveBaseURL())
	c.fact("catch_all", r.proxy.PanelCatchAll())
	c.fact("lobby", r.proxy.LobbyEnabled())
	c.fact("trusted_edge", r.cfg.Proxy.TrustedEdge)
	c.fact("ingress_proxy_protocol", r.cfg.Proxy.IngressProxyProtocol)
	c.fact("routes", len(r.proxy.RouteEntries()))

	if listeners, err := r.store.ListProxyListeners(ctx); err == nil {
		var rows []string
		for _, l := range listeners {
			state := "on"
			if !l.Enabled {
				state = "off"
			}
			tag := ""
			if l.IsDefault {
				tag = " default"
			}
			rows = append(rows, fmt.Sprintf("%s:%d %s%s", l.Name, l.Port, state, tag))
		}
		c.fact("listeners", strings.Join(rows, "; "))
	}

	proxied, direct := 0, 0
	if servers, err := r.store.ListServers(ctx); err == nil {
		for _, s := range servers {
			if len(s.ProxyHostnames) > 0 {
				proxied++
			} else {
				direct++
			}
		}
	}
	c.fact("servers_proxied", proxied)
	c.fact("servers_direct", direct)

	if !isLoopbackHost(r.cfg.Server.Host) && r.cfg.Server.Host != "0.0.0.0" && r.cfg.Server.Host != "::" && r.cfg.Server.Host != "" {
		c.note("Panel binds only %s, other interfaces cannot reach the UI", r.cfg.Server.Host)
	}
	if isLoopbackHost(r.cfg.Server.Host) {
		c.warn("Panel binds %s only, nothing outside this machine can reach it", r.cfg.Server.Host)
		c.fix("Set server.host to 0.0.0.0 (DISCOPANEL_SERVER_HOST).", docsConfiguration)
		return
	}
	if !enabled {
		c.info("Proxy is off, %d servers use direct ports", direct)
		return
	}
	if r.proxy.BaseURL() == "" && len(r.proxy.PanelHostnames()) == 0 {
		c.info("Proxy on with %d routed servers, no base domain set so suggestions use %s", proxied, r.proxy.EffectiveBaseURL())
		c.fix("Optional: set a base domain under Settings > Network once you own one, hostnames then derive from it.", docsProxy)
		return
	}
	c.info("Proxy on, %d servers routed by hostname, %d on direct ports", proxied, direct)
}

// Reads traffic shape counters for edge misconfigurations
func (r *Runner) checkEdge(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	snap := r.proxy.EdgeSnapshot()
	c.fact("http_requests", snap.HTTPTotal)
	c.fact("http_forwarded", snap.HTTPForwarded)
	c.fact("http_host_is_ip", snap.HTTPHostIP)
	c.fact("mc_handshakes", snap.MCTotal)
	c.fact("mc_host_is_ip", snap.MCHostIP)
	c.fact("recent_misses", len(snap.Misses))

	if snap.HTTPTotal == 0 && snap.MCTotal == 0 {
		c.info("No traffic observed since the panel started")
		return
	}

	panelNamed := len(r.proxy.PanelHostnames()) > 0 && !r.proxy.PanelCatchAll()
	var ipMissesHTTP, ipMissesMC, nameMissesMC []string
	seen := map[string]bool{}
	for _, m := range snap.Misses {
		key := m.Lane + ":" + m.Hostname
		if seen[key] {
			continue
		}
		seen[key] = true
		isIP := net.ParseIP(m.Hostname) != nil || m.Hostname == "localhost" || m.Hostname == ""
		switch {
		case m.Lane == "http" && isIP:
			ipMissesHTTP = append(ipMissesHTTP, orEmpty(m.Hostname))
		case m.Lane == "minecraft" && isIP:
			ipMissesMC = append(ipMissesMC, orEmpty(m.Hostname))
		case m.Lane == "minecraft":
			nameMissesMC = append(nameMissesMC, m.Hostname)
		}
	}
	limit := 8
	if len(snap.Misses) < limit {
		limit = len(snap.Misses)
	}
	for _, m := range snap.Misses[:limit] {
		c.note("%s %s %q from %s", m.At.Format(time.RFC3339), m.Lane, m.Hostname, m.Remote)
	}

	if snap.HTTPForwarded > 0 && !r.cfg.Proxy.TrustedEdge {
		c.warn("%d requests carried X-Forwarded headers from an upstream reverse proxy, but proxy.trusted_edge is off", snap.HTTPForwarded)
		c.fix("Set proxy.trusted_edge: true (DISCOPANEL_PROXY_TRUSTED_EDGE=true) so the panel sees real client addresses and the https scheme behind nginx, Caddy, Traefik, or a Cloudflare tunnel.", docsProxy)
	}
	if panelNamed && len(ipMissesHTTP) > 0 {
		c.warn("Browser requests arrived with an address instead of a hostname (%s) and were refused", abbreviate(ipMissesHTTP, 3))
		c.fix("The panel only answers its configured hostnames. Either open the UI by hostname, enable Catch all under Settings > Network, or make the reverse proxy pass the original Host header (nginx: proxy_set_header Host $host; Cloudflare tunnel: set the origin Host header).", docsProxy)
	}
	if len(ipMissesMC) > 0 {
		c.warn("Minecraft clients reached the proxy with an address instead of a hostname (%s)", abbreviate(ipMissesMC, 3))
		c.fix("Something in front rewrites the server address (a TCP proxy or tunnel), or players typed the IP. Give players the hostname, use plain TCP passthrough on the tunnel, or enable PROXY protocol ingress if the tunnel supports it.", docsProxy)
	}
	if len(nameMissesMC) > 0 {
		c.info("Players tried %s no server answers on: %s", plural(len(nameMissesMC), "hostname", "hostnames"), abbreviate(nameMissesMC, 4))
		c.fix("Add the hostname to the right server under its Network tab, or enable a catch all server.", docsProxy)
	}
	if c.severity == v1.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_UNSPECIFIED {
		c.pass("%d requests and %d handshakes routed cleanly", snap.HTTPTotal, snap.MCTotal)
	}
}

func orEmpty(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}

// Loads each configured certificate and checks expiry
func (r *Runner) checkTLS(ctx context.Context, c *check) {
	certs := r.cfg.Proxy.TLS.Certificates
	if len(certs) == 0 {
		c.info("No certificates configured, the panel serves plain HTTP")
		c.fix("Optional: add cert and key files under proxy.tls.certificates to serve HTTPS on the panel port, or terminate TLS on a reverse proxy in front.", docsTLS)
		return
	}
	var broken, expiring []string
	now := time.Now()
	for _, pair := range certs {
		cert, err := tls.LoadX509KeyPair(pair.CertFile, pair.KeyFile)
		if err != nil {
			broken = append(broken, pair.CertFile)
			c.note("%s: %v", pair.CertFile, err)
			continue
		}
		leaf := cert.Leaf
		if leaf == nil && len(cert.Certificate) > 0 {
			leaf, err = x509.ParseCertificate(cert.Certificate[0])
			if err != nil {
				broken = append(broken, pair.CertFile)
				c.note("%s: %v", pair.CertFile, err)
				continue
			}
		}
		names := leaf.DNSNames
		if len(names) == 0 && leaf.Subject.CommonName != "" {
			names = []string{leaf.Subject.CommonName}
		}
		left := leaf.NotAfter.Sub(now)
		switch {
		case left <= 0:
			broken = append(broken, pair.CertFile)
			c.note("%s: expired %s ago (%s)", pair.CertFile, (-left).Round(time.Hour), strings.Join(names, ", "))
		case left < certWarnWindow:
			expiring = append(expiring, pair.CertFile)
			c.note("%s: expires in %s (%s)", pair.CertFile, left.Round(time.Hour), strings.Join(names, ", "))
		default:
			c.note("%s: valid %d days (%s)", pair.CertFile, int(left.Hours()/24), strings.Join(names, ", "))
		}
	}
	c.fact("certificates", len(certs))
	switch {
	case len(broken) > 0:
		c.fail("%s unusable: %s", plural(len(broken), "certificate is", "certificates are"), abbreviate(broken, 3))
		c.fix("Renew or fix the listed files, then restart the panel. Expired or unreadable certificates make browsers refuse the panel.", docsTLS)
	case len(expiring) > 0:
		c.warn("%s within 14 days", plural(len(expiring), "certificate expires", "certificates expire"))
		c.fix("Renew them before expiry and restart the panel to load the new files.", docsTLS)
	default:
		c.pass("%s loaded and valid", plural(len(certs), "certificate", "certificates"))
	}
}
