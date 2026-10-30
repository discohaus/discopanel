package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserNotActive        = errors.New("user is not active")
	ErrInvalidToken         = errors.New("invalid token")
	ErrSessionExpired       = errors.New("session expired")
	ErrLocalAuthDisabled    = errors.New("local authentication is disabled")
	ErrRegistrationDisabled = errors.New("registration is disabled")
	ErrSessionTimeoutMin    = errors.New("session timeout must be at least 300 seconds (5 minutes)")
	ErrApiTokenExpired      = errors.New("api token has expired")
	ErrApiTokenNotFound     = errors.New("api token not found")
	ErrInvalidRecoveryKey   = errors.New("invalid recovery key")
)

// Auth override keys
const (
	settingLocalEnabled      = "auth.local.enabled"
	settingAllowRegistration = "auth.local.allow_registration"
	settingAnonymousAccess   = "auth.anonymous_access"
	settingSessionTimeout    = "auth.session_timeout"
)

type Manager struct {
	store       *db.Store
	enforcer    *rbac.Enforcer
	config      *config.AuthConfig
	cfgMu       sync.RWMutex
	jwtSecret   []byte
	recoveryKey string
}

const jwtSecretSettingKey = "jwt_secret"

func NewManager(store *db.Store, enforcer *rbac.Enforcer, cfg *config.AuthConfig) (*Manager, error) {
	ctx := context.Background()
	var secret []byte

	// Config value wins, then DB, then generate and persist
	if cfg.JWTSecret != "" {
		secret = []byte(cfg.JWTSecret)
	} else {
		stored, err := store.GetSystemSetting(ctx, jwtSecretSettingKey)
		if err == nil && stored.Value != "" {
			secret, err = hex.DecodeString(stored.Value)
			if err != nil {
				return nil, fmt.Errorf("failed to decode stored JWT secret: %w", err)
			}
		} else {
			// Generate new secret and persist it
			secret = make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return nil, fmt.Errorf("failed to generate JWT secret: %w", err)
			}
			if err := store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: jwtSecretSettingKey, Value: hex.EncodeToString(secret)}); err != nil {
				return nil, fmt.Errorf("failed to persist JWT secret: %w", err)
			}
			// Clean all sessions since old tokens are now invalid
			_ = store.CleanAllSessions(ctx)
		}
	}

	// Generate recovery key
	recoveryBytes := make([]byte, 32)
	if _, err := rand.Read(recoveryBytes); err != nil {
		return nil, fmt.Errorf("failed to generate recovery key: %w", err)
	}

	m := &Manager{
		store:       store,
		enforcer:    enforcer,
		config:      cfg,
		jwtSecret:   secret,
		recoveryKey: hex.EncodeToString(recoveryBytes),
	}

	m.loadSettingOverrides(ctx)

	return m, nil
}

func (m *Manager) Login(ctx context.Context, username, password string) (*v1.User, []string, string, time.Time, error) {
	if !m.IsLocalAuthEnabled() {
		return nil, nil, "", time.Time{}, ErrLocalAuthDisabled
	}

	user, err := m.store.GetUserByUsernameAndProvider(ctx, username, v1.AuthProvider_AUTH_PROVIDER_LOCAL)
	if err != nil {
		return nil, nil, "", time.Time{}, ErrInvalidCredentials
	}

	if !checkPassword(user.PasswordHash, password) {
		return nil, nil, "", time.Time{}, ErrInvalidCredentials
	}

	if !user.IsActive {
		return nil, nil, "", time.Time{}, ErrUserNotActive
	}

	// Get user roles
	roleNames, err := m.store.GetUserRoleNames(ctx, user.Id)
	if err != nil {
		return nil, nil, "", time.Time{}, fmt.Errorf("failed to get user roles: %w", err)
	}

	// Generate token
	expiresAt := time.Now().Add(m.SessionTTL())
	token, err := m.generateJWT(user.Id, user.Username, roleNames, expiresAt)
	if err != nil {
		return nil, nil, "", time.Time{}, err
	}

	// Create session
	session := &v1.Session{
		Id:        uuid.New().String(),
		UserId:    user.Id,
		Token:     token,
		ExpiresAt: timestamppb.New(expiresAt),
	}
	if err := m.store.CreateSession(ctx, session); err != nil {
		return nil, nil, "", time.Time{}, err
	}

	// Update last login
	user.LastLogin = timestamppb.Now()
	_ = m.store.UpdateUser(ctx, user)

	return user, roleNames, token, expiresAt, nil
}

