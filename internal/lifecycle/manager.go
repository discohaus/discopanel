// Single owner of server lifecycle transitions like start and stop
package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reports last observed online player count for a server
type PlayerCounter interface {
	PlayersOnline(serverID string) (count int, known bool)
}

type Manager struct {
	store    *storage.Store
	docker   *docker.Client
	prov     *provisioner.Provisioner
	sender   *command.Sender
	proxy    *proxy.Manager
	bus      *events.Bus
	cfg      *config.Config
	log      *logger.Logger
	rec      *metrics.Recorder
	players  PlayerCounter
	streamer *logger.LogStreamer

	// Per-server start locks reject concurrent starts of same server
	startMu sync.Mutex
	starts  map[string]bool

	// Last requested stop source per server, cleared on start
	stopIntentMu sync.Mutex
	stopIntents  map[string]string

	// Paused-server set consulted by proxy wake gate (hot path)
	pausedMu sync.RWMutex
	paused   map[string]bool

	// Roster tracking for first/last-connect RCON commands
	rosterMu     sync.Mutex
	roster       map[string]map[string]bool
	firstConnect map[string]bool

	// Idle tracking for autopause/autostop
	idleMu    sync.Mutex
	idle      map[string]*idleState
	stopWatch chan struct{}
	watchWG   sync.WaitGroup
}

func NewManager(store *storage.Store, dockerClient *docker.Client, prov *provisioner.Provisioner, sender *command.Sender, proxyManager *proxy.Manager, bus *events.Bus, cfg *config.Config, rec *metrics.Recorder, log *logger.Logger) *Manager {
	return &Manager{
		store:        store,
		docker:       dockerClient,
		prov:         prov,
		sender:       sender,
		proxy:        proxyManager,
		bus:          bus,
		cfg:          cfg,
		log:          log,
		rec:          rec,
		starts:       make(map[string]bool),
		stopIntents:  make(map[string]string),
		paused:       make(map[string]bool),
		roster:       make(map[string]map[string]bool),
		firstConnect: make(map[string]bool),
		idle:         make(map[string]*idleState),
	}
}

// Wires collector after construction to avoid circular dependency
func (m *Manager) SetPlayerCounter(pc PlayerCounter) {
	m.players = pc
}

// Wires log streamer so container output reaches server log stream
func (m *Manager) SetLogStreamer(streamer *logger.LogStreamer) {
	m.streamer = streamer
}

// Emits a lifecycle step line into the server's console stream
func (m *Manager) console(serverID, format string, args ...any) {
	if m.streamer == nil {
		return
	}
	m.streamer.AddSystemEntry(serverID, fmt.Sprintf(format, args...))
}

func (m *Manager) tryBeginStart(serverID string) bool {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if m.starts[serverID] {
		return false
	}
	m.starts[serverID] = true
	return true
}

func (m *Manager) endStart(serverID string) {
	m.startMu.Lock()
	delete(m.starts, serverID)
	m.startMu.Unlock()
}

// Reports whether a start or provision cycle is in flight
func (m *Manager) IsStarting(serverID string) bool {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	return m.starts[serverID]
}

func (m *Manager) setStatus(ctx context.Context, server *v1.Server, status v1.ServerStatus) {
	server.Status = status
	if err := m.store.UpdateServerFields(ctx, server.Id, map[string]any{"status": status}); err != nil {
		m.log.Error("lifecycle: failed to persist status %s for %s: %v", protometa.Name(status), server.Name, err)
	}
	m.syncRoute(server)
}

// Reports whether the container runs, db status can lie
func (m *Manager) containerLive(ctx context.Context, containerID string) bool {
	status, err := m.docker.GetContainerStatus(ctx, containerID)
	if err != nil {
		return false
	}
	switch status {
	case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_UNHEALTHY:
		return true
	}
	return false
}

// Persists container identity columns for the server row
func (m *Manager) persistContainer(ctx context.Context, server *v1.Server) error {
	return m.store.UpdateServerFields(ctx, server.Id, map[string]any{
		"container_id":   server.ContainerId,
		"runtime_digest": server.RuntimeDigest,
	})
}

// Reconciles proxy routes after status change for pinging clients
func (m *Manager) syncRoute(server *v1.Server) {
	if m.proxy == nil {
		return
	}
	if err := m.proxy.SyncServerRoutes(context.Background(), server); err != nil {
		m.log.Debug("lifecycle: proxy route sync for %s: %v", server.Name, err)
	}
}

