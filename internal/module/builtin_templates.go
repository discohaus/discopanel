package module

import (
	"context"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
)

// Template ids of the panel owned global modules
const (
	doctorTemplateID = "builtin-doctor"
	botTemplateID    = "builtin-bot"
)

// Panel user keeps seeded module writes owned by the panel
const (
	hostUID = "{{host.uid}}"
	hostGID = "{{host.gid}}"
)

// Default web port for the seeded doctor instance
func doctorPorts(cfg *config.Config) []*v1.NetworkPort {
	port := int32(8190)
	proxied := false
	if cfg != nil {
		if cfg.Module.PortRangeMax > 0 {
			port = int32(cfg.Module.PortRangeMax)
		}
		// Direct host bind keeps doctor reachable without proxy
		proxied = cfg.Proxy.Enabled
	}
	return []*v1.NetworkPort{
		{Name: "Web", ContainerPort: 8190, HostPort: port, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: proxied, CatchAll: false},
	}
}

func doctorEnv() map[string]string {
	return map[string]string{
		"DISCOPANEL_DATA_DIR": "{{config.storage.data_dir}}",
		"POLL_INTERVAL":       "15s",
		"DOCTOR_MODE":         "repair",
		"DOCTOR_INSTALL_DEPS": "on",
		"PORT":                "8190",
	}
}

// Default access urls for the seeded doctor instance
func doctorAccessURLs() []string {
	return []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"}
}

func doctorVolumes() []*v1.VolumeMount {
	return []*v1.VolumeMount{
		{Source: "{{config.storage.data_dir}}", Target: "/data"},
	}
}

// Seeded doctor instance, bootstrapped builtins hold supermodule tokens
func doctorInstance(cfg *config.Config) *v1.Module {
	return &v1.Module{
		Id:                    "builtin-doctor-instance",
		Name:                  "Doctor",
		TemplateId:            doctorTemplateID,
		Status:                v1.ModuleStatus_MODULE_STATUS_STOPPED,
		AutoStart:             true,
		FollowServerLifecycle: false,
		Memory:                512,
		Ports:                 doctorPorts(cfg),
		EnvOverrides:          doctorEnv(),
		VolumeOverrides:       doctorVolumes(),
		AccessUrls:            doctorAccessURLs(),
		Uid:                   hostUID,
		Gid:                   hostGID,
	}
}

func botEnv() map[string]string {
	return map[string]string{
		"POLL_INTERVAL": "10s",
		"PORT":          "8210",
	}
}

// Bot state lives in its own module data dir
func botVolumes() []*v1.VolumeMount {
	return []*v1.VolumeMount{
		{Source: "{{module.data_path}}", Target: "/data", CreateDir: true},
	}
}

// Seeded bot instance, stays off until a token is configured
func botInstance() *v1.Module {
	return &v1.Module{
		Id:                    "builtin-bot-instance",
		Name:                  "Discord Bot",
		TemplateId:            botTemplateID,
		Status:                v1.ModuleStatus_MODULE_STATUS_STOPPED,
		AutoStart:             false,
		FollowServerLifecycle: false,
		Memory:                256,
		EnvOverrides:          botEnv(),
		VolumeOverrides:       botVolumes(),
		Uid:                   hostUID,
		Gid:                   hostGID,
	}
}

