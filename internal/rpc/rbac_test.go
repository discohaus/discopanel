package rpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Short enum aliases keep tables readable
const (
	rServers  = optionsv1.ResourceType_RESOURCE_TYPE_SERVERS
	rUsers    = optionsv1.ResourceType_RESOURCE_TYPE_USERS
	rFiles    = optionsv1.ResourceType_RESOURCE_TYPE_FILES
	rSettings = optionsv1.ResourceType_RESOURCE_TYPE_SETTINGS
	rTasks    = optionsv1.ResourceType_RESOURCE_TYPE_TASKS
	aRead     = optionsv1.ActionType_ACTION_TYPE_READ
	aCreate   = optionsv1.ActionType_ACTION_TYPE_CREATE
	aUpdate   = optionsv1.ActionType_ACTION_TYPE_UPDATE
	aDelete   = optionsv1.ActionType_ACTION_TYPE_DELETE
	aStart    = optionsv1.ActionType_ACTION_TYPE_START
	aCommand  = optionsv1.ActionType_ACTION_TYPE_COMMAND
)

// Builds a server with real store, enforcer, auth manager
func testRig(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "rbac.db")
	cfg.Database.AutoMigrate = true
	cfg.Auth.SessionTimeout = 3600
	cfg.Auth.Local.Enabled = true
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
	authManager, err := auth.NewManager(store, enforcer, &cfg.Auth)
	if err != nil {
		t.Fatalf("auth manager failed %v", err)
	}
	return &Server{store: store, config: cfg, authManager: authManager, enforcer: enforcer, log: logger.New()}
}

// Creates a real user holding roles, returns api token
func seedUser(t *testing.T, s *Server, name string, roles ...string) string {
	t.Helper()
	ctx := context.Background()
	user := &v1.User{Id: name, Username: name, AuthProvider: v1.AuthProvider_AUTH_PROVIDER_LOCAL, IsActive: true, PasswordHash: "x"}
	if err := s.store.CreateUser(ctx, user); err != nil {
		t.Fatalf("user create failed %v", err)
	}
	for _, role := range roles {
		if err := s.store.AssignRole(ctx, user.Id, role, v1.RoleSource_ROLE_SOURCE_LOCAL); err != nil {
			t.Fatalf("role assign failed %v", err)
		}
	}
	token, _, err := s.authManager.GenerateApiToken(ctx, user.Id, name, nil)
	if err != nil {
		t.Fatalf("token mint failed %v", err)
	}
	return token
}

// Formats one grant row for golden comparison
func permKey(p *v1.Permission) string {
	res, act := "*", "*"
	if p.Resource != optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		res = protometa.Name(p.Resource)
	}
	if p.Action != optionsv1.ActionType_ACTION_TYPE_UNSPECIFIED {
		act = protometa.Name(p.Action)
	}
	return res + ":" + act
}

// Checks every procedure since missing annotations ship broken features
func TestEveryProcedureCarriesAnnotation(t *testing.T) {
	procedures := 0
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "discopanel.v1" {
			return true
		}
		svcs := fd.Services()
		for i := range svcs.Len() {
			sd := svcs.Get(i)
			methods := sd.Methods()
			for j := range methods.Len() {
				md := methods.Get(j)
				procedure := fmt.Sprintf("/%s/%s", sd.FullName(), md.Name())
				procedures++
				perm := protometa.Perm(procedure)
				if perm == nil {
					t.Errorf("%s has no rbac annotation", procedure)
					continue
				}
				if perm.GetPublic() && perm.GetSession() {
					t.Errorf("%s marked both public and session", procedure)
				}
				if !perm.GetPublic() && !perm.GetSession() {
					if perm.Resource == optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED || perm.Action == optionsv1.ActionType_ACTION_TYPE_UNSPECIFIED {
						t.Errorf("%s missing resource or action", procedure)
					}
				}
				if perm.ObjectIdField != "" {
					field := md.Input().Fields().ByName(protoreflect.Name(perm.ObjectIdField))
					if field == nil || field.Kind() != protoreflect.StringKind {
						t.Errorf("%s object id field %q is not a request string", procedure, perm.ObjectIdField)
					}
				}
				if perm.Scope != optionsv1.ObjectScope_OBJECT_SCOPE_UNSPECIFIED && perm.ObjectIdField == "" {
					t.Errorf("%s declares scope without object id field", procedure)
				}
			}
		}
		return true
	})
	if procedures == 0 {
		t.Fatal("registry walk found no procedures")
	}
}

