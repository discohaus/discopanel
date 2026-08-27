package seed

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// Rest routes derived from the oldest release in git history
func TestDiscoverRESTReadsRoutesFromGit(t *testing.T) {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git checkout")
	}
	repo := strings.TrimSpace(string(top))
	const tag = "v1.0.1"
	if err := exec.Command("git", "-C", repo, "rev-parse", "-q", "--verify", tag+"^{commit}").Run(); err != nil {
		t.Skipf("tag %s not fetched", tag)
	}

	surface, err := DiscoverREST(context.Background(), repo, tag)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if surface.Era != "rest" {
		t.Fatalf("era %q", surface.Era)
	}
	var create, list, register, listener *Operation
	for _, op := range surface.Ops {
		switch {
		case op.Method == http.MethodPost && op.Path == "/api/v1/servers":
			create = op
		case op.Method == http.MethodGet && op.Path == "/api/v1/servers":
			list = op
		case op.Method == http.MethodPost && op.Path == "/api/v1/auth/register":
			register = op
		case op.Method == http.MethodPost && op.Path == "/api/v1/proxy/listeners":
			listener = op
		}
	}
	if create == nil || list == nil || register == nil || listener == nil {
		t.Fatalf("routes missing from %d procedures", len(surface.Ops))
	}
	if list.Entity != "server" {
		t.Fatalf("list entity %q", list.Entity)
	}
	if create.Input == nil || create.Input.Field("mc_version") == nil {
		t.Fatalf("create body lost mc_version: %+v", create.Input)
	}
	loader := create.Input.Field("mod_loader")
	if loader == nil || loader.Shape.Kind != KindEnum {
		t.Fatalf("mod_loader shape wrong: %+v", loader)
	}
	found := false
	for _, v := range loader.Shape.Enum {
		if v == "vanilla" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mod_loader consts not read: %v", loader.Shape.Enum)
	}
	if register.Input.Field("password") == nil {
		t.Fatal("register body lost password")
	}
	// Model bodies resolve across packages through import aliases
	if listener.Input == nil || listener.Input.Field("port") == nil || listener.Input.Field("is_default") == nil {
		t.Fatalf("listener body not resolved from the db package: %+v", listener.Input)
	}
	if create.Input.Field("id") != nil {
		t.Fatal("anonymous request struct gained an id field")
	}
}