// Reconciles proxy route when properties change outside normal lifecycle
func (m *Manager) SyncProxyRoute(ctx context.Context, serverID string) {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	m.syncRoute(server)
}

// Provisions and starts container, run from a goroutine in handlers
func (m *Manager) Start(ctx context.Context, serverID string) error {
	if !m.tryBeginStart(serverID) {
		return fmt.Errorf("server is already starting")
	}
	defer m.endStart(serverID)
	m.clearStopIntent(serverID)

	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	serverCfg, err := m.store.GetServerProperties(ctx, serverID)
	if err != nil {
		return fmt.Errorf("failed to get server configuration: %w", err)
	}

	// Already running, nothing to do
	if server.ContainerId != "" {
		if status, err := m.docker.GetContainerStatus(ctx, server.ContainerId); err == nil {
			switch status {
			case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_STARTING:
				return nil
			case v1.ServerStatus_SERVER_STATUS_PAUSED:
				return m.Wake(ctx, serverID)
			}
		}
	}

	// Provision server files
	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_PROVISIONING)
	result, err := m.prov.Ensure(ctx, server, serverCfg)
	if err != nil {
		m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_ERROR)
		m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_START, metrics.Attrs{"error": err.Error()}, "provisioning failed: %v", err)
		return fmt.Errorf("provisioning failed: %w", err)
	}

	// Sync resolved facts (modpacks are authoritative for MC version)
	resolved := map[string]any{}
	if result.McVersion != "" && result.McVersion != server.McVersion {
		m.log.Info("lifecycle: %s resolved MC version %s (was %s)", server.Name, result.McVersion, server.McVersion)
		server.McVersion = result.McVersion
		resolved["mc_version"] = server.McVersion
	}
	if java := int32(result.JavaMajor); java != server.JavaVersion {
		server.JavaVersion = java
		resolved["java_version"] = server.JavaVersion
	}
	if len(resolved) > 0 {
		if err := m.store.UpdateServerFields(ctx, server.Id, resolved); err != nil {
			m.log.Error("lifecycle: failed to persist resolved versions for %s: %v", server.Name, err)
		}
	}

	// Provisions agent connection, non-fatal, falls back to SLP/RCON
	if err := m.writeAgentSpec(ctx, server, serverCfg); err != nil {
		m.log.Warn("lifecycle: agent spec for %s not written, telemetry disabled: %v", server.Name, err)
		m.console(server.Id, "agent telemetry unavailable: %v", err)
	}

	// Ensure a container matching the desired image exists
	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_CREATING)
	if err := m.ensureContainer(ctx, server, serverCfg); err != nil {
		m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_ERROR)
		m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_START, metrics.Attrs{"error": err.Error()}, "container setup failed: %v", err)
		return err
	}

	// Start it
	m.console(server.Id, "starting container...")
	if err := m.docker.StartContainer(ctx, server.ContainerId); err != nil {
		m.log.Warn("lifecycle: start failed for %s, recreating container: %v", server.Name, err)
		m.console(server.Id, "container failed to start (%v), recreating it...", err)
		recreated, rerr := m.docker.RecreateContainer(ctx, server.ContainerId, server, serverCfg, func(line string) {
			m.console(server.Id, "%s", line)
		})
		if rerr != nil {
			if recreated != nil && recreated.NewContainerID != "" {
				server.ContainerId = recreated.NewContainerID
				if perr := m.persistContainer(ctx, server); perr != nil {
					m.log.Error("lifecycle: failed to persist container for %s: %v", server.Name, perr)
				}
			}
			m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_ERROR)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_START, metrics.Attrs{"error": rerr.Error()}, "container start failed: %v", rerr)
			return fmt.Errorf("failed to start server container: %w", rerr)
		}
		server.ContainerId = recreated.NewContainerID
		m.recordRuntimeDigest(ctx, server)
		if perr := m.persistContainer(ctx, server); perr != nil {
			m.log.Error("lifecycle: failed to persist container for %s: %v", server.Name, perr)
		}
		if err := m.docker.StartContainer(ctx, server.ContainerId); err != nil {
			m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_ERROR)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_START, metrics.Attrs{"error": err.Error()}, "container start failed: %v", err)
			return fmt.Errorf("failed to start recreated container: %w", err)
		}
	}

	// Attach the containers output to the server's log stream
	if m.streamer != nil {
		if err := m.streamer.StartStreaming(server.Id, server.ContainerId); err != nil {
			m.log.Warn("lifecycle: failed to start log streaming for %s: %v", server.Name, err)
		}
	}
	m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_START, nil, "started the server")

	now := time.Now()
	server.Status = v1.ServerStatus_SERVER_STATUS_STARTING
	server.LastStarted = timestamppb.New(now)
	if err := m.store.UpdateServerFields(ctx, server.Id, map[string]any{
		"status":       v1.ServerStatus_SERVER_STATUS_STARTING,
		"last_started": now,
	}); err != nil {
		m.log.Error("lifecycle: failed to update server after start: %v", err)
	}

	if err := m.store.ClearEphemeralPropertyFields(ctx, server.Id); err != nil {
		m.log.Error("lifecycle: failed to clear ephemeral config fields: %v", err)
	}

	m.setPaused(server.Id, false)
	m.resetIdle(server.Id)

	if m.proxy != nil {
		if err := m.proxy.SyncServerRoutes(ctx, server); err != nil {
			m.log.Error("lifecycle: failed to update proxy routes for %s: %v", server.Name, err)
		}
	}

	if m.bus != nil {
		m.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
			ServerId: server.Id,
		})
	}

	return nil
}