// Matrix drift here means silent privilege change
func TestSeededDefaultRoleMatrix(t *testing.T) {
	s := testRig(t)
	browse := []string{
		"servers:read", "server_properties:read", "mods:read", "modpacks:read", "modules:read",
		"module_templates:read", "files:read", "tasks:read", "proxy:read",
	}
	ops := []string{"servers:start", "servers:stop", "servers:restart"}
	want := map[string][]string{
		"admin":     {"*:*"},
		"user":      append(append(slices.Clone(browse), ops...), "servers:command"),
		"module":    {"servers:read", "server_properties:read", "modpacks:read"},
		"doctor":    append([]string{"servers:read", "server_properties:read", "settings:read", "modpacks:read"}, ops...),
		"anonymous": browse,
	}

	matrix := s.enforcer.GetPermissionMatrix()
	for role := range want {
		if _, ok := matrix[role]; !ok {
			t.Errorf("role %s not seeded", role)
		}
	}
	for role, perms := range matrix {
		got := make([]string, 0, len(perms))
		for _, p := range perms {
			if p.ObjectId != "*" {
				t.Errorf("role %s seeded with object %q", role, p.ObjectId)
			}
			got = append(got, permKey(p))
		}
		expect := slices.Clone(want[role])
		slices.Sort(got)
		slices.Sort(expect)
		if !slices.Equal(got, expect) {
			t.Errorf("role %s grants %v want %v", role, got, expect)
		}
	}

	grants := []struct {
		name   string
		roles  []string
		res    optionsv1.ResourceType
		act    optionsv1.ActionType
		object string
		want   bool
	}{
		{"admin wildcard covers users", []string{"admin"}, rUsers, aDelete, "*", true},
		{"admin wildcard covers any object", []string{"admin"}, rServers, aUpdate, "srv-9", true},
		{"user commands servers", []string{"user"}, rServers, aCommand, "*", true},
		{"user starts one server", []string{"user"}, rServers, aStart, "srv-1", true},
		{"user cannot create servers", []string{"user"}, rServers, aCreate, "*", false},
		{"user cannot read users", []string{"user"}, rUsers, aRead, "*", false},
		{"anonymous reads servers", []string{"anonymous"}, rServers, aRead, "*", true},
		{"anonymous cannot start servers", []string{"anonymous"}, rServers, aStart, "*", false},
		{"module cannot read files", []string{"module"}, rFiles, aRead, "*", false},
		{"doctor reads settings", []string{"doctor"}, rSettings, aRead, "*", true},
		{"doctor cannot command servers", []string{"doctor"}, rServers, aCommand, "*", false},
		{"role union grants settings", []string{"user", "doctor"}, rSettings, aRead, "*", true},
		{"unknown role denied", []string{"ghost"}, rServers, aRead, "*", false},
		{"no roles denied", nil, rServers, aRead, "*", false},
	}
	for _, tc := range grants {
		allowed, err := s.enforcer.Enforce(tc.roles, tc.res, tc.act, tc.object)
		if err != nil {
			t.Errorf("%s errored %v", tc.name, err)
			continue
		}
		if allowed != tc.want {
			t.Errorf("%s got %v want %v", tc.name, allowed, tc.want)
		}
	}
}

