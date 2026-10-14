package diagnostics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Names every install needs to resolve: the registry, the hub, and the upstream route in use
// GitHub joins the list only while it is the release source
func (r *Runner) dnsProbeHosts() []string {
	hosts := []string{"ghcr.io", bareHost(hub.SupportHost())}
	for _, h := range hub.UpstreamHosts() {
		hosts = append(hosts, bareHost(h))
	}
	if !r.telemetryOn() {
		hosts = append(hosts, "api.github.com")
	}
	sort.Strings(hosts)
	return slices.Compact(hosts)
}

// Per name and per probe deadlines
const (
	lookupTimeout = 5 * time.Second
	clockSkewWarn = 2 * time.Minute
)

// Resolves one name under its own deadline
func lookup(ctx context.Context, name string) ([]net.IP, error) {
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lctx, name)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// Resolves the upstream names the panel depends on
func (r *Runner) checkDNS(ctx context.Context, c *check) {
	nameservers, search := readResolvConf()
	c.fact("nameservers", strings.Join(nameservers, ", "))
	c.fact("search", strings.Join(search, " "))
	c.fact("docker_dns", r.cfg.Docker.DNS)

	type result struct {
		name string
		ips  []net.IP
		err  error
	}
	dnsProbeHosts := r.dnsProbeHosts()
	results := make([]result, len(dnsProbeHosts))
	var wg sync.WaitGroup
	for i, name := range dnsProbeHosts {
		wg.Go(func() {
			ips, err := lookup(ctx, name)
			results[i] = result{name: name, ips: ips, err: err}
		})
	}
	wg.Wait()

	var failed []string
	for _, res := range results {
		if res.err != nil {
			failed = append(failed, res.name)
			c.note("%s: %s", res.name, describeNetErr(res.err))
			continue
		}
		if len(res.ips) == 0 {
			failed = append(failed, res.name)
			c.note("%s: resolved to no addresses", res.name)
			continue
		}
		c.note("%s: %s", res.name, res.ips[0])
	}

	containerized := dockerLike(detectContainer())
	switch {
	case len(failed) == len(dnsProbeHosts):
		c.fail("None of %d hostnames resolve", len(dnsProbeHosts))
		if containerized && loopbackOnly(nameservers) {
			c.fix(fmt.Sprintf("The container's resolv.conf only lists a loopback resolver (%s), which is the host's systemd-resolved stub and unreachable from inside. Add dns: [1.1.1.1] to the panel service in compose, and set docker.dns (DISCOPANEL_DOCKER_DNS) so server containers get a working resolver too.", strings.Join(nameservers, ", ")), docsTroubleshooting)
		} else {
			c.fix("Check the host resolver (/etc/resolv.conf, systemd-resolved) and outbound port 53 in the firewall. For server containers set docker.dns, for the daemon itself set dns in /etc/docker/daemon.json.", docsTroubleshooting)
		}
	case len(failed) > 0:
		c.warn("%s failed to resolve: %s", plural(len(failed), "hostname", "hostnames"), strings.Join(failed, ", "))
		c.fix("Partial failures point at a flaky or filtering resolver. Try docker.dns=1.1.1.1 or fix the upstream resolver.", docsTroubleshooting)
	default:
		c.pass("All %d hostnames resolve", len(dnsProbeHosts))
	}
}

// Confirms outbound https works and the clock is sane
func (r *Runner) checkInternet(ctx context.Context, c *check) {
	targets := []struct{ name, url string }{
		{"github", "https://api.github.com/"},
		{"aws", "https://checkip.amazonaws.com/"},
	}
	results := make([]probeResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			results[i] = r.probe(ctx, http.MethodGet, t.url, nil)
		})
	}
	wg.Wait()

	var skew time.Duration
	reachable := 0
	for i, res := range results {
		if res.Err != nil {
			c.note("%s: %s", targets[i].name, describeNetErr(res.Err))
			continue
		}
		reachable++
		c.note("%s: HTTP %d in %s", targets[i].name, res.Status, ms(res.Latency))
		if s := headerSkew(res.Header, time.Now()); s.Abs() > skew.Abs() {
			skew = s
		}
	}
	c.fact("clock_skew", skew.Round(time.Second).String())

	switch {
	case reachable == 0:
		c.fail("No outbound HTTPS, every probe failed")
		c.fix("Downloads, modpack installs, and image pulls all need outbound HTTPS. Check the firewall, HTTPS_PROXY environment, and network.dns.", docsTroubleshooting)
		return
	case reachable < len(targets):
		c.warn("%d of %d probes failed", len(targets)-reachable, len(targets))
	default:
		c.pass("Outbound HTTPS works")
	}
	if skew.Abs() > clockSkewWarn {
		c.warn("System clock is off by about %s", skew.Round(time.Second))
		c.fix("Enable time sync (timedatectl set-ntp true, or the container host's NTP). TLS validation and login tokens break with a wrong clock.", "")
	}
}

