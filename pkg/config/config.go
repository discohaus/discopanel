package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

type Config struct {
	Server    ServerConfig    `mapstructure:"server" json:"server"`
	Database  DatabaseConfig  `mapstructure:"database" json:"database"`
	Docker    DockerConfig    `mapstructure:"docker" json:"docker"`
	Storage   StorageConfig   `mapstructure:"storage" json:"storage"`
	Proxy     ProxyConfig     `mapstructure:"proxy" json:"proxy"`
	Module    ModuleConfig    `mapstructure:"module" json:"module"`
	Minecraft MinecraftConfig `mapstructure:"minecraft" json:"minecraft"`
	Logging   LoggingConfig   `mapstructure:"logging" json:"logging"`
	Upload    UploadConfig    `mapstructure:"upload" json:"upload"`
	Auth      AuthConfig      `mapstructure:"auth" json:"auth"`
}

type AuthConfig struct {
	SessionTimeout  int         `mapstructure:"session_timeout" json:"session_timeout"`
	AnonymousAccess bool        `mapstructure:"anonymous_access" json:"anonymous_access"`
	JWTSecret       string      `mapstructure:"jwt_secret" json:"jwt_secret" alias:"secret"`
	OIDC            OIDCConfig  `mapstructure:"oidc" json:"oidc"`
	Local           LocalConfig `mapstructure:"local" json:"local"`
}

type OIDCConfig struct {
	Enabled         bool              `mapstructure:"enabled" json:"enabled"`
	IssuerURI       string            `mapstructure:"issuer_uri" json:"issuer_uri"`
	ClientID        string            `mapstructure:"client_id" json:"client_id"`
	ClientSecret    string            `mapstructure:"client_secret" json:"client_secret" alias:"secret"`
	RedirectURL     string            `mapstructure:"redirect_url" json:"redirect_url"`
	Scopes          []string          `mapstructure:"scopes" json:"scopes"`
	RoleClaim       string            `mapstructure:"role_claim" json:"role_claim"`
	RoleMapping     map[string]string `mapstructure:"role_mapping" json:"role_mapping"`
	RejectUnmapped  bool              `mapstructure:"reject_unmapped" json:"reject_unmapped"`
	SkipTLSVerify   bool              `mapstructure:"skip_tls_verify" json:"skip_tls_verify"`
	ExtraClaimsURL  string            `mapstructure:"extra_claims_url" json:"extra_claims_url"`
	ExtraClaimsKey  string            `mapstructure:"extra_claims_key" json:"extra_claims_key"`
	ExtraClaimsName string            `mapstructure:"extra_claims_name" json:"extra_claims_name"`
	RequiredClaim   string            `mapstructure:"required_claim" json:"required_claim"`
	RequiredValues  []string          `mapstructure:"required_values" json:"required_values"`
}

type LocalConfig struct {
	Enabled           bool `mapstructure:"enabled" json:"enabled"`
	AllowRegistration bool `mapstructure:"allow_registration" json:"allow_registration"`
}

type ServerConfig struct {
	Port              string `mapstructure:"port" json:"port"`
	Host              string `mapstructure:"host" json:"host"`
	ReadHeaderTimeout int    `mapstructure:"read_header_timeout" json:"read_header_timeout"`
	IdleTimeout       int    `mapstructure:"idle_timeout" json:"idle_timeout"`
	UserAgent         string `mapstructure:"user_agent" json:"user_agent"`
}

type DockerConfig struct {
	SyncInterval int               `mapstructure:"sync_interval" json:"sync_interval"`
	Host         string            `mapstructure:"host" json:"host"`
	Version      string            `mapstructure:"version" json:"version"`
	NetworkName  string            `mapstructure:"network_name" json:"network_name"`
	RuntimeImage string            `mapstructure:"runtime_image" json:"runtime_image"` // Override for discoruntime repository
	AgentURL     string            `mapstructure:"agent_url" json:"agent_url"`         // Panel URL for runtime containers, auto-detected if empty
	DNS          string            `mapstructure:"dns" json:"dns"`
	Labels       map[string]string `mapstructure:"labels" json:"labels"`
}

type StorageConfig struct {
	DataDir   string `mapstructure:"data_dir" json:"data_dir"`
	BackupDir string `mapstructure:"backup_dir" json:"backup_dir"`
	TempDir   string `mapstructure:"temp_dir" json:"temp_dir"`
}

type ProxyConfig struct {
	Enabled      bool           `mapstructure:"enabled" json:"enabled"`
	PublicIp     string         `mapstructure:"public_ip" json:"public_ip"`     // Public address for hostname suggestions
	ListenPort   int            `mapstructure:"listen_port" json:"listen_port"` // Primary listen port
	PortRangeMin int            `mapstructure:"port_range_min" json:"port_range_min"`
	TrustedEdge  bool           `mapstructure:"trusted_edge" json:"trusted_edge"` // Honor forwarded headers from an upstream edge
	TLS          ProxyTLSConfig `mapstructure:"tls" json:"tls"`
}

