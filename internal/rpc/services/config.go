package services

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/discopanel/internal/config"
	storage "github.com/nickheyer/discopanel/internal/db"
	"github.com/nickheyer/discopanel/internal/docker"
	"github.com/nickheyer/discopanel/internal/proxy"
	"github.com/nickheyer/discopanel/pkg/logger"
	v1 "github.com/nickheyer/discopanel/pkg/proto/discopanel/v1"
	"github.com/nickheyer/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"gorm.io/gorm"
)

var _ discopanelv1connect.ConfigServiceHandler = (*ConfigService)(nil)

type ConfigService struct {
	store  *storage.Store
	config *config.Config
	docker *docker.Client
	proxy  *proxy.Manager
	log    *logger.Logger
}

// Creates new config service
func NewConfigService(store *storage.Store, cfg *config.Config, docker *docker.Client, proxy *proxy.Manager, log *logger.Logger) *ConfigService {
	return &ConfigService{
		store:  store,
		config: cfg,
		docker: docker,
		proxy:  proxy,
		log:    log,
	}
}

// Gets server config
func (s *ConfigService) GetServerConfig(ctx context.Context, req *connect.Request[v1.GetServerConfigRequest]) (*connect.Response[v1.GetServerConfigResponse], error) {
	msg := req.Msg

	// Get server to ensure it exists
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Ensure config is synced with server
	if err := s.store.SyncServerConfigWithServer(ctx, server); err != nil {
		s.log.Error("Failed to sync server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to sync server configuration"))
	}

	// Get the synced config
	config, err := s.store.GetServerConfig(ctx, msg.ServerId)
	if err != nil {
		s.log.Error("Failed to get server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get server configuration"))
	}

	// Convert to categorized format
	categories, err := buildConfigCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format configuration"))
	}

	return connect.NewResponse(&v1.GetServerConfigResponse{
		Categories: categories,
	}), nil
}

// Updates server config
func (s *ConfigService) UpdateServerConfig(ctx context.Context, req *connect.Request[v1.UpdateServerConfigRequest]) (*connect.Response[v1.UpdateServerConfigResponse], error) {
	msg := req.Msg

	// Get server info
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get existing config
	config, err := s.store.GetServerConfig(ctx, msg.ServerId)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			config = s.store.CreateDefaultServerConfig(msg.ServerId)
		} else {
			s.log.Error("Failed to get server config: %v", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get server configuration"))
		}
	}

	// Apply updates w/ reflection
	if err := applyConfigUpdates(config, msg.Updates); err != nil {
		s.log.Error("Failed to apply config updates: %v", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("failed to apply configuration updates"))
	}

	// Save updated config
	if err := s.store.SaveServerConfig(ctx, config); err != nil {
		s.log.Error("Failed to save server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save server configuration"))
	}

	// If server has a container, we need to recreate it to apply a new env
	if server.ContainerID != "" && s.docker != nil {
		if err := s.recreateContainer(ctx, server, config); err != nil {
			s.log.Error("Config saved but container recreation failed: %v", err)
		}
	}

	// A stopped server will not produce a status transition after a config save,
	// so refresh its route explicitly to apply Lazy Server changes immediately.
	if s.proxy != nil && server.ProxyHostname != "" && server.ContainerID != "" {
		if err := s.proxy.UpdateServerRoute(server); err != nil {
			s.log.Error("Config saved but proxy route update failed: %v", err)
		}
	}

	// Return updated config
	categories, err := buildConfigCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format configuration"))
	}

	return connect.NewResponse(&v1.UpdateServerConfigResponse{
		Categories: categories,
	}), nil
}

// Gets global settings
func (s *ConfigService) GetGlobalSettings(ctx context.Context, req *connect.Request[v1.GetGlobalSettingsRequest]) (*connect.Response[v1.GetGlobalSettingsResponse], error) {
	config, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get global settings"))
	}

	categories, err := buildConfigCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format configuration"))
	}

	return connect.NewResponse(&v1.GetGlobalSettingsResponse{
		Categories: categories,
	}), nil
}

