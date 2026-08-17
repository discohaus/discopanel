// Replica of v2.0.15 schema
package v2schema

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

type Server struct {
	ID              string          `gorm:"primaryKey"`
	Name            string          `gorm:"not null"`
	Description     string          ``
	ModLoader       string          `gorm:"not null"`
	MCVersion       string          `gorm:"not null;column:mc_version"`
	ContainerID     string          `gorm:"column:container_id"`
	Status          string          `gorm:"not null"`
	Port            int             ``
	ProxyPort       int             `gorm:"column:proxy_port"`
	ProxyHostname   string          `gorm:"column:proxy_hostname;uniqueIndex:idx_proxy_hostname_listener,where:proxy_hostname != ''"`
	ProxyListenerID string          `gorm:"column:proxy_listener_id;uniqueIndex:idx_proxy_hostname_listener,where:proxy_listener_id != ''"`
	MaxPlayers      int             `gorm:"default:20;column:max_players"`
	Memory          int             `gorm:"default:4096"`
	CreatedAt       time.Time       `gorm:"autoCreateTime"`
	UpdatedAt       time.Time       `gorm:"autoUpdateTime"`
	LastStarted     *time.Time      `gorm:"column:last_started"`
	JavaVersion     string          `gorm:"column:java_version"`
	DockerImage     string          `gorm:"column:docker_image"`
	DataPath        string          `gorm:"not null;column:data_path"`
	Detached        bool            `gorm:"default:false;column:detached"`
	AutoStart       bool            `gorm:"default:false;column:auto_start"`
	TPSCommand      string          `gorm:"column:tps_command"`
	AdditionalPorts json.RawMessage `gorm:"column:additional_ports;serializer:json"`
	DockerOverrides json.RawMessage `gorm:"column:docker_overrides;type:text;serializer:json"`
}

func (Server) TableName() string { return "servers" }

