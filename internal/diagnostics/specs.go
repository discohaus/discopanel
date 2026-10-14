package diagnostics

import (
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Every check the runner executes, in report order
func (r *Runner) specs() []spec {
	const (
		env      = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_ENVIRONMENT
		storage  = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_STORAGE
		dock     = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_DOCKER
		network  = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_NETWORK
		prox     = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_PROXY
		upstream = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_UPSTREAM
		auth     = v1.DiagnosticCategory_DIAGNOSTIC_CATEGORY_AUTH
	)
	return []spec{
		{id: "env.host", category: env, title: "Runtime environment", run: r.checkEnvironment},
		{id: "env.version", category: env, title: "DiscoPanel version", run: r.checkVersion},

		{id: "storage.dirs", category: storage, title: "Panel directories", run: r.checkDirectories},
		{id: "storage.servers", category: storage, title: "Server folders", run: r.checkServerDirs},
		{id: "storage.disk", category: storage, title: "Disk space", run: r.checkDiskSpace},
		{id: "storage.database", category: storage, title: "Database", run: r.checkDatabase},

		{id: "docker.daemon", category: dock, title: "Docker daemon", run: r.checkDockerDaemon},
		{id: "docker.network", category: dock, title: "Managed network", run: r.checkDockerNetwork},
		{id: "docker.ports", category: dock, title: "Published ports", run: r.checkPublishedPorts},
		{id: "docker.registry", category: dock, title: "Image registry via daemon", run: r.checkRegistry},
		{id: "docker.images", category: dock, title: "Image freshness", run: r.checkImages, timeout: 40 * time.Second},
		{id: "docker.data_mount", category: dock, title: "Data directory from a container", run: r.checkDataMount, timeout: probeTimeout},
		{id: "docker.server_write", category: dock, title: "Server folders from a container", run: r.checkServerWrite, timeout: probeTimeout},
		{id: "docker.container_dns", category: dock, title: "DNS inside containers", run: r.checkContainerDNS, timeout: probeTimeout},
		{id: "docker.agent_url", category: dock, title: "Panel address from a container", run: r.checkAgentURL, timeout: probeTimeout},

		{id: "network.dns", category: network, title: "DNS resolution", run: r.checkDNS},
		{id: "network.internet", category: network, title: "Internet access", run: r.checkInternet},
		{id: "network.nat", category: network, title: "Addresses and NAT", run: r.checkNAT},
		{id: "network.ports", category: network, title: "Listening ports", run: r.checkListeningPorts},
		{id: "network.forwarding", category: network, title: "Port forwarding", run: r.checkForwarding, timeout: 25 * time.Second},
		{id: "network.hostnames", category: network, title: "Hostname DNS", run: r.checkHostnames},

		{id: "proxy.config", category: prox, title: "Proxy configuration", run: r.checkProxyConfig},
		{id: "proxy.edge", category: prox, title: "Upstream edge traffic", run: r.checkEdge},
		{id: "proxy.tls", category: prox, title: "TLS certificates", run: r.checkTLS},

		{id: "upstream.index", category: upstream, title: "Upstream index", run: r.checkIndex, when: func() bool { return hub.IndexEnabled() }},
		{id: "upstream.curseforge", category: upstream, title: "CurseForge API", run: r.checkCurseForge},
		{id: "upstream.modrinth", category: upstream, title: "Modrinth API", run: r.checkModrinth},
		{id: "upstream.mojang", category: upstream, title: "Mojang version manifest", run: r.checkMojang},
		{id: "upstream.ghcr", category: upstream, title: "GitHub Container Registry", run: r.checkGHCR},
		{id: "upstream.support", category: upstream, title: "Support server", run: r.checkSupportServer},

		{id: "auth.settings", category: auth, title: "Login settings", run: r.checkAuthSettings},
		{id: "auth.oidc", category: auth, title: "OIDC provider", run: r.checkOIDC},
	}
}
