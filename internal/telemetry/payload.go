// Package telemetry sends the hourly heartbeat to the discohaus hub and
// keeps the hub's last answer for the release check
package telemetry

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Bounds the hub enforces on a heartbeat, exceeding any is a rejected request
const (
	maxShort   = 64
	maxOSArch  = 32
	maxImage   = 128
	maxServers = 200
	maxList    = 50
	maxCount   = 10_000
	maxPort    = 65_535
)

// Everything one heartbeat is built from
type Facts struct {
	InstallID     string
	Version       string
	OS            string
	Arch          string
	DockerVersion string
	Servers       []*v1.Server
	Modules       []*v1.Module
	// Runtime image references the servers run on
	RuntimeImages []string
	// Last diagnostics report, nil before the first run
	Report    *v1.DiagnosticReport
	PanelPort int
	Uptime    time.Duration
}

// Wire name of a loader: the proto enum name without its prefix, lowercase
// Unspecified is the empty string
func LoaderName(l v1.ModLoader) string {
	if l == v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(l.String(), "MOD_LOADER_"))
}

// Drops control characters and cuts the string to max runes
func clean(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) {
			continue
		}
		if n == max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Clamps a count into the hub's accepted range
func count(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > maxCount {
		return maxCount
	}
	return int32(n)
}

// Distinct cleaned values, sorted, at most limit of them
func distinct(values []string, maxLen, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = clean(v, maxLen)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	slices.Sort(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Servers grouped by loader and Minecraft version, sorted, at most maxServers groups
func summarizeServers(servers []*v1.Server) []hub.ServerSummary {
	type key struct{ loader, mc string }
	counts := map[key]int{}
	for _, s := range servers {
		if s == nil {
			continue
		}
		counts[key{LoaderName(s.ModLoader), clean(s.McVersion, maxShort)}]++
	}
	out := make([]hub.ServerSummary, 0, len(counts))
	for k, n := range counts {
		out = append(out, hub.ServerSummary{Loader: k.loader, MCVersion: k.mc, Count: count(n)})
	}
	slices.SortFunc(out, func(a, b hub.ServerSummary) int {
		if c := strings.Compare(a.Loader, b.Loader); c != 0 {
			return c
		}
		return strings.Compare(a.MCVersion, b.MCVersion)
	})
	if len(out) > maxServers {
		out = out[:maxServers]
	}
	return out
}

// Module usage as the distinct template ids, a module without one counts by name
func summarizeModules(modules []*v1.Module) []string {
	names := make([]string, 0, len(modules))
	for _, m := range modules {
		if m == nil {
			continue
		}
		if m.TemplateId != "" {
			names = append(names, m.TemplateId)
			continue
		}
		names = append(names, m.Name)
	}
	return distinct(names, maxShort, maxList)
}

// Builds the heartbeat the hub accepts from the gathered facts
func BuildPayload(f Facts) *hub.HeartbeatRequest {
	port := f.PanelPort
	if port < 0 {
		port = 0
	}
	if port > maxPort {
		port = maxPort
	}
	uptime := int64(f.Uptime / time.Second)
	if uptime < 0 {
		uptime = 0
	}
	req := &hub.HeartbeatRequest{
		InstallID:     f.InstallID,
		Version:       clean(f.Version, maxShort),
		OS:            clean(f.OS, maxOSArch),
		Arch:          clean(f.Arch, maxOSArch),
		DockerVersion: clean(f.DockerVersion, maxShort),
		Servers:       summarizeServers(f.Servers),
		Modules:       summarizeModules(f.Modules),
		RuntimeImages: distinct(f.RuntimeImages, maxImage, maxList),
		PanelPort:     int32(port),
		UptimeSeconds: uptime,
	}
	if f.Report != nil {
		req.DiagnosticsPassed = count(int(f.Report.PassCount))
		req.DiagnosticsWarned = count(int(f.Report.WarnCount))
		req.DiagnosticsFailed = count(int(f.Report.FailCount))
	}
	return req
}