// Classifies the public address and probes the router
func (r *Runner) checkNAT(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	lan, public, gateway := r.proxy.NetworkAddresses()
	c.fact("lan_ip", lan)
	c.fact("public_ip", public)
	c.fact("gateway_ip", gateway)
	if r.cfg.Proxy.PublicIp != "" {
		c.fact("public_ip_source", "proxy.public_ip")
	} else if public != "" {
		c.fact("public_ip_source", "internet echo")
	}
	if public == "" {
		c.warn("Public address unknown, the internet echo services did not answer")
		c.fix("Without it hostname suggestions and forwarding tests are unavailable. Check network.internet, or set proxy.public_ip if you know the address.", docsProxy)
		return
	}
	publicIP := net.ParseIP(public)
	switch {
	case isCGNAT(publicIP):
		c.fail("Public address %s is carrier grade NAT (100.64.0.0/10)", public)
		c.fix("Your ISP shares one public address across customers, so forwarded ports never reach you. Ask the ISP for a real public IPv4, or route players through a tunnel: the playit module, or a VPS relay with PROXY protocol.", docsModules)
		return
	case isPrivateIP(publicIP):
		c.warn("Echo services see a private address %s, an HTTP proxy rewrites outbound traffic", public)
		c.fix("Set proxy.public_ip to your real public address.", docsProxy)
		return
	}
	if lan == public {
		c.pass("Host holds the public address %s directly, no NAT in the way", public)
		return
	}

	wan, err := upnpExternalIP(ctx)
	if err != nil {
		c.fact("upnp", "unavailable")
		c.note("Router did not answer UPnP discovery (%v), double NAT cannot be ruled out", err)
		c.info("Behind NAT: LAN %s, gateway %s, public %s. Forward ports on the router at %s", lan, gateway, public, gateway)
		return
	}
	c.fact("upnp", "available")
	c.fact("router_wan_ip", wan)
	wanIP := net.ParseIP(wan)
	switch {
	case isCGNAT(wanIP) || isPrivateIP(wanIP):
		c.fail("Double NAT: the router's WAN address %s is private while the internet sees %s", wan, public)
		c.fix("Two devices each do NAT (ISP modem plus your router). Put the modem in bridge or passthrough mode, or forward the ports on both devices (modem to router, router to this host), or use the playit tunnel module.", docsProxy)
	case wan == public:
		c.pass("Single NAT behind %s, router WAN %s matches the public address", gateway, wan)
	default:
		c.info("Router WAN %s differs from the address echo services see (%s)", wan, public)
		c.note("A VPN, a second uplink, or a stale echo cache explains this")
	}
}

