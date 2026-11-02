// Loader build listings for pinning and default resolution
package provisioner

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Listings change a few times a day at most
const (
	metaDocTTL     = 10 * time.Minute
	metaFailureTTL = time.Minute // Retry failed upstreams soon
	metaDocMax     = 64          // Fabric and Quilt key per MC version
)

// One installable loader build
type LoaderBuild struct {
	Version     string
	Stable      bool // False for betas and prereleases
	Recommended bool // Build installed when none is pinned
}

type metaDocEntry struct {
	body    []byte
	err     error
	expires time.Time
	added   time.Time
}

// Bounded metadata document cache keyed by source URL
type metaDocCache struct {
	mu      sync.Mutex
	entries map[string]metaDocEntry
}

func (c *metaDocCache) get(key string) (metaDocEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return metaDocEntry{}, false
	}
	return e, true
}

func (c *metaDocCache) put(key string, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]metaDocEntry)
	}
	now := time.Now()
	ttl := metaDocTTL
	if err != nil {
		ttl = metaFailureTTL
	}
	if _, ok := c.entries[key]; !ok && len(c.entries) >= metaDocMax {
		c.evictLocked(now)
	}
	c.entries[key] = metaDocEntry{body: body, err: err, expires: now.Add(ttl), added: now}
}

// Drops expired entries, else the oldest one
func (c *metaDocCache) evictLocked(now time.Time) {
	oldest := ""
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
			continue
		}
		if oldest == "" || e.added.Before(c.entries[oldest].added) {
			oldest = k
		}
	}
	if len(c.entries) >= metaDocMax && oldest != "" {
		delete(c.entries, oldest)
	}
}

