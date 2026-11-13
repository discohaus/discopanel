package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestAllocatedMemoryMB(t *testing.T) {
	cases := []struct {
		name   string
		server *v1.Server
		want   int64
	}{
		{"memory only", &v1.Server{Memory: 4096}, 4096},
		{"lower override keeps memory", &v1.Server{Memory: 4096, DockerOverrides: &v1.DockerOverrides{MemoryLimit: 2048}}, 4096},
		{"higher override wins", &v1.Server{Memory: 4096, DockerOverrides: &v1.DockerOverrides{MemoryLimit: 8192}}, 8192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocatedMemoryMB(tc.server); got != tc.want {
				t.Fatalf("allocatedMemoryMB() = %d, want %d", got, tc.want)
			}
		})
	}
	if got := moduleMemoryMB(&v1.Module{}); got != defaultModuleMemoryMB {
		t.Fatalf("unset module memory = %d, want %d", got, defaultModuleMemoryMB)
	}
}

func TestSumAllocations(t *testing.T) {
	rows := map[memoryKey]int64{
		{memoryOwnerServer, "a"}: 4096,
		{memoryOwnerServer, "b"}: 6144,
		{memoryOwnerModule, "a"}: 512,
	}
	if got := sumAllocations(rows, memoryKey{}); got != 10752 {
		t.Fatalf("all = %d, want 10752", got)
	}
	if got := sumAllocations(rows, memoryKey{memoryOwnerServer, "a"}); got != 6656 {
		t.Fatalf("without server a = %d, want 6656", got)
	}
}

func TestCheckMemoryQuota(t *testing.T) {
	cases := []struct {
		name                     string
		capMB, others, requested int64
		wantErr                  bool
	}{
		{"unlimited", 0, 1 << 20, 1 << 20, false},
		{"fits", 12288, 4096, 6144, false},
		{"exactly full", 12288, 8192, 4096, false},
		{"over", 12288, 10240, 4096, true},
		{"alone over", 2048, 0, 4096, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMemoryQuota(tc.capMB, tc.others, tc.requested)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkMemoryQuota() err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("code = %v, want resource exhausted", connect.CodeOf(err))
			}
		})
	}
}

func TestCheckMemoryQuotaMessage(t *testing.T) {
	err := checkMemoryQuota(12288, 8192, 8192)
	want := "this would allocate 16 GB of the 12 GB memory limit (8 GB in use by other servers and modules)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("message = %v, want %q", err, want)
	}
	if got := formatGB(1536); got != "1.5 GB" {
		t.Fatalf("formatGB(1536) = %q", got)
	}
}

// Server service on a real store and auth manager, docker left out
func quotaService(t *testing.T, configCapMB int64, authEnabled bool) *ServerService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "quota.db")
	cfg.Database.AutoMigrate = true
	cfg.Auth.Local.Enabled = authEnabled
	cfg.Minecraft.MaxTotalMemoryMB = configCapMB
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.SeedSystemRoles(); err != nil {
		t.Fatalf("role seed failed %v", err)
	}
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("enforcer open failed %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("policy seed failed %v", err)
	}
	am, err := auth.NewManager(store, enforcer, &cfg.Auth)
	if err != nil {
		t.Fatalf("auth manager failed %v", err)
	}
	log := logger.New()
	return &ServerService{
		store: store, config: cfg, authManager: am, enforcer: enforcer, log: log,
		quota: NewMemoryQuota(store, cfg, am, log),
	}
}

func userCtx(role string) context.Context {
	return auth.WithUser(context.Background(), &v1.User{Id: role, Username: role, Roles: []string{role}})
}

// Persists a server through the quota like CreateServer does
func createWithQuota(ctx context.Context, s *ServerService, id string, memory int32) error {
	server := &v1.Server{Id: id, Name: id, Memory: memory, McVersion: "1.21.1", ModLoader: v1.ModLoader_MOD_LOADER_VANILLA}
	release, err := s.quota.reserveServer(ctx, server, -1)
	if err != nil {
		return err
	}
	defer release()
	return s.store.CreateServer(ctx, server)
}

// Applies a memory change through the quota like UpdateServer does
func updateWithQuota(ctx context.Context, s *ServerService, id string, memory int32) error {
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return err
	}
	prev := allocatedMemoryMB(server)
	server.Memory = memory
	release, err := s.quota.reserveServer(ctx, server, prev)
	if err != nil {
		return err
	}
	defer release()
	return s.store.UpdateServer(ctx, server)
}

func setQuota(t *testing.T, s *ServerService, req *v1.UpdateMemoryQuotaRequest) *v1.GetMemoryQuotaResponse {
	t.Helper()
	res, err := s.UpdateMemoryQuota(userCtx("admin"), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("update quota: %v", err)
	}
	return res.Msg.Quota
}

