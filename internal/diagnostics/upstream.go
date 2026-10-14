package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/indexers/fuego"
	"github.com/discohaus/discopanel/pkg/indexers/modrinth"
)

// Registry every runtime and module image comes from
const ghcrProbeURL = "https://ghcr.io/v2/"

// Host part of a url, the url itself when it has none
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// Hostname without a port, for resolver probes
func bareHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// How upstream data reaches the panel, for summaries
func upstreamRoute() string {
	if !hub.IndexEnabled() {
		return "directly"
	}
	return "through the index"
}

// Remedy for a host the panel cannot reach
func unreachableFix(base string) string {
	if !hub.IndexEnabled() {
		return fmt.Sprintf("The panel host cannot reach %s. Check network.dns and network.internet, then any firewall or proxy in between.", hostOf(base))
	}
	return fmt.Sprintf("The panel host cannot reach the index at %s. Check upstream.index, network.dns and network.internet, or set index.enabled: false to contact every upstream at its own host.", hostOf(base))
}

// Confirms the upstream index answers, runs only when upstreams route through it
func (r *Runner) checkIndex(ctx context.Context, c *check) {
	base := hub.IndexBase()
	c.fact("url", base)
	res := r.probe(ctx, http.MethodGet, base+"/health", map[string]string{"Accept": "application/json"})
	if res.Err != nil {
		c.fail("Upstream index unreachable: %s", describeNetErr(res.Err))
		c.fix(fmt.Sprintf("Modpack, loader, and version manifests come through %s. Check network.dns and network.internet, or set index.enabled: false to contact every upstream at its own host.", hostOf(base)), docsConfiguration)
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	var health struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
		Version string `json:"version"`
	}
	_ = json.Unmarshal(res.Body, &health)
	c.fact("index_version", health.Version)
	switch {
	case res.Status == http.StatusOK && health.OK:
		c.pass("Upstream index reachable (%s)", ms(res.Latency))
	case res.Status == http.StatusOK:
		c.warn("Upstream index answered without a healthy status")
		c.fix("Something other than the discohaus index answers at index.base_url. Check the setting, or set index.enabled: false.", docsConfiguration)
	case res.Status >= 500:
		c.warn("Upstream index is having trouble (HTTP %d)", res.Status)
		c.fix("Retry later, or set index.enabled: false to contact every upstream at its own host until it recovers.", docsConfiguration)
	default:
		c.warn("Upstream index answered HTTP %d", res.Status)
	}
}

// True when a 503 came from the index rather than the upstream behind it
func indexDown(status int) bool {
	return hub.IndexEnabled() && status == http.StatusServiceUnavailable
}