// Updates global settings
func (s *ConfigService) UpdateGlobalSettings(ctx context.Context, req *connect.Request[v1.UpdateGlobalSettingsRequest]) (*connect.Response[v1.UpdateGlobalSettingsResponse], error) {
	msg := req.Msg
	config, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get global settings"))
	}

	if err := applyConfigUpdates(config, msg.Updates); err != nil {
		s.log.Error("Failed to apply config updates: %v", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("failed to apply configuration updates"))
	}

	if err := s.store.UpdateGlobalSettings(ctx, config); err != nil {
		s.log.Error("Failed to save global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save global settings"))
	}

	categories, err := buildConfigCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format configuration"))
	}

	return connect.NewResponse(&v1.UpdateGlobalSettingsResponse{
		Categories: categories,
	}), nil
}

func (s *ConfigService) recreateContainer(ctx context.Context, server *storage.Server, config *storage.ServerConfig) error {
	oldContainerID := server.ContainerID
	wasRunning := false
	if server.Status == storage.StatusRunning {
		wasRunning = true
		if _, err := s.docker.StopContainer(ctx, oldContainerID); err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
	}

	if err := s.docker.RemoveContainer(ctx, oldContainerID); err != nil {
		return err
	}

	newContainerID, err := s.docker.CreateContainer(ctx, server, config)
	if err != nil {
		return err
	}

	server.ContainerID = newContainerID
	if err := s.store.UpdateServer(ctx, server); err != nil {
		return err
	}

	if wasRunning {
		if err := s.docker.StartContainer(ctx, newContainerID); err != nil {
			return err
		}
		server.Status = storage.StatusStarting
		now := time.Now()
		server.LastStarted = &now
		s.store.UpdateServer(ctx, server)
	}
	s.log.Info("Container recreated with updated configuration")
	return nil
}

// Maps updates w/ reflection
func applyConfigUpdates(config any, updates map[string]string) error {
	configValue := reflect.ValueOf(config).Elem()
	configType := configValue.Type()

	for key, strValue := range updates {
		// Find the field by json tag
		fieldIndex := -1
		for i := 0; i < configType.NumField(); i++ {
			field := configType.Field(i)
			jsonTag := field.Tag.Get("json")
			if jsonTag == key {
				fieldIndex = i
				break
			}
		}

		if fieldIndex == -1 {
			continue // Skip unknown fields
		}

		fieldValue := configValue.Field(fieldIndex)
		if !fieldValue.CanSet() {
			continue
		}

		// Unset
		if strValue == "" {
			if fieldValue.Kind() == reflect.Pointer {
				fieldValue.Set(reflect.Zero(fieldValue.Type()))
				continue
			}
			fieldValue.Set(reflect.Zero(fieldValue.Type()))
			continue
		}

		targetType := fieldValue.Type()
		isPtr := targetType.Kind() == reflect.Pointer
		if isPtr {
			targetType = targetType.Elem()
		}

		var val reflect.Value

		switch targetType.Kind() {
		case reflect.String:
			val = reflect.ValueOf(strValue)
		case reflect.Bool:
			b, err := strconv.ParseBool(strValue)
			if err != nil {
				return fmt.Errorf("invalid boolean for key %s: %v", key, err)
			}
			val = reflect.ValueOf(b)
		case reflect.Int, reflect.Int32, reflect.Int64:
			i, err := strconv.ParseInt(strValue, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid integer for key %s: %v", key, err)
			}
			// Convert to specific int type
			if targetType.Kind() == reflect.Int {
				val = reflect.ValueOf(int(i))
			} else if targetType.Kind() == reflect.Int32 {
				val = reflect.ValueOf(int32(i))
			} else {
				val = reflect.ValueOf(i)
			}
		case reflect.Float32, reflect.Float64:
			f, err := strconv.ParseFloat(strValue, 64)
			if err != nil {
				return fmt.Errorf("invalid float for key %s: %v", key, err)
			}
			if targetType.Kind() == reflect.Float32 {
				val = reflect.ValueOf(float32(f))
			} else {
				val = reflect.ValueOf(f)
			}
		default:
			// Skip complex types we don't support updating this way
			continue
		}

		if isPtr {
			// New pointer and set value
			ptr := reflect.New(targetType)
			ptr.Elem().Set(val)
			fieldValue.Set(ptr)
		} else {
			fieldValue.Set(val)
		}
	}

	return nil
}

