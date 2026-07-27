package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type OIDCHandler struct {
	manager      *Manager
	store        *db.Store
	config       *config.OIDCConfig
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	oauth2Config *oauth2.Config
	httpClient   *http.Client
	log          *logger.Logger
}

func NewOIDCHandler(manager *Manager, store *db.Store, cfg *config.OIDCConfig, log *logger.Logger) (*OIDCHandler, error) {
	if !cfg.Enabled {
		return &OIDCHandler{
			manager: manager,
			store:   store,
			config:  cfg,
			log:     log,
		}, nil
	}

	var httpClient *http.Client
	if cfg.SkipTLSVerify {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		log.Warn("OIDC: TLS verification disabled")
	}

	ctx := context.Background()
	if httpClient != nil {
		ctx = oidc.ClientContext(ctx, httpClient)
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURI)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	oauth2Config := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}

	return &OIDCHandler{
		manager:      manager,
		store:        store,
		config:       cfg,
		provider:     provider,
		verifier:     verifier,
		oauth2Config: oauth2Config,
		httpClient:   httpClient,
		log:          log,
	}, nil
}

func (h *OIDCHandler) IsEnabled() bool {
	return h.config.Enabled && h.provider != nil
}

func (h *OIDCHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if !h.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}

	state, err := generateState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}

	// Store state in cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, h.oauth2Config.AuthCodeURL(state), http.StatusFound)
}

func (h *OIDCHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}

	// Verify state
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Exchange code for token
	ctx := r.Context()
	if h.httpClient != nil {
		ctx = oidc.ClientContext(ctx, h.httpClient)
	}
	oauth2Token, err := h.oauth2Config.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		h.log.Error("OIDC: failed to exchange code for token: %v", err)
		http.Error(w, "Failed to exchange code for token", http.StatusInternalServerError)
		return
	}

	// Extract ID token
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		h.log.Error("OIDC: no id_token in token response")
		http.Error(w, "No id_token in response", http.StatusInternalServerError)
		return
	}

	// Verify ID token
	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		h.log.Error("OIDC: failed to verify ID token: %v", err)
		http.Error(w, "Failed to verify ID token", http.StatusInternalServerError)
		return
	}

	// Extract claims from ID token
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		h.log.Error("OIDC: failed to parse claims: %v", err)
		http.Error(w, "Failed to parse claims", http.StatusInternalServerError)
		return
	}

	// Fetch UserInfo - some oidc sets role/groups here
	tokenSource := h.oauth2Config.TokenSource(ctx, oauth2Token)
	userInfo, err := h.provider.UserInfo(ctx, tokenSource)
	if err == nil {
		var uiClaims map[string]any
		if err := userInfo.Claims(&uiClaims); err == nil {
			for k, v := range uiClaims {
				if _, exists := claims[k]; !exists {
					claims[k] = v
				}
			}
		}
	}

	// Fetch extra claims from provider API if configured
	if h.config.ExtraClaimsURL != "" {
		extra, err := h.fetchExtraClaims(ctx, oauth2Token.AccessToken)
		if err != nil {
			h.log.Error("OIDC: extra claims request failed (%s): %v", h.config.ExtraClaimsURL, err)
			http.Redirect(w, r, "/login?error=membership_check_failed", http.StatusFound)
			return
		}
		maps.Copy(claims, extra)
	}

	// Enforce required claim if configured
	if h.config.RequiredClaim != "" && len(h.config.RequiredValues) > 0 {
		if !h.checkRequiredClaim(claims) {
			h.log.Warn("OIDC: login rejected - required claim %q not satisfied", h.config.RequiredClaim)
			http.Redirect(w, r, "/login?error=access_denied", http.StatusFound)
			return
		}
	}

	// Extract user info from claims
	sub := idToken.Subject
	email, _ := claims["email"].(string)
	username, _ := claims["preferred_username"].(string)
	if username == "" {
		username, _ = claims["name"].(string)
	}
	if username == "" {
		username = email
	}
	if username == "" {
		username = sub
	}

	// Resolves roles before creating user to avoid orphans
	resolvedRoles := h.resolveClaimRoles(claims)
	if len(resolvedRoles) == 0 && h.config.RejectUnmapped {
		h.log.Warn("OIDC: login rejected - no mapped roles for user %s", username)
		http.Redirect(w, r, "/login?error=no_mapped_roles", http.StatusFound)
		return
	}

	user, err := h.findOrCreateOIDCUser(ctx, sub, username, email)
	if err != nil {
		h.log.Error("OIDC: failed to find or create user (sub=%s, username=%s): %v", sub, username, err)
		http.Error(w, "Failed to authenticate user", http.StatusInternalServerError)
		return
	}

	// Drops oidc roles the IdP no longer grants
	claimRoles := make(map[string]bool, len(resolvedRoles))
	for _, roleName := range resolvedRoles {
		claimRoles[roleName] = true
		_ = h.store.AssignRole(ctx, user.Id, roleName, v1.RoleSource_ROLE_SOURCE_OIDC)
	}
	var oidcAssigned []*v1.UserRole
	if err := h.store.DB().WithContext(ctx).Where("user_id = ? AND source = ?", user.Id, v1.RoleSource_ROLE_SOURCE_OIDC).Find(&oidcAssigned).Error; err == nil {
		for _, ur := range oidcAssigned {
			if !claimRoles[ur.RoleName] {
				_ = h.store.UnassignRole(ctx, user.Id, ur.RoleName)
			}
		}
	}

	// Get user roles
	roleNames, err := h.store.GetUserRoleNames(ctx, user.Id)
	if err != nil {
		roleNames = []string{}
	}

	// Generate session token
	expiresAt := time.Now().Add(time.Duration(h.manager.config.SessionTimeout) * time.Second)
	token, err := h.manager.generateJWT(user.Id, user.Username, roleNames, expiresAt)
	if err != nil {
		h.log.Error("OIDC: failed to generate JWT: %v", err)
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	// Create session
	session := &v1.Session{
		Id:        uuid.New().String(),
		UserId:    user.Id,
		Token:     token,
		ExpiresAt: timestamppb.New(expiresAt),
	}
	if err := h.store.CreateSession(ctx, session); err != nil {
		h.log.Error("OIDC: failed to create session: %v", err)
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	h.log.Info("OIDC: user %s authenticated successfully", user.Username)

	// Fragment delivery keeps the token out of logs and Referer
	http.Redirect(w, r, fmt.Sprintf("/login#token=%s", url.QueryEscape(token)), http.StatusFound)
}

// Finds or creates OIDC user, same username can coexist
func (h *OIDCHandler) findOrCreateOIDCUser(ctx context.Context, sub, username, email string) (*v1.User, error) {
	// Tries to find by OIDC subject, for returning users
	if user, err := h.store.GetUserByOIDCSubject(ctx, sub); err == nil {
		if !user.IsActive {
			return nil, ErrUserNotActive
		}
		// Update email/last login on returning users
		if email != "" {
			user.Email = &email
		}
		user.LastLogin = timestamppb.Now()
		_ = h.store.UpdateUser(ctx, user)
		return user, nil
	}

	// Creates a new OIDC user
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	user := &v1.User{
		Id:           uuid.New().String(),
		Username:     username,
		Email:        emailPtr,
		AuthProvider: v1.AuthProvider_AUTH_PROVIDER_OIDC,
		IsActive:     true,
		OidcSubject:  sub,
		OidcIssuer:   h.config.IssuerURI,
	}
	if err := h.store.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create OIDC user: %w", err)
	}

	// Default roles keep local source so claim sync spares them
	defaultRoles, _ := h.store.GetDefaultRoles(ctx)
	for _, role := range defaultRoles {
		_ = h.store.AssignRole(ctx, user.Id, role.Name, v1.RoleSource_ROLE_SOURCE_LOCAL)
	}

	h.log.Info("OIDC: created new user %s", user.Username)
	return user, nil
}