// Verifies the panel and every enabled listener are bound
func (r *Runner) checkListeningPorts(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	states := r.proxy.SocketStates()
	proxyOn := r.proxy.Enabled()
	c.fact("proxy_enabled", proxyOn)

	type row struct {
		port  int
		label string
	}
	var wanted []row
	if p, err := strconv.Atoi(r.cfg.Server.Port); err == nil {
		wanted = append(wanted, row{p, "panel"})
	}
	if listeners, err := r.store.ListProxyListeners(ctx); err == nil {
		for _, l := range listeners {
			if l.Id == proxy.PanelListenerID || !l.Enabled {
				continue
			}
			if !proxyOn {
				c.note("listener %s on %d idle, proxy disabled", l.Name, l.Port)
				continue
			}
			wanted = append(wanted, row{int(l.Port), "listener " + l.Name})
		}
	}

	var problems []string
	denied, inUse := false, false
	for _, w := range wanted {
		if states[w.port] {
			c.note("%d (%s): bound", w.port, w.label)
			continue
		}
		outcome, err := probeBind(r.cfg.Server.Host, w.port)
		switch outcome {
		case bindDenied:
			denied = true
			problems = append(problems, fmt.Sprintf("%d (%s) permission denied", w.port, w.label))
		case bindInUse:
			inUse = true
			problems = append(problems, fmt.Sprintf("%d (%s) held by another process", w.port, w.label))
		case bindFree:
			problems = append(problems, fmt.Sprintf("%d (%s) free but not bound", w.port, w.label))
		default:
			problems = append(problems, fmt.Sprintf("%d (%s) %v", w.port, w.label, err))
		}
	}
	if len(problems) == 0 {
		var ports []string
		for _, w := range wanted {
			ports = append(ports, strconv.Itoa(w.port))
		}
		c.fact("bound", strings.Join(ports, ", "))
		c.pass("%s bound and accepting", plural(len(wanted), "port", "ports"))
		return
	}
	for _, p := range problems {
		c.note("%s", p)
	}
	c.fail("%s not bound: %s", plural(len(problems), "port is", "ports are"), abbreviate(problems, 3))
	var fixes []string
	if denied {
		fixes = append(fixes, "Ports below 1024 need root or the bind capability: sudo setcap 'cap_net_bind_service=+ep' /path/to/discopanel, or pick a port above 1024, or run in Docker.")
	}
	if inUse {
		fixes = append(fixes, "Another program holds the port (ss -ltnp | grep :PORT shows which). Stop it or change the listener port in Settings > Network.")
	}
	if len(fixes) == 0 {
		fixes = append(fixes, "Restart the panel and look for 'Failed to start listener' in the log.")
	}
	c.fix(strings.Join(fixes, " "), docsProxy)
}

// One inbound port reachability probe
type forwardTarget struct {
	port     int
	label    string
	mc       bool
	hostname string
}

// Routed minecraft hostname on a port, probe name otherwise
func (r *Runner) listenerHostname(port int) string {
	for _, e := range r.proxy.RouteEntries() {
		if e.Port == port && e.Route != nil && e.Route.Protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT && e.Route.Hostname != "" {
			return e.Route.Hostname
		}
	}
	return proxy.ProbeHostname
}

// Ports players and browsers must reach from outside
func (r *Runner) forwardTargets(ctx context.Context) []forwardTarget {
	var targets []forwardTarget
	seen := map[int]bool{}
	if p, err := strconv.Atoi(r.cfg.Server.Port); err == nil {
		targets = append(targets, forwardTarget{port: p, label: "panel", hostname: proxy.ProbeHostname})
		seen[p] = true
	}
	if r.proxy.Enabled() {
		if listeners, err := r.store.ListProxyListeners(ctx); err == nil {
			for _, l := range listeners {
				port := int(l.Port)
				if l.Id == proxy.PanelListenerID || !l.Enabled || seen[port] {
					continue
				}
				seen[port] = true
				targets = append(targets, forwardTarget{port: port, label: "listener " + l.Name, mc: true, hostname: r.listenerHostname(port)})
			}
		}
	}
	if servers, err := r.store.ListServers(ctx); err == nil {
		for _, s := range servers {
			if len(s.ProxyHostnames) > 0 || seen[int(s.Port)] || s.Port == 0 {
				continue
			}
			if s.Status != v1.ServerStatus_SERVER_STATUS_RUNNING && s.Status != v1.ServerStatus_SERVER_STATUS_UNHEALTHY {
				continue
			}
			seen[int(s.Port)] = true
			targets = append(targets, forwardTarget{port: int(s.Port), label: "server " + s.Name, mc: true, hostname: proxy.ProbeHostname})
		}
	}
	return targets
}

// What the hub saw when it connected back to one port
type forwardOutcome struct {
	reachable bool
	latency   time.Duration
	observed  string
	// Why the hub could not connect
	reason string
	// The relay call itself failed, the port stays untested
	relayErr error
}

// Asks the hub's reachability relay to connect back to one port on this panel's public address
func (r *Runner) probeForwardExternal(ctx context.Context, t forwardTarget) forwardOutcome {
	protocol := "tcp"
	if t.mc {
		protocol = "minecraft"
	}
	res, err := hub.ReachabilityCheck(ctx, t.port, protocol, t.hostname)
	if err != nil {
		return forwardOutcome{relayErr: err}
	}
	return forwardOutcome{
		reachable: res.Reachable,
		latency:   time.Duration(res.LatencyMs) * time.Millisecond,
		observed:  res.ObservedIP,
		reason:    res.Error,
	}
}

