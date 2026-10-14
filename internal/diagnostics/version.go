package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GitHub release probe endpoints and cadence, used only when telemetry is off
const (
	releasesAPIURL  = "https://api.github.com/repos/discohaus/discopanel/releases?per_page=30"
	releasesPageURL = "https://github.com/discohaus/discopanel/releases"
	versionTTL      = 6 * time.Hour
	versionRetry    = 15 * time.Minute
	versionTimeout  = 8 * time.Second
)

// Refreshes the GitHub release status on a slow cadence
func (r *Runner) versionLoop() {
	ticker := time.NewTicker(versionTTL)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			// The heartbeat answer covers it while telemetry is on
			if r.telemetryOn() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
			r.refreshVersion(ctx)
			cancel()
		}
	}
}

// Release status: the hub's heartbeat answer when telemetry is on, a cached GitHub probe otherwise
func (r *Runner) VersionStatus(ctx context.Context) *v1.GetVersionStatusResponse {
	if r.telemetryOn() {
		return r.hubVersionStatus()
	}
	r.versionMu.Lock()
	cached, at := r.versionRes, r.versionAt
	r.versionMu.Unlock()
	if cached != nil {
		ttl := versionTTL
		if cached.CheckError != "" {
			ttl = versionRetry
		}
		if time.Since(at) < ttl {
			return cached
		}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	return r.refreshVersion(ctx)
}

// Release status from the hub's last heartbeat answer, no probe of its own
func (r *Runner) hubVersionStatus() *v1.GetVersionStatusResponse {
	res := &v1.GetVersionStatusResponse{CurrentVersion: r.version}
	if !r.cfg.Diagnostics.VersionCheck {
		res.CheckError = "release check disabled by configuration"
		return res
	}
	r.versionMu.Lock()
	src := r.releases
	r.versionMu.Unlock()
	if src == nil {
		res.CheckError = "heartbeat sender not wired to the release check"
		return res
	}
	resp, at, err := src.LastHeartbeat()
	if resp == nil {
		if err != nil {
			res.CheckError = fmt.Sprintf("heartbeat to %s failed: %v", hub.SupportBase(), err)
		} else {
			res.CheckError = fmt.Sprintf("waiting for the first heartbeat to %s", hub.SupportBase())
		}
		return res
	}
	res.CheckedAt = timestamppb.New(at)
	res.HubNotice = resp.Notice
	if _, ok := parseSemver(resp.LatestVersion); ok {
		res.LatestVersion = resp.LatestVersion
		res.ReleaseUrl = resp.LatestReleaseURL
		if res.ReleaseUrl == "" {
			res.ReleaseUrl = releasesPageURL
		}
		res.UpdateAvailable = updateAvailable(r.version, resp.LatestVersion)
	} else {
		res.CheckError = fmt.Sprintf("%s names no versioned release (%q)", hub.SupportBase(), resp.LatestVersion)
	}
	// The last answer stands while a later heartbeat fails
	if err != nil {
		res.CheckError = fmt.Sprintf("last heartbeat to %s failed: %v", hub.SupportBase(), err)
	}
	return res
}

// Probes GitHub once, concurrent callers get the cache
func (r *Runner) refreshVersion(ctx context.Context) *v1.GetVersionStatusResponse {
	r.versionMu.Lock()
	if r.versionBusy {
		cached := r.versionRes
		r.versionMu.Unlock()
		if cached != nil {
			return cached
		}
		return &v1.GetVersionStatusResponse{CurrentVersion: r.version, CheckError: "release check in progress"}
	}
	r.versionBusy = true
	prev := r.versionRes
	r.versionMu.Unlock()
	defer func() {
		r.versionMu.Lock()
		r.versionBusy = false
		r.versionMu.Unlock()
	}()

	res := &v1.GetVersionStatusResponse{CurrentVersion: r.version, CheckedAt: timestamppb.Now()}
	if prev != nil {
		// Keeps the last known release while a probe fails
		res.LatestVersion = prev.LatestVersion
		res.ReleaseUrl = prev.ReleaseUrl
		res.UpdateAvailable = prev.UpdateAvailable
	}
	if !r.cfg.Diagnostics.VersionCheck {
		res.CheckError = "release check disabled by configuration"
	} else if tag, url, err := r.fetchLatestRelease(ctx); err != nil {
		res.CheckError = err.Error()
	} else {
		res.LatestVersion = tag
		res.ReleaseUrl = url
		res.UpdateAvailable = updateAvailable(r.version, tag)
	}

	r.versionMu.Lock()
	r.versionRes = proto.Clone(res).(*v1.GetVersionStatusResponse)
	r.versionAt = time.Now()
	r.versionMu.Unlock()
	return res
}

// One published release as GitHub lists it
type ghRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Newest release that fits the running channel
func (r *Runner) fetchLatestRelease(ctx context.Context) (string, string, error) {
	// Release notes ride along, the list runs well past 64k
	res := r.probeLimit(ctx, http.MethodGet, releasesAPIURL, map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}, largeBodyCap)
	if res.Err != nil {
		return "", "", fmt.Errorf("github unreachable: %s", describeNetErr(res.Err))
	}
	switch {
	case res.Status == http.StatusForbidden || res.Status == http.StatusTooManyRequests:
		return "", "", fmt.Errorf("github rate limited the release check (HTTP %d)", res.Status)
	case res.Status != http.StatusOK:
		return "", "", fmt.Errorf("github answered HTTP %d", res.Status)
	}
	var releases []ghRelease
	if err := json.Unmarshal(res.Body, &releases); err != nil {
		return "", "", fmt.Errorf("github release response unreadable")
	}
	tag, url := pickRelease(r.version, releases)
	if tag == "" {
		return "", "", fmt.Errorf("github lists no versioned releases")
	}
	if url == "" {
		url = releasesPageURL
	}
	return tag, url, nil
}

