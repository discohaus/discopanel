package db

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const MinecraftDefaultPort = 25565

// Port the server listens on inside its container
func InContainerPort(s *v1.Server) int {
	if s.ProxyHostname != "" {
		return MinecraftDefaultPort
	}
	return int(s.Port)
}

// Fills transient proxy port from listener rows
func (s *Store) HydrateProxyPorts(ctx context.Context, servers ...*v1.Server) error {
	needed := false
	for _, server := range servers {
		if server != nil && server.ProxyListenerId != "" {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	listeners, err := s.ListProxyListeners(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]int32, len(listeners))
	for _, l := range listeners {
		byID[l.Id] = l.Port
	}
	for _, server := range servers {
		if server == nil {
			continue
		}
		if server.ProxyHostname != "" {
			server.ProxyPort = byID[server.ProxyListenerId]
		} else {
			server.ProxyPort = 0
		}
	}
	return nil
}

// Splits the platform's force include list into patterns
func ForceIncludePatterns(loader v1.ModLoader, cfg *v1.ServerProperties) []string {
	if cfg == nil {
		return nil
	}
	field := platformField(minecraft.PackSourceFor(loader), cfg)
	if field == nil || *field == nil {
		return nil
	}
	return minecraft.SplitPatterns(**field)
}

// Selects the platform's force include column
func platformField(source optionsv1.PackSource, cfg *v1.ServerProperties) **string {
	switch source {
	case optionsv1.PackSource_PACK_SOURCE_CURSEFORGE:
		return &cfg.CfForceIncludeMods
	case optionsv1.PackSource_PACK_SOURCE_MODRINTH:
		return &cfg.ModrinthForceIncludeFiles
	}
	return nil
}

type Store struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewSQLiteStore(cfg *config.Config) (*Store, error) {
	dsn := cfg.Database.Path
	// Pragmas reduce locked database errors under load
	if dsn != ":memory:" {
		pragmas := "_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=on"
		if strings.Contains(dsn, "?") {
			dsn += "&" + pragmas
		} else {
			dsn += "?" + pragmas
		}
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get database handle: %w", err)
	}

	if cfg.Database.MaxConnections > 0 {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxConnections)
	}
	if cfg.Database.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	}
	if cfg.Database.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(cfg.Database.ConnMaxLifetime) * time.Second)
	}

	store := &Store{db: db, cfg: cfg}

	if cfg.Database.AutoMigrate {
		if err := store.Migrate(); err != nil {
			return nil, fmt.Errorf("failed to migrate database: %w", err)
		}
	}

	return store, nil
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Creates the server then seeds its synced properties row
func (s *Store) CreateServer(ctx context.Context, server *v1.Server) error {
	err := s.db.WithContext(ctx).Create(server).Error
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}
	return s.SyncServerPropertiesWithServer(ctx, server)
}

// Saves the server then re-syncs its properties row
func (s *Store) UpdateServer(ctx context.Context, server *v1.Server) error {
	if err := s.db.WithContext(ctx).Save(server).Error; err != nil {
		return err
	}
	return s.SyncServerPropertiesWithServer(ctx, server)
}

// Sweeps every child row explicitly, old tables lack live cascades
func (s *Store) DeleteServer(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tokenIDs []string
		if err := tx.Model(&v1.Module{}).Where("server_id = ? AND token_id != ''", id).Pluck("token_id", &tokenIDs).Error; err != nil {
			return err
		}
		if len(tokenIDs) > 0 {
			if err := tx.Where("id IN ?", tokenIDs).Delete(&v1.ApiToken{}).Error; err != nil {
				return err
			}
		}
		for _, child := range []any{
			&v1.TaskExecution{},
			&v1.ScheduledTask{},
			&v1.Module{},
			&v1.Mod{},
			&v1.ServerProperties{},
			&v1.MetricsSample{},
			&v1.ServerAction{},
			&v1.FindingDismissal{},
		} {
			if err := tx.Where("server_id = ?", id).Delete(child).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&v1.Server{}, "id = ?", id).Error
	})
}

// Uses datetime() since stored timestamps may lack UTC offset