type ServerConfig struct {
	ID        string    `gorm:"primaryKey"`
	ServerID  string    `gorm:"not null;index;column:server_id"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
	Server    *Server   `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE"`

	UID                    *int
	GID                    *int
	Memory                 *string
	InitMemory             *string
	MaxMemory              *string
	TZ                     *string
	EnableRollingLogs      *bool
	EnableJMX              *bool
	JMXHost                *string
	UseAikarFlags          *bool
	UseMeowiceFlags        *bool
	UseMeowiceGraalVMFlags *bool
	JVMOpts                *string
	JVMXXOpts              *string
	JVMDDOpts              *string
	ExtraArgs              *string
	LogTimestamp           *bool

	Type                           *string
	CustomServer                   *string
	CustomJarExec                  *string
	EULA                           *string
	Version                        *string
	MOTD                           *string
	Difficulty                     *string
	Icon                           *string
	OverrideIcon                   *bool
	MaxPlayers                     *int
	MaxWorldSize                   *int
	AllowNether                    *bool
	AnnouncePlayerAchievements     *bool
	EnableCommandBlock             *bool
	ForceGamemode                  *bool
	GenerateStructures             *bool
	Hardcore                       *bool
	SnooperEnabled                 *bool
	MaxBuildHeight                 *int
	SpawnAnimals                   *bool
	SpawnMonsters                  *bool
	SpawnNPCs                      *bool
	SpawnProtection                *int
	ViewDistance                   *int
	Seed                           *string
	Mode                           *string
	PVP                            *bool
	LevelType                      *string
	GeneratorSettings              *string
	Level                          *string
	OnlineMode                     *bool
	AllowFlight                    *bool
	ServerName                     *string
	ServerPort                     *int
	PlayerIdleTimeout              *int
	SyncChunkWrites                *bool
	EnableStatus                   *bool
	EntityBroadcastRangePercentage *int
	FunctionPermissionLevel        *int
	NetworkCompressionThreshold    *int
	OpPermissionLevel              *int
	PreventProxyConnections        *bool
	UseNativeTransport             *bool
	SimulationDistance             *int
	EnableQuery                    *bool
	QueryPort                      *int
	ServerPropertiesEscapeUnicode  *bool
	AcceptsTransfers               *bool
	BroadcastConsoleToOps          *bool
	BugReportLink                  *string
	EnforceSecureProfile           *bool
	HideOnlinePlayers              *bool
	LogIPs                         *bool
	MaxChainedNeighborUpdates      *int
	PauseWhenEmptySeconds          *int
	RateLimit                      *int
	RegionFileCompression          *string
	ResourcePackID                 *string
	ResourcePackPrompt             *string
	StatusHeartbeatInterval        *int
	ExecDirectly                   *bool
	StopServerAnnounceDelay        *int
	Proxy                          *string
	Console                        *bool
	GUI                            *bool
	StopDuration                   *int
	SetupOnly                      *bool
	UseFlareFlags                  *bool
	UseSimdFlags                   *bool
	CustomServerProperties         *string

	ResourcePack        *string
	ResourcePackSHA1    *string
	ResourcePackEnforce *bool

	ManagementServerAllowedOrigins      *string
	ManagementServerEnabled             *bool
	ManagementServerHost                *string
	ManagementServerPort                *int
	ManagementServerSecret              *string
	ManagementServerTLSEnabled          *bool
	ManagementServerTLSKeystore         *string
	ManagementServerTLSKeystorePassword *string

	UserAPIProvider *string
	Ops             *string
	OpsFile         *string
	ExistingOpsFile *string

	EnableWhitelist       *bool
	Whitelist             *string
	WhitelistFile         *string
	OverrideWhitelist     *bool
	ExistingWhitelistFile *string
	EnforceWhitelist      *bool

	EnableRCON             *bool
	RCONPassword           *string
	RCONPort               *int
	BroadcastRCONToOps     *bool
	RCONCmdsStartup        *string
	RCONCmdsOnConnect      *string
	RCONCmdsFirstConnect   *string
	RCONCmdsOnDisconnect   *string
	RCONCmdsLastDisconnect *string

	EnableAutopause         *bool
	AutopauseTimeoutEst     *int
	AutopauseTimeoutInit    *int
	AutopauseTimeoutKn      *int
	AutopausePeriod         *int
	AutopauseKnockInterface *string
	DebugAutopause          *bool

	EnableAutostop      *bool
	AutostopTimeoutEst  *int
	AutostopTimeoutInit *int
	AutostopPeriod      *int
	DebugAutostop       *bool

	ForgeVersion      *string
	ForgeInstaller    *string
	ForgeInstallerURL *string

	CFAPIKey                  *string
	CFAPIKeyFile              *string
	CFPageURL                 *string
	CFSlug                    *string
	CFFileID                  *string
	CFModpackZip              *string
	CFFilenameMatcher         *string
	CFExcludeIncludeFile      *string
	CFExcludeMods             *string
	CFForceIncludeMods        *string
	CFForceSynchronize        *bool
	CFSetLevelFrom            *string
	CFParallelDownloads       *int
	CFOverridesSkipExisting   *bool
	CFForceReinstallModloader *bool

	ModrinthModpack                    *string
	ModrinthModpackVersionType         *string
	ModrinthVersion                    *string
	ModrinthLoader                     *string
	ModrinthIgnoreMissingFiles         *string
	ModrinthExcludeFiles               *string
	ModrinthForceIncludeFiles          *string
	ModrinthForceSynchronize           *bool
	ModrinthDefaultExcludeIncludes     *string
	ModrinthOverridesExclusions        *string
	ModrinthProjects                   *string
	ModrinthDownloadDependencies       *string
	ModrinthProjectsDefaultVersionType *string
	VersionFromModrinthProjects        *bool
}

func (ServerConfig) TableName() string { return "server_configs" }

