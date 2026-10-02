package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestLoaderNames(t *testing.T) {
	cases := map[v1.ModLoader]string{
		v1.ModLoader_MOD_LOADER_UNSPECIFIED:    "",
		v1.ModLoader_MOD_LOADER_VANILLA:        "vanilla",
		v1.ModLoader_MOD_LOADER_FABRIC:         "fabric",
		v1.ModLoader_MOD_LOADER_PAPER:          "paper",
		v1.ModLoader_MOD_LOADER_NEOFORGE:       "neoforge",
		v1.ModLoader_MOD_LOADER_SPONGE_VANILLA: "sponge_vanilla",
	}
	for loader, want := range cases {
		if got := LoaderName(loader); got != want {
			t.Errorf("LoaderName(%s) = %q, want %q", loader, got, want)
		}
	}
}

func TestBuildPayloadGroupsAndBounds(t *testing.T) {
	servers := []*v1.Server{
		{ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.21.1"},
		{ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.21.1"},
		{ModLoader: v1.ModLoader_MOD_LOADER_PAPER, McVersion: "1.20.4"},
		{ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.20.1"},
		nil,
	}
	modules := []*v1.Module{
		{TemplateId: "bluemap", Name: "Map one"},
		{TemplateId: "bluemap", Name: "Map two"},
		{TemplateId: "", Name: "custom\x00thing"},
		{TemplateId: "doctor"},
	}
	req := BuildPayload(Facts{
		InstallID:     "0123456789abcdef0123456789abcdef",
		Version:       "v1.9.0\n",
		OS:            "linux",
		Arch:          "amd64",
		DockerVersion: "28.5.2",
		Servers:       servers,
		Modules:       modules,
		RuntimeImages: []string{"ghcr.io/discohaus/discoruntime:java21", "ghcr.io/discohaus/discoruntime:java21", "ghcr.io/discohaus/discoruntime:java17"},
		Report:        &v1.DiagnosticReport{PassCount: 20, WarnCount: 2, FailCount: 1, InfoCount: 3},
		PanelPort:     8080,
		Uptime:        90 * time.Second,
	})

	wantServers := []hub.ServerSummary{
		{Loader: "fabric", MCVersion: "1.20.1", Count: 1},
		{Loader: "fabric", MCVersion: "1.21.1", Count: 2},
		{Loader: "paper", MCVersion: "1.20.4", Count: 1},
	}
	if len(req.Servers) != len(wantServers) {
		t.Fatalf("servers = %+v, want %+v", req.Servers, wantServers)
	}
	for i := range wantServers {
		if req.Servers[i] != wantServers[i] {
			t.Errorf("servers[%d] = %+v, want %+v", i, req.Servers[i], wantServers[i])
		}
	}
	if got := strings.Join(req.Modules, ","); got != "bluemap,customthing,doctor" {
		t.Errorf("modules = %q", got)
	}
	if got := strings.Join(req.RuntimeImages, ","); got != "ghcr.io/discohaus/discoruntime:java17,ghcr.io/discohaus/discoruntime:java21" {
		t.Errorf("runtime images = %q", got)
	}
	if req.Version != "v1.9.0" {
		t.Errorf("version = %q, control characters must be stripped", req.Version)
	}
	if req.DiagnosticsPassed != 20 || req.DiagnosticsWarned != 2 || req.DiagnosticsFailed != 1 {
		t.Errorf("diagnostics counts = %d/%d/%d", req.DiagnosticsPassed, req.DiagnosticsWarned, req.DiagnosticsFailed)
	}
	if req.PanelPort != 8080 || req.UptimeSeconds != 90 {
		t.Errorf("port = %d uptime = %d", req.PanelPort, req.UptimeSeconds)
	}
}

func TestBuildPayloadCapsLists(t *testing.T) {
	var servers []*v1.Server
	for i := 0; i < 250; i++ {
		servers = append(servers, &v1.Server{ModLoader: v1.ModLoader_MOD_LOADER_VANILLA, McVersion: "1." + strings.Repeat("9", i%70+1)})
	}
	var modules []*v1.Module
	var images []string
	for i := 0; i < 80; i++ {
		modules = append(modules, &v1.Module{TemplateId: strings.Repeat("m", i+1)})
		images = append(images, "ghcr.io/x/y:"+strings.Repeat("t", i+1))
	}
	req := BuildPayload(Facts{
		OS:        strings.Repeat("o", 40),
		Arch:      strings.Repeat("a", 40),
		Servers:   servers,
		Modules:   modules,
		Report:    &v1.DiagnosticReport{PassCount: 50_000},
		PanelPort: 99_999,
		Uptime:    -time.Minute,
	})
	req.RuntimeImages = distinct(images, maxImage, maxList)
	if len(req.Servers) > maxServers {
		t.Errorf("servers = %d, want at most %d", len(req.Servers), maxServers)
	}
	for _, s := range req.Servers {
		if len(s.MCVersion) > maxShort {
			t.Errorf("mc version %q exceeds %d", s.MCVersion, maxShort)
		}
	}
	if len(req.Modules) != maxList {
		t.Errorf("modules = %d, want %d", len(req.Modules), maxList)
	}
	for _, m := range req.Modules {
		if len(m) > maxShort {
			t.Errorf("module %q exceeds %d", m, maxShort)
		}
	}
	if len(req.RuntimeImages) != maxList {
		t.Errorf("runtime images = %d, want %d", len(req.RuntimeImages), maxList)
	}
	if len(req.OS) != maxOSArch || len(req.Arch) != maxOSArch {
		t.Errorf("os/arch lengths = %d/%d, want %d", len(req.OS), len(req.Arch), maxOSArch)
	}
	if req.DiagnosticsPassed != maxCount {
		t.Errorf("passed = %d, want clamped to %d", req.DiagnosticsPassed, maxCount)
	}
	if req.PanelPort != maxPort {
		t.Errorf("port = %d, want clamped to %d", req.PanelPort, maxPort)
	}
	if req.UptimeSeconds != 0 {
		t.Errorf("uptime = %d, want 0", req.UptimeSeconds)
	}
}

// The wire shape is proto3 json with lowerCamelCase names and int64 as a string
func TestPayloadJSON(t *testing.T) {
	req := BuildPayload(Facts{
		InstallID:     "0123456789abcdef0123456789abcdef",
		Version:       "v1.9.0",
		OS:            "linux",
		Arch:          "arm64",
		DockerVersion: "28.5.2",
		Servers:       []*v1.Server{{ModLoader: v1.ModLoader_MOD_LOADER_FABRIC, McVersion: "1.21.1"}},
		Modules:       []*v1.Module{{TemplateId: "bluemap"}},
		RuntimeImages: []string{"ghcr.io/discohaus/discoruntime:java21"},
		Report:        &v1.DiagnosticReport{PassCount: 20, WarnCount: 2, FailCount: 1},
		PanelPort:     8080,
		Uptime:        3661 * time.Second,
	})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"installId":"0123456789abcdef0123456789abcdef","version":"v1.9.0","os":"linux","arch":"arm64","dockerVersion":"28.5.2",` +
		`"servers":[{"loader":"fabric","mcVersion":"1.21.1","count":1}],"modules":["bluemap"],` +
		`"runtimeImages":["ghcr.io/discohaus/discoruntime:java21"],"diagnosticsPassed":20,"diagnosticsWarned":2,"diagnosticsFailed":1,` +
		`"panelPort":8080,"uptimeSeconds":"3661"}`
	if string(raw) != want {
		t.Errorf("json =\n%s\nwant\n%s", raw, want)
	}
}

// Empty lists encode as arrays, never null
func TestPayloadJSONEmptyLists(t *testing.T) {
	raw, err := json.Marshal(BuildPayload(Facts{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"servers":[]`, `"modules":[]`, `"runtimeImages":[]`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("json %s lacks %s", raw, field)
		}
	}
}
