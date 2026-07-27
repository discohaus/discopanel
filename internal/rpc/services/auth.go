package services

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ discopanelv1connect.AuthServiceHandler = (*AuthService)(nil)

type AuthService struct {
	store       *storage.Store
	authManager *auth.Manager
	enforcer    *rbac.Enforcer
	oidcHandler *auth.OIDCHandler
	log         *logger.Logger
}

func NewAuthService(store *storage.Store, authManager *auth.Manager, enforcer *rbac.Enforcer, oidcHandler *auth.OIDCHandler, log *logger.Logger) *AuthService {
	return &AuthService{
		store:       store,
		authManager: authManager,
		enforcer:    enforcer,
		oidcHandler: oidcHandler,
		log:         log,
	}
}

func (s *AuthService) GetAuthStatus(ctx context.Context, req *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
	userCount, err := s.store.CountUsers(ctx)
	if err != nil {
		s.log.Error("Failed to count users: %v", err)
		userCount = 0
	}

	oidcEnabled := s.oidcHandler != nil && s.oidcHandler.IsEnabled()

	return connect.NewResponse(&v1.GetAuthStatusResponse{
		LocalAuthEnabled:       s.authManager.IsLocalAuthEnabled(),
		OidcEnabled:            oidcEnabled,
		AllowRegistration:      s.authManager.IsRegistrationAllowed(),
		FirstUserSetup:         userCount == 0,
		AnonymousAccessEnabled: s.authManager.IsAnonymousAccessEnabled(),
	}), nil
}

func (s *AuthService) Login(ctx context.Context, req *connect.Request[v1.LoginRequest]) (*connect.Response[v1.LoginResponse], error) {
	msg := req.Msg

	if msg.Username == "" || msg.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password are required"))
	}

	user, roles, token, expiresAt, err := s.authManager.Login(ctx, msg.Username, msg.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) || errors.Is(err, auth.ErrUserNotActive) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid credentials"))
		}
		if errors.Is(err, auth.ErrLocalAuthDisabled) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("local authentication is disabled"))
		}
		s.log.Error("Login failed: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("login failed"))
	}

	user.Roles = roles
	return connect.NewResponse(&v1.LoginResponse{
		Token:     token,
		User:      user.Redact(),
		ExpiresAt: timestamppb.New(expiresAt),
	}), nil
}

func (s *AuthService) Logout(ctx context.Context, req *connect.Request[v1.LogoutRequest]) (*connect.Response[v1.LogoutResponse], error) {
	// Extract token from Authorization header
	token := ""
	if authHeader := req.Header().Get("Authorization"); authHeader != "" {
		token, _ = strings.CutPrefix(authHeader, "Bearer ")
		token, _ = strings.CutPrefix(token, "bearer ")
	}

	if token != "" {
		if err := s.authManager.Logout(ctx, token); err != nil {
			s.log.Debug("Logout error: %v", err)
		}
	}

	return connect.NewResponse(&v1.LogoutResponse{
		Message: "logged out",
	}), nil
}

func (s *AuthService) Register(ctx context.Context, req *connect.Request[v1.RegisterRequest]) (*connect.Response[v1.RegisterResponse], error) {
	msg := req.Msg

	userCount, _ := s.store.CountUsers(ctx)
	isFirstUser := userCount == 0

	var invite *v1.RegistrationInvite

	if msg.InviteCode != nil && *msg.InviteCode != "" {
		// Validate invite
		var err error
		invite, err = s.store.GetRegistrationInviteByCode(ctx, *msg.InviteCode)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("invalid invite code"))
		}
		if invite.ExpiresAt != nil && invite.ExpiresAt.AsTime().Before(time.Now()) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("invite has expired"))
		}
		if invite.MaxUses > 0 && invite.UseCount >= invite.MaxUses {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("invite has reached maximum uses"))
		}
		if invite.PinHash != "" {
			if msg.InvitePin == nil || *msg.InvitePin == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("PIN is required for this invite"))
			}
			if err := bcrypt.CompareHashAndPassword([]byte(invite.PinHash), []byte(*msg.InvitePin)); err != nil {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("incorrect PIN"))
			}
		}
	} else if !isFirstUser && !s.authManager.IsRegistrationAllowed() {
		return nil, connect.NewError(connect.CodePermissionDenied, auth.ErrRegistrationDisabled)
	}

	if msg.Username == "" || msg.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password are required"))
	}

	user, err := s.authManager.CreateLocalUser(ctx, msg.Username, msg.Email, msg.Password)
	if err != nil {
		s.log.Error("Registration failed: %v", err)
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("registration failed"))
	}

	// First user gets admin, invite gives invite roles, else default
	if isFirstUser {
		_ = s.store.AssignRole(ctx, user.Id, "admin", v1.RoleSource_ROLE_SOURCE_LOCAL)
	} else if invite != nil && len(invite.Roles) > 0 {
		for _, roleName := range invite.Roles {
			_ = s.store.AssignRole(ctx, user.Id, roleName, v1.RoleSource_ROLE_SOURCE_INVITE)
		}
	} else {
		defaultRoles, _ := s.store.GetDefaultRoles(ctx)
		for _, role := range defaultRoles {
			_ = s.store.AssignRole(ctx, user.Id, role.Name, v1.RoleSource_ROLE_SOURCE_LOCAL)
		}
	}

	// Increment invite use count after successful registration
	if invite != nil {
		_ = s.store.IncrementInviteUseCount(ctx, invite.Id)
	}

	user.Roles, _ = s.store.GetUserRoleNames(ctx, user.Id)

	return connect.NewResponse(&v1.RegisterResponse{
		User: user.Redact(),
	}), nil
}