func (c *metaDocCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Loaders whose native installer takes a pinned build
var pinnableLoaders = []v1.ModLoader{
	v1.ModLoader_MOD_LOADER_FORGE,
	v1.ModLoader_MOD_LOADER_NEOFORGE,
	v1.ModLoader_MOD_LOADER_FABRIC,
	v1.ModLoader_MOD_LOADER_QUILT,
}

// Reports whether a loader build can be listed and pinned
func SupportsLoaderBuilds(loader v1.ModLoader) bool {
	return slices.Contains(pinnableLoaders, loader)
}

// Builds travel into installer URLs and commands
var loaderBuildPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// Reports whether a pinned build is safe to use in a URL
func ValidLoaderBuild(version string) bool {
	return loaderBuildPattern.MatchString(version)
}

// Fabric and Quilt loaders are still on major 0, Forge and NeoForge never were
func foreignLoaderBuild(loader v1.ModLoader, pin string) bool {
	fabricShaped := strings.HasPrefix(pin, "0.")
	switch loader {
	case v1.ModLoader_MOD_LOADER_FABRIC, v1.ModLoader_MOD_LOADER_QUILT:
		return !fabricShaped
	case v1.ModLoader_MOD_LOADER_FORGE, v1.ModLoader_MOD_LOADER_NEOFORGE:
		return fabricShaped
	}
	return false
}

// Clears a pin left over from another loader family, older panels kept it
func (p *Provisioner) dropForeignPin(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) {
	pin := strVal(cfg.ForgeVersion)
	if pin == "" || !foreignLoaderBuild(server.ModLoader, pin) {
		return
	}
	p.progress(server, "ignoring loader build %s, it belongs to another loader, installing the recommended %s build instead",
		pin, protometa.Label(server.ModLoader))
	cfg.ForgeVersion = nil
	if p.store == nil || cfg.Id == "" {
		return
	}
	if err := p.store.UpdateServerPropertiesFields(ctx, cfg.Id, map[string]any{"forge_version": nil}); err != nil {
		p.log.Warn("provisioner: could not clear stale loader build for %s: %v", server.Name, err)
	}
}

// Lists builds for a loader and MC version, newest first
func (p *Provisioner) LoaderBuilds(ctx context.Context, loader v1.ModLoader, mc string) ([]LoaderBuild, error) {
	if !SupportsLoaderBuilds(loader) {
		return nil, fmt.Errorf("%s has no selectable loader builds", protometa.Label(loader))
	}
	var builds []LoaderBuild
	var err error
	switch loader {
	case v1.ModLoader_MOD_LOADER_FABRIC:
		builds, err = p.fabricBuilds(ctx, mc)
	case v1.ModLoader_MOD_LOADER_QUILT:
		builds, err = p.quiltBuilds(ctx, mc)
	case v1.ModLoader_MOD_LOADER_NEOFORGE:
		builds, err = p.neoforgeBuilds(ctx, mc)
	case v1.ModLoader_MOD_LOADER_FORGE:
		builds, err = p.forgeBuilds(ctx, mc)
	}
	if err != nil {
		return nil, err
	}
	return builds, nil
}

// Build installed when nothing is pinned
func recommendedBuild(builds []LoaderBuild) string {
	for _, b := range builds {
		if b.Recommended {
			return b.Version
		}
	}
	return ""
}

// Flags the newest stable build, else the newest
func markNewestStable(builds []LoaderBuild) {
	for i := range builds {
		if builds[i].Stable {
			builds[i].Recommended = true
			return
		}
	}
	if len(builds) > 0 {
		builds[0].Recommended = true
	}
}

// Orders builds newest first by numeric segments
func sortBuildsDesc(builds []LoaderBuild) {
	slices.SortStableFunc(builds, func(a, b LoaderBuild) int {
		return minecraft.CompareGameVersions(b.Version, a.Version)
	})
}

func (p *Provisioner) fabricBuilds(ctx context.Context, mc string) ([]LoaderBuild, error) {
	body, err := p.metaBytes(ctx, fmt.Sprintf("%s/versions/loader/%s", fabricMetaURL(), url.PathEscape(mc)))
	if err != nil {
		return nil, fmt.Errorf("failed to list Fabric loader versions: %w", err)
	}
	builds, err := parseFabricLoaders(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Fabric loader versions: %w", err)
	}
	return builds, nil
}

func (p *Provisioner) quiltBuilds(ctx context.Context, mc string) ([]LoaderBuild, error) {
	body, err := p.metaBytes(ctx, fmt.Sprintf("%s/versions/loader/%s", quiltMetaURL(), url.PathEscape(mc)))
	if err != nil {
		return nil, fmt.Errorf("failed to list Quilt loader versions: %w", err)
	}
	builds, err := parseQuiltLoaders(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Quilt loader versions: %w", err)
	}
	return builds, nil
}

func (p *Provisioner) neoforgeBuilds(ctx context.Context, mc string) ([]LoaderBuild, error) {
	src := neoforgeSourceFor(mc)
	body, err := p.metaBytes(ctx, src.api)
	if err != nil {
		return nil, fmt.Errorf("failed to list NeoForge versions: %w", err)
	}
	builds, err := parseNeoForgeVersions(body, src.prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to read NeoForge versions: %w", err)
	}
	return builds, nil
}

func (p *Provisioner) forgeBuilds(ctx context.Context, mc string) ([]LoaderBuild, error) {
	body, err := p.metaBytes(ctx, forgeMavenURL()+"/maven-metadata.xml")
	if err != nil {
		return nil, fmt.Errorf("failed to list Forge versions: %w", err)
	}
	builds, err := parseForgeMetadata(body, mc)
	if err != nil {
		return nil, fmt.Errorf("failed to read Forge versions: %w", err)
	}
	// Promotions only decorate, the listing stands without them
	if promos, err := p.metaBytes(ctx, forgePromotionsURL()); err == nil {
		markForgePromotion(builds, forgePromotedVersion(promos, mc))
	} else {
		p.log.Warn("provisioner: Forge promotions unavailable: %v", err)
	}
	return builds, nil
}

// Fetches a metadata document, cached per URL so one fetch serves every MC version
func (p *Provisioner) metaBytes(ctx context.Context, rawURL string) ([]byte, error) {
	if e, ok := p.metaDocs.get(rawURL); ok {
		return e.body, e.err
	}
	body, err := p.metaClient(rawURL).DoBytes(ctx, rawURL)
	// Cancelled callers say nothing about the upstream
	if ctx.Err() == nil {
		p.metaDocs.put(rawURL, body, err)
	}
	return body, err
}

// Fabric meta lists loaders newest first with a stable flag
func parseFabricLoaders(body []byte) ([]LoaderBuild, error) {
	var rows []struct {
		Loader struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		} `json:"loader"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	builds := make([]LoaderBuild, 0, len(rows))
	for _, r := range rows {
		if r.Loader.Version == "" {
			continue
		}
		builds = append(builds, LoaderBuild{Version: r.Loader.Version, Stable: r.Loader.Stable})
	}
	markNewestStable(builds)
	return builds, nil
}

// Quilt meta lists loaders newest first, prereleases carry a dash
func parseQuiltLoaders(body []byte) ([]LoaderBuild, error) {
	var rows []struct {
		Loader struct {
			Version string `json:"version"`
		} `json:"loader"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	builds := make([]LoaderBuild, 0, len(rows))
	for _, r := range rows {
		if r.Loader.Version == "" {
			continue
		}
		builds = append(builds, LoaderBuild{Version: r.Loader.Version, Stable: !strings.Contains(r.Loader.Version, "-")})
	}
	markNewestStable(builds)
	return builds, nil
}

// NeoForge api lists every version, filtered to the MC prefix
func parseNeoForgeVersions(body []byte, prefix string) ([]LoaderBuild, error) {
	var listing struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, err
	}
	var builds []LoaderBuild
	for _, v := range listing.Versions {
		if strings.HasPrefix(v, prefix) {
			builds = append(builds, LoaderBuild{Version: v, Stable: !strings.Contains(v, "-beta")})
		}
	}
	// Legacy versions lead with the MC version, sort on the rest
	slices.SortStableFunc(builds, func(a, b LoaderBuild) int {
		return minecraft.CompareGameVersions(strings.TrimPrefix(b.Version, prefix), strings.TrimPrefix(a.Version, prefix))
	})
	markNewestStable(builds)
	return builds, nil
}

