package seed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Message shape from name and kind pairs
func msg(name string, fields ...*Field) *Shape {
	return &Shape{Kind: KindMessage, Name: name, Fields: fields}
}

func str(name string) *Field  { return &Field{Name: name, Shape: &Shape{Kind: KindString}} }
func num(name string) *Field  { return &Field{Name: name, Shape: &Shape{Kind: KindInt}} }
func flag(name string) *Field { return &Field{Name: name, Shape: &Shape{Kind: KindBool}} }

// Fake panel recording every call in arrival order
type fakePanel struct {
	mu    sync.Mutex
	calls []string
	body  map[string][]map[string]any
	auth  map[string]string
	ids   int
}

func (f *fakePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path)
	f.body[r.URL.Path] = append(f.body[r.URL.Path], body)
	f.auth[r.URL.Path] = r.Header.Get("Authorization")
	f.ids++
	id := f.ids
	f.mu.Unlock()

	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/t.Auth/Register":
		reply(map[string]any{"user": map[string]any{"id": "u1", "username": body["username"]}})
	case "/t.Auth/Login":
		if body["password"] != DefaultPrincipal.Password {
			w.WriteHeader(401)
			return
		}
		reply(map[string]any{"token": "tok-1", "user": map[string]any{"id": "u1"}})
	case "/t.Roles/ListRoles":
		reply(map[string]any{"roles": []any{map[string]any{"id": "r1", "name": "user"}}})
	case "/t.Widgets/CreateWidget":
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(401)
			return
		}
		reply(map[string]any{"widget": map[string]any{"id": "w" + itoa(id), "name": body["name"]}})
	case "/t.Gadgets/CreateGadget":
		if body["widget_id"] == nil || body["widget_id"] == "" {
			w.WriteHeader(400)
			reply(map[string]any{"code": "invalid_argument"})
			return
		}
		reply(map[string]any{"gadget": map[string]any{"id": "g" + itoa(id)}})
	default:
		w.WriteHeader(404)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Surface matching the fake panel above
func fakeSurface() *Surface {
	widget := msg("Widget", str("id"), str("name"))
	gadget := msg("Gadget", str("id"))
	role := msg("Role", str("id"), str("name"))
	return &Surface{Era: "connect", Ops: []*Operation{
		{Name: "Register", Method: "POST", Path: "/t.Auth/Register",
			Input:  msg("RegisterRequest", str("username"), str("email"), str("password"), &Field{Name: "invite_code", Optional: true, Shape: &Shape{Kind: KindString}}),
			Output: msg("RegisterResponse", &Field{Name: "user", Shape: msg("User", str("id"))})},
		{Name: "Login", Method: "POST", Path: "/t.Auth/Login",
			Input:  msg("LoginRequest", str("username"), str("password")),
			Output: msg("LoginResponse", str("token"))},
		{Name: "ListRoles", Method: "POST", Path: "/t.Roles/ListRoles",
			Input:  msg("ListRolesRequest"),
			Output: msg("ListRolesResponse", &Field{Name: "roles", Shape: &Shape{Kind: KindList, Elem: role}})},
		// Declared before its producer to prove ordering is dependency driven
		{Name: "CreateGadget", Method: "POST", Path: "/t.Gadgets/CreateGadget",
			Input:  msg("CreateGadgetRequest", str("widget_id"), str("name")),
			Output: msg("CreateGadgetResponse", &Field{Name: "gadget", Shape: gadget})},
		{Name: "CreateWidget", Method: "POST", Path: "/t.Widgets/CreateWidget",
			Input: msg("CreateWidgetRequest", str("name"), num("port"), flag("enabled"),
				&Field{Name: "kind", Shape: &Shape{Kind: KindEnum, Enum: []string{"KIND_A", "KIND_B"}}},
				&Field{Name: "roles", Shape: &Shape{Kind: KindList, Elem: &Shape{Kind: KindString}}},
				&Field{Name: "modpack_id", Shape: &Shape{Kind: KindString}}),
			Output: msg("CreateWidgetResponse", &Field{Name: "widget", Shape: widget})},
		{Name: "SyncWidgets", Method: "POST", Path: "/t.Widgets/SyncWidgets", Input: msg("SyncRequest")},
	}}
}

func TestSeederRunsCreatesInDependencyOrder(t *testing.T) {
	panel := &fakePanel{body: map[string][]map[string]any{}, auth: map[string]string{}}
	srv := httptest.NewServer(panel)
	defer srv.Close()

	s := New(fakeSurface(), NewClient(srv.URL))
	s.Log = t.Logf
	report, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !report.Authenticated {
		t.Fatal("token was not captured from login")
	}

	register := panel.body["/t.Auth/Register"][0]
	if _, ok := register["invite_code"]; ok {
		t.Fatalf("register carried a synthesized invite code: %v", register)
	}
	if register["username"] != DefaultPrincipal.Username {
		t.Fatalf("register used %v instead of the principal", register["username"])
	}

	widgetAt, gadgetAt := -1, -1
	for i, p := range panel.calls {
		if p == "/t.Widgets/CreateWidget" && widgetAt < 0 {
			widgetAt = i
		}
		if p == "/t.Gadgets/CreateGadget" && gadgetAt < 0 {
			gadgetAt = i
		}
	}
	if widgetAt < 0 || gadgetAt < 0 || gadgetAt < widgetAt {
		t.Fatalf("gadget ran before widget, calls %v", panel.calls)
	}
	for _, p := range panel.calls {
		if strings.Contains(p, "Sync") {
			t.Fatalf("skipped procedure was called: %s", p)
		}
	}

	gadget := panel.body["/t.Gadgets/CreateGadget"][0]
	if !strings.HasPrefix(gadget["widget_id"].(string), "w") {
		t.Fatalf("gadget did not reference a harvested widget: %v", gadget)
	}
	widget := panel.body["/t.Widgets/CreateWidget"][0]
	if widget["kind"] != "KIND_A" {
		t.Fatalf("enum picked %v", widget["kind"])
	}
	if roles, _ := widget["roles"].([]any); len(roles) != 1 || roles[0] != "user" {
		t.Fatalf("roles did not borrow harvested names: %v", widget["roles"])
	}
	if _, ok := widget["modpack_id"]; ok {
		t.Fatalf("unresolvable reference was synthesized: %v", widget["modpack_id"])
	}
	if widget["enabled"] != true {
		t.Fatalf("enabled flag stayed off: %v", widget)
	}
	if panel.auth["/t.Widgets/CreateWidget"] != "Bearer tok-1" {
		t.Fatal("creates ran without the session token")
	}

	for _, op := range report.Ops {
		if strings.HasPrefix(op.Name, "Create") && op.Succeeded != 2 {
			t.Fatalf("%s succeeded %d of %d, %s", op.Name, op.Succeeded, op.Attempts, op.LastError)
		}
	}
	if report.Pools["widget"] != 2 || report.Pools["gadget"] != 2 || report.Pools["role"] != 1 {
		t.Fatalf("pools wrong: %v", report.Pools)
	}
}

func TestRefEntityAndSingular(t *testing.T) {
	cases := map[string]string{
		"server_id":          "server",
		"proxyListenerId":    "proxylistener",
		"id":                 "",
		"name":               "",
		"modpack_version_id": "modpackversion",
	}
	for in, want := range cases {
		if got := RefEntity(in); got != want {
			t.Errorf("RefEntity(%q) = %q, want %q", in, got, want)
		}
	}
	if Singular("entries") != "entry" || Singular("servers") != "server" || Singular("status") != "status" {
		t.Fatal("singular rules drifted")
	}
}