func (s *AuthService) GetCurrentUser(ctx context.Context, req *connect.Request[v1.GetCurrentUserRequest]) (*connect.Response[v1.GetCurrentUserResponse], error) {
	authUser := auth.GetUserFromContext(ctx)
	if authUser == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	// Auth disabled means synthetic admin has no db row
	if !s.authManager.IsAnyAuthEnabled() {
		var permissions []*v1.Permission
		for _, role := range authUser.Roles {
			permissions = append(permissions, s.enforcer.GetPermissionsForRole(role)...)
		}

		return connect.NewResponse(&v1.GetCurrentUserResponse{
			User:        authUser,
			Permissions: permissions,
		}), nil
	}

	// Fetch user from db
	dbUser, err := s.store.GetUser(ctx, authUser.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get user"))
	}

	// Fetch roles from db
	roles, _ := s.store.GetUserRoleNames(ctx, authUser.Id)

	// Collect permissions from all user roles via the RBAC enforcer
	var permissions []*v1.Permission
	seen := make(map[string]bool)
	for _, role := range roles {
		for _, p := range s.enforcer.GetPermissionsForRole(role) {
			key := fmt.Sprintf("%d:%d:%s", p.Resource, p.Action, p.ObjectId)
			if !seen[key] {
				seen[key] = true
				permissions = append(permissions, p)
			}
		}
	}

	dbUser.Roles = roles
	return connect.NewResponse(&v1.GetCurrentUserResponse{
		User:        dbUser.Redact(),
		Permissions: permissions,
	}), nil
}

func (s *AuthService) ChangePassword(ctx context.Context, req *connect.Request[v1.ChangePasswordRequest]) (*connect.Response[v1.ChangePasswordResponse], error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	msg := req.Msg
	if msg.OldPassword == "" || msg.NewPassword == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("old and new passwords are required"))
	}

	if err := s.authManager.ChangePassword(ctx, user.Id, msg.OldPassword, msg.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("incorrect current password"))
		}
		s.log.Error("Change password failed: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to change password"))
	}

	return connect.NewResponse(&v1.ChangePasswordResponse{
		Message: "password changed",
	}), nil
}

func (s *AuthService) GetOIDCLoginURL(ctx context.Context, req *connect.Request[v1.GetOIDCLoginURLRequest]) (*connect.Response[v1.GetOIDCLoginURLResponse], error) {
	if s.oidcHandler == nil || !s.oidcHandler.IsEnabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("OIDC is not enabled"))
	}

	return connect.NewResponse(&v1.GetOIDCLoginURLResponse{
		LoginUrl: "/api/v1/auth/oidc/login",
	}), nil
}