type Mod struct {
	ID          string    `gorm:"primaryKey"`
	ServerID    string    `gorm:"not null;index;column:server_id"`
	Name        string    `gorm:"not null"`
	FileName    string    `gorm:"not null;column:file_name"`
	Version     string    ``
	ModID       string    `gorm:"column:mod_id"`
	Description string    ``
	Enabled     bool      `gorm:"default:true"`
	UploadedAt  time.Time `gorm:"autoCreateTime;column:uploaded_at"`
	FileSize    int64     `gorm:"column:file_size"`
	Server      *Server   `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE"`
}

func (Mod) TableName() string { return "mods" }

type IndexedModpack struct {
	ID             string    `gorm:"primaryKey"`
	IndexerID      string    `gorm:"index;column:indexer_id"`
	Indexer        string    `gorm:"index"`
	Name           string    `gorm:"not null;index"`
	Slug           string    `gorm:"index"`
	Summary        string    ``
	Description    string    `gorm:"type:text"`
	LogoURL        string    `gorm:"column:logo_url"`
	WebsiteURL     string    `gorm:"column:website_url"`
	DownloadCount  int64     `gorm:"column:download_count"`
	Categories     string    ``
	GameVersions   string    ``
	ModLoaders     string    ``
	LatestFileID   string    `gorm:"column:latest_file_id"`
	DateCreated    time.Time `gorm:"column:date_created"`
	DateModified   time.Time `gorm:"column:date_modified"`
	DateReleased   time.Time `gorm:"column:date_released"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime"`
	IndexedAt      time.Time `gorm:"autoCreateTime"`
	MCVersion      string    `gorm:"column:mc_version"`
	JavaVersion    string    `gorm:"column:java_version"`
	DockerImage    string    `gorm:"column:docker_image"`
	RecommendedRAM int       `gorm:"column:recommended_ram"`
}

func (IndexedModpack) TableName() string { return "indexed_modpacks" }

type IndexedModpackFile struct {
	ID               string          `gorm:"primaryKey"`
	ModpackID        string          `gorm:"index;column:modpack_id"`
	DisplayName      string          `gorm:"column:display_name"`
	FileName         string          `gorm:"column:file_name"`
	FileDate         time.Time       `gorm:"column:file_date"`
	FileLength       int64           `gorm:"column:file_length"`
	ReleaseType      string          `gorm:"column:release_type"`
	DownloadURL      string          `gorm:"column:download_url"`
	GameVersions     string          ``
	ModLoader        string          `gorm:"column:mod_loader"`
	ServerPackFileID *string         `gorm:"column:server_pack_file_id"`
	Modpack          *IndexedModpack `gorm:"foreignKey:ModpackID;constraint:OnDelete:CASCADE"`
}

func (IndexedModpackFile) TableName() string { return "indexed_modpack_files" }

type ModpackFavorite struct {
	ID        string          `gorm:"primaryKey"`
	ModpackID string          `gorm:"index;column:modpack_id"`
	CreatedAt time.Time       `gorm:"autoCreateTime"`
	Modpack   *IndexedModpack `gorm:"foreignKey:ModpackID;constraint:OnDelete:CASCADE"`
}

func (ModpackFavorite) TableName() string { return "modpack_favorites" }

