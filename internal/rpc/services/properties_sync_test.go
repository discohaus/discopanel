package services

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
func i32p(i int32) *int32   { return &i }

// Every annotated prop keyed by property key
func propsByKey() map[string]protometa.Prop {
	out := map[string]protometa.Prop{}
	for _, p := range protometa.Props((&v1.ServerProperties{}).ProtoReflect().Descriptor()) {
		out[p.Field.JSONName()] = p
	}
	return out
}

func TestSyncExclusionTable(t *testing.T) {
	props := propsByKey()
	cases := []struct {
		key      string
		excluded bool
	}{
		// Identity, ports, hosts and secrets
		{"serverPort", true},
		{"rconPort", true},
		{"queryPort", true},
		{"managementServerPort", true},
		{"jmxHost", true},
		{"managementServerHost", true},
		{"serverName", true},
		{"level", true},
		{"seed", true},
		{"rconPassword", true},
		{"managementServerSecret", true},
		{"cfApiKey", true},
		{"maxPlayers", true},
		{"initMemory", true},
		{"maxMemory", true},
		{"eula", true},
		{"forceProvision", true},
		{"uid", true},
		{"gid", true},
		{"icon", true},
		// Launch, code and command shaped
		{"customServer", true},
		{"customJarExec", true},
		{"customServerProperties", true},
		{"jvmOpts", true},
		{"jvmXxOpts", true},
		{"jvmDdOpts", true},
		{"extraArgs", true},
		{"enableJmx", true},
		{"enableAgent", true},
		{"useAikarFlags", true},
		{"useZgcFlags", true},
		{"rconCmdsStartup", true},
		{"rconCmdsOnConnect", true},
		{"rconCmdsFirstConnect", true},
		{"rconCmdsOnDisconnect", true},
		{"rconCmdsLastDisconnect", true},
		// Auth and panel plumbing
		{"onlineMode", true},
		{"enforceSecureProfile", true},
		{"preventProxyConnections", true},
		{"enableRcon", true},
		{"acceptsTransfers", true},
		// Proxy routing and pack sources
		{"enableProxyProtocol", true},
		{"proxyPreserveHostname", true},
		{"enableWakeOnConnect", true},
		{"modrinthModpack", true},
		{"cfSlug", true},
		{"forgeVersion", true},
		// Shared settings
		{"whitelist", false},
		{"enableWhitelist", false},
		{"enforceWhitelist", false},
		{"overrideWhitelist", false},
		{"ops", false},
		{"motd", false},
		{"difficulty", false},
		{"viewDistance", false},
		{"resourcePack", false},
		{"tz", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			p, ok := props[tc.key]
			if !ok {
				t.Fatalf("no property %s", tc.key)
			}
			reason := syncExclusion(p)
			if got := reason != ""; got != tc.excluded {
				t.Fatalf("excluded = %v (%q), want %v", got, reason, tc.excluded)
			}
		})
	}
}

// A new ServerProperties field must be classified before it can sync
func TestEveryPropertyIsClassified(t *testing.T) {
	props := propsByKey()
	for key, p := range props {
		if syncExclusion(p) == syncReasonUnclassified {
			t.Errorf("%s is not classified, add it to syncAllowedKeys or syncExcludedKeys", key)
		}
	}
	for key := range syncAllowedKeys {
		p, ok := props[key]
		if !ok {
			t.Errorf("allowed key %s is not a property", key)
			continue
		}
		if _, dup := syncExcludedKeys[key]; dup {
			t.Errorf("%s is both allowed and excluded", key)
		}
		if reason := syncExclusion(p); reason != "" {
			t.Errorf("allowed key %s is excluded by a rule: %s", key, reason)
		}
	}
	for key := range syncExcludedKeys {
		if _, ok := props[key]; !ok {
			t.Errorf("excluded key %s is not a property", key)
		}
	}
	for key := range syncSensitiveKeys {
		if !syncAllowedKeys[key] {
			t.Errorf("sensitive key %s is not syncable", key)
		}
	}
	for _, group := range syncLinkedGroups {
		for _, key := range group {
			if !syncAllowedKeys[key] {
				t.Errorf("linked key %s is not syncable", key)
			}
		}
	}
}

// Camel-case words that mark per-server or code carrying values
var riskyWord = regexp.MustCompile(`(^|[a-z])(Port|Host|Password|Secret|Token|ApiKey|Opts|Args|Cmds|Exec|Jmx|Agent|Flags|Icon)([A-Z]|$)|^(port|host|icon)`)

