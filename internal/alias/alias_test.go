package alias

import (
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
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