// Returns ordered samples, aggregated into buckets when bucketSeconds is positive
func (s *Store) GetMetricsHistory(ctx context.Context, serverID string, from, to time.Time, bucketSeconds int) ([]*v1.MetricsSample, error) {
	var samples []*v1.MetricsSample
	if bucketSeconds <= 0 {
		err := s.db.WithContext(ctx).
			Where("server_id = ? AND datetime(timestamp) >= datetime(?) AND datetime(timestamp) <= datetime(?)",
				serverID, from.UTC(), to.UTC()).
			Order("datetime(timestamp) ASC").
			Find(&samples).Error
		return samples, err
	}
	query := `
		SELECT server_id, ? AS resolution,
			datetime((CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ?, 'unixepoch') AS timestamp,
			AVG(tps) AS tps, AVG(mspt) AS mspt, MAX(players) AS players,
			AVG(cpu_percent) AS cpu_percent, AVG(memory_mb) AS memory_mb,
			AVG(heap_used_mb) AS heap_used_mb, MAX(disk_bytes) AS disk_bytes,
			MAX(proxy_active_conns) AS proxy_active_conns, SUM(proxy_bytes_in) AS proxy_bytes_in,
			SUM(proxy_bytes_out) AS proxy_bytes_out, SUM(proxy_logins) AS proxy_logins
		FROM metrics_samples
		WHERE server_id = ? AND datetime(timestamp) >= datetime(?) AND datetime(timestamp) <= datetime(?)
		GROUP BY CAST(strftime('%s', timestamp) AS INTEGER) / ?
		ORDER BY timestamp ASC`
	err := s.db.WithContext(ctx).Model(&v1.MetricsSample{}).
		Raw(query, bucketSeconds, bucketSeconds, bucketSeconds, serverID, from.UTC(), to.UTC(), bucketSeconds).
		Scan(&samples).Error
	return samples, err
}

// Folds raw samples older than cutoff into buckets
func (s *Store) RollupMetricsSamples(ctx context.Context, olderThan time.Time, bucketSeconds int) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := `
			INSERT INTO metrics_samples (server_id, resolution, timestamp, tps, mspt, players, cpu_percent, memory_mb, heap_used_mb, disk_bytes,
				proxy_active_conns, proxy_bytes_in, proxy_bytes_out, proxy_logins)
			SELECT server_id, ?,
				datetime((CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ?, 'unixepoch'),
				AVG(tps), AVG(mspt), MAX(players), AVG(cpu_percent), AVG(memory_mb), AVG(heap_used_mb), MAX(disk_bytes),
				MAX(proxy_active_conns), SUM(proxy_bytes_in), SUM(proxy_bytes_out), SUM(proxy_logins)
			FROM metrics_samples
			WHERE resolution = 0 AND datetime(timestamp) < datetime(?)
			GROUP BY server_id, CAST(strftime('%s', timestamp) AS INTEGER) / ?`
		if err := tx.Exec(insert, bucketSeconds, bucketSeconds, bucketSeconds, olderThan.UTC(), bucketSeconds).Error; err != nil {
			return err
		}
		return tx.Where("resolution = 0 AND datetime(timestamp) < datetime(?)", olderThan.UTC()).
			Delete(&v1.MetricsSample{}).Error
	})
}

// Clears all ephemeral property fields
func (s *Store) ClearEphemeralPropertyFields(ctx context.Context, serverID string) error {
	config, err := s.GetServerProperties(ctx, serverID)
	if err != nil {
		return err
	}
	config.ForceProvision = nil
	return s.UpdateServerProperties(ctx, config)
}

// Syncs system fields in ServerProperties from Server settings
func (s *Store) SyncServerPropertiesWithServer(ctx context.Context, server *v1.Server) error {
	config, err := s.GetServerProperties(ctx, server.Id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			config = s.CreateDefaultServerProperties(server.Id)
		} else {
			return err
		}
	}

	int32Ptr := func(i int32) *int32 { return &i }
	config.ServerPort = int32Ptr(server.Port)
	config.MaxPlayers = int32Ptr(server.MaxPlayers)
	runtimespec.SyncPropertiesMemory(config, server)

	return s.UpdateServerProperties(ctx, config)
}