// Identity and code shaped fields stay excluded without anyone remembering to
func TestSyncExclusionCoversRiskyShapedFields(t *testing.T) {
	for key, p := range propsByKey() {
		risky := riskyWord.MatchString(key) || p.Meta.Input == "password" ||
			p.Meta.System || p.Meta.Ephemeral || p.Meta.Category == "proxy"
		if risky && syncExclusion(p) == "" {
			t.Errorf("%s looks server specific or code carrying but would sync", key)
		}
	}
	for _, key := range []string{"rconPort", "queryPort", "managementServerHost", "cfApiKey", "jvmXxOpts", "extraArgs", "rconCmdsStartup", "customJarExec", "enableJmx", "enableAgent", "useZgcFlags", "icon"} {
		if !riskyWord.MatchString(key) {
			t.Errorf("pattern misses %s", key)
		}
	}
}

func TestSyncableFieldsUnsetAndBlankDefaults(t *testing.T) {
	defaults := &v1.ServerProperties{
		Motd:          strp("Hello"),
		RconPort:      i32p(1234),
		Whitelist:     strp(" , \n ,"),
		Ops:           strp("Alice"),
		BugReportLink: strp("   "),
	}
	fields, excluded := syncableFields(defaults)
	keys := map[string]bool{}
	for _, f := range fields {
		keys[f.key] = true
		if f.key == "ops" && !f.list {
			t.Error("ops not flagged as list")
		}
	}
	if !keys["motd"] || !keys["ops"] {
		t.Fatalf("set defaults missing from fields: %v", keys)
	}
	for _, k := range []string{"rconPort", "difficulty", "whitelist", "bugReportLink"} {
		if keys[k] {
			t.Errorf("%s offered for syncing", k)
		}
	}
	reasons := map[string]string{}
	for _, e := range excluded {
		reasons[e.Key] = e.Reason
	}
	for _, k := range []string{"difficulty", "whitelist", "bugReportLink"} {
		if reasons[k] != syncReasonUnset {
			t.Errorf("%s reason = %q, want unset", k, reasons[k])
		}
	}
	if reasons["rconPort"] != syncReasonPort {
		t.Errorf("rconPort reason = %q, want port", reasons["rconPort"])
	}
}