func buildConfigCategories(config any) ([]*v1.ConfigCategory, error) {
	categories := []*v1.ConfigCategory{
		{Name: "JVM Configuration", Properties: []*v1.ConfigProperty{}},
		{Name: "Server Settings", Properties: []*v1.ConfigProperty{}},
		{Name: "Game Settings", Properties: []*v1.ConfigProperty{}},
		{Name: "World Generation", Properties: []*v1.ConfigProperty{}},
		{Name: "RCON", Properties: []*v1.ConfigProperty{}},
		{Name: "Resource Pack", Properties: []*v1.ConfigProperty{}},
		{Name: "Management Server", Properties: []*v1.ConfigProperty{}},
		{Name: "Ops/Admins", Properties: []*v1.ConfigProperty{}},
		{Name: "Whitelist", Properties: []*v1.ConfigProperty{}},
		{Name: "Auto-Pause", Properties: []*v1.ConfigProperty{}},
		{Name: "Auto-Stop", Properties: []*v1.ConfigProperty{}},
		{Name: "CurseForge", Properties: []*v1.ConfigProperty{}},
		{Name: "Modrinth", Properties: []*v1.ConfigProperty{}},
	}

	configValue := reflect.ValueOf(config).Elem()
	configType := configValue.Type()

	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" || jsonTag == "id" || jsonTag == "serverId" || jsonTag == "updatedAt" {
			continue
		}

		// Metadata tags
		envTag := field.Tag.Get("env")
		defaultTag := field.Tag.Get("default")
		descTag := field.Tag.Get("desc")
		inputTag := field.Tag.Get("input")
		requiredTag := field.Tag.Get("required")
		labelTag := field.Tag.Get("label")
		systemTag := field.Tag.Get("system")
		ephemeralTag := field.Tag.Get("ephemeral")

		fieldValue := configValue.Field(i)
		var strValue string
		if fieldValue.Kind() == reflect.Pointer {
			if fieldValue.IsNil() {
				// explicitly nil/unset
				strValue = ""
			} else {
				// dereference and stringify
				strValue = fmt.Sprintf("%v", fieldValue.Elem().Interface())
			}
		} else {
			// stringify direct value
			strValue = fmt.Sprintf("%v", fieldValue.Interface())
		}

		label := labelTag
		if label == "" {
			label = jsonTag
		}

		prop := &v1.ConfigProperty{
			Key:         jsonTag,
			Label:       label,
			Value:       strValue,
			Type:        inputTag,
			Description: descTag,
			Required:    requiredTag == "true",
			System:      systemTag == "true",
			Ephemeral:   ephemeralTag == "true",
			EnvVar:      envTag,
		}

		// Only set default_value if it's explicitly specified in the struct tag
		if defaultTag != "" {
			prop.DefaultValue = &defaultTag
		}

		if inputTag == "select" {
			prop.Options = getSelectOptions(jsonTag)
		}

		categoryIndex := getCategoryIndex(jsonTag)
		if categoryIndex >= 0 && categoryIndex < len(categories) {
			categories[categoryIndex].Properties = append(categories[categoryIndex].Properties, prop)
		}
	}

	// Filter empty
	var nonEmptyCategories []*v1.ConfigCategory
	for _, cat := range categories {
		if len(cat.Properties) > 0 {
			nonEmptyCategories = append(nonEmptyCategories, cat)
		}
	}

	return nonEmptyCategories, nil
}

// getSelectOptions returns options for select fields
func getSelectOptions(key string) []string {
	switch key {
	case "difficulty":
		return []string{"peaceful", "easy", "normal", "hard"}
	case "mode":
		return []string{"creative", "survival", "adventure", "spectator"}
	case "cfSetLevelFrom":
		return []string{"", "WORLD_FILE", "OVERRIDES"}
	case "userApiProvider":
		return []string{"playerdb", "mojang"}
	case "existingOpsFile":
		return []string{"SKIP", "SYNCHRONIZE", "MERGE", "SYNC_FILE_MERGE_LIST"}
	case "existingWhitelistFile":
		return []string{"SKIP", "SYNCHRONIZE", "MERGE", "SYNC_FILE_MERGE_LIST"}
	case "modrinthDownloadDependencies":
		return []string{"none", "required", "optional"}
	case "modrinthProjectsDefaultVersionType":
		return []string{"release", "beta", "alpha"}
	case "modrinthModpackVersionType":
		return []string{"release", "beta", "alpha"}
	case "modrinthLoader":
		return []string{"forge", "fabric", "quilt"}
	default:
		return []string{}
	}
}