// Creates container if missing, recreates when image or config drifts
func (m *Manager) ensureContainer(ctx context.Context, server *v1.Server, serverCfg *v1.ServerProperties) error {
	desired := m.docker.DesiredImage(server)
	progress := func(line string) { m.console(server.Id, "%s", line) }

	if server.ContainerId != "" {
		current, upToDate, err := m.docker.ContainerImageState(ctx, server.ContainerId, desired)
		if err != nil && !docker.IsNotFound(err) {
			// Transient inspect failure must not tear down a container
			return fmt.Errorf("failed to inspect server container: %w", err)
		}
		if err != nil {
			m.log.Info("lifecycle: %s container is gone, recreating container", server.Name)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_RECREATE, metrics.Attrs{"reason": "container missing"}, "container went missing, recreating container")
		} else if upToDate {
			currentHash, herr := m.docker.ContainerConfigHash(ctx, server.ContainerId)
			if herr == nil && currentHash == m.docker.DesiredConfigHash(server, serverCfg) {
				if m.recordRuntimeDigest(ctx, server) {
					return m.persistContainer(ctx, server)
				}
				return nil
			}
			m.log.Info("lifecycle: %s container configuration drifted, recreating container", server.Name)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_RECREATE, metrics.Attrs{"reason": "settings changed"}, "server settings changed, recreating container")
		} else if current != desired {
			m.log.Info("lifecycle: %s image changed (%s -> %s), recreating container", server.Name, current, desired)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_RECREATE, metrics.Attrs{"reason": "image changed", "from": current, "to": desired}, "runtime image changed (%s -> %s), recreating container", current, desired)
		} else {
			m.log.Info("lifecycle: %s runtime image %s was updated, recreating container", server.Name, desired)
			m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_RECREATE, metrics.Attrs{"reason": "image updated"}, "runtime image updated, recreating container")
		}
		result, err := m.docker.RecreateContainer(ctx, server.ContainerId, server, serverCfg, progress)
		if err != nil {
			return fmt.Errorf("failed to recreate server container: %w", err)
		}
		server.ContainerId = result.NewContainerID
		m.recordRuntimeDigest(ctx, server)
		return m.persistContainer(ctx, server)
	}

	m.console(server.Id, "creating container (image %s)...", desired)
	containerID, err := m.docker.CreateContainer(ctx, server, serverCfg, progress)
	if err != nil {
		return fmt.Errorf("failed to create server container: %w", err)
	}
	server.ContainerId = containerID
	m.recordRuntimeDigest(ctx, server)
	return m.persistContainer(ctx, server)
}