func TestSyncLinkedFields(t *testing.T) {
	defaults := &v1.ServerProperties{ResourcePack: strp("https://example.com/pack.zip"), Motd: strp("Hi")}
	all, excluded := syncableFields(defaults)
	selected, err := selectSyncFields(all, excluded, []string{"resourcePack"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range selected {
		keys = append(keys, f.key)
	}
	if !slices.Equal(keys, []string{"resourcePackId", "resourcePack", "resourcePackSha1"}) {
		t.Fatalf("selected = %v, want pack with partners", keys)
	}
	current := &v1.ServerProperties{ResourcePack: strp("https://old/pack.zip"), ResourcePackSha1: strp("abc")}
	updates := syncUpdates(diffSyncFields(current, selected, v1.ListSyncMode_LIST_SYNC_MODE_ADD))
	want := map[string]string{"resourcePack": "https://example.com/pack.zip", "resourcePackSha1": ""}
	if len(updates) != len(want) || updates["resourcePack"] != want["resourcePack"] || updates["resourcePackSha1"] != "" {
		t.Fatalf("updates = %v, want %v", updates, want)
	}
	// Without a set partner the unset fields stay excluded
	lone, _ := syncableFields(&v1.ServerProperties{Motd: strp("Hi")})
	for _, f := range lone {
		if f.key == "resourcePackSha1" {
			t.Error("unset partner offered without its group")
		}
	}
}

func TestSyncDefaultsVersion(t *testing.T) {
	a, _ := syncableFields(&v1.ServerProperties{Motd: strp("Hi")})
	b, _ := syncableFields(&v1.ServerProperties{Motd: strp("Hi")})
	c, _ := syncableFields(&v1.ServerProperties{Motd: strp("Hello")})
	if syncDefaultsVersion(a) != syncDefaultsVersion(b) {
		t.Error("equal defaults gave different versions")
	}
	if syncDefaultsVersion(a) == syncDefaultsVersion(c) {
		t.Error("edited defaults kept the same version")
	}
}

func TestSelectSyncFields(t *testing.T) {
	defaults := &v1.ServerProperties{Motd: strp("Hello"), Difficulty: strp("hard"), Whitelist: strp("Alice")}
	all, excluded := syncableFields(defaults)
	cases := []struct {
		name    string
		keys    []string
		want    []string
		wantErr string
	}{
		{"empty means all", nil, []string{"motd", "difficulty", "whitelist"}, ""},
		{"declaration order and dedupe", []string{"whitelist", "motd", "whitelist"}, []string{"motd", "whitelist"}, ""},
		{"excluded key", []string{"motd", "rconPort"}, nil, "cannot be synced"},
		{"unset default", []string{"pvp"}, nil, "cannot be synced"},
		{"unknown key", []string{"nope"}, nil, "unknown property"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectSyncFields(all, excluded, tc.keys)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, f := range got {
				keys = append(keys, f.key)
			}
			if !slices.Equal(keys, tc.want) {
				t.Fatalf("keys = %v, want %v", keys, tc.want)
			}
		})
	}
}

func TestMergePlayerList(t *testing.T) {
	add := v1.ListSyncMode_LIST_SYNC_MODE_ADD
	mirror := v1.ListSyncMode_LIST_SYNC_MODE_MIRROR
	cases := []struct {
		name                   string
		current, defaults      string
		mode                   v1.ListSyncMode
		merged, added, removed []string
	}{
		{"add appends missing", "carol", "Alice,Bob", add, []string{"carol", "Alice", "Bob"}, []string{"Alice", "Bob"}, nil},
		{"add keeps extras", "alice,carol", "Alice", add, []string{"alice", "carol"}, nil, nil},
		{"unspecified is add", "carol", "Alice", v1.ListSyncMode_LIST_SYNC_MODE_UNSPECIFIED, []string{"carol", "Alice"}, []string{"Alice"}, nil},
		{"add into empty", "", "Alice\nBob", add, []string{"Alice", "Bob"}, []string{"Alice", "Bob"}, nil},
		{"add drops duplicate current", "carol,Carol", "carol", add, []string{"carol"}, nil, nil},
		{"mirror replaces", "carol,Alice", "Alice,Bob", mirror, []string{"Alice", "Bob"}, []string{"Bob"}, []string{"carol"}},
		{"mirror same set other order", "bob, alice", "Alice,Bob", mirror, []string{"Alice", "Bob"}, nil, nil},
		{"mirror dedupes defaults", "", "Alice,alice", mirror, []string{"Alice"}, []string{"Alice"}, nil},
		{"mirror blank default never wipes", "carol,dave", " , \n", mirror, []string{"carol", "dave"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, added, removed := mergePlayerList(splitPlayerList(tc.current), splitPlayerList(tc.defaults), tc.mode)
			if !slices.Equal(merged, tc.merged) || !slices.Equal(added, tc.added) || !slices.Equal(removed, tc.removed) {
				t.Fatalf("got merged=%v added=%v removed=%v, want %v %v %v", merged, added, removed, tc.merged, tc.added, tc.removed)
			}
		})
	}
}

func TestDiffSyncFields(t *testing.T) {
	defaults := &v1.ServerProperties{
		Motd:            strp("Hello"),
		Difficulty:      strp("hard"),
		Pvp:             boolp(false),
		ViewDistance:    i32p(12),
		Whitelist:       strp("Alice,Bob"),
		EnableWhitelist: boolp(true),
	}
	fields, _ := syncableFields(defaults)
	cases := []struct {
		name    string
		current *v1.ServerProperties
		mode    v1.ListSyncMode
		want    map[string]string // key to new value
		oldSet  map[string]bool
	}{
		{
			name:    "everything matches",
			current: &v1.ServerProperties{Motd: strp("Hello"), Difficulty: strp("hard"), Pvp: boolp(false), ViewDistance: i32p(12), Whitelist: strp("bob,alice"), EnableWhitelist: boolp(true)},
			want:    map[string]string{},
		},
		{
			name:    "unset fields differ",
			current: &v1.ServerProperties{Motd: strp("Hello"), Difficulty: strp("hard"), Pvp: boolp(false), ViewDistance: i32p(12), EnableWhitelist: boolp(true)},
			want:    map[string]string{"whitelist": "Alice,Bob"},
			oldSet:  map[string]bool{"whitelist": false},
		},
		{
			name:    "scalars and list add",
			current: &v1.ServerProperties{Motd: strp("Mine"), Difficulty: strp("easy"), Pvp: boolp(true), ViewDistance: i32p(8), Whitelist: strp("carol"), EnableWhitelist: boolp(false)},
			want:    map[string]string{"motd": "Hello", "difficulty": "hard", "pvp": "false", "viewDistance": "12", "whitelist": "carol,Alice,Bob", "enableWhitelist": "true"},
			oldSet:  map[string]bool{"motd": true},
		},
		{
			name:    "list mirror",
			mode:    v1.ListSyncMode_LIST_SYNC_MODE_MIRROR,
			current: &v1.ServerProperties{Motd: strp("Hello"), Difficulty: strp("hard"), Pvp: boolp(false), ViewDistance: i32p(12), Whitelist: strp("carol,alice"), EnableWhitelist: boolp(true)},
			want:    map[string]string{"whitelist": "Alice,Bob"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes := diffSyncFields(tc.current, fields, tc.mode)
			got := syncUpdates(changes)
			if len(got) != len(tc.want) {
				t.Fatalf("updates = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			for _, c := range changes {
				if want, ok := tc.oldSet[c.Key]; ok && c.OldSet != want {
					t.Errorf("%s old set = %v, want %v", c.Key, c.OldSet, want)
				}
			}
		})
	}
}

func TestSyncNotes(t *testing.T) {
	removal := []*v1.DefaultsSyncChange{{Key: "whitelist", NewValue: "Alice", Removed: []string{"carol"}}}
	if notes := syncNotes(&v1.ServerProperties{}, removal); len(notes) != 1 {
		t.Fatalf("notes = %v, want lingering whitelist note", notes)
	}
	if notes := syncNotes(&v1.ServerProperties{OverrideWhitelist: boolp(true)}, removal); len(notes) != 0 {
		t.Fatalf("notes = %v, override regenerates the file", notes)
	}
	// Override turned on by the same sync counts
	withOverride := append(slices.Clone(removal), &v1.DefaultsSyncChange{Key: "overrideWhitelist", NewValue: "true"})
	if notes := syncNotes(&v1.ServerProperties{}, withOverride); len(notes) != 0 {
		t.Fatalf("notes = %v, override set by sync", notes)
	}
	ops := []*v1.DefaultsSyncChange{{Key: "ops", NewValue: "Alice", Removed: []string{"carol"}}}
	if notes := syncNotes(&v1.ServerProperties{}, ops); len(notes) != 1 {
		t.Fatalf("notes = %v, want ops note", notes)
	}
	if notes := syncNotes(&v1.ServerProperties{}, []*v1.DefaultsSyncChange{{Key: "whitelist", NewValue: "a,b", Added: []string{"b"}}}); len(notes) != 0 {
		t.Fatalf("notes = %v, additions need no note", notes)
	}
}

func TestSyncRestartPlan(t *testing.T) {
	running := func(status v1.ServerStatus) *v1.Server {
		return &v1.Server{Id: "s", ContainerId: "c", Status: status}
	}
	cases := []struct {
		name       string
		server     *v1.Server
		busy       bool
		restartNow bool
		canRestart bool
		want       v1.DefaultsSyncRestart
	}{
		{"stopped", &v1.Server{Status: v1.ServerStatus_SERVER_STATUS_STOPPED}, false, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEXT_START},
		{"stopped with container", running(v1.ServerStatus_SERVER_STATUS_STOPPED), false, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEXT_START},
		{"running next start mode", running(v1.ServerStatus_SERVER_STATUS_RUNNING), false, false, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEEDED},
		{"running restart now", running(v1.ServerStatus_SERVER_STATUS_RUNNING), false, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED},
		{"running without lifecycle", running(v1.ServerStatus_SERVER_STATUS_RUNNING), false, true, false, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEEDED},
		{"paused restart now", running(v1.ServerStatus_SERVER_STATUS_PAUSED), false, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED},
		{"busy running", running(v1.ServerStatus_SERVER_STATUS_RUNNING), true, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_BUSY},
		{"busy stopped", &v1.Server{Status: v1.ServerStatus_SERVER_STATUS_STOPPING}, true, false, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_BUSY},
		{"provisioning", &v1.Server{Status: v1.ServerStatus_SERVER_STATUS_PROVISIONING}, true, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START},
		{"creating", &v1.Server{Status: v1.ServerStatus_SERVER_STATUS_CREATING}, false, false, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START},
		{"starting in flight", running(v1.ServerStatus_SERVER_STATUS_STARTING), true, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START},
		{"starting stale status", running(v1.ServerStatus_SERVER_STATUS_STARTING), false, true, true, v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, note := syncRestartPlan(tc.server, tc.busy, tc.restartNow, tc.canRestart)
			if got != tc.want || note == "" {
				t.Fatalf("plan = %v (%q), want %v", got, note, tc.want)
			}
		})
	}
}

// Records restarts and tracks how many overlap
type fakeLifecycle struct {
	mu        sync.Mutex
	busy      map[string]bool
	restarted []string
	active    atomic.Int32
	peak      atomic.Int32
	hold      time.Duration
	done      chan string
}

func newFakeLifecycle(hold time.Duration, busy ...string) *fakeLifecycle {
	f := &fakeLifecycle{busy: map[string]bool{}, hold: hold, done: make(chan string, 64)}
	for _, id := range busy {
		f.busy[id] = true
	}
	return f
}

func (f *fakeLifecycle) Busy(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busy[id]
}

func (f *fakeLifecycle) Restart(ctx context.Context, id string) error {
	n := f.active.Add(1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(f.hold)
	f.active.Add(-1)
	f.mu.Lock()
	f.restarted = append(f.restarted, id)
	f.mu.Unlock()
	f.done <- id
	return nil
}

func (f *fakeLifecycle) waitRestarts(t *testing.T, n int) []string {
	t.Helper()
	var ids []string
	for range n {
		select {
		case id := <-f.done:
			ids = append(ids, id)
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d restarts ran", len(ids), n)
		}
	}
	slices.Sort(ids)
	return ids
}

func TestRunSyncRestartsThrottles(t *testing.T) {
	lc := newFakeLifecycle(20 * time.Millisecond)
	ids := []string{"a", "b", "c", "d", "e"}
	done := runSyncRestarts(context.Background(), lc, ids, 2, logger.New())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restart pool never finished")
	}
	if got := lc.peak.Load(); got > 2 {
		t.Errorf("peak concurrent restarts = %d, want at most 2", got)
	}
	if len(lc.restarted) != len(ids) {
		t.Errorf("restarted = %v, want all of %v", lc.restarted, ids)
	}
}

// Grants keyed by resource, action and object, errors for chosen objects
type fakeEnforcer struct {
	grants map[string]bool
	fail   map[string]bool
}

func (f fakeEnforcer) Enforce(roles []string, resource optionsv1.ResourceType, action optionsv1.ActionType, objectID string) (bool, error) {
	if f.fail[objectID] {
		return false, errors.New("policy store unavailable")
	}
	return f.grants[protometa.Name(resource)+"/"+protometa.Name(action)+"/"+objectID], nil
}

func TestSyncAccessFor(t *testing.T) {
	enforcer := fakeEnforcer{grants: map[string]bool{
		"servers/read/a":             true,
		"server_properties/update/a": true,
		"servers/read/b":             true,
		"server_properties/update/d": true,
	}}
	ctx := auth.WithUser(context.Background(), &v1.User{Id: "u", Roles: []string{"r"}})
	want := map[string]syncAccess{
		"a": {visible: true, editable: true},
		"b": {visible: true, editable: false},
		"c": {visible: false, editable: false},
		"d": {visible: true, editable: true},
	}
	for id, w := range want {
		got, err := syncAccessFor(ctx, enforcer, &v1.Server{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("%s access = %+v, want %+v", id, got, w)
		}
	}
	if _, err := syncAccessFor(context.Background(), enforcer, &v1.Server{Id: "a"}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous err = %v, want unauthenticated", err)
	}
	if _, err := syncAccessFor(ctx, nil, &v1.Server{Id: "a"}); connect.CodeOf(err) != connect.CodeInternal {
		t.Errorf("nil enforcer err = %v, want internal", err)
	}
}

// Real store and enforcer behind the properties service
func syncRig(t *testing.T) (*PropertiesService, *storage.Store, *rbac.Enforcer) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "sync.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("enforcer open failed %v", err)
	}
	log := logger.New()
	svc := NewPropertiesService(store, cfg, nil, nil, enforcer, metrics.NewRecorder(store, log), log)
	return svc, store, enforcer
}