// Reseeding on boot must never clobber operator edits
func TestReseedKeepsRoleEdits(t *testing.T) {
	s := testRig(t)
	custom := []*v1.Permission{{Resource: rServers, Action: aRead}}
	if err := s.enforcer.SetPermissionsForRole("user", custom); err != nil {
		t.Fatalf("role edit failed %v", err)
	}
	if err := s.enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("reseed failed %v", err)
	}
	perms := s.enforcer.GetPermissionsForRole("user")
	if len(perms) != 1 || permKey(perms[0]) != "servers:read" {
		t.Fatalf("reseed rewrote edited role, now %d grants", len(perms))
	}
	if admin := s.enforcer.GetPermissionsForRole("admin"); len(admin) != 1 {
		t.Fatalf("reseed duplicated admin rows, now %d", len(admin))
	}
	if err := s.enforcer.SetPermissionsForRole("admin", custom); err == nil {
		t.Fatal("admin role edit was not refused")
	}
}

// Mounts a stub procedure behind the real interceptor
func mount[Req, Resp any](mux *http.ServeMux, s *Server, procedure string) {
	mux.Handle(procedure, connect.NewUnaryHandler(
		procedure,
		func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Resp], error) {
			return connect.NewResponse(new(Resp)), nil
		},
		connect.WithInterceptors(s.authInterceptor()),
	))
}

// Calls one procedure with an optional bearer token
func call[Req, Resp any](base, procedure, token string, msg *Req) error {
	req := connect.NewRequest(msg)
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	_, err := connect.NewClient[Req, Resp](http.DefaultClient, base+procedure).CallUnary(context.Background(), req)
	return err
}