func (s *Store) CreateDefaultServerProperties(serverID string) *v1.ServerProperties {
	boolPtr := func(b bool) *bool { return &b }
	stringPtr := func(s string) *string { return &s }
	int32Ptr := func(i int32) *int32 { return &i }

	config := &v1.ServerProperties{
		Id:           serverID + "-config",
		ServerId:     serverID,
		Eula:         stringPtr("TRUE"),
		EnableRcon:   boolPtr(true),
		RconPassword: stringPtr(generateRCONPassword()),
		RconPort:     int32Ptr(25575),
		Difficulty:   stringPtr("easy"),
		Mode:         stringPtr("survival"),
		MaxPlayers:   int32Ptr(20),
	}

	// Skip global settings lookup when creating global settings
	if serverID == GlobalSettingsID {
		return config
	}

	// Copies non-nil global settings pointers into the new row
	var globalSettings v1.ServerProperties
	err := s.db.Where("id = ?", GlobalSettingsID).First(&globalSettings).Error
	if err == nil {
		globalValue := reflect.ValueOf(&globalSettings).Elem()
		configValue := reflect.ValueOf(config).Elem()
		configType := configValue.Type()

		for i := 0; i < configType.NumField(); i++ {
			field := configType.Field(i)
			if field.PkgPath != "" {
				continue
			}
			// Server specific fields never inherit
			if field.Name == "Id" || field.Name == "ServerId" || field.Name == "UpdatedAt" ||
				field.Name == "RconPassword" ||
				field.Name == "InitMemory" || field.Name == "MaxMemory" || field.Name == "ServerPort" ||
				field.Name == "MaxPlayers" {
				continue
			}

			globalField := globalValue.FieldByName(field.Name)
			if globalField.IsValid() && globalField.Kind() == reflect.Pointer && !globalField.IsNil() {
				configValue.Field(i).Set(globalField)
			}
		}
	}

	return config
}

// Server ids are public so the secret must be random
const rconPasswordAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Generates the default RCON password once at properties creation
func generateRCONPassword() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = rconPasswordAlphabet[int(b)%len(rconPasswordAlphabet)]
	}
	return string(out)
}

// Filters indexed modpacks with optional search terms
func (s *Store) SearchIndexedModpacks(ctx context.Context, query string, gameVersion string, modLoader string, indexer string, offset, limit int) ([]*v1.IndexedModpack, int64, error) {
	db := s.db.WithContext(ctx).Model(&v1.IndexedModpack{})

	if query != "" {
		db = db.Where("name LIKE ? OR summary LIKE ?", "%"+query+"%", "%"+query+"%")
	}

	if gameVersion != "" {
		db = db.Where("game_versions LIKE ?", "%"+gameVersion+"%")
	}

	if modLoader != "" {
		db = db.Where("mod_loaders LIKE ?", "%"+modLoader+"%")
	}

	if indexer != "" {
		db = db.Where("indexer = ?", indexer)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var modpacks []*v1.IndexedModpack
	err := db.Order("download_count DESC").
		Offset(offset).
		Limit(limit).
		Find(&modpacks).Error

	return modpacks, total, err
}

// Checks if any servers are using the specified modpack
func (s *Store) CheckModpackInUse(ctx context.Context, modpackID string) ([]*v1.Server, error) {
	var servers []*v1.Server
	var configs []*v1.ServerProperties

	// Manual modpacks set CFSlug to manual plus the id
	cfSlug := "manual-" + modpackID

	if err := s.db.WithContext(ctx).Where("cf_slug = ?", cfSlug).Find(&configs).Error; err != nil {
		return nil, err
	}

	if len(configs) > 0 {
		serverIDs := make([]string, 0, len(configs))
		for _, config := range configs {
			serverIDs = append(serverIDs, config.ServerId)
		}
		if err := s.db.WithContext(ctx).Where("id IN ?", serverIDs).Find(&servers).Error; err != nil {
			return nil, err
		}
	}

	return servers, nil
}

// Favorited modpack ids as a lookup set
func (s *Store) FavoriteModpackIDs(ctx context.Context) (map[string]bool, error) {
	var ids []string
	if err := s.db.WithContext(ctx).Model(&v1.ModpackFavorite{}).Pluck("modpack_id", &ids).Error; err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// Modpack counts grouped by indexer
func (s *Store) CountIndexedModpacksByIndexer(ctx context.Context) (map[string]int64, error) {
	var rows []struct {
		Indexer string
		Count   int64
	}
	if err := s.db.WithContext(ctx).Model(&v1.IndexedModpack{}).
		Select("indexer, COUNT(*) as count").Group("indexer").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.Indexer] = r.Count
	}
	return counts, nil
}

// Global Settings operations (using ServerProperties with a special ID)
const GlobalSettingsID = "global-settings"