func TestNewPropertiesServiceAvoidsTypedNil(t *testing.T) {
	svc := NewPropertiesService(nil, nil, nil, nil, nil, nil, logger.New())
	if svc.enforcer != nil || svc.restarter != nil {
		t.Fatalf("nil dependencies became non-nil interfaces: enforcer=%v restarter=%v", svc.enforcer, svc.restarter)
	}
}

// Seeds defaults plus servers sharing one starting state
func seedSyncServers(t *testing.T, store *storage.Store, servers ...*v1.Server) {
	t.Helper()
	ctx := context.Background()
	defaults, _, err := store.GetGlobalSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaults.Motd = strp("Network MOTD")
	defaults.Whitelist = strp("Alice,Bob")
	defaults.Ops = strp("Alice")
	defaults.RconPort = i32p(30000)
	if err := store.UpdateGlobalSettings(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		server.Name = server.Id
		server.DataPath = t.TempDir()
		server.Port = 25565
		server.MaxPlayers = 20
		server.Memory = 2048
		if err := store.CreateServer(ctx, server); err != nil {
			t.Fatalf("server create failed %v", err)
		}
		props, err := store.GetServerProperties(ctx, server.Id)
		if err != nil {
			t.Fatal(err)
		}
		props.Motd = strp("Own MOTD")
		props.Whitelist = strp("carol")
		props.Ops = strp("carol")
		props.RconPort = i32p(25575)
		if err := store.UpdateServerProperties(ctx, props); err != nil {
			t.Fatal(err)
		}
	}
}