// File configured certificates the proxy terminates with
type ProxyTLSConfig struct {
	Certificates []TLSCertificate `mapstructure:"certificates" json:"certificates"`
}

// One pem pair on disk, traefik style
type TLSCertificate struct {
	CertFile string `mapstructure:"cert_file" json:"cert_file"`
	KeyFile  string `mapstructure:"key_file" json:"key_file"`
}

type ModuleConfig struct {
	Enabled      bool `mapstructure:"enabled" json:"enabled"`
	PortRangeMin int  `mapstructure:"port_range_min" json:"port_range_min"`
	PortRangeMax int  `mapstructure:"port_range_max" json:"port_range_max"`

	// Seeds and runs the global crash doctor module
	DoctorEnabled bool `mapstructure:"doctor_enabled" json:"doctor_enabled"`
}

type DatabaseConfig struct {
	Path            string `mapstructure:"path" json:"path"`
	MaxConnections  int    `mapstructure:"max_connections" json:"max_connections"`
	MaxIdleConns    int    `mapstructure:"max_idle_conns" json:"max_idle_conns"`
	ConnMaxLifetime int    `mapstructure:"conn_max_lifetime" json:"conn_max_lifetime"`
	AutoMigrate     bool   `mapstructure:"auto_migrate" json:"auto_migrate"`
}

type MinecraftConfig struct {
	ResetGlobal  bool           `mapstructure:"reset_global" json:"reset_global"`
	GlobalConfig map[string]any `mapstructure:"global_config" json:"global_config"`
}

type LoggingConfig struct {
	Enabled    bool   `mapstructure:"enabled" json:"enabled"`
	FilePath   string `mapstructure:"file_path" json:"file_path"`
	MaxSize    int    `mapstructure:"max_size" json:"max_size"`
	MaxBackups int    `mapstructure:"max_backups" json:"max_backups"`
	MaxAge     int    `mapstructure:"max_age" json:"max_age"`
	Compress   bool   `mapstructure:"compress" json:"compress"`
}

type UploadConfig struct {
	SessionTTL       int   `mapstructure:"session_ttl" json:"session_ttl"`               // Minutes, default 240 (4 hours)
	DefaultChunkSize int   `mapstructure:"default_chunk_size" json:"default_chunk_size"` // Bytes, default 5MB, client overriden
	MaxChunkSize     int   `mapstructure:"max_chunk_size" json:"max_chunk_size"`         // Bytes, default 10MB, server overriden
	MaxUploadSize    int64 `mapstructure:"max_upload_size" json:"max_upload_size"`       // Bytes, default 0 (unlimited)
}