// One line for a failed relay call
func describeRelayErr(err error) string {
	if he := hub.AsError(err); he != nil {
		return he.Error()
	}
	return describeNetErr(err)
}

// Tries every public facing port from outside the network through the hub's relay
func (r *Runner) checkForwarding(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	lan, public, gateway := r.proxy.NetworkAddresses()
	if public == "" {
		c.skip("Public address unknown, see network.nat")
		return
	}
	if ip := net.ParseIP(public); isCGNAT(ip) || isPrivateIP(ip) {
		c.skip("Public address %s cannot accept forwarded ports, see network.nat", public)
		return
	}
	c.fact("public_ip", public)

	targets := r.forwardTargets(ctx)
	if len(targets) == 0 {
		c.skip("No public facing ports to test")
		return
	}
	c.fact("relay", hub.SupportBase())
	results := make([]forwardOutcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			results[i] = r.probeForwardExternal(ctx, t)
		})
	}
	wg.Wait()

	// Panel port often stays private on purpose
	var ok, failed []string
	var failedPorts, untested []string
	panelHidden := false
	observed := ""
	var relayErr error
	for i, t := range targets {
		res := results[i]
		if res.relayErr != nil {
			relayErr = res.relayErr
			untested = append(untested, fmt.Sprintf("%d (%s)", t.port, t.label))
			c.note("%d %s: relay failed: %s", t.port, t.label, describeRelayErr(res.relayErr))
			continue
		}
		if res.observed != "" {
			observed = res.observed
		}
		if !res.reachable {
			c.note("%d %s: %s", t.port, t.label, res.reason)
			if !t.mc {
				panelHidden = true
				continue
			}
			failed = append(failed, fmt.Sprintf("%d (%s)", t.port, t.label))
			failedPorts = append(failedPorts, strconv.Itoa(t.port))
			continue
		}
		ok = append(ok, strconv.Itoa(t.port))
		c.note("%d %s: reachable in %s", t.port, t.label, ms(res.latency))
	}
	c.fact("reachable", strings.Join(ok, ", "))
	c.fact("unreachable", strings.Join(failedPorts, ", "))
	c.fact("untested", strings.Join(untested, ", "))
	if observed != "" {
		c.fact("observed_ip", observed)
		if observed != public {
			c.note("The hub reached this panel at %s, network.nat detected %s", observed, public)
		}
	}
	if panelHidden {
		c.note("Panel port not reachable from the internet, expected when the UI stays on the LAN or behind a reverse proxy")
	}
	if len(untested) == len(targets) {
		c.warn("Reachability relay at %s unavailable: %s", hub.SupportHost(), describeRelayErr(relayErr))
		c.fix("The hub connects back to test each port. Check upstream.support and network.internet, then run diagnostics again.", docsProxy)
		return
	}

	switch {
	case len(failed) == 0 && len(ok) == 0 && len(untested) > 0:
		c.warn("%s untested, the relay failed: %s", plural(len(untested), "port", "ports"), abbreviate(untested, 4))
		c.fix("The hub connects back to test each port. Check upstream.support and network.internet, then run diagnostics again.", docsProxy)
	case len(failed) == 0 && len(ok) == 0:
		c.info("Only the panel port was tested and it stays private, no Minecraft ports face the internet yet")
	case len(failed) == 0:
		c.pass("All %d Minecraft ports answer on %s from the internet", len(ok), public)
	case lan == public:
		c.fail("%s not reachable on %s: %s", plural(len(failed), "port is", "ports are"), public, abbreviate(failed, 4))
		c.fix(fmt.Sprintf("The host firewall or the provider's security group blocks TCP %s. Open them (ufw allow PORT/tcp, firewall-cmd --add-port=PORT/tcp, or the cloud console) and confirm server.host is 0.0.0.0.", strings.Join(failedPorts, ", ")), docsProxy)
	default:
		c.fail("%s not reachable on %s: %s", plural(len(failed), "port is", "ports are"), public, abbreviate(failed, 4))
		c.fix(fmt.Sprintf("Forward TCP %s on the router at %s to %s, then open them in the host firewall (ufw allow PORT/tcp or firewall-cmd --add-port=PORT/tcp).", strings.Join(failedPorts, ", "), gateway, lan), docsProxy)
	}
}