// Preview version for the given request
func previewVersion(t *testing.T, ctx context.Context, svc *PropertiesService) string {
	t.Helper()
	resp, err := svc.PreviewDefaultsSync(ctx, connect.NewRequest(&v1.PreviewDefaultsSyncRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	return resp.Msg.DefaultsVersion
}

func TestApplyDefaultsSyncEndToEnd(t *testing.T) {
	svc, store, enforcer := syncRig(t)
	ctx := context.Background()
	seedSyncServers(t, store, &v1.Server{Id: "srv-edit"}, &v1.Server{Id: "srv-read"}, &v1.Server{Id: "srv-hidden"})

	perms := []*v1.Permission{
		{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SETTINGS, Action: optionsv1.ActionType_ACTION_TYPE_UPDATE},
		{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, Action: optionsv1.ActionType_ACTION_TYPE_READ, ObjectId: "srv-edit"},
		{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES, Action: optionsv1.ActionType_ACTION_TYPE_UPDATE, ObjectId: "srv-edit"},
		{Resource: optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, Action: optionsv1.ActionType_ACTION_TYPE_READ, ObjectId: "srv-read"},
	}
	if err := enforcer.SetPermissionsForRole("syncer", perms); err != nil {
		t.Fatal(err)
	}
	ctx = metrics.WithSource(auth.WithUser(ctx, &v1.User{Id: "u", Username: "u", Roles: []string{"syncer"}}), "u")

	// Preview hides unreadable servers and values of read-only ones
	preview, err := svc.PreviewDefaultsSync(ctx, connect.NewRequest(&v1.PreviewDefaultsSyncRequest{Keys: []string{"motd", "whitelist"}}))
	if err != nil {
		t.Fatal(err)
	}
	version := preview.Msg.DefaultsVersion
	byID := map[string]*v1.DefaultsSyncServer{}
	for _, s := range preview.Msg.Servers {
		byID[s.ServerId] = s
	}
	if _, ok := byID["srv-hidden"]; ok {
		t.Error("preview leaked an unreadable server")
	}
	if s := byID["srv-read"]; s == nil || s.Selectable || len(s.Changes) != 0 || s.SkipReason == "" {
		t.Errorf("read-only server preview = %+v", s)
	}
	if s := byID["srv-edit"]; s == nil || !s.Selectable || len(s.Changes) != 2 {
		t.Fatalf("editable server preview = %+v", s)
	}
	for _, f := range preview.Msg.Fields {
		if f.Key == "rconPort" {
			t.Error("rconPort offered as a syncable field")
		}
		if f.Key == "ops" && !f.Sensitive {
			t.Error("ops not marked sensitive")
		}
	}

	resp, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{
		Keys:            []string{"motd", "whitelist"},
		ServerIds:       []string{"srv-edit", "srv-read", "srv-hidden", "srv-missing", "srv-edit"},
		DefaultsVersion: version,
	}))
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]*v1.DefaultsSyncResult{}
	for _, r := range resp.Msg.Results {
		outcomes[r.ServerId] = r
	}
	if len(resp.Msg.Results) != 4 {
		t.Fatalf("results = %d, want 4 after dedupe", len(resp.Msg.Results))
	}
	if r := outcomes["srv-edit"]; r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED || r.Restart != v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEXT_START {
		t.Errorf("srv-edit = %+v", r)
	}
	if r := outcomes["srv-read"]; r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_SKIPPED || r.Reason != syncReasonNoAccess {
		t.Errorf("srv-read = %+v", r)
	}
	for _, id := range []string{"srv-hidden", "srv-missing"} {
		if r := outcomes[id]; r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_SKIPPED || r.ServerName != "" || r.Reason != "Server not found" {
			t.Errorf("%s = %+v", id, r)
		}
	}

	edited, _ := store.GetServerProperties(ctx, "srv-edit")
	if *edited.Motd != "Network MOTD" || *edited.Whitelist != "carol,Alice,Bob" || *edited.RconPort != 25575 {
		t.Errorf("srv-edit props motd=%q whitelist=%q rcon=%d", *edited.Motd, *edited.Whitelist, *edited.RconPort)
	}
	for _, id := range []string{"srv-read", "srv-hidden"} {
		untouched, _ := store.GetServerProperties(ctx, id)
		if *untouched.Motd != "Own MOTD" || *untouched.Whitelist != "carol" {
			t.Errorf("%s was modified", id)
		}
	}

	// The shared save path leaves its ledger entry, tagged as a sync
	actions, err := store.GetServerActions(ctx, "srv-edit", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(actions, func(a *v1.ServerAction) bool {
		return a.Kind == v1.ServerActionKind_SERVER_ACTION_KIND_PROPERTIES_UPDATE && a.Source == syncSource && a.Attrs["by"] == "u"
	}) {
		t.Errorf("apply bypassed the normal update path or lost its source: %v", actions)
	}

	// A second run finds nothing to do
	again, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd", "whitelist"}, ServerIds: []string{"srv-edit"}, DefaultsVersion: version}))
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Msg.Results[0].Outcome; got != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_UNCHANGED {
		t.Errorf("second run outcome = %v", got)
	}

	// Mirror drops entries the defaults lack
	if _, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{Keys: []string{"whitelist"}, ServerIds: []string{"srv-edit"}, ListMode: v1.ListSyncMode_LIST_SYNC_MODE_MIRROR, DefaultsVersion: version})); err != nil {
		t.Fatal(err)
	}
	mirrored, _ := store.GetServerProperties(ctx, "srv-edit")
	if *mirrored.Whitelist != "Alice,Bob" {
		t.Errorf("mirrored whitelist = %q", *mirrored.Whitelist)
	}

	invalid := []struct {
		req  *v1.ApplyDefaultsSyncRequest
		code connect.Code
	}{
		{&v1.ApplyDefaultsSyncRequest{Keys: []string{"rconPort"}, ServerIds: []string{"srv-edit"}, DefaultsVersion: version}, connect.CodeInvalidArgument},
		{&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd"}, DefaultsVersion: version}, connect.CodeInvalidArgument},
		{&v1.ApplyDefaultsSyncRequest{ServerIds: []string{"srv-edit"}, DefaultsVersion: version}, connect.CodeInvalidArgument},
		{&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd"}, ServerIds: []string{"srv-edit"}}, connect.CodeInvalidArgument},
		{&v1.ApplyDefaultsSyncRequest{Keys: []string{"ops"}, ServerIds: []string{"srv-edit"}, DefaultsVersion: version}, connect.CodeInvalidArgument},
		{&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd"}, ServerIds: []string{"srv-edit"}, DefaultsVersion: "stale"}, connect.CodeFailedPrecondition},
	}
	for _, tc := range invalid {
		if _, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(tc.req)); connect.CodeOf(err) != tc.code {
			t.Errorf("request %v err = %v, want %v", tc.req, err, tc.code)
		}
	}

	// Sensitive fields go through once confirmed
	conf, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{Keys: []string{"ops"}, ServerIds: []string{"srv-edit"}, DefaultsVersion: version, ConfirmSensitive: true}))
	if err != nil || conf.Msg.Results[0].Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED {
		t.Errorf("confirmed ops sync = %v, %v", conf, err)
	}
}