// Resolve OIDC claim values to local roles
func (h *OIDCHandler) resolveClaimRoles(claims map[string]any) []string {
	if h.config.RoleClaim == "" {
		return nil
	}

	// Extract groups/roles from claims
	var claimValues []string
	claimValue, ok := claims[h.config.RoleClaim]
	if !ok {
		h.log.Warn("OIDC: role claim %q not found in token claims", h.config.RoleClaim)
		return nil
	}
	switch v := claimValue.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				claimValues = append(claimValues, s)
			}
		}
	case string:
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			claimValues = arr
		} else {
			claimValues = []string{v}
		}
	}

	// Resolve claim values to local role names
	var resolvedRoles []string
	if len(h.config.RoleMapping) > 0 {
		for _, claimVal := range claimValues {
			for mapKey, localRole := range h.config.RoleMapping {
				if strings.EqualFold(claimVal, mapKey) {
					resolvedRoles = append(resolvedRoles, localRole)
					break
				}
			}
		}
	} else if !h.config.RejectUnmapped {
		// Uses claim values directly when no mapping and not rejecting
		resolvedRoles = claimValues
	}

	return resolvedRoles
}

// Fetches extra claim from configured URL using gjson path
func (h *OIDCHandler) fetchExtraClaims(ctx context.Context, accessToken string) (map[string]any, error) {
	client := http.DefaultClient
	if h.httpClient != nil {
		client = h.httpClient
	}

	req, err := http.NewRequestWithContext(ctx, "GET", h.config.ExtraClaimsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("response is not valid JSON")
	}

	name := h.config.ExtraClaimsName
	if name == "" {
		name = "extra"
	}

	// Parses whole response as claim value if no key path
	if h.config.ExtraClaimsKey == "" {
		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		return map[string]any{name: parsed}, nil
	}

	result := gjson.GetBytes(body, h.config.ExtraClaimsKey)
	if !result.Exists() {
		return nil, fmt.Errorf("key %q not found in response", h.config.ExtraClaimsKey)
	}

	return map[string]any{name: gjsonToAny(result)}, nil
}

// Converts gjson.Result to native Go type for claims
func gjsonToAny(r gjson.Result) any {
	if r.IsArray() {
		var out []any
		r.ForEach(func(_, v gjson.Result) bool {
			out = append(out, gjsonToAny(v))
			return true
		})
		return out
	}
	if r.IsObject() {
		out := map[string]any{}
		r.ForEach(func(k, v gjson.Result) bool {
			out[k.String()] = gjsonToAny(v)
			return true
		})
		return out
	}
	return r.Value()
}

// True if claims satisfy required claim value match
func (h *OIDCHandler) checkRequiredClaim(claims map[string]any) bool {
	value, ok := claims[h.config.RequiredClaim]
	if !ok {
		return false
	}

	required := make(map[string]bool, len(h.config.RequiredValues))
	for _, v := range h.config.RequiredValues {
		required[v] = true
	}

	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if required[fmt.Sprint(item)] {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if required[item] {
				return true
			}
		}
	case string:
		return required[v]
	default:
		return required[fmt.Sprint(v)]
	}

	return false
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