// True for agent reachability names that never need public DNS
func infraName(name string) bool {
	if net.ParseIP(name) != nil || !strings.Contains(name, ".") {
		return true
	}
	return name == "host.docker.internal"
}

// Hostnames the proxy answers on, keyed by lane use
func (r *Runner) configuredHostnames(ctx context.Context) (map[string]bool, []string) {
	mcNames := map[string]bool{}
	seen := map[string]bool{}
	var names []string
	add := func(name string, mc bool) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || infraName(name) {
			return
		}
		if mc {
			mcNames[name] = true
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, name := range r.proxy.PanelHostnames() {
		add(name, false)
	}
	for _, e := range r.proxy.RouteEntries() {
		if e.Route == nil || e.Route.Hostname == "" {
			continue
		}
		add(e.Route.Hostname, e.Route.Protocol == v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT)
	}
	if servers, err := r.store.ListServers(ctx); err == nil {
		for _, s := range servers {
			for _, name := range s.ProxyHostnames {
				add(name, true)
			}
		}
	}
	sort.Strings(names)
	return mcNames, names
}

// Verifies configured hostnames resolve to this install
func (r *Runner) checkHostnames(ctx context.Context, c *check) {
	if r.proxy == nil {
		c.skip("Proxy manager unavailable")
		return
	}
	mcNames, names := r.configuredHostnames(ctx)
	if len(names) == 0 {
		c.skip("No hostnames configured")
		return
	}
	lan, public, _ := r.proxy.NetworkAddresses()
	c.fact("public_ip", public)
	c.fact("lan_ip", lan)
	c.fact("hostnames", len(names))

	type result struct {
		ips []net.IP
		err error
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			ips, err := lookup(ctx, name)
			results[i] = result{ips, err}
		})
	}
	wg.Wait()

	var missing, cloudflare, elsewhere, lanOnly []string
	for i, name := range names {
		res := results[i]
		if res.err != nil {
			missing = append(missing, name)
			c.note("%s: %s", name, describeNetErr(res.err))
			continue
		}
		var addrs []string
		hitPublic, hitLAN, hitCF := false, false, false
		for _, ip := range res.ips {
			addrs = append(addrs, ip.String())
			s := ip.String()
			hitPublic = hitPublic || s == public
			hitLAN = hitLAN || s == lan
			hitCF = hitCF || isCloudflare(ip)
		}
		switch {
		case hitCF && mcNames[name]:
			cloudflare = append(cloudflare, name)
			c.note("%s: %s (Cloudflare proxy)", name, strings.Join(addrs, ", "))
		case hitPublic:
			c.note("%s: %s ok", name, strings.Join(addrs, ", "))
		case hitLAN:
			lanOnly = append(lanOnly, name)
			c.note("%s: %s (LAN only)", name, strings.Join(addrs, ", "))
		default:
			elsewhere = append(elsewhere, name)
			c.note("%s: %s (not this install)", name, strings.Join(addrs, ", "))
		}
	}

	switch {
	case len(missing) > 0:
		c.fail("%s not resolve: %s", plural(len(missing), "hostname does", "hostnames do"), abbreviate(missing, 4))
		c.fix(fmt.Sprintf("Create DNS A records for them (a wildcard like *.%s covers every server) pointing at %s. New records take minutes to propagate.", r.proxy.EffectiveBaseURL(), public), docsProxy)
	case len(cloudflare) > 0:
		c.fail("%s proxied by Cloudflare: %s", plural(len(cloudflare), "Minecraft hostname is", "Minecraft hostnames are"), abbreviate(cloudflare, 4))
		c.fix("Cloudflare's proxy (orange cloud) only carries HTTP. Set those records to DNS only (grey cloud) so players connect straight to "+public+".", docsProxy)
	case len(elsewhere) > 0:
		c.warn("%s to another address: %s", plural(len(elsewhere), "hostname points", "hostnames point"), abbreviate(elsewhere, 4))
		c.fix(fmt.Sprintf("Update those DNS records to %s. If that other address is a VPS or tunnel you relay through on purpose, set proxy.public_ip to it so this check knows.", public), docsProxy)
	case len(lanOnly) == len(names):
		c.info("All %d hostnames resolve to the LAN address %s, fine for local play only", len(names), lan)
	default:
		c.pass("%d hostnames point at %s", len(names)-len(lanOnly), public)
	}
}