// Forge maven names versions mc-forge with optional branch suffix
func parseForgeMetadata(body []byte, mc string) ([]LoaderBuild, error) {
	var metadata struct {
		Versioning struct {
			Versions struct {
				Version []string `xml:"version"`
			} `xml:"versions"`
		} `xml:"versioning"`
	}
	if err := xml.Unmarshal(body, &metadata); err != nil {
		return nil, err
	}
	var builds []LoaderBuild
	seen := make(map[string]bool)
	for _, v := range metadata.Versioning.Versions.Version {
		rest, ok := strings.CutPrefix(v, mc+"-")
		if !ok {
			continue
		}
		// Branch suffixes like -1.7.10 or -new are not part of the pin
		if i := strings.Index(rest, "-"); i >= 0 {
			rest = rest[:i]
		}
		if rest == "" || seen[rest] {
			continue
		}
		seen[rest] = true
		builds = append(builds, LoaderBuild{Version: rest, Stable: true})
	}
	sortBuildsDesc(builds)
	return builds, nil
}

// Forge promotes a recommended build, else a latest one
func forgePromotedVersion(body []byte, mc string) string {
	var promotions struct {
		Promos map[string]string `json:"promos"`
	}
	if json.Unmarshal(body, &promotions) != nil {
		return ""
	}
	if v := promotions.Promos[mc+"-recommended"]; v != "" {
		return v
	}
	return promotions.Promos[mc+"-latest"]
}

func markForgePromotion(builds []LoaderBuild, version string) {
	for i := range builds {
		if builds[i].Version == version {
			builds[i].Recommended = true
			return
		}
	}
}

// Where NeoForge publishes builds for an MC version
type neoforgeSource struct {
	api, maven, artifact, prefix string
}

// 1.20.1 builds live under the legacy forge artifact
func neoforgeSourceFor(mc string) neoforgeSource {
	if mc == "1.20.1" {
		return neoforgeSource{api: neoforgeLegacyAPI(), maven: neoforgeLegacyURL(), artifact: "forge", prefix: "1.20.1-"}
	}
	return neoforgeSource{api: neoforgeVersionAPI(), maven: neoforgeMavenURL(), artifact: "neoforge", prefix: neoforgePrefix(mc)}
}

// Pinned build for a native install, checked against the listing
func (p *Provisioner) pinnedBuild(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) (string, error) {
	pin := pinnedLoaderVersion(server, cfg)
	if pin == "" {
		return "", nil
	}
	if !ValidLoaderBuild(pin) {
		return "", fmt.Errorf("loader build %q is not a valid version, fix or clear the Loader Build setting", pin)
	}
	// Supplied installers carry their own version
	if server.ModLoader == v1.ModLoader_MOD_LOADER_FORGE && (strVal(cfg.ForgeInstaller) != "" || strVal(cfg.ForgeInstallerUrl) != "") {
		return pin, nil
	}
	builds, err := p.LoaderBuilds(ctx, server.ModLoader, server.McVersion)
	if err != nil {
		// Install fails on its own if the build truly is missing
		p.progress(server, "could not verify loader build %s (%v), trying it anyway", pin, err)
		return pin, nil
	}
	name := protometa.Label(server.ModLoader)
	if len(builds) == 0 {
		return "", fmt.Errorf("%s has no builds for MC %s, clear the Loader Build setting or pick another Minecraft version", name, server.McVersion)
	}
	for _, b := range builds {
		if b.Version == pin {
			return pin, nil
		}
	}
	return "", fmt.Errorf("%s build %q does not exist for MC %s (newest is %s), pick another build or clear the Loader Build setting",
		name, pin, server.McVersion, builds[0].Version)
}