// Seeds built-in templates and reseeds existing code owned rows
func InitBuiltinTemplates(store *storage.Store) error {
	ctx := context.Background()

	templates := []*v1.ModuleTemplate{
		{
			Id:             "builtin-geyser",
			Name:           "Geyser",
			Description:    "Allows Bedrock Edition players to join Java Edition servers. Requires Floodgate plugin on the server for seamless authentication.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-geyser:latest",
			Category:       "proxy",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "users",
			Ports: []*v1.NetworkPort{
				{Name: "Bedrock", ContainerPort: 19132, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_UDP, ProxyEnabled: true},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Bedrock.host_port}}"},
			DefaultEnv: map[string]string{
				"PUID":               "{{host.uid}}",
				"PGID":               "{{host.gid}}",
				"OVERWRITE_CONFIG":   "false",
				"BEDROCK_ADDRESS":    "0.0.0.0",
				"BEDROCK_PORT":       "{{module.ports.Bedrock.container_port}}",
				"BEDROCK_MOTD1":      "GeyserMC",
				"BEDROCK_MOTD2":      "Minecraft Server",
				"BEDROCK_SERVERNAME": "Geyser",
				"REMOTE_ADDRESS":     "discopanel-server-{{server.id}}",
				"REMOTE_PORT":        "{{server.container_port}}",
				"REMOTE_AUTH_TYPE":   "offline",
			},
			DefaultVolumes: []*v1.VolumeMount{
				{Source: "{{server.data_path}}/modules/geyser", Target: "/data"},
			},
			Documentation:   "Geyser acts as a proxy, translating Bedrock packets to Java packets.",
			HealthCheckPort: 19132,
			DefaultMemory:   1024,
		},
		{
			Id:             "builtin-minecraft-exporter",
			Name:           "Prometheus Exporter",
			Description:    "Exports Minecraft server metrics to Prometheus for monitoring dashboards",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-exporter:latest",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "chart-bar",
			Ports: []*v1.NetworkPort{
				{Name: "Metrics", ContainerPort: 9225, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: true, CatchAll: false},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Metrics.host_port}}/metrics"},
			DefaultEnv: map[string]string{
				"EXPORT_SERVERS": "discopanel-server-{{server.id}}:{{server.container_port}}",
				"EXPORT_PORT":    "{{module.ports.Metrics.container_port}}",
			},
			HealthCheckPath: "/metrics",
			HealthCheckPort: 9225,
			Documentation:   "Exports server status, player count, TPS, and other metrics in Prometheus format. Connect to /metrics endpoint to scrape metrics.",
			DefaultMemory:   512,
		},
		{
			Id:             "builtin-bluemap",
			Name:           "BlueMap",
			Description:    "Interactive 3D map renderer for Minecraft worlds with a web-based viewer. Renders overworld, nether, and end dimensions.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/bluemap-minecraft/bluemap:latest",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "map",
			DefaultCmd:     "-r -u -w",
			Ports: []*v1.NetworkPort{
				{Name: "Web", ContainerPort: 8100, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: true, CatchAll: false},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"},
			DefaultVolumes: []*v1.VolumeMount{
				{Source: "{{server.data_path}}/modules/bluemap/config", Target: "/app/config", CreateDir: true},
				{Source: "{{server.data_path}}/world", Target: "/app/world", ReadOnly: true},
				{Source: "{{server.data_path}}/modules/bluemap/data", Target: "/app/data", CreateDir: true},
				{Source: "{{server.data_path}}/modules/bluemap/web", Target: "/app/web", CreateDir: true},
			},
			HealthCheckPath:         "/",
			HealthCheckPort:         8100,
			Documentation:           "Renders 3D maps of your Minecraft worlds accessible via a web interface. Supports overworld, nether, and end dimensions. World volumes are mounted read-only from the server data path. Config, data, and web assets are stored in the bluemap module directory.",
			DefaultMemory:           2048,
			DefaultUid:              "{{host.uid}}",
			DefaultGid:              "{{host.gid}}",
			DefaultInitCommand:      `sed -i 's/accept-download: false/accept-download: true/' /app/config/core.conf`,
			DefaultInitCommandDelay: 1,
			DefaultRestartAfterInit: true,
		},
		{
			Id:             "builtin-status-panel",
			Name:           "Status Panel",
			Description:    "Real-time server status dashboard showing player count, TPS, memory usage, and server info via the DiscoPanel API.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-status:latest",
			Category:       "monitoring",
			SupportsProxy:  true,
			RequiresServer: true,
			Icon:           "monitor",
			Ports: []*v1.NetworkPort{
				{Name: "Web", ContainerPort: 8181, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: true, CatchAll: false},
			},
			DefaultAccessUrls: []string{"http://{{host.hostname}}:{{module.ports.Web.host_port}}"},
			DefaultEnv: map[string]string{
				"POLL_INTERVAL": "10s",
				"PORT":          "{{module.ports.Web.container_port}}",
			},
			HealthCheckPath: "/health",
			HealthCheckPort: 8181,
			Documentation:   "Displays a real-time status dashboard for the attached Minecraft server. Fetches status via the DiscoPanel API including player count, TPS, CPU/memory usage, and server configuration. Automatically refreshes every 10 seconds.",
			DefaultMemory:   512,
		},
		{
			Id:             "builtin-steambridge",
			Name:           "Steam Bridge",
			Description:    "Expose this server over Steam networking (Valve SDR relay / direct P2P). Players join through the DiscoPanel Bridge app or a compatible client mod using this module's SteamID64. No port forwarding or public IP needed.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-steambridge:latest",
			Category:       "proxy",
			SupportsProxy:  false,
			RequiresServer: true,
			Icon:           "gamepad-2",
			Ports: []*v1.NetworkPort{
				{Name: "Status", ContainerPort: 8200, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: false},
			},
			DefaultEnv: map[string]string{
				"TARGET_HOSTNAME":    "{{server.proxy_hostnames}}",
				"PROXY_PORT":         "{{server.proxy_port}}",
				"PROXY_PORT_DEFAULT": "{{config.proxy.listen_port}}",
				"VIRTUAL_PORT":       "0",
				"ALLOW_WITHOUT_AUTH": "true",
			},
			ConfigFields: []*v1.ModuleConfigField{
				{
					Env:          "STEAM_EXTERNAL",
					Label:        "Use external Steam session",
					Description:  "Skip credentials and reuse a Steam login already in the data volume",
					Group:        "Steam account",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL,
					DefaultValue: "false",
				},
				{
					Env:            "STEAM_USERNAME",
					Label:          "Steam username",
					Description:    "Dedicated throwaway account, never your personal one",
					Group:          "Steam account",
					Type:           v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_STRING,
					Required:       true,
					RequiredUnless: "STEAM_EXTERNAL",
					Severity:       v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
				{
					Env:            "STEAM_PASSWORD",
					Label:          "Steam password",
					Description:    "Approve the first login via the Steam mobile app while watching logs",
					Group:          "Steam account",
					Type:           v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_PASSWORD,
					Required:       true,
					RequiredUnless: "STEAM_EXTERNAL",
					Severity:       v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
				{
					Env:          "TRANSPORT_MODE",
					Label:        "Transport mode",
					Description:  "Auto picks between Valve relay and direct P2P",
					Group:        "Access",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT,
					DefaultValue: "auto",
					Severity:     v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
					Options: []*v1.ModuleConfigOption{
						{Value: "auto", Label: "Auto"},
						{Value: "p2p", Label: "Direct P2P"},
						{Value: "relay", Label: "Valve relay"},
					},
				},
				{
					Env:          "ACCESS_POLICY",
					Label:        "Access policy",
					Description:  "Friends means friends of this module's Steam account",
					Group:        "Access",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_SELECT,
					DefaultValue: "everyone",
					Severity:     v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
					Options: []*v1.ModuleConfigOption{
						{Value: "everyone", Label: "Everyone"},
						{Value: "friends", Label: "Friends"},
						{Value: "allowlist", Label: "Allowlist"},
					},
				},
				{
					Env:          "ALLOWED_STEAM_IDS",
					Label:        "Allowed SteamID64s",
					Description:  "Comma or newline separated, used with the allowlist policy",
					Group:        "Access",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_MULTILINE,
					Regex:        `^[0-9,\s]*$`,
					RegexMessage: "ALLOWED_STEAM_IDS must be numeric SteamID64s separated by commas",
				},
				{
					Env:          "VOICE_FORWARD",
					Label:        "Forward voice traffic",
					Description:  "Relay Simple Voice Chat and Plasmo Voice UDP",
					Group:        "Voice",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL,
					DefaultValue: "true",
				},
				{
					Env:          "VOICE_PORT",
					Label:        "Voice port",
					Description:  "UDP port voice mods listen on",
					Group:        "Voice",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT,
					DefaultValue: "24454",
					Min:          proto.Int32(1),
					Max:          proto.Int32(65535),
					Severity:     v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
			},
			DefaultVolumes: []*v1.VolumeMount{
				{Source: "{{server.data_path}}/modules/steambridge", Target: "/data", CreateDir: true},
			},
			HealthCheckPath: "/health",
			HealthCheckPort: 8200,
			DefaultUid:      "{{host.uid}}",
			DefaultGid:      "{{host.gid}}",
			// Steam runtime sandbox requires user namespaces
			DefaultSecurityOpt: []string{"seccomp=unconfined", "apparmor=unconfined"},
			// Login raises a runtime prompt for the guard code
			Metadata:      map[string]string{"supports_prompts": "true", "status_path": "/status"},
			Documentation: "Runs a headless Steam client plus a gateway that terminates Steam Networking Sockets connections and relays them into the DiscoPanel hostname proxy, so wake-on-connect and sleeping-server behavior keep working. Requires a dedicated Steam account (never your personal one). Set STEAM_USERNAME and STEAM_PASSWORD, then start the module. https://store.steampowered.com/account/authorizeddevices -> Disable Guard Code, OR when Steam asks for a Steam Guard code the panel shows an input prompt in the module dialog, enter the current code from the account email or authenticator and login retries automatically. The login session persists in the module data volume. Players connect with the DiscoPanel Bridge app from https://github.com/discohaus/discomodule-releases/releases (no mods needed, works with any Minecraft version) by running it, entering this module's SteamID64 (shown on the module card while running), and joining localhost in Minecraft. The open source Steam Bridge client mod also works as an alternative. ACCESS_POLICY everyone, friends, or allowlist controls who may connect, with friends meaning friends of the module's Steam account. Voice traffic from Simple Voice Chat and Plasmo Voice tunnels automatically when VOICE_FORWARD is on. Uses Steam AppID 480 (Spacewar), so keep the account disposable.",
			DefaultMemory: 2048,
		},
		{
			Id:             "builtin-playit",
			Name:           "Playit.gg",
			Description:    "Publish this server through a free playit.gg tunnel. Players join via your tunnel's public address, no port forwarding or public IP needed.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-playit:latest",
			Category:       "proxy",
			SupportsProxy:  false,
			RequiresServer: true,
			Icon:           "globe",
			Ports: []*v1.NetworkPort{
				{Name: "Status", ContainerPort: 8201, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: false},
			},
			DefaultAccessUrls: []string{"https://playit.gg/account/tunnels"},
			DefaultEnv: map[string]string{
				"TARGET_HOSTNAME":    "{{server.proxy_hostnames}}",
				"PROXY_PORT":         "{{server.proxy_port}}",
				"PROXY_PORT_DEFAULT": "{{config.proxy.listen_port}}",
			},
			ConfigFields: []*v1.ModuleConfigField{
				{
					Env:         "SECRET_KEY",
					Label:       "Agent secret key",
					Description: "Generate under Agents on playit.gg, persists in the data volume",
					Type:        v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_PASSWORD,
					Required:    true,
					Placeholder: "playit.gg agent secret",
					Severity:    v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
				{
					Env:          "LISTEN_PORT",
					Label:        "Tunnel listen port",
					Description:  "Local port your playit tunnel targets",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT,
					DefaultValue: "25565",
					Min:          proto.Int32(1),
					Max:          proto.Int32(65535),
					Severity:     v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
				{
					Env:          "UDP_FORWARD",
					Label:        "Forward UDP",
					Description:  "Relay UDP tunnels for voice mods",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL,
					DefaultValue: "true",
				},
				{
					Env:          "VOICE_PORT",
					Label:        "Voice port",
					Description:  "UDP port voice mods listen on",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_INT,
					DefaultValue: "24454",
					Min:          proto.Int32(1),
					Max:          proto.Int32(65535),
					Severity:     v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
			},
			DefaultVolumes: []*v1.VolumeMount{
				{Source: "{{server.data_path}}/modules/playit", Target: "/data", CreateDir: true},
			},
			HealthCheckPath: "/health",
			HealthCheckPort: 8201,
			Metadata:        map[string]string{"status_path": "/status"},
			Documentation:   "Runs the official playit.gg agent next to a gateway that rewrites incoming Minecraft handshakes onto this server's proxy hostname and relays them into the DiscoPanel proxy, keeping wake-on-connect working. Generate an agent secret key on playit.gg, set it as SECRET_KEY, then create a Minecraft Java tunnel whose local address is 127.0.0.1 on LISTEN_PORT. The provisioned secret persists in the module data volume. UDP tunnels for voice mods forward straight to the server container when UDP_FORWARD is on.",
			DefaultMemory:   256,
		},
		{
			Id:             doctorTemplateID,
			Name:           "Doctor",
			Description:    "Global crash doctor. Watches every DiscoPanel server, diagnoses crashes from structured exit reports, disables or sources mods with a full revert trail, and verifies repairs by restarting through the panel.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-doctor:latest",
			Category:       "automation",
			SupportsProxy:  true,
			RequiresServer: false,
			Global:         true,
			Icon:           "stethoscope",
			Ports: []*v1.NetworkPort{
				{Name: "Web", ContainerPort: 8190, HostPort: 0, Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_HTTP, ProxyEnabled: true, CatchAll: false},
			},
			DefaultAccessUrls: doctorAccessURLs(),
			DefaultEnv:        doctorEnv(),
			DefaultVolumes:    doctorVolumes(),
			HealthCheckPath:   "/health",
			HealthCheckPort:   8190,
			DefaultUid:        hostUID,
			DefaultGid:        hostGID,
			Metadata:          map[string]string{"module_role": "doctor"},
			Documentation:     "Runs as a single global module for the whole panel. Discovers servers through the DiscoPanel API, watches their exit history on the shared data volume, and repairs crash loops with reversible mod disables, re-enables, and dependency installs from CurseForge or Modrinth using the panel API keys. Configure it from the Doctor category in Settings, with per-server overrides on each server's properties page. Doctor Mode observe diagnoses without acting, and Install Missing Dependencies off disables downloads.",
			DefaultMemory:     512,
		},
		{
			Id:             botTemplateID,
			Name:           "Discord Bot",
			Description:    "Global Discord bot. Bridges in-game chat with Discord channels, announces joins, deaths, advancements, crashes and doctor repairs, keeps live status boards, and adds slash commands to check on and control servers from Discord.",
			Type:           v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN,
			DockerImage:    "ghcr.io/discohaus/discomodule-bot:latest",
			Category:       "community",
			SupportsProxy:  false,
			RequiresServer: false,
			Global:         true,
			Icon:           "bot",
			DefaultEnv:     botEnv(),
			ConfigFields: []*v1.ModuleConfigField{
				{
					Env:         "DISCORD_TOKEN",
					Label:       "Bot token",
					Description: "Reset Token on the Bot page of your application at discord.com/developers, saved here and never shown again",
					Group:       "Discord",
					Type:        v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_PASSWORD,
					Required:    true,
					Severity:    v1.ModuleConfigSeverity_MODULE_CONFIG_SEVERITY_DENY,
				},
				{
					Env:          "DISCORD_GUILD_ID",
					Label:        "Guild ID",
					Description:  "Registers slash commands instantly in one server, empty registers them globally which can take up to an hour",
					Group:        "Discord",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_STRING,
					Regex:        `^[0-9]*$`,
					RegexMessage: "DISCORD_GUILD_ID must be a numeric snowflake",
					Placeholder:  "Right click your server, Copy Server ID",
				},
				{
					Env:          "BOT_AVATARS",
					Label:        "Player heads",
					Description:  "Bridged chat posts through a channel webhook with the player's name and head",
					Group:        "Bridge",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL,
					DefaultValue: "true",
				},
				{
					Env:          "BOT_PRESENCE",
					Label:        "Presence",
					Description:  "Show the online player count as the bot's Discord status",
					Group:        "Bridge",
					Type:         v1.ModuleConfigFieldType_MODULE_CONFIG_FIELD_TYPE_BOOL,
					DefaultValue: "true",
				},
			},
			DefaultVolumes:  botVolumes(),
			HealthCheckPath: "/health",
			HealthCheckPort: 8210,
			DefaultUid:      hostUID,
			DefaultGid:      hostGID,
			Metadata:        map[string]string{"module_role": "bot", "status_path": "/status"},
			Documentation:   "Runs as a single global module for the whole panel. Create an application at discord.com/developers, add a Bot, and enable the Message Content intent under Privileged Gateway Intents so Discord messages can be relayed into the game. Invite it with the bot and applications.commands scopes plus Send Messages, Embed Links, Attach Files, Read Message History and Manage Webhooks permissions. On the Modules page open the bot's settings, paste the token under Configuration and save, then flip Enabled. In Discord run /link in a channel to bind it to a server, choosing which streams it carries (chat, player events, lifecycle and crash announcements, or the full console). /board posts a status embed the bot keeps updated, /status, /players and /health answer on demand, and /start, /stop, /restart, /cmd, /backups and /answer stay behind the Manage Server permission, which server admins can retarget per role under Integrations. Chat typed in a linked channel shows in game as <name> text, and in-game chat posts back through a channel webhook with the player's head when Player heads is on.",
			DefaultMemory:   256,
		},
	}

	// Builtins are code owned so reseed overwrites them
	for _, template := range templates {
		existing, err := store.GetModuleTemplate(ctx, template.Id)
		if err != nil {
			if err := store.CreateModuleTemplate(ctx, template); err != nil {
				return err
			}
			continue
		}
		template.CreatedAt = existing.CreatedAt
		if err := store.UpdateModuleTemplate(ctx, template); err != nil {
			return err
		}
	}

	// Builtin rows the code no longer ships get dropped
	keep := make(map[string]bool, len(templates))
	for _, template := range templates {
		keep[template.Id] = true
	}
	existing, err := store.ListModuleTemplatesByType(ctx, v1.ModuleTemplateType_MODULE_TEMPLATE_TYPE_BUILTIN)
	if err != nil {
		return err
	}
	for _, template := range existing {
		if keep[template.Id] {
			continue
		}
		if err := store.DeleteModuleTemplate(ctx, template.Id); err != nil {
			return err
		}
	}

	return nil
}