// Drives the interceptor through real connect round trips
func TestAuthInterceptor(t *testing.T) {
	s := testRig(t)
	ctx := context.Background()

	adminToken := seedUser(t, s, "boss", "admin")
	userToken := seedUser(t, s, "player", "user")
	scopedToken := seedUser(t, s, "tenant", "scoped")

	if err := s.store.CreateRole(ctx, &v1.Role{Id: "role-scoped", Name: "scoped"}); err != nil {
		t.Fatalf("role create failed %v", err)
	}
	scopedPerms := []*v1.Permission{
		{Resource: rServers, Action: aRead, ObjectId: "srv-1"},
		{Resource: rTasks, Action: aRead, ObjectId: "srv-1"},
	}
	if err := s.enforcer.SetPermissionsForRole("scoped", scopedPerms); err != nil {
		t.Fatalf("scoped grants failed %v", err)
	}
	if err := s.store.CreateScheduledTask(ctx, &v1.ScheduledTask{Id: "task-1", ServerId: "srv-1", Name: "backup"}); err != nil {
		t.Fatalf("task create failed %v", err)
	}

	const ghost = "/discopanel.v1.GhostService/Poke"
	mux := http.NewServeMux()
	mount[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse](mux, s, discopanelv1connect.AuthServiceGetAuthStatusProcedure)
	mount[v1.ListAPITokensRequest, v1.ListAPITokensResponse](mux, s, discopanelv1connect.AuthServiceListAPITokensProcedure)
	mount[v1.GetServerRequest, v1.GetServerResponse](mux, s, ghost)
	mount[v1.CreateServerRequest, v1.CreateServerResponse](mux, s, discopanelv1connect.ServerServiceCreateServerProcedure)
	mount[v1.StartServerRequest, v1.StartServerResponse](mux, s, discopanelv1connect.ServerServiceStartServerProcedure)
	mount[v1.GetServerRequest, v1.GetServerResponse](mux, s, discopanelv1connect.ServerServiceGetServerProcedure)
	mount[v1.ListServersRequest, v1.ListServersResponse](mux, s, discopanelv1connect.ServerServiceListServersProcedure)
	mount[v1.GetTaskRequest, v1.GetTaskResponse](mux, s, discopanelv1connect.TaskServiceGetTaskProcedure)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	base := ts.URL

	cases := []struct {
		name string
		want connect.Code
		call func() error
	}{
		{"public needs no token", 0, func() error {
			return call[v1.GetAuthStatusRequest, v1.GetAuthStatusResponse](base, discopanelv1connect.AuthServiceGetAuthStatusProcedure, "", &v1.GetAuthStatusRequest{})
		}},
		{"session takes any authenticated user", 0, func() error {
			return call[v1.ListAPITokensRequest, v1.ListAPITokensResponse](base, discopanelv1connect.AuthServiceListAPITokensProcedure, userToken, &v1.ListAPITokensRequest{})
		}},
		{"session rejects missing token", connect.CodeUnauthenticated, func() error {
			return call[v1.ListAPITokensRequest, v1.ListAPITokensResponse](base, discopanelv1connect.AuthServiceListAPITokensProcedure, "", &v1.ListAPITokensRequest{})
		}},
		{"session rejects garbage token", connect.CodeUnauthenticated, func() error {
			return call[v1.ListAPITokensRequest, v1.ListAPITokensResponse](base, discopanelv1connect.AuthServiceListAPITokensProcedure, "dp_bogus", &v1.ListAPITokensRequest{})
		}},
		{"unannotated denies even admin", connect.CodePermissionDenied, func() error {
			return call[v1.GetServerRequest, v1.GetServerResponse](base, ghost, adminToken, &v1.GetServerRequest{})
		}},
		{"grantless role denied", connect.CodePermissionDenied, func() error {
			return call[v1.CreateServerRequest, v1.CreateServerResponse](base, discopanelv1connect.ServerServiceCreateServerProcedure, userToken, &v1.CreateServerRequest{})
		}},
		{"admin grant allows create", 0, func() error {
			return call[v1.CreateServerRequest, v1.CreateServerResponse](base, discopanelv1connect.ServerServiceCreateServerProcedure, adminToken, &v1.CreateServerRequest{})
		}},
		{"seeded grant allows start", 0, func() error {
			return call[v1.StartServerRequest, v1.StartServerResponse](base, discopanelv1connect.ServerServiceStartServerProcedure, userToken, &v1.StartServerRequest{Id: "srv-1"})
		}},
		{"object grant matches its server", 0, func() error {
			return call[v1.GetServerRequest, v1.GetServerResponse](base, discopanelv1connect.ServerServiceGetServerProcedure, scopedToken, &v1.GetServerRequest{Id: "srv-1"})
		}},
		{"object grant stops at other servers", connect.CodePermissionDenied, func() error {
			return call[v1.GetServerRequest, v1.GetServerResponse](base, discopanelv1connect.ServerServiceGetServerProcedure, scopedToken, &v1.GetServerRequest{Id: "srv-2"})
		}},
		{"object grant never widens to all", connect.CodePermissionDenied, func() error {
			return call[v1.ListServersRequest, v1.ListServersResponse](base, discopanelv1connect.ServerServiceListServersProcedure, scopedToken, &v1.ListServersRequest{})
		}},
		{"task scope resolves to granted server", 0, func() error {
			return call[v1.GetTaskRequest, v1.GetTaskResponse](base, discopanelv1connect.TaskServiceGetTaskProcedure, scopedToken, &v1.GetTaskRequest{Id: "task-1"})
		}},
		{"task scope rejects unknown task", connect.CodeNotFound, func() error {
			return call[v1.GetTaskRequest, v1.GetTaskResponse](base, discopanelv1connect.TaskServiceGetTaskProcedure, scopedToken, &v1.GetTaskRequest{Id: "nope"})
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		if tc.want == 0 {
			if err != nil {
				t.Errorf("%s failed %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s succeeded, want %v", tc.name, tc.want)
			continue
		}
		if got := connect.CodeOf(err); got != tc.want {
			t.Errorf("%s got %v want %v", tc.name, got, tc.want)
		}
	}
}