func (m *Manager) ValidateSession(ctx context.Context, token string) (*v1.User, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}

	// Validate JWT
	claims, err := m.validateJWT(token)
	if err != nil {
		return nil, err
	}

	// Get session from database
	session, err := m.store.GetSession(ctx, token, time.Now().UTC())
	if err != nil {
		return nil, ErrSessionExpired
	}

	userID, _ := claims["user_id"].(string)
	if session.UserId != userID {
		return nil, ErrInvalidToken
	}

	// Get user
	user, err := m.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	if !user.IsActive {
		return nil, ErrUserNotActive
	}

	// Get roles
	roleNames, err := m.store.GetUserRoleNames(ctx, user.Id)
	if err != nil {
		return nil, err
	}

	user.Roles = roleNames
	return user, nil
}

func (m *Manager) Logout(ctx context.Context, token string) error {
	return m.store.DeleteSession(ctx, token)
}

func (m *Manager) CreateLocalUser(ctx context.Context, username, email, password string) (*v1.User, error) {
	hashedPassword, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}

	user := &v1.User{
		Id:           uuid.New().String(),
		Username:     username,
		Email:        emailPtr,
		AuthProvider: v1.AuthProvider_AUTH_PROVIDER_LOCAL,
		IsActive:     true,
		PasswordHash: hashedPassword,
	}

	if err := m.store.CreateUser(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}

func (m *Manager) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error {
	user, err := m.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}

	if user.AuthProvider != v1.AuthProvider_AUTH_PROVIDER_LOCAL {
		return errors.New("password change only available for local auth users")
	}

	if !checkPassword(user.PasswordHash, oldPassword) {
		return ErrInvalidCredentials
	}

	hashedPassword, err := hashPassword(newPassword)
	if err != nil {
		return err
	}

	user.PasswordHash = hashedPassword
	return m.store.UpdateUser(ctx, user)
}

func (m *Manager) AnonymousUser() *v1.User {
	return &v1.User{
		Id:           "anonymous",
		Username:     "anonymous",
		Roles:        []string{"anonymous"},
		AuthProvider: v1.AuthProvider_AUTH_PROVIDER_ANONYMOUS,
	}
}

// Synthetic admin identity while auth is disabled
func (m *Manager) SystemUser() *v1.User {
	return &v1.User{
		Id:           "admin",
		Username:     "admin",
		Roles:        []string{"admin"},
		AuthProvider: v1.AuthProvider_AUTH_PROVIDER_NONE,
	}
}

// Reports whether the context user holds a global grant
func (m *Manager) Can(ctx context.Context, resource optionsv1.ResourceType, action optionsv1.ActionType) bool {
	user := GetUserFromContext(ctx)
	if user == nil {
		return false
	}
	allowed, err := m.enforcer.Enforce(user.Roles, resource, action, "*")
	return err == nil && allowed
}

func (m *Manager) IsAnonymousAccessEnabled() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.config.AnonymousAccess
}

func (m *Manager) IsAnyAuthEnabled() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.config.Local.Enabled || m.config.OIDC.Enabled
}

// Session lifetime from the live timeout setting
func (m *Manager) SessionTTL() time.Duration {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return time.Duration(m.config.SessionTimeout) * time.Second
}

// Validates bearer token, handles session, API token, or anon
func (m *Manager) AuthenticateFromHeader(ctx context.Context, authHeader string) (*v1.User, error) {
	if !m.IsAnyAuthEnabled() {
		return m.SystemUser(), nil
	}

	token := ""
	if authHeader != "" {
		token = strings.TrimPrefix(strings.TrimPrefix(authHeader, "Bearer "), "bearer ")
	}

	if token != "" {
		if strings.HasPrefix(token, "dp_") {
			return m.ValidateApiToken(ctx, token)
		}
		return m.ValidateSession(ctx, token)
	}

	if m.IsAnonymousAccessEnabled() {
		return m.AnonymousUser(), nil
	}

	return nil, ErrInvalidToken
}