type ProxyConfig struct {
	ID        string    `gorm:"primaryKey"`
	Enabled   bool      `gorm:"not null;default:false"`
	BaseURL   string    `gorm:"column:base_url"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (ProxyConfig) TableName() string { return "proxy_configs" }

type ProxyListener struct {
	ID          string    `gorm:"primaryKey"`
	Port        int       `gorm:"not null;uniqueIndex"`
	Name        string    ``
	Description string    ``
	Enabled     bool      `gorm:"not null;default:true"`
	IsDefault   bool      `gorm:"not null;default:false"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

func (ProxyListener) TableName() string { return "proxy_listeners" }

type RegistrationInvite struct {
	ID          string          `gorm:"primaryKey"`
	Code        string          `gorm:"not null;uniqueIndex"`
	Description string          ``
	Roles       json.RawMessage `gorm:"column:roles;serializer:json"`
	PinHash     string          `gorm:"column:pin_hash"`
	MaxUses     int             `gorm:"default:0;column:max_uses"`
	UseCount    int             `gorm:"default:0;column:use_count"`
	ExpiresAt   *time.Time      `gorm:"column:expires_at"`
	CreatedBy   string          `gorm:"not null;column:created_by"`
	CreatedAt   time.Time       `gorm:"autoCreateTime"`
}

func (RegistrationInvite) TableName() string { return "registration_invites" }

type User struct {
	ID           string     `gorm:"primaryKey"`
	Username     string     `gorm:"not null;uniqueIndex:idx_user_provider"`
	Email        *string    `gorm:"index"`
	PasswordHash string     `gorm:"column:password_hash"`
	AuthProvider string     `gorm:"not null;default:'local';uniqueIndex:idx_user_provider"`
	OIDCSubject  string     `gorm:"column:oidc_subject;uniqueIndex:idx_oidc_identity,where:oidc_subject != ''"`
	OIDCIssuer   string     `gorm:"column:oidc_issuer;uniqueIndex:idx_oidc_identity,where:oidc_subject != ''"`
	IsActive     bool       `gorm:"not null;default:true"`
	LastLogin    *time.Time `gorm:"column:last_login"`
	CreatedAt    time.Time  `gorm:"autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"autoUpdateTime"`
}

func (User) TableName() string { return "users" }

type Role struct {
	ID          string    `gorm:"primaryKey"`
	Name        string    `gorm:"not null;uniqueIndex"`
	Description string    ``
	IsSystem    bool      `gorm:"not null;default:false"`
	IsDefault   bool      `gorm:"not null;default:false"`
	CreatedAt   time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`
}

func (Role) TableName() string { return "roles" }

type UserRole struct {
	ID        string    `gorm:"primaryKey"`
	UserID    string    `gorm:"not null;index;column:user_id"`
	RoleName  string    `gorm:"not null;index;column:role_name"`
	Source    string    `gorm:"not null;default:'local'"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

func (UserRole) TableName() string { return "user_roles" }

type SystemSetting struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"not null"`
}

func (SystemSetting) TableName() string { return "system_settings" }

type APIToken struct {
	ID            string     `gorm:"primaryKey"`
	UserID        string     `gorm:"not null;index;column:user_id"`
	Name          string     `gorm:"not null"`
	TokenHash     string     `gorm:"not null;uniqueIndex;column:token_hash"`
	ExpiresAt     *time.Time `gorm:"column:expires_at"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at"`
	IsModuleToken bool       `gorm:"default:false;column:is_module_token"`
	CreatedAt     time.Time  `gorm:"autoCreateTime"`
	User          *User      `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
}

func (APIToken) TableName() string { return "api_tokens" }

type Session struct {
	ID        string    `gorm:"primaryKey"`
	UserID    string    `gorm:"not null;index;column:user_id"`
	Token     string    `gorm:"not null;uniqueIndex"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	User      *User     `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
}

func (Session) TableName() string { return "sessions" }

type ScheduledTask struct {
	ID          string `gorm:"primaryKey"`
	ServerID    string `gorm:"not null;index;column:server_id"`
	Name        string `gorm:"not null"`
	Description string ``
	TaskType    string `gorm:"not null;column:task_type"`
	Status      string `gorm:"not null;default:enabled"`
	Schedule    string `gorm:"not null"`

	CronExpr      string          `gorm:"column:cron_expr"`
	IntervalSecs  int             `gorm:"column:interval_secs"`
	RunAt         *time.Time      `gorm:"column:run_at"`
	EventTriggers json.RawMessage `gorm:"column:event_triggers;serializer:json"`
	NextRun       *time.Time      `gorm:"index;column:next_run"`
	LastRun       *time.Time      `gorm:"column:last_run"`
	Timezone      string          `gorm:"default:UTC"`

	Config string `gorm:"type:text"`

	Timeout       int  `gorm:"default:300"`
	RetryCount    int  `gorm:"default:0"`
	RetryDelay    int  `gorm:"default:60"`
	RequireOnline bool `gorm:"default:true"`
	FailureNotify bool `gorm:"default:false"`

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`

	Server *Server `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE"`
}

func (ScheduledTask) TableName() string { return "scheduled_tasks" }

type TaskExecution struct {
	ID        string         `gorm:"primaryKey"`
	TaskID    string         `gorm:"not null;index;column:task_id"`
	ServerID  string         `gorm:"not null;index;column:server_id"`
	Status    string         `gorm:"not null"`
	StartedAt time.Time      `gorm:"not null;column:started_at"`
	EndedAt   *time.Time     `gorm:"column:ended_at"`
	Duration  int64          `gorm:"default:0"`
	Output    string         `gorm:"type:text"`
	Error     string         `gorm:"type:text"`
	RetryNum  int            `gorm:"default:0;column:retry_num"`
	Trigger   string         `gorm:"default:scheduled"`
	Task      *ScheduledTask `gorm:"foreignKey:TaskID;constraint:OnDelete:CASCADE"`
	Server    *Server        `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE"`
}

func (TaskExecution) TableName() string { return "task_executions" }

type ModuleTemplate struct {
	ID              string    `gorm:"primaryKey"`
	Name            string    `gorm:"not null;uniqueIndex"`
	Description     string    ``
	Type            string    `gorm:"not null;default:custom"`
	DockerImage     string    `gorm:"not null;column:docker_image"`
	DefaultEnv      string    `gorm:"type:text;column:default_env"`
	DefaultVolumes  string    `gorm:"type:text;column:default_volumes"`
	HealthCheckPath string    `gorm:"column:health_check_path"`
	HealthCheckPort int       `gorm:"column:health_check_port"`
	RequiresServer  bool      `gorm:"default:true;column:requires_server"`
	SupportsProxy   bool      `gorm:"default:true;column:supports_proxy"`
	Icon            string    ``
	Category        string    ``
	Documentation   string    ``
	CreatedAt       time.Time `gorm:"autoCreateTime"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime"`

	Ports                 json.RawMessage `gorm:"column:ports;serializer:json"`
	SuggestedDependencies json.RawMessage `gorm:"column:suggested_dependencies;serializer:json"`
	DefaultHooks          json.RawMessage `gorm:"column:default_hooks;serializer:json"`
	Metadata              json.RawMessage `gorm:"column:metadata;serializer:json"`
	DefaultCmd            string          `gorm:"column:default_cmd"`
	DefaultAccessUrls     json.RawMessage `gorm:"column:default_access_urls;serializer:json"`
	DefaultMemory         int             `gorm:"column:default_memory;default:512"`
	DefaultUID            string          `gorm:"column:default_uid;default:''"`
	DefaultGID            string          `gorm:"column:default_gid;default:''"`

	DefaultInitCommand      string `gorm:"column:default_init_command;default:''"`
	DefaultInitCommandDelay int    `gorm:"column:default_init_command_delay;default:0"`
	DefaultRestartAfterInit bool   `gorm:"column:default_restart_after_init;default:false"`
}

func (ModuleTemplate) TableName() string { return "module_templates" }

type Module struct {
	ID          string `gorm:"primaryKey"`
	Name        string `gorm:"not null"`
	ServerID    string `gorm:"not null;index;column:server_id"`
	TemplateID  string `gorm:"not null;index;column:template_id"`
	ContainerID string `gorm:"column:container_id"`
	Status      string `gorm:"not null;default:stopped"`

	Config          string `gorm:"type:text"`
	EnvOverrides    string `gorm:"type:text;column:env_overrides"`
	VolumeOverrides string `gorm:"type:text;column:volume_overrides"`

	Memory   int     `gorm:"default:512"`
	CPULimit float64 `gorm:"column:cpu_limit"`

	UID string `gorm:"column:uid;default:''"`
	GID string `gorm:"column:gid;default:''"`

	AutoStart             bool   `gorm:"default:false;column:auto_start"`
	FollowServerLifecycle bool   `gorm:"default:true;column:follow_server_lifecycle"`
	Detached              bool   `gorm:"default:false"`
	DataPath              string `gorm:"column:data_path"`

	CreatedAt   time.Time  `gorm:"autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"autoUpdateTime"`
	LastStarted *time.Time `gorm:"column:last_started"`

	Ports        json.RawMessage `gorm:"column:ports;serializer:json"`
	Dependencies json.RawMessage `gorm:"column:dependencies;serializer:json"`

	HealthCheckInterval int `gorm:"column:health_check_interval;default:30"`
	HealthCheckTimeout  int `gorm:"column:health_check_timeout;default:5"`
	HealthCheckRetries  int `gorm:"column:health_check_retries;default:3"`

	EventHooks json.RawMessage `gorm:"column:event_hooks;serializer:json"`
	Metadata   json.RawMessage `gorm:"column:metadata;serializer:json"`

	CmdOverride string `gorm:"column:cmd_override"`

	InitCommand      string `gorm:"column:init_command;default:''"`
	InitCommandDelay int    `gorm:"column:init_command_delay;default:0"`
	RestartAfterInit bool   `gorm:"column:restart_after_init;default:false"`

	AccessUrls json.RawMessage `gorm:"column:access_urls;serializer:json"`

	CreatedBy      string `gorm:"column:created_by"`
	TokenID        string `gorm:"column:token_id"`
	TokenPlaintext string `gorm:"column:token_plaintext"`

	Server   *Server         `gorm:"foreignKey:ServerID;constraint:OnDelete:CASCADE"`
	Template *ModuleTemplate `gorm:"foreignKey:TemplateID;constraint:OnDelete:RESTRICT"`
}

func (Module) TableName() string { return "modules" }

// Gormigrate ledger table v2 kept
type gormigrateRow struct {
	ID string `gorm:"primaryKey;size:200;column:id"`
}

func (gormigrateRow) TableName() string { return "migrations" }

// Casbin adapter table exactly as v3.41.0 makes it
type casbinRule struct {
	ID    uint   `gorm:"primaryKey;autoIncrement"`
	Ptype string `gorm:"size:100"`
	V0    string `gorm:"size:100"`
	V1    string `gorm:"size:100"`
	V2    string `gorm:"size:100"`
	V3    string `gorm:"size:100"`
	V4    string `gorm:"size:100"`
	V5    string `gorm:"size:100"`
}

func (casbinRule) TableName() string { return "casbin_rule" }

// Every v2 table in automigrate order
func AllModels() []any {
	return []any{
		&Server{},
		&ServerConfig{},
		&Mod{},
		&IndexedModpack{},
		&IndexedModpackFile{},
		&ModpackFavorite{},
		&ProxyConfig{},
		&ProxyListener{},
		&User{},
		&Role{},
		&UserRole{},
		&Session{},
		&APIToken{},
		&RegistrationInvite{},
		&ScheduledTask{},
		&TaskExecution{},
		&ModuleTemplate{},
		&Module{},
		&SystemSetting{},
	}
}

// Builds a pristine v2.0.15 database on one connection
// Mirrors v2 boot, automigrate then adapter side tables
func Materialize(db *gorm.DB) error {
	if err := db.AutoMigrate(AllModels()...); err != nil {
		return err
	}
	if err := db.AutoMigrate(&gormigrateRow{}, &casbinRule{}); err != nil {
		return err
	}
	// Adapter builds its unique index outside tags
	hasIndex := db.Migrator().HasIndex(&casbinRule{}, "idx_casbin_rule")
	if !hasIndex {
		if err := db.Exec("CREATE UNIQUE INDEX idx_casbin_rule ON casbin_rule (ptype,v0,v1,v2,v3,v4,v5)").Error; err != nil {
			return err
		}
	}
	return nil
}