// Category a property belongs to
func getCategoryIndex(key string) int {
	switch key {
	// JVM Configuration (0)
	case "uid", "gid", "memory", "initMemory", "maxMemory", "tz", "enableRollingLogs",
		"enableJmx", "jmxHost", "useAikarFlags", "useMeowiceFlags", "useMeowiceGraalvmFlags",
		"jvmOpts", "jvmXxOpts", "jvmDdOpts", "extraArgs", "logTimestamp":
		return 0

	// Server Settings (1)
	case "type", "customServer", "customJarExec", "eula", "version", "motd", "icon", "overrideIcon", "serverName",
		"serverPort", "console", "gui", "stopDuration", "setupOnly", "execDirectly",
		"stopServerAnnounceDelay", "proxy", "useFlareFlags", "useSimdFlags",
		"serverPropertiesEscapeUnicode", "bugReportLink", "customServerProperties":
		return 1

	// Game Settings (2)
	case "difficulty", "maxPlayers", "allowNether", "announcePlayerAchievements",
		"enableCommandBlock", "forceGamemode", "hardcore", "snooperEnabled", "maxBuildHeight",
		"spawnAnimals", "spawnMonsters", "spawnNpcs", "spawnProtection", "viewDistance",
		"mode", "pvp", "onlineMode", "allowFlight", "playerIdleTimeout", "syncChunkWrites",
		"enableStatus", "entityBroadcastRangePercentage", "functionPermissionLevel",
		"networkCompressionThreshold", "opPermissionLevel", "preventProxyConnections",
		"useNativeTransport", "simulationDistance", "enableQuery", "queryPort",
		"acceptsTransfers", "broadcastConsoleToOps", "enforceSecureProfile",
		"hideOnlinePlayers", "logIps", "maxChainedNeighborUpdates", "pauseWhenEmptySeconds",
		"rateLimit", "statusHeartbeatInterval":
		return 2

	// World Generation (3)
	case "generateStructures", "maxWorldSize", "seed", "levelType", "generatorSettings", "level",
		"regionFileCompression":
		return 3

	// RCON (4)
	case "enableRcon", "rconPassword", "rconPort", "rconCmdsStartup",
		"rconCmdsOnConnect", "rconCmdsFirstConnect", "rconCmdsOnDisconnect", "rconCmdsLastDisconnect":
		return 4

	// Resource Pack (5)
	case "resourcePack", "resourcePackSha1", "resourcePackEnforce", "resourcePackId", "resourcePackPrompt":
		return 5

	// Management Server (6)
	case "managementServerAllowedOrigins", "managementServerEnabled", "managementServerHost",
		"managementServerPort", "managementServerSecret", "managementServerTlsEnabled",
		"managementServerTlsKeystore", "managementServerTlsKeystorePassword":
		return 6

	// Ops/Admins (7)
	case "userApiProvider", "ops", "opsFile", "existingOpsFile":
		return 7

	// Whitelist (8)
	case "enableWhitelist", "whitelist", "whitelistFile", "overrideWhitelist",
		"existingWhitelistFile", "enforceWhitelist":
		return 8

	// Auto-Pause (9)
	case "enableAutopause", "autopauseTimeoutEst", "autopauseTimeoutInit", "autopauseTimeoutKn",
		"autopausePeriod", "autopauseKnockInterface", "debugAutopause":
		return 9

	// Auto-Stop (10)
	case "enableAutostop", "enableLazyServer", "lazyServerMotd", "lazyServerStartingMessage",
		"autostopTimeoutEst", "autostopTimeoutInit", "autostopPeriod", "debugAutostop":
		return 10

	// CurseForge (11)
	case "cfApiKey", "cfApiKeyFile", "cfPageUrl", "cfSlug", "cfFileId", "cfFilenameMatcher",
		"cfExcludeIncludeFile", "cfExcludeMods", "cfForceIncludeMods", "cfForceSynchronize",
		"cfSetLevelFrom", "cfParallelDownloads", "cfOverridesSkipExisting", "cfForceReinstallModloader":
		return 11

	// Modrinth (12)
	case "modrinthModpack", "modrinthModpackVersionType", "modrinthVersion", "modrinthLoader",
		"modrinthIgnoreMissingFiles", "modrinthExcludeFiles", "modrinthForceIncludeFiles",
		"modrinthForceSynchronize", "modrinthDefaultExcludeIncludes", "modrinthOverridesExclusions",
		"modrinthProjects", "modrinthDownloadDependencies", "modrinthProjectsDefaultVersionType",
		"versionFromModrinthProjects":
		return 12

	default:
		return -1 // Unknown
	}
}