// Validates the configured CurseForge key against the API, or the route to it through the index
func (r *Runner) checkCurseForge(ctx context.Context, c *check) {
	key := ""
	if global, _, err := r.store.GetGlobalSettings(ctx); err == nil && global != nil && global.CfApiKey != nil {
		key = strings.TrimSpace(*global.CfApiKey)
	}
	base := fuego.BaseURL()
	c.fact("url", base)
	if key == "" && !hub.IndexEnabled() {
		c.info("No CurseForge API key configured")
		c.fix("CurseForge modpacks and mod lookups need a key from https://console.curseforge.com/#/api-keys while the index is off. Paste it under Settings > Server defaults > CurseForge.", docsModpacks)
		return
	}
	headers := map[string]string{"Accept": "application/json"}
	if key != "" {
		c.fact("key_length", len(key))
		headers["x-api-key"] = key
	}
	res := r.probe(ctx, http.MethodGet, base+"/games/432", headers)
	if res.Err != nil {
		c.fail("CurseForge API unreachable %s: %s", upstreamRoute(), describeNetErr(res.Err))
		c.fix(unreachableFix(base), docsFAQ)
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	switch {
	case res.Status == http.StatusOK && !hub.IndexEnabled():
		c.pass("CurseForge accepted the API key (%s)", ms(res.Latency))
	case res.Status == http.StatusOK:
		if key != "" {
			c.note("A CurseForge key is configured and rides along, the index answers with its own")
		}
		c.pass("CurseForge reachable through the index (%s)", ms(res.Latency))
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		if !hub.IndexEnabled() {
			c.fail("CurseForge rejected the API key (HTTP %d)", res.Status)
			c.fix("New keys can take a day or two to activate. Check it at console.curseforge.com and paste it again without surrounding spaces. Keys that never activate usually work after being recreated.", docsFAQ)
		} else {
			c.fail("The index refused the CurseForge request (HTTP %d)", res.Status)
			c.fix("The index answers CurseForge with its own key. Retry later, or set index.enabled: false and configure your own key.", docsModpacks)
		}
	case res.Status == http.StatusTooManyRequests:
		retry := res.Header.Get("Retry-After")
		if retry == "" {
			retry = "unknown"
		}
		c.fact("retry_after", retry)
		c.warn("CurseForge is rate limiting this panel (HTTP 429, retry after %s)", retry)
		c.fix("Wait for the cooldown, DiscoPanel paces requests on its own. Large modpack installs and long mod scans trigger this, it clears by itself.", docsModpacks)
	case indexDown(res.Status):
		c.warn("The index is unavailable for CurseForge (HTTP 503)")
		c.fix("Retry later, or set index.enabled: false and configure a CurseForge key to bypass the index.", docsConfiguration)
	case res.Status >= 500:
		c.warn("CurseForge API is having trouble (HTTP %d)", res.Status)
		c.fix("Upstream outage, retry later. Nothing to change on your side.", "")
	default:
		c.warn("CurseForge answered HTTP %d", res.Status)
	}
}

// Confirms the Modrinth API answers
func (r *Runner) checkModrinth(ctx context.Context, c *check) {
	// The origin root redirects to the docs, which the index correctly refuses.
	base := modrinth.BaseURL()
	endpoint := base + "/tag/loader"
	c.fact("url", endpoint)
	res := r.probeLimit(ctx, http.MethodGet, endpoint, map[string]string{"Accept": "application/json"}, largeBodyCap)
	if res.Err != nil {
		c.fail("Modrinth API unreachable %s: %s", upstreamRoute(), describeNetErr(res.Err))
		c.fix("Modrinth packs and mod downloads will fail. "+unreachableFix(base), "")
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	switch {
	case res.Status == http.StatusOK:
		var loaders []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(res.Body, &loaders); err != nil || len(loaders) == 0 || loaders[0].Name == "" {
			c.warn("Modrinth answered without a valid loader list %s", upstreamRoute())
			c.fix("Check the response from the URL above; a proxy may be serving a page instead of the API.", "")
			return
		}
		c.fact("loaders", len(loaders))
		c.pass("Modrinth API reachable %s (%s)", upstreamRoute(), ms(res.Latency))
	case res.Status == http.StatusTooManyRequests:
		c.warn("Modrinth is rate limiting this panel (HTTP 429)")
		c.fix("Wait a minute, DiscoPanel paces requests automatically.", "")
	case indexDown(res.Status):
		c.warn("The index is unavailable for Modrinth (HTTP 503)")
		c.fix("Retry later, or set index.enabled: false to bypass the index.", docsConfiguration)
	case res.Status >= 500:
		c.warn("Modrinth API request failed %s (HTTP %d)", upstreamRoute(), res.Status)
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(res.Body, &detail) == nil && detail.Message != "" {
			c.note("%s", detail.Message)
		}
	default:
		c.warn("Modrinth answered HTTP %d", res.Status)
	}
}

// Confirms the Mojang version manifest is downloadable
func (r *Runner) checkMojang(ctx context.Context, c *check) {
	base := hub.MojangMeta()
	manifest := base + "/mc/game/version_manifest_v2.json"
	c.fact("url", manifest)
	res := r.probe(ctx, http.MethodHead, manifest, nil)
	if res.Err == nil && (res.Status == http.StatusMethodNotAllowed || res.Status == http.StatusForbidden) {
		res = r.probe(ctx, http.MethodGet, manifest, nil)
	}
	if res.Err != nil {
		c.fail("Mojang version manifest unreachable %s: %s", upstreamRoute(), describeNetErr(res.Err))
		c.fix(fmt.Sprintf("Vanilla, Fabric, and Paper installs need the version manifest from %s. ", hostOf(base))+unreachableFix(base), "")
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	switch {
	case res.Status == http.StatusOK:
		c.pass("Mojang version manifest reachable %s (%s)", upstreamRoute(), ms(res.Latency))
	case indexDown(res.Status):
		c.warn("The index is unavailable for the Mojang manifest (HTTP 503)")
		c.fix("Retry later, or set index.enabled: false to bypass the index.", docsConfiguration)
	default:
		c.warn("Mojang answered HTTP %d for the version manifest", res.Status)
	}
}

// Confirms the panel host itself can reach ghcr
func (r *Runner) checkGHCR(ctx context.Context, c *check) {
	res := r.probe(ctx, http.MethodGet, ghcrProbeURL, nil)
	if res.Err != nil {
		c.fail("ghcr.io unreachable from the panel host: %s", describeNetErr(res.Err))
		c.fix("Runtime and module images live on ghcr.io. If docker.registry passes, only the panel process is blocked (HTTP proxy or DNS inside the panel container).", "")
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	// Anonymous v2 probes answer 401 with a token challenge
	if res.Status == http.StatusOK || res.Status == http.StatusUnauthorized {
		c.pass("ghcr.io reachable from the panel host (%s)", ms(res.Latency))
		return
	}
	c.warn("ghcr.io answered HTTP %d", res.Status)
}

// Confirms the support service answers: bundle uploads, heartbeats, and the reachability relay all go there
func (r *Runner) checkSupportServer(ctx context.Context, c *check) {
	base := hub.SupportBase()
	c.fact("url", base)
	c.fact("install_id", hub.InstallID())
	if r.telemetryOn() {
		c.fact("telemetry", "on")
	} else {
		c.fact("telemetry", "off")
	}
	if hub.InstallID() == "" {
		c.fail("No install id, every request to %s is refused", hostOf(base))
		c.fix(fmt.Sprintf("The panel could not write %s at startup, see the startup log. Make the data directory writable by the panel user and restart.", filepath.Join(r.cfg.Storage.DataDir, hub.InstallIDFile)), docsTroubleshooting)
		return
	}
	res := r.probe(ctx, http.MethodGet, base+"/health", map[string]string{"Accept": "application/json"})
	if res.Err != nil {
		c.warn("Support server unreachable: %s", describeNetErr(res.Err))
		c.fix("Bundle uploads from the Support tab, the telemetry heartbeat, and the port reachability relay all need it. Download the bundle instead and attach it to your report.", "")
		return
	}
	c.fact("status", res.Status)
	c.fact("latency", ms(res.Latency))
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	_ = json.Unmarshal(res.Body, &health)
	c.fact("hub_version", health.Version)
	if res.Status == http.StatusOK && health.OK {
		c.pass("Support server reachable (%s)", ms(res.Latency))
		return
	}
	c.warn("Support server answered HTTP %d", res.Status)
}