func wantExhausted(t *testing.T, err error) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("err = %v, want resource exhausted", err)
	}
}

func TestMemoryQuotaCreate(t *testing.T) {
	s := quotaService(t, 12288, true)
	ctx := userCtx("user")
	if err := createWithQuota(ctx, s, "a", 4096); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := createWithQuota(ctx, s, "b", 6144); err != nil {
		t.Fatalf("second create: %v", err)
	}
	err := createWithQuota(ctx, s, "c", 4096)
	wantExhausted(t, err)
	if !strings.Contains(err.Error(), "14 GB of the 12 GB memory limit (10 GB in use") {
		t.Fatalf("message = %v", err)
	}
	if err := createWithQuota(ctx, s, "d", 2048); err != nil {
		t.Fatalf("create filling the cap: %v", err)
	}
}

func TestMemoryQuotaUpdate(t *testing.T) {
	s := quotaService(t, 12288, true)
	ctx := userCtx("user")
	for id, mb := range map[string]int32{"a": 4096, "b": 6144} {
		if err := createWithQuota(ctx, s, id, mb); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := updateWithQuota(ctx, s, "a", 6144); err != nil {
		t.Fatalf("raise within cap: %v", err)
	}
	wantExhausted(t, updateWithQuota(ctx, s, "a", 7168))

	// Lowering the cap below usage still lets servers shrink or stay
	setQuota(t, s, &v1.UpdateMemoryQuotaRequest{MaxTotalMb: 8192})
	if err := updateWithQuota(ctx, s, "b", 6144); err != nil {
		t.Fatalf("unchanged memory over cap: %v", err)
	}
	if err := updateWithQuota(ctx, s, "b", 4096); err != nil {
		t.Fatalf("decrease over cap: %v", err)
	}
	wantExhausted(t, updateWithQuota(ctx, s, "b", 5120))
}

func TestMemoryQuotaAdminBypass(t *testing.T) {
	s := quotaService(t, 8192, true)
	if err := createWithQuota(userCtx("user"), s, "a", 6144); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wantExhausted(t, createWithQuota(userCtx("user"), s, "b", 4096))
	if err := createWithQuota(userCtx("admin"), s, "b", 4096); err != nil {
		t.Fatalf("admin create over cap: %v", err)
	}
	if err := updateWithQuota(userCtx("admin"), s, "a", 8192); err != nil {
		t.Fatalf("admin raise over cap: %v", err)
	}
}

// Auth off makes every caller the system admin, the cap must still bind
func TestMemoryQuotaAuthDisabledEnforces(t *testing.T) {
	s := quotaService(t, 8192, false)
	ctx := auth.WithUser(context.Background(), s.authManager.SystemUser())
	if err := createWithQuota(ctx, s, "a", 6144); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wantExhausted(t, createWithQuota(ctx, s, "b", 4096))
	q, err := s.memoryQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !q.AuthDisabled {
		t.Fatal("quota does not report auth disabled")
	}
}

func TestMemoryQuotaUnlimited(t *testing.T) {
	s := quotaService(t, 0, true)
	ctx := userCtx("user")
	for i := range 4 {
		if err := createWithQuota(ctx, s, fmt.Sprintf("s%d", i), 65536); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if err := updateWithQuota(ctx, s, "s0", 131072); err != nil {
		t.Fatalf("raise without cap: %v", err)
	}
}

func TestMemoryQuotaSettingOverridesConfig(t *testing.T) {
	s := quotaService(t, 4096, true)
	ctx := userCtx("user")
	wantExhausted(t, createWithQuota(ctx, s, "a", 6144))

	q := setQuota(t, s, &v1.UpdateMemoryQuotaRequest{MaxTotalMb: 0})
	if q.MaxTotalMb != 0 || !q.Overridden || q.ConfigMaxTotalMb != 4096 {
		t.Fatalf("quota after unlimited = %+v", q)
	}
	if err := createWithQuota(ctx, s, "a", 6144); err != nil {
		t.Fatalf("create with setting unlimited: %v", err)
	}

	q = setQuota(t, s, &v1.UpdateMemoryQuotaRequest{UseConfig: true})
	if q.MaxTotalMb != 4096 || q.Overridden || q.AllocatedMb != 6144 {
		t.Fatalf("quota after reset = %+v", q)
	}
	wantExhausted(t, createWithQuota(ctx, s, "b", 1024))

	// Odd MB values round-trip exactly
	if q := setQuota(t, s, &v1.UpdateMemoryQuotaRequest{MaxTotalMb: 10000}); q.MaxTotalMb != 10000 {
		t.Fatalf("stored cap = %d, want 10000", q.MaxTotalMb)
	}

	if _, err := s.UpdateMemoryQuota(userCtx("admin"), connect.NewRequest(&v1.UpdateMemoryQuotaRequest{MaxTotalMb: -1})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative cap err = %v, want invalid argument", err)
	}
}

// Module containers draw from the same cap as servers
func TestMemoryQuotaCountsModules(t *testing.T) {
	s := quotaService(t, 8192, true)
	ctx := userCtx("user")
	if err := createWithQuota(ctx, s, "a", 4096); err != nil {
		t.Fatalf("seed: %v", err)
	}
	module := &v1.Module{Id: "m", Name: "m", ServerId: "a", TemplateId: "t", Memory: 2048}
	release, err := s.quota.reserveModule(ctx, module, -1)
	if err != nil {
		t.Fatalf("module within cap: %v", err)
	}
	if err := s.store.CreateModule(ctx, module); err != nil {
		t.Fatal(err)
	}
	release()

	wantExhausted(t, createWithQuota(ctx, s, "b", 4096))
	module.Memory = 6144
	if _, err := s.quota.reserveModule(ctx, module, 2048); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("module raise err = %v, want resource exhausted", err)
	}
	q, err := s.memoryQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.AllocatedMb != 6144 || q.ModulesMb != 2048 {
		t.Fatalf("quota = %+v, want 6144 allocated with 2048 in modules", q)
	}
}

// Concurrent creates must never overshoot the cap together
func TestMemoryQuotaConcurrentCreates(t *testing.T) {
	s := quotaService(t, 12288, true)
	ctx := userCtx("user")
	const attempts = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, rejected := 0, 0
	for i := range attempts {
		wg.Go(func() {
			err := createWithQuota(ctx, s, fmt.Sprintf("s%d", i), 4096)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case connect.CodeOf(err) == connect.CodeResourceExhausted:
				rejected++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	if created != 3 || rejected != attempts-3 {
		t.Fatalf("created %d rejected %d, want 3 and %d", created, rejected, attempts-3)
	}
	assertWithinCap(t, s, 12288)
}

// Concurrent updates of one server racing creates never overshoot the cap
func TestMemoryQuotaConcurrentUpdatesSameServer(t *testing.T) {
	s := quotaService(t, 12288, true)
	ctx := userCtx("user")
	if err := createWithQuota(ctx, s, "a", 2048); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			// Alternating targets so holds for "a" differ in size
			_ = updateWithQuota(ctx, s, "a", int32(6144+(i%2)*2048))
		})
		wg.Go(func() {
			_ = createWithQuota(ctx, s, fmt.Sprintf("s%d", i), 4096)
		})
	}
	wg.Wait()
	assertWithinCap(t, s, 12288)
}

func assertWithinCap(t *testing.T, s *ServerService, capMB int64) {
	t.Helper()
	rows, err := s.quota.allocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total := sumAllocations(rows, memoryKey{}); total > capMB {
		t.Fatalf("allocated %d MB over the %d MB cap", total, capMB)
	}
}

// Two holds on one server count at the larger, so a third request sees it
func TestMemoryQuotaHoldsPerRequest(t *testing.T) {
	q := &MemoryQuota{}
	x := memoryKey{memoryOwnerServer, "x"}
	rows := func() (map[memoryKey]int64, error) { return map[memoryKey]int64{x: 2048}, nil }

	releaseA, err := q.hold(rows, x, 8192, 12288, true)
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := q.hold(rows, x, 6144, 12288, true)
	if err != nil {
		t.Fatal(err)
	}
	// A may still save last, so 8 GB of x stays reserved
	y := memoryKey{memoryOwnerServer, "y"}
	if _, err := q.hold(rows, y, 6144, 12288, true); err == nil {
		t.Fatal("larger hold on x was under counted")
	}
	releaseB()
	if _, err := q.hold(rows, y, 6144, 12288, true); err == nil {
		t.Fatal("releasing B dropped A's hold")
	}
	releaseA()
	releaseY, err := q.hold(rows, y, 6144, 12288, true)
	if err != nil {
		t.Fatalf("released holds still counted: %v", err)
	}
	releaseY()
}

// Held memory blocks others until released, a failed request frees it
func TestMemoryQuotaHoldUntilRelease(t *testing.T) {
	q := &MemoryQuota{}
	none := func() (map[memoryKey]int64, error) { return map[memoryKey]int64{}, nil }
	key := func(id string) memoryKey { return memoryKey{memoryOwnerServer, id} }
	release, err := q.hold(none, key("a"), 8192, 12288, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.hold(none, key("b"), 8192, 12288, true); err == nil {
		t.Fatal("pending hold was not counted")
	}
	// Exempt holds still count against others
	releaseB, err := q.hold(none, key("b"), 8192, 12288, false)
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
	release()
	if _, err := q.hold(none, key("c"), 12288, 12288, true); err != nil {
		t.Fatalf("released hold still counted: %v", err)
	}

	failing := func() (map[memoryKey]int64, error) { return nil, errors.New("db down") }
	if _, err := q.hold(failing, key("d"), 1024, 12288, true); err == nil {
		t.Fatal("list error was swallowed")
	}
}

// Serves the mojang version manifest and metadata through the index
func fakeMojangMeta(t *testing.T, mcVersion string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mojang/meta/mc/game/version_manifest_v2.json":
			json.NewEncoder(w).Encode(map[string]any{
				"latest":   map[string]string{"release": mcVersion, "snapshot": mcVersion},
				"versions": []map[string]string{{"id": mcVersion, "type": "release", "url": srv.URL + "/meta/" + mcVersion + ".json"}},
			})
		case "/meta/" + mcVersion + ".json":
			json.NewEncoder(w).Encode(map[string]any{"javaVersion": map[string]any{"component": "java-runtime-delta", "majorVersion": 21}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	settings := hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: srv.URL, IndexEnabled: true, InstallID: "0123456789abcdef0123456789abcdef"}
	if err := hub.Configure(settings); err != nil {
		t.Fatalf("configure hub: %v", err)
	}
	t.Cleanup(func() {
		hub.Configure(hub.Settings{SupportBase: hub.DefaultSupportBase, IndexBase: hub.DefaultIndexBase})
	})
}

// Grabs an os assigned port then frees it
func quotaFreePort(t *testing.T) int32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return int32(ln.Addr().(*net.TCPAddr).Port)
}

// Server service wired for the real create and update RPCs, no docker
func quotaRPCService(t *testing.T, capMB int64) *ServerService {
	t.Helper()
	s := quotaService(t, capMB, true)
	s.config.Storage.DataDir = t.TempDir()
	s.config.Server.Host = "127.0.0.1"
	s.config.Server.Port = strconv.Itoa(int(quotaFreePort(t)))
	s.config.Proxy.Enabled = false
	pm, err := proxy.NewManager(s.store, nil, s.config, s.log)
	if err != nil {
		t.Fatalf("proxy manager: %v", err)
	}
	t.Cleanup(func() { _ = pm.Stop() })
	s.proxy = pm
	s.rec = metrics.NewRecorder(s.store, s.log)
	fakeMojangMeta(t, "1.21.1")
	return s
}

// The quota runs inside the real CreateServer and UpdateServer handlers
func TestMemoryQuotaThroughRPCs(t *testing.T) {
	s := quotaRPCService(t, 12288)
	ctx := userCtx("user")

	create := func(name string, memory int32) (*v1.Server, error) {
		res, err := s.CreateServer(ctx, connect.NewRequest(&v1.CreateServerRequest{
			Name: name, McVersion: "1.21.1", Port: quotaFreePort(t), Memory: memory,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.Server, nil
	}

	a, err := create("a", 4096)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := create("b", 6144)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	_, err = create("c", 4096)
	wantExhausted(t, err)

	// Raising memory past the cap is refused and nothing persists
	_, err = s.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{Id: a.Id, Memory: 8192}))
	wantExhausted(t, err)
	if got, _ := s.store.GetServer(ctx, a.Id); got.Memory != 4096 {
		t.Fatalf("rejected update persisted memory %d", got.Memory)
	}

	// A docker memory override is the container limit, it counts too
	_, err = s.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: b.Id, DockerOverrides: &v1.DockerOverrides{MemoryLimit: 16384},
	}))
	wantExhausted(t, err)
	if got, _ := s.store.GetServer(ctx, b.Id); got.GetDockerOverrides().GetMemoryLimit() != 0 {
		t.Fatal("rejected override persisted")
	}
	if _, err := s.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: b.Id, DockerOverrides: &v1.DockerOverrides{MemoryLimit: 8192},
	})); err != nil {
		t.Fatalf("override within cap: %v", err)
	}

	// Lowering is always fine, and frees headroom for a new server
	if _, err := s.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{Id: a.Id, Memory: 2048})); err != nil {
		t.Fatalf("lower a: %v", err)
	}
	if _, err := create("d", 2048); err != nil {
		t.Fatalf("create d after lowering: %v", err)
	}

	host, err := s.GetHostMemory(ctx, connect.NewRequest(&v1.GetHostMemoryRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if host.Msg.MaxTotalMb != 12288 || host.Msg.CapExempt {
		t.Fatalf("host memory = %+v", host.Msg)
	}
	var total int64
	for _, a := range host.Msg.Allocations {
		total += int64(a.Memory)
	}
	if total != 12288 {
		t.Fatalf("reported allocations %d, want 12288 with the override counted", total)
	}
}
