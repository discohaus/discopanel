package diagnostics

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Callback path the panel serves for OIDC
const oidcCallbackPath = "/api/v1/auth/oidc/callback"

// Records login related settings as facts
func (r *Runner) checkAuthSettings(ctx context.Context, c *check) {
	a := r.cfg.Auth
	c.fact("local_enabled", a.Local.Enabled)
	c.fact("allow_registration", a.Local.AllowRegistration)
	c.fact("oidc_enabled", a.OIDC.Enabled)
	c.fact("anonymous_access", a.AnonymousAccess)
	c.fact("session_timeout", (time.Duration(a.SessionTimeout) * time.Second).String())
	c.fact("jwt_secret", map[bool]string{true: "configured", false: "auto generated"}[a.JWTSecret != ""])
	if !a.Local.Enabled && !a.OIDC.Enabled {
		c.warn("Every login method is disabled, the panel is open to anyone who can reach it")
		c.fix("Enable auth.local or auth.oidc unless the panel sits behind another login.", docsConfiguration)
		return
	}
	if a.AnonymousAccess {
		c.info("Anonymous access is on, visitors can browse without logging in")
		return
	}
	c.info("Local login %s, OIDC %s", onOff(a.Local.Enabled), onOff(a.OIDC.Enabled))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Fetches the provider discovery document and checks it
func (r *Runner) checkOIDC(ctx context.Context, c *check) {
	o := r.cfg.Auth.OIDC
	if !o.Enabled {
		c.skip("OIDC is disabled")
		return
	}
	c.fact("issuer", o.IssuerURI)
	c.fact("client_id", o.ClientID)
	c.fact("redirect_url", o.RedirectURL)
	if o.IssuerURI == "" || o.ClientID == "" || o.ClientSecret == "" {
		c.fail("OIDC is enabled but issuer, client id, or client secret is empty")
		c.fix("Fill in auth.oidc.issuer_uri, client_id, and client_secret from your provider.", docsOIDC)
		return
	}

	if ru, err := url.Parse(o.RedirectURL); err != nil || ru.Scheme == "" || ru.Host == "" {
		c.warn("Redirect URL %q is not an absolute URL", o.RedirectURL)
		c.fix("Set auth.oidc.redirect_url to the panel address players reach, ending in "+oidcCallbackPath+", and register the same URL with the provider.", docsOIDC)
	} else if ru.Path != oidcCallbackPath {
		c.warn("Redirect URL path is %s, the panel serves the callback at %s", ru.Path, oidcCallbackPath)
		c.fix("Point auth.oidc.redirect_url at "+oidcCallbackPath+" and update the provider's allowed redirect list.", docsOIDC)
	}

	client := r.http
	if o.SkipTLSVerify {
		client = &http.Client{Timeout: r.http.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	discovery := strings.TrimRight(o.IssuerURI, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery, nil)
	if err != nil {
		c.fail("Issuer URL is invalid: %v", err)
		return
	}
	req.Header.Set("User-Agent", r.userAgent())
	resp, err := client.Do(req)
	if err != nil {
		c.fail("Provider discovery failed: %s", describeNetErr(err))
		c.fix("The panel must reach the issuer over HTTPS. Check DNS, firewalls, and for self signed certificates set auth.oidc.skip_tls_verify.", docsOIDC)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.fail("Provider discovery answered HTTP %d at %s", resp.StatusCode, discovery)
		c.fix("The issuer must serve /.well-known/openid-configuration. Keycloak issuers look like https://host/realms/<realm>, Authelia and Authentik use their root URL.", docsOIDC)
		return
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		c.fail("Provider discovery document unreadable: %v", err)
		return
	}
	c.fact("authorization_endpoint", doc.Authorization)
	if strings.TrimRight(doc.Issuer, "/") != strings.TrimRight(o.IssuerURI, "/") {
		c.fail("Provider reports issuer %s but auth.oidc.issuer_uri is %s", doc.Issuer, o.IssuerURI)
		c.fix("Set issuer_uri to exactly what the provider advertises, the login library rejects mismatches.", docsOIDC)
		return
	}
	c.pass("OIDC provider %s answers discovery", o.IssuerURI)
}