// Newest tag, prereleases count only for prerelease builds
func pickRelease(current string, releases []ghRelease) (string, string) {
	cur, ok := parseSemver(current)
	allowPre := ok && cur.pre != ""
	var bestTag, bestURL string
	var best semver
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		v, ok := parseSemver(rel.TagName)
		if !ok || (!allowPre && (rel.Prerelease || v.pre != "")) {
			continue
		}
		if bestTag == "" || compareSemver(v, best) > 0 {
			best, bestTag, bestURL = v, rel.TagName, rel.HTMLURL
		}
	}
	return bestTag, bestURL
}

// Parsed semantic version
type semver struct {
	major, minor, patch int
	pre                 string
}

// Parses vX.Y.Z with optional prerelease and build suffix
func parseSemver(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, true
}

// Orders versions, a release outranks its prereleases
func compareSemver(a, b semver) int {
	switch {
	case a.major != b.major:
		return a.major - b.major
	case a.minor != b.minor:
		return a.minor - b.minor
	case a.patch != b.patch:
		return a.patch - b.patch
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return comparePrerelease(a.pre, b.pre)
}

// Dot separated identifiers, numeric ones compare as numbers
func comparePrerelease(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return an - bn
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// True when latest is a newer version than current
func updateAvailable(current, latest string) bool {
	cur, ok := parseSemver(current)
	if !ok {
		return false
	}
	lat, ok := parseSemver(latest)
	if !ok {
		return false
	}
	return compareSemver(lat, cur) > 0
}

// Compares the running build against the newest release
func (r *Runner) checkVersion(ctx context.Context, c *check) {
	st := r.VersionStatus(ctx)
	c.fact("current", st.CurrentVersion)
	c.fact("latest", st.LatestVersion)
	c.fact("release_url", st.ReleaseUrl)
	if st.CheckError != "" {
		c.note("%s", st.CheckError)
	}
	if st.HubNotice != "" {
		c.fact("hub_notice", st.HubNotice)
		c.note("Notice from %s: %s", hub.SupportHost(), st.HubNotice)
	}
	if r.telemetryOn() {
		c.fact("source", "hub heartbeat")
	} else {
		c.fact("source", "github")
	}
	current := st.CurrentVersion
	if current == "" {
		current = config.UnknownVersion
	}
	if _, ok := parseSemver(current); !ok {
		if st.LatestVersion != "" {
			c.info("Running an unversioned build (%s), newest release is %s", current, st.LatestVersion)
		} else {
			c.info("Running an unversioned build (%s)", current)
		}
		return
	}
	switch {
	case st.LatestVersion == "":
		c.info("Running %s, release check unavailable", current)
	case st.UpdateAvailable:
		c.warn("DiscoPanel %s is behind the newest release %s", current, st.LatestVersion)
		c.fix("Update the panel. Compose: docker compose pull && docker compose up -d. Proxmox helper script: run update in the container console. Binary: replace it from the releases page and restart.", docsFAQ)
	default:
		c.pass("DiscoPanel %s is up to date", current)
	}
}