func (s *Store) GetGlobalSettings(ctx context.Context) (*v1.ServerProperties, bool, error) {
	var config v1.ServerProperties
	isNew := false
	err := s.db.WithContext(ctx).Where("id = ?", GlobalSettingsID).First(&config).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Create empty global settings, no defaults
			created := &v1.ServerProperties{
				Id:       GlobalSettingsID,
				ServerId: GlobalSettingsID,
			}
			if err := s.db.WithContext(ctx).Create(created).Error; err != nil {
				return nil, isNew, err
			}

			isNew = true
			return created, isNew, nil
		}
		return nil, isNew, err
	}
	return &config, isNew, nil
}

func (s *Store) UpdateGlobalSettings(ctx context.Context, config *v1.ServerProperties) error {
	config.Id = GlobalSettingsID
	config.ServerId = GlobalSettingsID
	return s.UpdateServerProperties(ctx, config)
}

func (s *Store) SeedGlobalSettings() error {
	ctx := context.Background()
	_, isNew, err := s.GetGlobalSettings(ctx)
	if err != nil {
		return err
	}
	if isNew || s.cfg.Minecraft.ResetGlobal {
		gc := s.CreateDefaultServerProperties(GlobalSettingsID)
		if len(s.cfg.Minecraft.GlobalConfig) > 0 {
			mapstructure.WeakDecode(s.cfg.Minecraft.GlobalConfig, gc)
			gc.Id = GlobalSettingsID + "-config"
			gc.ServerId = GlobalSettingsID
		}
		return s.UpdateGlobalSettings(ctx, gc)
	}
	return nil
}

// Returns the singleton proxy config, defaults when missing
func (s *Store) GetProxyConfig(ctx context.Context) (*v1.ProxyConfig, bool, error) {
	var config v1.ProxyConfig
	err := s.db.WithContext(ctx).First(&config).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Missing row mirrors the file config defaults
			return &v1.ProxyConfig{
				Id:      "default",
				Enabled: s.cfg.Proxy.Enabled,
				BaseUrl: s.cfg.Proxy.BaseUrl,
			}, true, nil
		}
		return nil, false, err
	}
	return &config, false, nil
}

// Persists the singleton proxy config row
func (s *Store) SaveProxyConfig(ctx context.Context, config *v1.ProxyConfig) error {
	if config.Id == "" {
		config.Id = "default"
	}
	return s.UpdateProxyConfig(ctx, config)
}

// Seeds the fixed system roles when missing
func (s *Store) SeedSystemRoles() error {
	ctx := context.Background()
	roles := []*v1.Role{
		{Id: "role-admin", Name: "admin", Description: "Full system access", IsSystem: true},
		{Id: "role-user", Name: "user", Description: "Standard user access", IsSystem: true, IsDefault: true},
		{Id: "role-anonymous", Name: "anonymous", Description: "Unauthenticated user access", IsSystem: true},
		{Id: "role-module", Name: "module", Description: "Module container access", IsSystem: true},
	}
	for _, role := range roles {
		_, err := s.GetRoleByName(ctx, role.Name)
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := s.CreateRole(ctx, role); err != nil {
			return err
		}
	}
	return nil
}

// Deletes a role after system and assignment checks
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	role, err := s.GetRole(ctx, id)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return fmt.Errorf("cannot delete system role")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role_name = ?", role.Name).Delete(&v1.UserRole{}).Error; err != nil {
			return err
		}
		return tx.Delete(&v1.Role{}, "id = ?", id).Error
	})
}

// Assigns a role once, repeat calls are no-ops
func (s *Store) AssignRole(ctx context.Context, userID, roleName string, source v1.RoleSource) error {
	existing, err := s.GetUserRoleAssignment(ctx, userID, roleName)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	return s.CreateUserRole(ctx, &v1.UserRole{
		UserId:   userID,
		RoleName: roleName,
		Source:   source,
	})
}

// Role names for a user, system roles first
func (s *Store) GetUserRoleNames(ctx context.Context, userID string) ([]string, error) {
	var names []string
	err := s.db.WithContext(ctx).
		Model(&v1.UserRole{}).
		Select("user_roles.role_name").
		Joins("LEFT JOIN roles ON roles.name = user_roles.role_name").
		Where("user_roles.user_id = ?", userID).
		Order("roles.is_system DESC, roles.name ASC").
		Pluck("user_roles.role_name", &names).Error
	if err != nil {
		return nil, err
	}
	return names, nil
}

