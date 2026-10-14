package alias

import (
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Tagged config secrets never surface through the alias system
func TestSecretConfigFieldsHidden(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "supersecretjwt"
	cfg.Auth.OIDC.ClientSecret = "supersecretclient"
	ctx := &Context{Config: cfg}

	for _, info := range GetAvailableAliases(ctx) {
		if strings.Contains(info.Path, "jwt_secret") || strings.Contains(info.Path, "client_secret") {
			t.Errorf("secret alias listed: %s", info.Alias)
		}
	}

	for _, input := range []string{"{{config.auth.jwt_secret}}", "{{config.auth.oidc.client_secret}}"} {
		if out := Substitute(input, ctx); strings.Contains(out, "supersecret") {
			t.Errorf("secret resolved through %s: %q", input, out)
		}
	}
}

func TestDescriptionsCarryOwnerPrefix(t *testing.T) {
	if got := generateDescription("config.auth.session_timeout"); got != "The config.auth's Session timeout" {
		t.Errorf("unexpected description %q", got)
	}
	if got := generateDescription("server.rconPassword"); got != "The server's Rcon password" {
		t.Errorf("unexpected description %q", got)
	}
	for _, info := range GetAvailableAliases(nil) {
		if strings.Contains(info.Description, "The 's") {
			t.Errorf("alias %s renders empty owner: %q", info.Alias, info.Description)
		}
	}
}

// Password inputs on server properties never surface through aliases
func TestServerPropertySecretsHidden(t *testing.T) {
	props := &v1.ServerProperties{}
	msg := props.ProtoReflect()
	var secretPaths []string
	for _, p := range protometa.Props(msg.Descriptor()) {
		if p.Meta.Input != "password" {
			continue
		}
		msg.Set(p.Field, protoreflectString("hidden-"+string(p.Field.Name())))
		secretPaths = append(secretPaths, "server.config."+string(p.Field.Name()))
	}
	if len(secretPaths) < 4 {
		t.Fatalf("expected rcon, curseforge, management secret and keystore password inputs, found %d", len(secretPaths))
	}
	ctx := &Context{ServerProperties: props}

	for _, info := range GetAvailableAliases(ctx) {
		for _, path := range secretPaths {
			if info.Path == path {
				t.Errorf("secret alias listed: %s", info.Alias)
			}
		}
		if strings.Contains(info.ExampleValue, "hidden-") {
			t.Errorf("secret value exposed through %s", info.Alias)
		}
	}
	for alias, value := range GetResolvedAliases(ctx) {
		if strings.Contains(value, "hidden-") {
			t.Errorf("secret value resolved through %s", alias)
		}
	}
	for _, path := range secretPaths {
		if out := Substitute("{{"+path+"}}", ctx); strings.Contains(out, "hidden-") {
			t.Errorf("secret resolved through %s: %q", path, out)
		}
	}
}

func protoreflectString(s string) protoreflect.Value {
	return protoreflect.ValueOfString(s)
}