func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Set config name and type
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	// Explicit path wins, a directory joins the search list
	if configPath != "" {
		if info, err := os.Stat(configPath); err == nil && info.IsDir() {
			v.AddConfigPath(configPath)
		} else {
			v.SetConfigFile(configPath)
		}
	}
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	v.AddConfigPath("/etc/discopanel")

	// Set defaults
	setDefaults(v)

	// Enable environment variables
	v.SetEnvPrefix("DISCOPANEL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found, uses defaults and environment
	}

	// Flatten nested maps like docker.labels into map[string]string
	flattenMapSetting(v, "docker.labels")

	// Decodes config, handling JSON strings from env
	var cfg Config
	if err := v.Unmarshal(&cfg, func(dc *mapstructure.DecoderConfig) {
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			jsonStringToMapHook(),
		)
	}); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %w", err)
	}

	// Validate and expand paths
	if err := validateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("config validation error: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// Server defaults
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.read_header_timeout", 15)
	v.SetDefault("server.idle_timeout", 60)
	v.SetDefault("server.user_agent", "DiscoPanel/1.0 (github.com/discohaus/discopanel)")

	// Database defaults
	v.SetDefault("database.path", "./data/discopanel.db")
	v.SetDefault("database.max_connections", 25)
	v.SetDefault("database.max_idle_conns", 5)
	v.SetDefault("database.conn_max_lifetime", 300)
	v.SetDefault("database.auto_migrate", true)

	// Docker defaults
	v.SetDefault("docker.sync_interval", 5)
	v.SetDefault("docker.host", "unix:///var/run/docker.sock")
	v.SetDefault("docker.version", "")
	v.SetDefault("docker.network_name", "discopanel-network")
	v.SetDefault("docker.runtime_image", "")
	v.SetDefault("docker.agent_url", "")
	v.SetDefault("docker.dns", "")
	v.SetDefault("docker.labels", map[string]string{})

	// Storage defaults
	dataDir, err := filepath.Abs("./data")
	if err != nil {
		panic("Unable to find data dir")
	}
	v.SetDefault("storage.data_dir", dataDir)
	v.SetDefault("storage.backup_dir", "./backups")
	v.SetDefault("storage.temp_dir", "./tmp")

	// Proxy defaults
	v.SetDefault("proxy.enabled", true)
	v.SetDefault("proxy.public_ip", "")
	v.SetDefault("proxy.listen_port", 25565)
	v.SetDefault("proxy.port_range_min", 25565)

	// Module defaults
	v.SetDefault("module.enabled", true)
	v.SetDefault("module.port_range_min", 8100)
	v.SetDefault("module.port_range_max", 8199)
	v.SetDefault("module.doctor_enabled", true)

	v.SetDefault("minecraft.reset_global", false)

	// Logging defaults
	v.SetDefault("logging.enabled", true)
	v.SetDefault("logging.file_path", "./data/discopanel.log")
	v.SetDefault("logging.max_size", 10)   // 10 MB
	v.SetDefault("logging.max_backups", 5) // Keeps 5
	v.SetDefault("logging.max_age", 30)    // 30 days
	v.SetDefault("logging.compress", true) // Compresses rotated logs

	// Auth defaults
	v.SetDefault("auth.session_timeout", 86400)
	v.SetDefault("auth.anonymous_access", false)
	v.SetDefault("auth.jwt_secret", "")
	v.SetDefault("auth.oidc.enabled", false)
	v.SetDefault("auth.oidc.issuer_uri", "")
	v.SetDefault("auth.oidc.client_id", "")
	v.SetDefault("auth.oidc.client_secret", "")
	v.SetDefault("auth.oidc.redirect_url", "")
	v.SetDefault("auth.oidc.scopes", []string{"openid", "profile", "email"})
	v.SetDefault("auth.oidc.role_claim", "groups")
	v.SetDefault("auth.oidc.role_mapping", map[string]string{})
	v.SetDefault("auth.oidc.reject_unmapped", false)
	v.SetDefault("auth.oidc.skip_tls_verify", false)
	v.SetDefault("auth.oidc.extra_claims_url", "")
	v.SetDefault("auth.oidc.extra_claims_key", "")
	v.SetDefault("auth.oidc.extra_claims_name", "")
	v.SetDefault("auth.oidc.required_claim", "")
	v.SetDefault("auth.oidc.required_values", []string{})
	v.SetDefault("auth.local.enabled", true)
	v.SetDefault("auth.local.allow_registration", false)

	// Upload defaults
	v.SetDefault("upload.session_ttl", 240)                // 4 hours (in minutes)
	v.SetDefault("upload.default_chunk_size", 5*1024*1024) // 5MB
	v.SetDefault("upload.max_chunk_size", 10*1024*1024)    // 10MB
	v.SetDefault("upload.max_upload_size", 0)              // Unlimited
}

func validateConfig(cfg *Config) error {
	// Expand paths to absolute
	var err error
	cfg.Database.Path, err = filepath.Abs(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("invalid database path: %w", err)
	}

	cfg.Storage.DataDir, err = filepath.Abs(cfg.Storage.DataDir)
	if err != nil {
		return fmt.Errorf("invalid data directory: %w", err)
	}

	cfg.Storage.BackupDir, err = filepath.Abs(cfg.Storage.BackupDir)
	if err != nil {
		return fmt.Errorf("invalid backup directory: %w", err)
	}

	cfg.Storage.TempDir, err = filepath.Abs(cfg.Storage.TempDir)
	if err != nil {
		return fmt.Errorf("invalid temp directory: %w", err)
	}

	// Validate port ranges
	if cfg.Module.PortRangeMin >= cfg.Module.PortRangeMax {
		return fmt.Errorf("module port range min must be less than max")
	}

	// Zero interval would panic every ticker consumer
	if cfg.Docker.SyncInterval < 1 {
		cfg.Docker.SyncInterval = 5
	}

	// Validate custom Docker labels do not use reserved namespace 'discopanel.'
	for k := range cfg.Docker.Labels {
		if strings.HasPrefix(k, "discopanel.") {
			return fmt.Errorf("custom docker labels cannot begin with 'discopanel.', namespace reserved for internal management, invalid key: %s", k)
		}
	}

	return nil
}

// Decodes JSON object strings into map types
func jsonStringToMapHook() mapstructure.DecodeHookFuncType {
	return func(from, to reflect.Type, data any) (any, error) {
		if from.Kind() != reflect.String || to.Kind() != reflect.Map {
			return data, nil
		}
		var m any
		if err := json.Unmarshal([]byte(data.(string)), &m); err != nil {
			return data, nil
		}
		return m, nil
	}
}

// Flattens a nested viper setting into a flat map
func flattenMapSetting(v *viper.Viper, key string) {
	val := v.Get(key)
	if val == nil {
		return
	}

	m, ok := val.(map[string]any)
	if !ok {
		return
	}

	flat := make(map[string]string)
	flattenMap(m, "", flat)
	v.Set(key, flat)
}

func flattenMap(src map[string]any, prefix string, dst map[string]string) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenMap(val, key, dst)
		default:
			dst[key] = fmt.Sprintf("%v", val)
		}
	}
}