func TestApplyDefaultsSyncDefaultsChanged(t *testing.T) {
	svc, store, _ := syncRig(t)
	seedSyncServers(t, store, &v1.Server{Id: "srv"})
	ctx := auth.WithUser(context.Background(), &v1.User{Id: "admin", Roles: []string{"admin"}})
	svc.enforcer = fakeEnforcer{grants: map[string]bool{"servers/read/srv": true, "server_properties/update/srv": true}}
	version := previewVersion(t, ctx, svc)

	defaults, _, _ := store.GetGlobalSettings(ctx)
	defaults.Motd = strp("Edited after preview")
	if err := store.UpdateGlobalSettings(ctx, defaults); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd"}, ServerIds: []string{"srv"}, DefaultsVersion: version}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "review again") {
		t.Fatalf("err = %v, want defaults changed", err)
	}
	props, _ := store.GetServerProperties(ctx, "srv")
	if *props.Motd != "Own MOTD" {
		t.Error("stale apply still wrote the server")
	}
}

func TestApplyDefaultsSyncRestarts(t *testing.T) {
	servers := func() []*v1.Server {
		return []*v1.Server{
			{Id: "run-a", ContainerId: "c1", Status: v1.ServerStatus_SERVER_STATUS_RUNNING},
			{Id: "run-b", ContainerId: "c2", Status: v1.ServerStatus_SERVER_STATUS_RUNNING},
			{Id: "run-c", ContainerId: "c3", Status: v1.ServerStatus_SERVER_STATUS_UNHEALTHY},
			{Id: "busy", ContainerId: "c4", Status: v1.ServerStatus_SERVER_STATUS_RUNNING},
			{Id: "prov", Status: v1.ServerStatus_SERVER_STATUS_PROVISIONING},
			{Id: "stopped", Status: v1.ServerStatus_SERVER_STATUS_STOPPED},
		}
	}
	ids := []string{"run-a", "run-b", "run-c", "busy", "prov", "stopped"}
	grants := map[string]bool{}
	for _, id := range ids {
		grants["servers/read/"+id] = true
		grants["server_properties/update/"+id] = true
	}

	for _, restartNow := range []bool{false, true} {
		name := "next start"
		if restartNow {
			name = "restart now"
		}
		t.Run(name, func(t *testing.T) {
			svc, store, _ := syncRig(t)
			seedSyncServers(t, store, servers()...)
			lc := newFakeLifecycle(30*time.Millisecond, "busy", "prov")
			svc.restarter = lc
			svc.enforcer = fakeEnforcer{grants: grants}
			ctx := auth.WithUser(context.Background(), &v1.User{Id: "admin", Roles: []string{"r"}})

			resp, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{
				Keys: []string{"motd"}, ServerIds: ids, DefaultsVersion: previewVersion(t, ctx, svc), RestartRunning: restartNow,
			}))
			if err != nil {
				t.Fatal(err)
			}
			running := v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEEDED
			if restartNow {
				running = v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED
			}
			want := map[string]v1.DefaultsSyncRestart{
				"run-a":   running,
				"run-b":   running,
				"run-c":   running,
				"busy":    v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_BUSY,
				"prov":    v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START,
				"stopped": v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEXT_START,
			}
			for _, r := range resp.Msg.Results {
				if r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED || r.Restart != want[r.ServerId] || r.RestartNote == "" {
					t.Errorf("%s = %v/%v %q, want applied/%v", r.ServerId, r.Outcome, r.Restart, r.RestartNote, want[r.ServerId])
				}
			}

			if !restartNow {
				time.Sleep(100 * time.Millisecond)
				if len(lc.restarted) != 0 {
					t.Fatalf("next start mode restarted %v", lc.restarted)
				}
				return
			}
			got := lc.waitRestarts(t, 3)
			if !slices.Equal(got, []string{"run-a", "run-b", "run-c"}) {
				t.Errorf("restarted %v, want the three running servers", got)
			}
			if peak := lc.peak.Load(); peak > syncRestartWorkers {
				t.Errorf("peak concurrent restarts = %d, want at most %d", peak, syncRestartWorkers)
			}
		})
	}
}