func (m *Manager) IsLocalAuthEnabled() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.config.Local.Enabled
}

func (m *Manager) IsRegistrationAllowed() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.config.Local.Enabled && m.config.Local.AllowRegistration
}

// Returns a settings snapshot safe to read without locks
func (m *Manager) GetConfig() config.AuthConfig {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return *m.config
}

// Applies db setting overrides so db wins over config.yaml
func (m *Manager) loadSettingOverrides(ctx context.Context) {
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	if v, err := m.store.GetSystemSetting(ctx, settingLocalEnabled); err == nil {
		if b, err := strconv.ParseBool(v.Value); err == nil {
			m.config.Local.Enabled = b
		}
	}
	if v, err := m.store.GetSystemSetting(ctx, settingAllowRegistration); err == nil {
		if b, err := strconv.ParseBool(v.Value); err == nil {
			m.config.Local.AllowRegistration = b
		}
	}
	if v, err := m.store.GetSystemSetting(ctx, settingAnonymousAccess); err == nil {
		if b, err := strconv.ParseBool(v.Value); err == nil {
			m.config.AnonymousAccess = b
		}
	}
	if v, err := m.store.GetSystemSetting(ctx, settingSessionTimeout); err == nil {
		if i, err := strconv.Atoi(v.Value); err == nil && i > 0 {
			m.config.SessionTimeout = i
		}
	}
}

// UpdateSettings updates mutable auth settings. Only non-nil parameters are applied.
func (m *Manager) UpdateSettings(ctx context.Context, localEnabled, allowReg, anonAccess *bool, sessionTimeout *int32) error {
	// Validate session timeout
	if sessionTimeout != nil && *sessionTimeout < 300 {
		return ErrSessionTimeoutMin
	}

	// Persist and apply each provided field
	if localEnabled != nil {
		if err := m.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: settingLocalEnabled, Value: strconv.FormatBool(*localEnabled)}); err != nil {
			return fmt.Errorf("failed to save local auth setting: %w", err)
		}
		m.config.Local.Enabled = *localEnabled
	}

	if allowReg != nil {
		if err := m.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: settingAllowRegistration, Value: strconv.FormatBool(*allowReg)}); err != nil {
			return fmt.Errorf("failed to save registration setting: %w", err)
		}
		m.config.Local.AllowRegistration = *allowReg
	}

	if anonAccess != nil {
		if err := m.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: settingAnonymousAccess, Value: strconv.FormatBool(*anonAccess)}); err != nil {
			return fmt.Errorf("failed to save anonymous access setting: %w", err)
		}
		m.config.AnonymousAccess = *anonAccess
	}

	if sessionTimeout != nil {
		if err := m.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: settingSessionTimeout, Value: strconv.Itoa(int(*sessionTimeout))}); err != nil {
			return fmt.Errorf("failed to save session timeout setting: %w", err)
		}
		m.config.SessionTimeout = int(*sessionTimeout)
	}

	return nil
}

// Creates an API token, plaintext returned and hash stored
func (m *Manager) GenerateApiToken(ctx context.Context, userID, name string, expiresInDays *int32) (string, *v1.ApiToken, error) {
	// Generate 32 random bytes
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("failed to generate token: %w", err)
	}

	plaintext := "dp_" + base64.RawURLEncoding.EncodeToString(raw)

	// SHA-256 hash for storage
	hash := sha256.Sum256([]byte(plaintext))
	hashHex := hex.EncodeToString(hash[:])

	var expiresAt *timestamppb.Timestamp
	if expiresInDays != nil && *expiresInDays > 0 {
		expiresAt = timestamppb.New(time.Now().Add(time.Duration(*expiresInDays) * 24 * time.Hour))
	}

	token := &v1.ApiToken{
		Id:        uuid.New().String(),
		Name:      name,
		ExpiresAt: expiresAt,
		UserId:    userID,
		TokenHash: hashHex,
	}

	if err := m.store.CreateApiToken(ctx, token); err != nil {
		return "", nil, fmt.Errorf("failed to store api token: %w", err)
	}

	return plaintext, token, nil
}