// Deletes all sessions, tokens, roles, invites, and users
func (s *Store) ResetAllUsers(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("1 = 1").Delete(&v1.Session{}).Error; err != nil {
			return fmt.Errorf("failed to delete sessions: %w", err)
		}
		if err := tx.Where("1 = 1").Delete(&v1.ApiToken{}).Error; err != nil {
			return fmt.Errorf("failed to delete api tokens: %w", err)
		}
		if err := tx.Where("1 = 1").Delete(&v1.UserRole{}).Error; err != nil {
			return fmt.Errorf("failed to delete user roles: %w", err)
		}
		if err := tx.Where("1 = 1").Delete(&v1.RegistrationInvite{}).Error; err != nil {
			return fmt.Errorf("failed to delete registration invites: %w", err)
		}
		if err := tx.Where("1 = 1").Delete(&v1.User{}).Error; err != nil {
			return fmt.Errorf("failed to delete users: %w", err)
		}
		return nil
	})
}

// Removes old executions per task, always keeping the newest few
func (s *Store) CleanOldTaskExecutions(ctx context.Context, olderThan time.Time, keepMinimum int) error {
	var taskIDs []string
	if err := s.db.WithContext(ctx).Model(&v1.ScheduledTask{}).Pluck("id", &taskIDs).Error; err != nil {
		return err
	}

	for _, taskID := range taskIDs {
		var count int64
		if err := s.db.WithContext(ctx).Model(&v1.TaskExecution{}).Where("task_id = ?", taskID).Count(&count).Error; err != nil {
			continue
		}

		// Only delete when count exceeds the minimum to keep
		if count > int64(keepMinimum) {
			var keepIDs []string
			s.db.WithContext(ctx).Model(&v1.TaskExecution{}).
				Where("task_id = ?", taskID).
				Order("datetime(started_at) DESC").
				Limit(keepMinimum).
				Pluck("id", &keepIDs)

			s.db.WithContext(ctx).
				Where("task_id = ? AND datetime(started_at) < datetime(?) AND id NOT IN ?", taskID, olderThan.UTC(), keepIDs).
				Delete(&v1.TaskExecution{})
		}
	}
	return nil
}

// Transport a protocol rides on, unspecified reads tcp
func TransportOf(p v1.ModuleProtocol) v1.NetworkTransport {
	if p == v1.ModuleProtocol_MODULE_PROTOCOL_UDP {
		return v1.NetworkTransport_NETWORK_TRANSPORT_UDP
	}
	return v1.NetworkTransport_NETWORK_TRANSPORT_TCP
}

// Returns enabled tasks subscribed to an event for a server
func (s *Store) ListEventTriggeredTasks(ctx context.Context, serverID string, eventType v1.TriggeredEventType) ([]*v1.ScheduledTask, error) {
	tasks, err := s.ListEventScheduledTasks(ctx, serverID, v1.TaskStatus_TASK_STATUS_ENABLED, v1.ScheduleType_SCHEDULE_TYPE_EVENT)
	if err != nil {
		return nil, err
	}
	matching := make([]*v1.ScheduledTask, 0, len(tasks))
	for _, t := range tasks {
		for _, e := range t.EventTriggers {
			if e == eventType {
				matching = append(matching, t)
				break
			}
		}
	}
	return matching, nil
}

// Keeps the per-server ledger bounded
const maxServerActions = 2000

// Appends one action row to the server's ledger
func (s *Store) AppendServerAction(ctx context.Context, action *v1.ServerAction) error {
	if action.Timestamp == nil {
		action.Timestamp = timestamppb.Now()
	}
	if err := s.CreateServerAction(ctx, action); err != nil {
		return err
	}
	if action.Id%128 == 0 {
		s.pruneServerActions(ctx, action.ServerId)
	}
	return nil
}

func (s *Store) pruneServerActions(ctx context.Context, serverID string) {
	s.db.WithContext(ctx).Exec(
		"DELETE FROM server_actions WHERE server_id = ? AND id NOT IN (SELECT id FROM server_actions WHERE server_id = ? ORDER BY id DESC LIMIT ?)",
		serverID, serverID, maxServerActions)
}

// Returns ledger rows oldest first, after_id pages forward
func (s *Store) GetServerActions(ctx context.Context, serverID string, afterID uint) ([]*v1.ServerAction, error) {
	var actions []*v1.ServerAction
	q := s.db.WithContext(ctx).Where("server_id = ?", serverID)
	if afterID > 0 {
		q = q.Where("id > ?", afterID)
	}
	err := q.Order("id asc").Limit(maxServerActions).Find(&actions).Error
	return actions, err
}