func (s *AuthService) GetAuthConfig(ctx context.Context, req *connect.Request[v1.GetAuthConfigRequest]) (*connect.Response[v1.GetAuthConfigResponse], error) {
	cfg := s.authManager.GetConfig()
	oidcEnabled := s.oidcHandler != nil && s.oidcHandler.IsEnabled()

	userCount, err := s.store.CountUsers(ctx)
	if err != nil {
		s.log.Error("Failed to count users: %v", err)
		userCount = 0
	}

	resp := &v1.GetAuthConfigResponse{
		LocalAuthEnabled:  cfg.Local.Enabled,
		AllowRegistration: cfg.Local.AllowRegistration,
		AnonymousAccess:   cfg.AnonymousAccess,
		SessionTimeout:    int32(cfg.SessionTimeout),
		OidcEnabled:       oidcEnabled,
		FirstUserSetup:    userCount == 0,
	}

	if oidcEnabled {
		resp.OidcIssuerUri = &cfg.OIDC.IssuerURI
		resp.OidcClientId = &cfg.OIDC.ClientID
		resp.OidcRedirectUrl = &cfg.OIDC.RedirectURL
		resp.OidcScopes = cfg.OIDC.Scopes
		resp.OidcRoleClaim = &cfg.OIDC.RoleClaim
	}

	return connect.NewResponse(resp), nil
}

func (s *AuthService) UpdateAuthSettings(ctx context.Context, req *connect.Request[v1.UpdateAuthSettingsRequest]) (*connect.Response[v1.UpdateAuthSettingsResponse], error) {
	msg := req.Msg

	if err := s.authManager.UpdateSettings(ctx, msg.LocalAuthEnabled, msg.AllowRegistration, msg.AnonymousAccess, msg.SessionTimeout); err != nil {
		if errors.Is(err, auth.ErrSessionTimeoutMin) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to update auth settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update auth settings"))
	}

	// Return the updated config
	configResp, err := s.GetAuthConfig(ctx, connect.NewRequest(&v1.GetAuthConfigRequest{}))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.UpdateAuthSettingsResponse{
		Config: configResp.Msg,
	}), nil
}

func (s *AuthService) CreateInvite(ctx context.Context, req *connect.Request[v1.CreateInviteRequest]) (*connect.Response[v1.CreateInviteResponse], error) {
	msg := req.Msg

	// Validate roles exist
	if len(msg.Roles) > 0 {
		existingRoles, err := s.store.ListRoles(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list roles"))
		}
		roleSet := make(map[string]bool, len(existingRoles))
		for _, r := range existingRoles {
			roleSet[r.Name] = true
		}
		for _, roleName := range msg.Roles {
			if !roleSet[roleName] {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role not found: "+roleName))
			}
		}
	}

	// Generate crypto-random code
	codeBytes := make([]byte, 32)
	if _, err := rand.Read(codeBytes); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to generate invite code"))
	}
	code := base64.RawURLEncoding.EncodeToString(codeBytes)

	// Hash PIN if provided
	var pinHash string
	if msg.Pin != nil && *msg.Pin != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*msg.Pin), bcrypt.DefaultCost)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to hash PIN"))
		}
		pinHash = string(hash)
	}

	// Calculate expiration
	var expiresAt *time.Time
	if msg.ExpiresInHours != nil && *msg.ExpiresInHours > 0 {
		t := time.Now().Add(time.Duration(*msg.ExpiresInHours) * time.Hour)
		expiresAt = &t
	}

	// Get creator from context
	authUser := auth.GetUserFromContext(ctx)
	createdBy := ""
	if authUser != nil {
		createdBy = authUser.Username
	}

	invite := &v1.RegistrationInvite{
		Code:        code,
		Description: msg.Description,
		Roles:       msg.Roles,
		PinHash:     pinHash,
		MaxUses:     msg.MaxUses,
		ExpiresAt:   expiresAtPb(expiresAt),
		CreatedBy:   createdBy,
	}

	if err := s.store.CreateRegistrationInvite(ctx, invite); err != nil {
		s.log.Error("Failed to create invite: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create invite"))
	}

	invite.HasPin = invite.PinHash != ""
	return connect.NewResponse(&v1.CreateInviteResponse{
		Invite: invite.Redact(),
	}), nil
}

func (s *AuthService) ListInvites(ctx context.Context, req *connect.Request[v1.ListInvitesRequest]) (*connect.Response[v1.ListInvitesResponse], error) {
	invites, err := s.store.ListRegistrationInvites(ctx)
	if err != nil {
		s.log.Error("Failed to list invites: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list invites"))
	}

	protoInvites := make([]*v1.RegistrationInvite, 0, len(invites))
	for _, inv := range invites {
		inv.HasPin = inv.PinHash != ""
		protoInvites = append(protoInvites, inv.Redact())
	}

	return connect.NewResponse(&v1.ListInvitesResponse{
		Invites: protoInvites,
	}), nil
}