// Records the container image digest, reports true when it changed
func (m *Manager) recordRuntimeDigest(ctx context.Context, server *v1.Server) bool {
	digest, err := m.docker.ContainerImageDigest(ctx, server.ContainerId)
	if err != nil || digest == "" || digest == server.RuntimeDigest {
		return false
	}
	if server.RuntimeDigest != "" {
		m.log.Info("lifecycle: %s runtime digest changed (%s -> %s)", server.Name, shortDigest(server.RuntimeDigest), shortDigest(digest))
		m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_RUNTIME_UPDATE, metrics.Attrs{"from": shortDigest(server.RuntimeDigest), "to": shortDigest(digest)}, "runtime build changed (%s -> %s)", shortDigest(server.RuntimeDigest), shortDigest(digest))
	}
	server.RuntimeDigest = digest
	return true
}

// Trims a digest reference to its last 12 hex chars
func shortDigest(digest string) string {
	if i := strings.LastIndex(digest, ":"); i >= 0 {
		digest = digest[i+1:]
	}
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return digest
}

// Gracefully stops server, sends optional announcement before SIGTERM
func (m *Manager) Stop(ctx context.Context, serverID string) error {
	m.setStopIntent(serverID, metrics.SourceFrom(ctx))
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}

	if server.ContainerId == "" {
		m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_STOPPED)
		return nil
	}

	serverCfg, _ := m.store.GetServerProperties(ctx, serverID)
	stopDuration := docker.DefaultStopTimeoutSeconds
	announceDelay := 0
	if serverCfg != nil {
		if serverCfg.StopDuration != nil && *serverCfg.StopDuration > 0 {
			stopDuration = int(*serverCfg.StopDuration)
		}
		if serverCfg.StopServerAnnounceDelay != nil {
			announceDelay = int(min(*serverCfg.StopServerAnnounceDelay, 300))
		}
	}

	// Paused container cannot process signals, resume it first
	if paused, err := m.docker.IsContainerPaused(ctx, server.ContainerId); err == nil && paused {
		if err := m.docker.UnpauseContainer(ctx, server.ContainerId); err != nil {
			m.log.Warn("lifecycle: failed to unpause %s before stop: %v", server.Name, err)
		}
		m.setPaused(server.Id, false)
	} else if announceDelay > 0 && m.containerLive(ctx, server.ContainerId) {
		msg := fmt.Sprintf("say Server is shutting down in %d seconds", announceDelay)
		if _, err := m.sender.SendCommand(ctx, server.Id, msg); err == nil {
			select {
			case <-time.After(time.Duration(announceDelay) * time.Second):
			case <-ctx.Done():
			}
		}
	}

	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_STOPPING)
	m.console(server.Id, "stopping server (up to %ds for world save)...", stopDuration)

	found, err := m.docker.StopContainer(ctx, server.ContainerId, stopDuration)
	if err != nil {
		m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_ERROR)
		m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_STOP, metrics.Attrs{"error": err.Error()}, "stop failed: %v", err)
		return fmt.Errorf("failed to stop server: %w", err)
	}

	if !found {
		m.log.Warn("lifecycle: container %s not found, cleaning up stale reference", server.ContainerId)
		server.ContainerId = ""
		if err := m.store.UpdateServerFields(ctx, server.Id, map[string]any{"container_id": ""}); err != nil {
			m.log.Error("lifecycle: failed to clear stale container for %s: %v", server.Name, err)
		}
	}
	// Reconciles proxy route, keeps wake-on-connect servers joinable
	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_STOPPED)
	m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_STOP, nil, "stopped the server")

	m.setPaused(server.Id, false)
	m.resetIdle(server.Id)

	if m.bus != nil {
		m.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
			ServerId: server.Id,
		})
	}

	return nil
}

// Stops then fully restarts, reapplying provisioned configuration files
func (m *Manager) Restart(ctx context.Context, serverID string) error {
	if err := m.Stop(ctx, serverID); err != nil {
		return err
	}
	// Yields when another actor claimed the server mid restart
	if src := m.StopRequestedBy(serverID); src != metrics.SourceFrom(ctx) {
		if src == "" {
			return fmt.Errorf("restart aborted, another start took over")
		}
		return fmt.Errorf("restart aborted, %s requested a stop", src)
	}
	if err := m.Start(ctx, serverID); err != nil {
		return err
	}
	if m.bus != nil {
		m.bus.Emit(ctx, events.Event{
			Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
			ServerId: serverID,
		})
	}
	return nil
}