// Mints a module token, empty user means supermodule
func (m *Manager) GenerateModuleToken(ctx context.Context, userID, moduleName, moduleID, role string) (string, *v1.ApiToken, error) {
	if userID == "" && role == "" {
		return "", nil, fmt.Errorf("supermodule token requires a module role")
	}
	tokenName := fmt.Sprintf("module:%s:%s", moduleName, moduleID)
	plaintext, token, err := m.GenerateApiToken(ctx, userID, tokenName, nil)
	if err != nil {
		return "", nil, err
	}

	// Mark as module token
	token.IsModuleToken = true
	token.ModuleRole = role
	if err := m.store.DB().WithContext(ctx).Save(token).Error; err != nil {
		return "", nil, fmt.Errorf("failed to mark token as module token: %w", err)
	}

	return plaintext, token, nil
}

// Validates a raw dp token and returns the user
func (m *Manager) ValidateApiToken(ctx context.Context, rawToken string) (*v1.User, error) {
	if !strings.HasPrefix(rawToken, "dp_") {
		return nil, ErrInvalidToken
	}

	// Hash the incoming token
	hash := sha256.Sum256([]byte(rawToken))
	hashHex := hex.EncodeToString(hash[:])

	// Look up by hash
	apiToken, err := m.store.GetApiTokenByHash(ctx, hashHex)
	if err != nil {
		return nil, ErrApiTokenNotFound
	}

	// Check expiry
	if apiToken.ExpiresAt != nil && apiToken.ExpiresAt.AsTime().Before(time.Now()) {
		return nil, ErrApiTokenExpired
	}

	// Supermodule tokens are userless, identity comes from token
	if apiToken.IsModuleToken && apiToken.UserId == "" {
		if apiToken.ModuleRole == "" {
			return nil, ErrInvalidToken
		}
		go func() {
			_ = m.store.UpdateApiTokenLastUsed(context.Background(), time.Now().UTC(), apiToken.Id)
		}()
		return &v1.User{
			Id:           apiToken.Id,
			Username:     apiToken.Name,
			Roles:        []string{apiToken.ModuleRole},
			AuthProvider: v1.AuthProvider_AUTH_PROVIDER_MODULE,
		}, nil
	}

	// Resolve user
	user, err := m.store.GetUser(ctx, apiToken.UserId)
	if err != nil {
		return nil, fmt.Errorf("failed to get token user: %w", err)
	}

	if !user.IsActive {
		return nil, ErrUserNotActive
	}

	// Module tokens carry their pinned role, never the creator's
	var roleNames []string
	if apiToken.IsModuleToken {
		roleNames = []string{"module"}
		if apiToken.ModuleRole != "" {
			roleNames = []string{apiToken.ModuleRole}
		}
	} else {
		roleNames, err = m.store.GetUserRoleNames(ctx, user.Id)
		if err != nil {
			return nil, fmt.Errorf("failed to get user roles: %w", err)
		}
	}

	// Background-update last_used_at
	go func() {
		_ = m.store.UpdateApiTokenLastUsed(context.Background(), time.Now().UTC(), apiToken.Id)
	}()

	user.Roles = roleNames
	return user, nil
}

func (m *Manager) GetRecoveryKey() string {
	return m.recoveryKey
}

func (m *Manager) UseRecoveryKey(ctx context.Context, key string) error {
	if m.recoveryKey == "" || key != m.recoveryKey {
		return ErrInvalidRecoveryKey
	}
	if err := m.store.ResetAllUsers(ctx); err != nil {
		return fmt.Errorf("failed to reset users: %w", err)
	}
	m.recoveryKey = ""
	return nil
}

func (m *Manager) generateJWT(userID, username string, roles []string, expiresAt time.Time) (string, error) {
	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		"roles":    roles,
		"exp":      expiresAt.Unix(),
		"iat":      time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.jwtSecret)
}

func (m *Manager) validateJWT(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return m.jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if exp, ok := claims["exp"].(float64); ok {
			if time.Now().Unix() > int64(exp) {
				return nil, ErrSessionExpired
			}
		}
		return claims, nil
	}

	return nil, ErrInvalidToken
}

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func checkPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}