func (s *AuthService) GetInvite(ctx context.Context, req *connect.Request[v1.GetInviteRequest]) (*connect.Response[v1.GetInviteResponse], error) {
	invite, err := s.store.GetRegistrationInvite(ctx, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("invite not found"))
	}

	invite.HasPin = invite.PinHash != ""
	return connect.NewResponse(&v1.GetInviteResponse{
		Invite: invite.Redact(),
	}), nil
}

func (s *AuthService) DeleteInvite(ctx context.Context, req *connect.Request[v1.DeleteInviteRequest]) (*connect.Response[v1.DeleteInviteResponse], error) {
	if err := s.store.DeleteRegistrationInvite(ctx, req.Msg.Id); err != nil {
		s.log.Error("Failed to delete invite: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete invite"))
	}

	return connect.NewResponse(&v1.DeleteInviteResponse{}), nil
}

func (s *AuthService) ValidateInvite(ctx context.Context, req *connect.Request[v1.ValidateInviteRequest]) (*connect.Response[v1.ValidateInviteResponse], error) {
	if req.Msg.Code == "" {
		return connect.NewResponse(&v1.ValidateInviteResponse{Valid: false}), nil
	}

	invite, err := s.store.GetRegistrationInviteByCode(ctx, req.Msg.Code)
	if err != nil {
		return connect.NewResponse(&v1.ValidateInviteResponse{Valid: false}), nil
	}

	if invite.ExpiresAt != nil && invite.ExpiresAt.AsTime().Before(time.Now()) {
		return connect.NewResponse(&v1.ValidateInviteResponse{Valid: false}), nil
	}

	if invite.MaxUses > 0 && invite.UseCount >= invite.MaxUses {
		return connect.NewResponse(&v1.ValidateInviteResponse{Valid: false}), nil
	}

	return connect.NewResponse(&v1.ValidateInviteResponse{
		Valid:       true,
		RequiresPin: invite.PinHash != "",
		Description: invite.Description,
	}), nil
}

func (s *AuthService) CreateAPIToken(ctx context.Context, req *connect.Request[v1.CreateAPITokenRequest]) (*connect.Response[v1.CreateAPITokenResponse], error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	msg := req.Msg
	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("token name is required"))
	}

	plaintext, apiToken, err := s.authManager.GenerateApiToken(ctx, user.Id, msg.Name, msg.ExpiresInDays)
	if err != nil {
		s.log.Error("Failed to create API token: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create API token"))
	}

	return connect.NewResponse(&v1.CreateAPITokenResponse{
		PlaintextToken: plaintext,
		ApiToken:       apiToken.Redact(),
	}), nil
}

func (s *AuthService) ListAPITokens(ctx context.Context, req *connect.Request[v1.ListAPITokensRequest]) (*connect.Response[v1.ListAPITokensResponse], error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	tokens, err := s.store.ListApiTokensByUser(ctx, user.Id)
	if err != nil {
		s.log.Error("Failed to list API tokens: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list API tokens"))
	}

	protoTokens := make([]*v1.ApiToken, 0, len(tokens))
	for _, t := range tokens {
		protoTokens = append(protoTokens, t.Redact())
	}

	return connect.NewResponse(&v1.ListAPITokensResponse{
		ApiTokens: protoTokens,
	}), nil
}

func (s *AuthService) DeleteAPIToken(ctx context.Context, req *connect.Request[v1.DeleteAPITokenRequest]) (*connect.Response[v1.DeleteAPITokenResponse], error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("token ID is required"))
	}

	if err := s.store.DeleteUserApiToken(ctx, req.Msg.Id, user.Id); err != nil {
		s.log.Error("Failed to delete API token: %v", err)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("API token not found"))
	}

	return connect.NewResponse(&v1.DeleteAPITokenResponse{}), nil
}

func (s *AuthService) UseRecoveryKey(ctx context.Context, req *connect.Request[v1.UseRecoveryKeyRequest]) (*connect.Response[v1.UseRecoveryKeyResponse], error) {
	if req.Msg.RecoveryKey == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("recovery key is required"))
	}

	if err := s.authManager.UseRecoveryKey(ctx, req.Msg.RecoveryKey); err != nil {
		if errors.Is(err, auth.ErrInvalidRecoveryKey) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid recovery key"))
		}
		s.log.Error("Recovery key reset failed: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("recovery reset failed"))
	}

	return connect.NewResponse(&v1.UseRecoveryKeyResponse{
		Message: "panel reset to first-user setup",
	}), nil
}

// Optional deadline as a proto timestamp
func expiresAtPb(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