func TestApplyDefaultsSyncPermissionErrorIsPerServer(t *testing.T) {
	svc, store, _ := syncRig(t)
	seedSyncServers(t, store, &v1.Server{Id: "ok"}, &v1.Server{Id: "broken"})
	svc.enforcer = fakeEnforcer{
		grants: map[string]bool{"servers/read/ok": true, "server_properties/update/ok": true},
		fail:   map[string]bool{"broken": true},
	}
	ctx := auth.WithUser(context.Background(), &v1.User{Id: "admin", Roles: []string{"r"}})
	resp, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{
		Keys: []string{"motd"}, ServerIds: []string{"broken", "ok"}, DefaultsVersion: previewVersionAll(t, svc),
	}))
	if err != nil {
		t.Fatalf("one failing check aborted the batch: %v", err)
	}
	if r := resp.Msg.Results[0]; r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_FAILED {
		t.Errorf("broken = %+v, want failed", r)
	}
	if r := resp.Msg.Results[1]; r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED {
		t.Errorf("ok = %+v, want applied", r)
	}
}

// Defaults version without going through a preview's permission checks
func previewVersionAll(t *testing.T, svc *PropertiesService) string {
	t.Helper()
	sel, err := svc.syncFields(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return sel.version
}

func TestApplyDefaultsSyncSurvivesClientDisconnect(t *testing.T) {
	svc, store, _ := syncRig(t)
	seedSyncServers(t, store, &v1.Server{Id: "a"}, &v1.Server{Id: "b"})
	svc.enforcer = fakeEnforcer{grants: map[string]bool{
		"servers/read/a": true, "server_properties/update/a": true,
		"servers/read/b": true, "server_properties/update/b": true,
	}}
	version := previewVersionAll(t, svc)
	ctx, cancel := context.WithCancel(auth.WithUser(context.Background(), &v1.User{Id: "admin", Roles: []string{"r"}}))
	cancel()
	resp, err := svc.ApplyDefaultsSync(ctx, connect.NewRequest(&v1.ApplyDefaultsSyncRequest{Keys: []string{"motd"}, ServerIds: []string{"a", "b"}, DefaultsVersion: version}))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Msg.Results {
		if r.Outcome != v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED {
			t.Errorf("%s = %+v after disconnect, want applied", r.ServerId, r)
		}
	}
}