// Destroys container and brings server up from scratch
func (m *Manager) Recreate(ctx context.Context, serverID string) error {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}

	if server.ContainerId != "" {
		m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_CONTAINER_RECREATE, metrics.Attrs{"reason": "requested"}, "recreating the container from scratch")
		if _, err := m.docker.StopContainer(ctx, server.ContainerId, docker.DefaultStopTimeoutSeconds); err != nil {
			m.log.Warn("lifecycle: failed to stop container during recreate: %v", err)
		}
		if err := m.docker.RemoveContainer(ctx, server.ContainerId); err != nil {
			m.log.Debug("lifecycle: failed to remove container during recreate (may not exist): %v", err)
		}
		server.ContainerId = ""
		if err := m.store.UpdateServerFields(ctx, server.Id, map[string]any{"container_id": ""}); err != nil {
			return err
		}
	}

	return m.Start(ctx, serverID)
}

// Freezes a running server's container for autopause
func (m *Manager) Pause(ctx context.Context, serverID string) error {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if server.ContainerId == "" {
		return fmt.Errorf("server has no container")
	}
	if err := m.docker.PauseContainer(ctx, server.ContainerId); err != nil {
		return fmt.Errorf("failed to pause container: %w", err)
	}
	m.setPaused(server.Id, true)
	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_PAUSED)
	m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_PAUSE, nil, "paused the idle server")
	m.log.Info("lifecycle: paused idle server %s", server.Name)
	return nil
}

// Resumes a paused server's container on wake-on-connect
func (m *Manager) Wake(ctx context.Context, serverID string) error {
	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if server.ContainerId == "" {
		return fmt.Errorf("server has no container")
	}
	paused, err := m.docker.IsContainerPaused(ctx, server.ContainerId)
	if err != nil {
		return err
	}
	if !paused {
		m.setPaused(server.Id, false)
		return nil
	}
	if err := m.docker.UnpauseContainer(ctx, server.ContainerId); err != nil {
		return fmt.Errorf("failed to unpause container: %w", err)
	}
	m.setPaused(server.Id, false)
	m.resetIdle(server.Id)
	m.setStatus(ctx, server, v1.ServerStatus_SERVER_STATUS_RUNNING)
	m.rec.Announce(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_WAKE, nil, "woke the server")
	m.log.Info("lifecycle: woke server %s", server.Name)
	return nil
}

func (m *Manager) setStopIntent(serverID, source string) {
	m.stopIntentMu.Lock()
	defer m.stopIntentMu.Unlock()
	m.stopIntents[serverID] = source
}

func (m *Manager) clearStopIntent(serverID string) {
	m.stopIntentMu.Lock()
	delete(m.stopIntents, serverID)
	m.stopIntentMu.Unlock()
}

// Reports who last asked for a stop, empty after starts
func (m *Manager) StopRequestedBy(serverID string) string {
	m.stopIntentMu.Lock()
	defer m.stopIntentMu.Unlock()
	return m.stopIntents[serverID]
}

func (m *Manager) setPaused(serverID string, paused bool) {
	m.pausedMu.Lock()
	if paused {
		m.paused[serverID] = true
	} else {
		delete(m.paused, serverID)
	}
	m.pausedMu.Unlock()
}

func (m *Manager) isPausedFast(serverID string) bool {
	m.pausedMu.RLock()
	defer m.pausedMu.RUnlock()
	return m.paused[serverID]
}

func (m *Manager) writeAgentSpec(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties) error {
	enabled := cfg == nil || cfg.EnableAgent == nil || *cfg.EnableAgent
	if !enabled {
		return runtimespec.WriteAgentSpec(server.DataPath, &v1.AgentSpec{Version: 1, Enabled: false})
	}

	panelURL := m.cfg.Docker.AgentURL
	if panelURL == "" {
		url, err := m.docker.PanelAgentURL(ctx, m.cfg.Server.Port)
		if err != nil {
			return fmt.Errorf("cannot resolve panel URL for the agent: %w", err)
		}
		panelURL = url
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("failed to generate agent token: %w", err)
	}
	token := "dpa_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	server.AgentTokenHash = hex.EncodeToString(sum[:])
	if err := m.store.UpdateServerFields(ctx, server.Id, map[string]any{"agent_token_hash": server.AgentTokenHash}); err != nil {
		return fmt.Errorf("failed to persist agent token hash: %w", err)
	}

	return runtimespec.WriteAgentSpec(server.DataPath, &v1.AgentSpec{
		Version:  1,
		Enabled:  true,
		PanelUrl: panelURL,
		Token:    token,
		ServerId: server.Id,
	})
}
