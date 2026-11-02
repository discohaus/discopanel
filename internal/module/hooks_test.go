package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Docker engine double: every container runs, labels per container,
// and every call is recorded
type fakeEngine struct {
	mu     sync.Mutex
	labels map[string]string // container id to module config hash
	calls  []string
}

var containerPath = regexp.MustCompile(`^/v[0-9.]+/containers/([^/]+)(/[a-z]+)?$`)

func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Api-Version", "1.43")
	if strings.HasSuffix(r.URL.Path, "/_ping") {
		w.Write([]byte("OK"))
		return
	}
	e.mu.Lock()
	e.calls = append(e.calls, r.Method+" "+containerPath.ReplaceAllString(r.URL.Path, "/containers/$1$2"))
	label := ""
	m := containerPath.FindStringSubmatch(r.URL.Path)
	if m != nil {
		label = e.labels[m[1]]
	}
	e.mu.Unlock()

	switch {
	case m != nil && r.Method == http.MethodGet && m[2] == "/json":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"Id":     m[1],
			"State":  map[string]any{"Status": "running", "Running": true},
			"Config": map[string]any{"Labels": map[string]string{docker.LabelModuleConfigHash: label}},
		})
	case m != nil && r.Method == http.MethodPost && m[2] == "/stop":
		w.WriteHeader(http.StatusNoContent)
	case m != nil && r.Method == http.MethodDelete && m[2] == "":
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"message":"not faked"}`, http.StatusInternalServerError)
	}
}

// Calls matching a method and path suffix
func (e *fakeEngine) count(method, suffix string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.calls {
		if strings.HasPrefix(c, method+" ") && strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

// Module manager on a real store and a fake docker engine
func hooksManager(t *testing.T, wake *bool) (*Manager, *fakeEngine) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "modules.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()

	if err := store.CreateServer(ctx, &v1.Server{
		Id: "srv", Name: "srv", ModLoader: v1.ModLoader_MOD_LOADER_VANILLA,
		McVersion: "1.21.1", Status: v1.ServerStatus_SERVER_STATUS_STOPPED, DataPath: t.TempDir(),
	}); err != nil {
		t.Fatalf("create server failed %v", err)
	}
	if wake != nil {
		if err := store.CreateServerProperties(ctx, &v1.ServerProperties{Id: "srv", ServerId: "srv", EnableWakeOnConnect: wake}); err != nil {
			t.Fatalf("properties create failed %v", err)
		}
	}

	running := v1.ModuleStatus_MODULE_STATUS_RUNNING
	udp := func(port int32, proxied, bedrock bool) []*v1.NetworkPort {
		return []*v1.NetworkPort{{Name: "Bedrock", HostPort: port, ContainerPort: 19132, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_UDP, ProxyEnabled: proxied, Bedrock: bedrock}}
	}
	metricsPort := []*v1.NetworkPort{{Name: "Metrics", HostPort: 9225, ContainerPort: 9225, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: true}}
	if err := store.CreateModule(ctx,
		&v1.Module{Id: "geyser", Name: "Geyser", ServerId: "srv", ContainerId: "c1", Status: running, FollowServerLifecycle: true, Ports: udp(19132, true, true)},
		&v1.Module{Id: "geyser-legacy", Name: "Old Geyser", ServerId: "srv", TemplateId: proxy.GeyserTemplateID, ContainerId: "c7", Status: running, FollowServerLifecycle: true, Ports: udp(19134, true, false)},
		&v1.Module{Id: "geyser-direct", Name: "Geyser Direct", ServerId: "srv", ContainerId: "c2", Status: running, FollowServerLifecycle: true, Ports: udp(19133, false, true)},
		&v1.Module{Id: "voice", Name: "Voice", ServerId: "srv", ContainerId: "c6", Status: running, FollowServerLifecycle: true, Ports: udp(24454, true, false)},
		&v1.Module{Id: "exporter", Name: "Exporter", ServerId: "srv", ContainerId: "c3", Status: running, FollowServerLifecycle: true, Ports: metricsPort},
		&v1.Module{Id: "detached", Name: "Detached", ServerId: "srv", ContainerId: "c4", Status: running, FollowServerLifecycle: true, Detached: true},
		&v1.Module{Id: "stopped", Name: "Stopped", ServerId: "srv", ContainerId: "c5", Status: v1.ModuleStatus_MODULE_STATUS_STOPPED, FollowServerLifecycle: true},
	); err != nil {
		t.Fatalf("module create failed %v", err)
	}

	engine := &fakeEngine{labels: map[string]string{}}
	ts := httptest.NewServer(engine)
	t.Cleanup(ts.Close)
	dc, err := docker.NewClient("tcp://"+strings.TrimPrefix(ts.URL, "http://"), logger.New())
	if err != nil {
		t.Fatalf("docker client failed %v", err)
	}
	return NewManager(store, dc, nil, cfg, nil, logger.New()), engine
}

func moduleIDs(mods []*v1.Module) []string {
	var ids []string
	for _, mod := range mods {
		ids = append(ids, mod.Id)
	}
	slices.Sort(ids)
	return ids
}

// Idle stops of wakeable servers spare marked bedrock gateways only
func TestLifecycleStopModulesSparesGatewaysOnIdleStop(t *testing.T) {
	on, off := true, false
	all := []string{"exporter", "geyser", "geyser-direct", "geyser-legacy", "voice"}
	cases := []struct {
		name    string
		wake    *bool
		reason  string
		proxyOn bool
		want    []string
	}{
		{"idle stop wakeable", &on, events.StopReasonAutostop, true, []string{"exporter", "geyser-direct", "voice"}},
		{"manual stop wakeable", &on, events.StopReasonManual, true, all},
		{"scheduled stop wakeable", &on, events.StopReasonScheduled, true, all},
		{"restart wakeable", &on, events.StopReasonRestart, true, all},
		{"idle stop not wakeable", &off, events.StopReasonAutostop, true, all},
		{"idle stop without properties", nil, events.StopReasonAutostop, true, all},
		{"idle stop proxy off", &on, events.StopReasonAutostop, false, all},
		{"untagged event", &on, "", true, all},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := hooksManager(t, tc.wake)
			got := moduleIDs(m.lifecycleStopModules(context.Background(), "srv", tc.reason, tc.proxyOn))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("stopping %v, want %v", got, tc.want)
			}
		})
	}
}

// Running modules of a stopped server, by id
func runningModules(t *testing.T, m *Manager) []string {
	t.Helper()
	mods, err := m.store.ListServerModules(context.Background(), "srv")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, mod := range mods {
		if mod.Status == v1.ModuleStatus_MODULE_STATUS_RUNNING {
			ids = append(ids, mod.Id)
		}
	}
	slices.Sort(ids)
	return ids
}

// Turning wake off retires spared gateways and nothing else
func TestReconcileSparedModules(t *testing.T) {
	ctx := context.Background()
	on := true
	m, engine := hooksManager(t, &on)

	// Wake is on but there is no proxy, so sparing no longer applies
	m.ReconcileSparedModules(ctx, "srv")
	got := runningModules(t, m)
	want := []string{"detached", "exporter", "geyser-direct", "voice"}
	if !slices.Equal(got, want) {
		t.Fatalf("running after reconcile %v, want %v", got, want)
	}
	if n := engine.count("POST", "/stop"); n != 2 {
		t.Fatalf("stopped %d containers, want 2", n)
	}

	// Running servers are left alone
	m2, engine2 := hooksManager(t, nil)
	if err := m2.store.UpdateServerFields(ctx, "srv", map[string]any{"status": v1.ServerStatus_SERVER_STATUS_RUNNING}); err != nil {
		t.Fatal(err)
	}
	m2.ReconcileSparedModules(ctx, "")
	if n := engine2.count("POST", "/stop"); n != 0 {
		t.Fatalf("reconcile touched a running server's modules (%d stops)", n)
	}
}

// Shutdown stops what a stopped server's stop no longer will
func TestStopModulesOfStoppedServers(t *testing.T) {
	on := true
	m, engine := hooksManager(t, &on)
	m.StopModulesOfStoppedServers(context.Background())
	if got := runningModules(t, m); !slices.Equal(got, []string{"detached"}) {
		t.Fatalf("running after shutdown %v, want only the detached module", got)
	}
	if n := engine.count("POST", "/stop"); n != 5 {
		t.Fatalf("stopped %d containers, want 5", n)
	}
}

// Live autostart modules skip the start but still rebuild on drift
func TestRefreshRunningModule(t *testing.T) {
	ctx := context.Background()
	m, engine := hooksManager(t, nil)
	if err := m.store.CreateModuleTemplate(ctx, &v1.ModuleTemplate{Id: "tpl", Name: "tpl", DockerImage: "example/geyser"}); err != nil {
		t.Fatal(err)
	}
	mod, err := m.store.GetModule(ctx, "geyser")
	if err != nil {
		t.Fatal(err)
	}
	mod.TemplateId = "tpl"
	if err := m.store.UpdateModule(ctx, mod); err != nil {
		t.Fatal(err)
	}

	// Current hash: no start, no rebuild
	tpl, _ := m.store.GetModuleTemplate(ctx, "tpl")
	server, err := m.loadServer(ctx, "srv")
	if err != nil {
		t.Fatal(err)
	}
	engine.labels["c1"] = m.docker.DesiredModuleConfigHash(mod, tpl, server, nil, m.config, m.siblingModules(ctx, mod))
	if err := m.refreshRunningModule(ctx, "geyser"); err != nil {
		t.Fatalf("refresh of a current module failed %v", err)
	}
	if engine.count("POST", "/start") != 0 || engine.count("DELETE", "/containers/c1") != 0 {
		t.Fatalf("current module was restarted or rebuilt: %v", engine.calls)
	}

	// Drifted while asleep: old container goes, a rebuild is attempted
	engine.labels["c1"] = "stale"
	_ = m.refreshRunningModule(ctx, "geyser")
	if engine.count("DELETE", "/containers/c1") != 1 {
		t.Fatalf("drifted module was not rebuilt: %v", engine.calls)
	}
}

// Stopped or containerless modules never count as live
func TestModuleLive(t *testing.T) {
	m, _ := hooksManager(t, nil)
	ctx := context.Background()
	for _, mod := range []*v1.Module{
		{Status: v1.ModuleStatus_MODULE_STATUS_STOPPED, ContainerId: "c"},
		{Status: v1.ModuleStatus_MODULE_STATUS_RUNNING},
	} {
		if m.moduleLive(ctx, mod) {
			t.Fatalf("module %+v must not read live", mod)
		}
	}
	if !m.moduleLive(ctx, &v1.Module{Status: v1.ModuleStatus_MODULE_STATUS_RUNNING, ContainerId: "c"}) {
		t.Fatal("running container must read live")
	}
}
