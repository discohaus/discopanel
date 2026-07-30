package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	models "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// Default Minecraft server port inside containers
	DefaultMinecraftPort = models.MinecraftDefaultPort

	// Default RCON port inside containers
	DefaultRCONPort = 25575

	// Offset added to game port for RCON host binding
	RCONPortOffset = 10

	// Graceful stop window when server has no configured duration
	DefaultStopTimeoutSeconds = 60
)

type ContainerStats struct {
	CpuPercent  float64 `json:"cpu_percent"`
	CpuCount    int     `json:"cpu_count"`
	MemoryUsage float64 `json:"memory_usage"` // In MB
	MemoryLimit float64 `json:"memory_limit"` // In MB
}

// Converts a container-internal path to a host path
func TranslateToHostPath(path string) string {
	hostDataPath := os.Getenv("DISCOPANEL_HOST_DATA_PATH")
	if hostDataPath == "" {
		return path
	}
	containerDataDir := os.Getenv("DISCOPANEL_DATA_DIR")
	if containerDataDir == "" {
		containerDataDir = "/app/data"
	}
	relPath, err := filepath.Rel(containerDataDir, path)
	if err != nil || strings.HasPrefix(relPath, "..") {
		// Path is not under the container data dir, return as-is
		return path
	}
	return filepath.Join(hostDataPath, relPath)
}

// Reports panel-side health for running containers
type HealthChecker interface {
	ContainerHealth(containerID string, startedAt time.Time) v1.ServerStatus
}

type ClientConfig struct {
	APIVersion   string
	NetworkName  string
	RuntimeImage string
	DNS          string
	Labels       map[string]string
}

type Client struct {
	docker        *client.Client
	config        ClientConfig
	healthChecker HealthChecker
	log           *logger.Logger

	// Background image refresh bookkeeping for ensureImage
	refreshMu      sync.Mutex
	imageRefreshed map[string]time.Time

	// Cached module-facing panel URL, stable per process
	panelURLOnce sync.Once
	panelURL     string
}

// Registers panel-side health source for container status
func (c *Client) SetHealthChecker(hc HealthChecker) {
	c.healthChecker = hc
}

func NewClient(host string, log *logger.Logger, config ...ClientConfig) (*Client, error) {
	opts := []client.Opt{
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	}

	// Apply API version if provided
	if len(config) > 0 && config[0].APIVersion != "" {
		opts = append(opts, client.WithVersion(config[0].APIVersion))
	}

	if host != "" && host != "unix:///var/run/docker.sock" {
		opts = append(opts, client.WithHost(host))
	}

	docker, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	c := &Client{docker: docker, log: log, imageRefreshed: make(map[string]time.Time)}
	if len(config) > 0 {
		c.config = config[0]
	} else {
		// Set defaults
		c.config = ClientConfig{
			NetworkName: "discopanel-network",
		}
	}

	return c, nil
}

func (c *Client) Close() error {
	return c.docker.Close()
}

// Get the docker client instance from the client object
func (c *Client) GetDockerClient() *client.Client {
	return c.docker
}

// Applies DockerOverrides to container and host configs
func ApplyOverrides(overrides *v1.DockerOverrides, config *container.Config, hostConfig *container.HostConfig) {
	if overrides == nil {
		return
	}

	// Apply environment variable overrides
	if len(overrides.GetEnvironment()) > 0 {
		for key, value := range overrides.GetEnvironment() {
			config.Env = append(config.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Apply additional volume mounts
	for _, vol := range overrides.GetVolumes() {
		mountType := mount.Type(vol.GetType())
		if mountType == "" {
			mountType = mount.TypeBind
		}
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
			Type:     mountType,
			Source:   vol.GetSource(),
			Target:   vol.GetTarget(),
			ReadOnly: vol.GetReadOnly(),
		})
	}

	// Apply restart policy override
	if overrides.GetRestartPolicy() != "" {
		hostConfig.RestartPolicy = container.RestartPolicy{
			Name: container.RestartPolicyMode(overrides.GetRestartPolicy()),
		}
	}

	// Apply resource limits
	if overrides.GetCpuLimit() > 0 {
		hostConfig.Resources.NanoCPUs = int64(overrides.GetCpuLimit() * 1e9)
	}
	if overrides.GetMemoryLimit() > 0 {
		hostConfig.Resources.Memory = overrides.GetMemoryLimit() * 1024 * 1024
		hostConfig.Resources.MemorySwap = overrides.GetMemoryLimit() * 1024 * 1024
	}

	// Apply additional labels
	if len(overrides.GetLabels()) > 0 {
		maps.Copy(config.Labels, overrides.GetLabels())
	}

	// Apply capabilities
	if len(overrides.GetCapAdd()) > 0 {
		hostConfig.CapAdd = overrides.GetCapAdd()
	}
	if len(overrides.GetCapDrop()) > 0 {
		hostConfig.CapDrop = overrides.GetCapDrop()
	}

	// Apply devices
	for _, device := range overrides.GetDevices() {
		parts := strings.Split(device, ":")
		if len(parts) >= 2 {
			hostConfig.Devices = append(hostConfig.Devices, container.DeviceMapping{
				PathOnHost:        parts[0],
				PathInContainer:   parts[1],
				CgroupPermissions: "rwm",
			})
		}
	}

	// Apply extra hosts
	if len(overrides.GetExtraHosts()) > 0 {
		hostConfig.ExtraHosts = overrides.GetExtraHosts()
	}

	// Apply security settings
	hostConfig.Privileged = overrides.GetPrivileged()
	hostConfig.ReadonlyRootfs = overrides.GetReadOnly()
	if len(overrides.GetSecurityOpt()) > 0 {
		hostConfig.SecurityOpt = overrides.GetSecurityOpt()
	}

	// Apply SHM size
	if overrides.GetShmSize() > 0 {
		hostConfig.ShmSize = overrides.GetShmSize()
	}

	// Apply user
	if overrides.GetUser() != "" {
		config.User = overrides.GetUser()
	}

	// Apply working directory
	if overrides.GetWorkingDir() != "" {
		config.WorkingDir = overrides.GetWorkingDir()
	}

	// Apply entrypoint
	if len(overrides.GetEntrypoint()) > 0 {
		config.Entrypoint = overrides.GetEntrypoint()
	}

	// Apply command
	if len(overrides.GetCommand()) > 0 {
		config.Cmd = overrides.GetCommand()
	}

	// Apply network mode override
	if overrides.GetNetworkMode() != "" {
		hostConfig.NetworkMode = container.NetworkMode(overrides.GetNetworkMode())
	}

	// Apply DNS override
	if len(overrides.GetDns()) > 0 {
		hostConfig.DNS = overrides.GetDns()
	}
}

// Creates server container and reports setup progress via callback
func (c *Client) CreateContainer(ctx context.Context, server *v1.Server, serverConfig *v1.ServerProperties, progress func(string)) (string, error) {
	imageName := c.DesiredImage(server)

	if err := c.ensureImage(ctx, imageName, progress); err != nil {
		return "", err
	}

	// Build environment variables
	env := buildEnvFromConfig(serverConfig)

	// Proxy servers always use default port internally
	useProxy := server.ProxyHostname != ""
	containerPort := models.InContainerPort(server)

	c.log.Debug("Creating container for server %s with image %s", server.Id, imageName)

	// Build exposed ports
	exposedPorts := nat.PortSet{
		nat.Port(fmt.Sprintf("%d/tcp", containerPort)):   struct{}{},
		nat.Port(fmt.Sprintf("%d/tcp", DefaultRCONPort)): struct{}{},
	}
	for _, port := range server.AdditionalPorts {
		protocol := protometa.Name(models.TransportOf(port.GetProtocol()))
		exposedPorts[nat.Port(fmt.Sprintf("%d/%s", port.GetContainerPort(), protocol))] = struct{}{}
	}

	// Build port bindings
	portBindings := nat.PortMap{}
	if !useProxy {
		// Bind game port to host
		portBindings[nat.Port(fmt.Sprintf("%d/tcp", containerPort))] = []nat.PortBinding{
			{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", server.Port)},
		}
		// Bind RCON to localhost only
		portBindings[nat.Port(fmt.Sprintf("%d/tcp", DefaultRCONPort))] = []nat.PortBinding{
			{HostIP: "127.0.0.1", HostPort: fmt.Sprintf("%d", server.Port+RCONPortOffset)},
		}
	}
	// Add additional port bindings
	for _, port := range server.AdditionalPorts {
		// Proxied ports reach the container over the panel network
		if port.GetProxyEnabled() {
			continue
		}
		protocol := protometa.Name(models.TransportOf(port.GetProtocol()))
		portKey := nat.Port(fmt.Sprintf("%d/%s", port.GetContainerPort(), protocol))
		portBindings[portKey] = []nat.PortBinding{
			{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port.GetHostPort())},
		}
		c.log.Debug("Additional port mapping: %s (%d:%d/%s)", port.GetName(), port.GetHostPort(), port.GetContainerPort(), protocol)
	}

	// Handle path translation when DiscoPanel runs in a container
	dataPath := TranslateToHostPath(server.DataPath)

	if err := os.MkdirAll(server.DataPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create server data directory: %w", err)
	}

	config := &container.Config{
		Image: imageName,
		Env:   env,
		// PTY adds per-line overhead and the streamer demuxes pipes fine
		Tty:          false,
		AttachStdout: true,
		AttachStderr: true,
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"discopanel.server.id":      server.Id,
			"discopanel.server.name":    server.Name,
			"discopanel.server.loader":  protometa.Name(server.ModLoader),
			"discopanel.server.version": server.McVersion,
			"discopanel.managed":        "true",
			LabelConfigHash:             c.DesiredConfigHash(server, serverConfig),
		},
	}

	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: dataPath, Target: "/data", BindOptions: &mount.BindOptions{CreateMountpoint: true}},
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		Resources: container.Resources{
			Memory:     int64(server.Memory) * 1024 * 1024,
			MemorySwap: int64(server.Memory) * 1024 * 1024,
			// Servers win CPU contention against modules and sidecars
			CPUShares: 8192,
		},
		LogConfig: container.LogConfig{
			// Local driver skips the json-file double write per line
			Type:   "local",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
	}

	// Apply global DNS from config
	if c.config.DNS != "" {
		hostConfig.DNS = []string{c.config.DNS}
	}

	// Apply global labels from config
	if c.config.Labels != nil {
		maps.Copy(config.Labels, c.config.Labels)
	}

	// Apply docker overrides
	ApplyOverrides(server.DockerOverrides, config, hostConfig)

	// Lets the supervisor raise tick thread priority
	if !slices.Contains(hostConfig.CapAdd, "SYS_NICE") {
		hostConfig.CapAdd = append(hostConfig.CapAdd, "SYS_NICE")
	}

	// Network configuration
	networkConfig := &network.NetworkingConfig{}
	if c.config.NetworkName != "" && hostConfig.NetworkMode == "" {
		networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			c.config.NetworkName: {},
		}
	}

	resp, err := c.docker.ContainerCreate(
		ctx, config, hostConfig, networkConfig, nil,
		fmt.Sprintf("discopanel-server-%s", server.Id),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	return c.docker.ContainerStart(ctx, containerID, container.StartOptions{})
}

// Stops container gracefully then force kills on failure
func (c *Client) StopContainer(ctx context.Context, containerID string, timeoutSeconds int) (bool, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = DefaultStopTimeoutSeconds
	}
	err := c.docker.ContainerStop(ctx, containerID, container.StopOptions{
		Timeout: &timeoutSeconds,
	})

	if err != nil {
		// If container non-existent on stop
		if errdefs.IsNotFound(err) {
			c.log.Debug("Container %s not found, treating as already stopped", containerID)
			return false, nil
		}
		// If graceful stop fails, force kill the container
		c.log.Warn("Graceful stop failed for container %s: %v, attempting force kill", containerID, err)
		killErr := c.docker.ContainerKill(ctx, containerID, "KILL")
		if killErr != nil {
			// If container non-existent on kill
			if errdefs.IsNotFound(killErr) {
				return false, nil
			}
			return false, fmt.Errorf("failed to stop container: graceful stop error: %v, force kill error: %v", err, killErr)
		}
	}

	return true, nil
}

func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	return c.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	})
}

// Stops and starts a container with an optional delay
func (c *Client) RestartContainer(ctx context.Context, containerID string, delay time.Duration) error {
	if _, err := c.StopContainer(ctx, containerID, DefaultStopTimeoutSeconds); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	if delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}

	if err := c.StartContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	return nil
}

// Result of a container recreation operation
type RecreateContainerResult struct {
	NewContainerID string
	WasRunning     bool
}

// Recreates container and restores prior run state
func (c *Client) RecreateContainer(ctx context.Context, oldContainerID string, server *v1.Server, serverConfig *v1.ServerProperties, progress func(string)) (*RecreateContainerResult, error) {
	result := &RecreateContainerResult{}

	// Check if container was running before we stop it
	if oldContainerID != "" {
		status, err := c.GetContainerStatus(ctx, oldContainerID)
		if err != nil {
			// Container may not exist, that's ok - continue with creation
			c.log.Debug("Container %s not found during recreation: %v", oldContainerID, err)
		} else if status == v1.ServerStatus_SERVER_STATUS_RUNNING || status == v1.ServerStatus_SERVER_STATUS_UNHEALTHY {
			result.WasRunning = true
			if _, err := c.StopContainer(ctx, oldContainerID, DefaultStopTimeoutSeconds); err != nil {
				return nil, fmt.Errorf("failed to stop container: %w", err)
			}
		}

		// Remove old container
		if err := c.RemoveContainer(ctx, oldContainerID); err != nil {
			// Log but continue - container may already be removed
			c.log.Debug("Could not remove old container (may not exist): %v", err)
		}
	}

	// Create new container
	newContainerID, err := c.CreateContainer(ctx, server, serverConfig, progress)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	result.NewContainerID = newContainerID

	// Start if it was running before
	if result.WasRunning {
		if err := c.StartContainer(ctx, newContainerID); err != nil {
			return result, fmt.Errorf("failed to start new container: %w", err)
		}
	}

	return result, nil
}

func (c *Client) GetContainerStatus(ctx context.Context, containerID string) (v1.ServerStatus, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return v1.ServerStatus_SERVER_STATUS_ERROR, err
	}

	switch inspect.State.Status {
	case "running":
		// Health comes from the panel-side SLP checker
		if c.healthChecker != nil {
			startedAt, _ := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
			if verdict := c.healthChecker.ContainerHealth(containerID, startedAt); verdict != v1.ServerStatus_SERVER_STATUS_UNSPECIFIED {
				return verdict, nil
			}
		}
		return v1.ServerStatus_SERVER_STATUS_RUNNING, nil
	case "paused":
		return v1.ServerStatus_SERVER_STATUS_PAUSED, nil
	case "restarting":
		return v1.ServerStatus_SERVER_STATUS_STARTING, nil
	case "exited", "dead", "created", "removing":
		return v1.ServerStatus_SERVER_STATUS_STOPPED, nil
	default:
		return v1.ServerStatus_SERVER_STATUS_ERROR, nil
	}
}

// Freezes all container processes for autopause
func (c *Client) PauseContainer(ctx context.Context, containerID string) error {
	return c.docker.ContainerPause(ctx, containerID)
}

// Resumes paused container for wake-on-connect
func (c *Client) UnpauseContainer(ctx context.Context, containerID string) error {
	return c.docker.ContainerUnpause(ctx, containerID)
}

// Reports whether container is currently paused
func (c *Client) IsContainerPaused(ctx context.Context, containerID string) (bool, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}
	return inspect.State.Status == "paused", nil
}

// Reports container image name and desired build match
func (c *Client) ContainerImageState(ctx context.Context, containerID, desired string) (string, bool, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", false, err
	}
	name := inspect.Config.Image
	if name != desired {
		return name, false, nil
	}
	img, err := c.docker.ImageInspect(ctx, desired)
	if err != nil {
		// Tag missing locally, keep the container
		return name, true, nil
	}
	return name, img.ID == inspect.Image, nil
}

// Returns the registry digest of a container's image
func (c *Client) ContainerImageDigest(ctx context.Context, containerID string) (string, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	img, err := c.docker.ImageInspect(ctx, inspect.Image)
	if err != nil {
		return "", err
	}
	if len(img.RepoDigests) > 0 {
		return img.RepoDigests[0], nil
	}
	// Locally built images have no repo digest
	return img.ID, nil
}

// Raw run state for the panel-side health tracker
type ContainerRunInfo struct {
	Running   bool
	Paused    bool
	StartedAt time.Time
}

// Returns raw container run state without health interpretation
func (c *Client) GetContainerRunInfo(ctx context.Context, containerID string) (*ContainerRunInfo, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	startedAt, _ := time.Parse(time.RFC3339Nano, inspect.State.StartedAt)
	return &ContainerRunInfo{
		Running:   inspect.State.Status == "running",
		Paused:    inspect.State.Status == "paused",
		StartedAt: startedAt,
	}, nil
}

func (c *Client) GetContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	// Get real-time stats
	statsResponse, err := c.docker.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, err
	}
	defer statsResponse.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(statsResponse.Body).Decode(&stats); err != nil {
		return nil, err
	}

	// Calculate CPU percentage (ns)
	cpuPercent := 0.0
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage) - float64(stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage) - float64(stats.PreCPUStats.SystemUsage)

	// Number of CPU cores
	cpuCount := float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	if cpuCount == 0 {
		cpuCount = float64(stats.CPUStats.OnlineCPUs)
	}
	if cpuCount == 0 {
		cpuCount = 1.0
	}

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemDelta) * cpuCount * 100.0
	}

	// Get memory usage in MB (excluding reclaimable file cache)
	memoryUsage := memUsageNoCache(stats.MemoryStats) / 1024 / 1024
	memoryLimit := float64(stats.MemoryStats.Limit) / 1024 / 1024

	return &ContainerStats{
		CpuPercent:  cpuPercent,
		CpuCount:    int(cpuCount),
		MemoryUsage: memoryUsage,
		MemoryLimit: memoryLimit,
	}, nil
}

// Subtracts reclaimable file cache like docker stats does
func memUsageNoCache(mem container.MemoryStats) float64 {
	// Cgroup v1 key
	if v, ok := mem.Stats["total_inactive_file"]; ok && v < mem.Usage {
		return float64(mem.Usage - v)
	}
	// Cgroup v2 key
	if v := mem.Stats["inactive_file"]; v < mem.Usage {
		return float64(mem.Usage - v)
	}
	return float64(mem.Usage)
}

// Runs command inside container, returns stdout and stderr
func (c *Client) Exec(ctx context.Context, containerID string, execCmd []string) (string, string, error) {
	execConfig := container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          execCmd,
	}

	execResp, err := c.docker.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", "", fmt.Errorf("failed to create exec: %w", err)
	}

	attachResp, err := c.docker.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer attachResp.Close()

	// Demultiplexes the docker stream into split buffers
	var stdout, stderr bytes.Buffer
	if _, err = stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return "", "", fmt.Errorf("failed to read exec output: %w", err)
	}

	inspectResp, err := c.docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspectResp.ExitCode != 0 {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return "", "", fmt.Errorf("command failed with exit code %d: %s", inspectResp.ExitCode, detail)
	}

	return stdout.String(), stderr.String(), nil
}

// Pulls image only when absent so starts never block
func (c *Client) ensureImage(ctx context.Context, imageName string, progress func(string)) error {
	if _, err := c.docker.ImageInspect(ctx, imageName); err == nil {
		c.refreshImageAsync(imageName)
		return nil
	}

	if progress != nil {
		progress(fmt.Sprintf("pulling image %s...", imageName))
	}
	reader, err := c.docker.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Aggregate per-layer pull progress into one throttled line
	type layerState struct{ current, total int64 }
	layers := map[string]*layerState{}
	lastReport := time.Now()
	dec := json.NewDecoder(reader)
	for {
		var msg struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to complete image pull for %s: %w", imageName, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("failed to pull image %s: %s", imageName, msg.Error)
		}
		if msg.ID != "" && msg.Status == "Downloading" && msg.ProgressDetail.Total > 0 {
			ls := layers[msg.ID]
			if ls == nil {
				ls = &layerState{}
				layers[msg.ID] = ls
			}
			ls.current = msg.ProgressDetail.Current
			ls.total = msg.ProgressDetail.Total
		}
		if progress != nil && time.Since(lastReport) >= 2*time.Second && len(layers) > 0 {
			var current, total int64
			for _, ls := range layers {
				current += ls.current
				total += ls.total
			}
			progress(fmt.Sprintf("pulling image %s: %.1f/%.1f MB",
				imageName, float64(current)/1024/1024, float64(total)/1024/1024))
			lastReport = time.Now()
		}
	}

	if progress != nil {
		progress(fmt.Sprintf("image %s ready", imageName))
	}
	return nil
}

// Re-pulls present image in background without blocking starts
func (c *Client) refreshImageAsync(imageName string) {
	c.refreshMu.Lock()
	if time.Since(c.imageRefreshed[imageName]) < time.Hour {
		c.refreshMu.Unlock()
		return
	}
	c.imageRefreshed[imageName] = time.Now()
	c.refreshMu.Unlock()

	// Never clobbers a locally built image with registry pull
	if img, err := c.docker.ImageInspect(context.Background(), imageName); err == nil &&
		img.Config != nil && img.Config.Labels["app.discopanel.build"] == "local" {
		c.log.Debug("Image %s is locally built, skipping background refresh", imageName)
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		reader, err := c.docker.ImagePull(ctx, imageName, image.PullOptions{})
		if err != nil {
			c.log.Debug("Background refresh of image %s failed: %v", imageName, err)
			return
		}
		defer reader.Close()
		if _, err := io.Copy(io.Discard, reader); err != nil {
			c.log.Debug("Background refresh of image %s interrupted: %v", imageName, err)
		}
	}()
}

// Resolves container IP on the panel network
func (c *Client) ContainerIP(ctx context.Context, containerID string) (string, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w", err)
	}
	if c.config.NetworkName != "" {
		if network, ok := inspect.NetworkSettings.Networks[c.config.NetworkName]; ok && network.IPAddress != "" {
			return network.IPAddress, nil
		}
	}
	for _, network := range inspect.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress, nil
		}
	}
	return "", fmt.Errorf("no IP address found for container")
}

// DNS name containerized panel registers on managed network
const PanelNetworkAlias = "discopanel-panel"

// Resolves URL runtime containers use to reach the panel
func (c *Client) PanelAgentURL(ctx context.Context, panelPort string) (string, error) {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		if hostname, err := os.Hostname(); err == nil {
			if info, err := c.docker.ContainerInspect(ctx, hostname); err == nil {
				if ep, ok := info.NetworkSettings.Networks[c.config.NetworkName]; ok {
					if slices.Contains(ep.Aliases, PanelNetworkAlias) {
						return fmt.Sprintf("http://%s:%s", PanelNetworkAlias, panelPort), nil
					}
					if ep.IPAddress != "" {
						return fmt.Sprintf("http://%s:%s", ep.IPAddress, panelPort), nil
					}
				}
			}
		}
	}
	nw, err := c.docker.NetworkInspect(ctx, c.config.NetworkName, network.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to inspect network %s: %w", c.config.NetworkName, err)
	}
	for _, ipam := range nw.IPAM.Config {
		if ipam.Gateway != "" {
			return fmt.Sprintf("http://%s:%s", ipam.Gateway, panelPort), nil
		}
	}
	return "", fmt.Errorf("no gateway on network %s", c.config.NetworkName)
}

// Resolves and caches panel URL for module containers
func (c *Client) ModulePanelURL(panelPort string) string {
	c.panelURLOnce.Do(func() {
		url, err := c.PanelAgentURL(context.Background(), panelPort)
		if err != nil {
			c.log.Warn("Falling back to host gateway for module panel URL: %v", err)
			url = "http://host.docker.internal:" + panelPort
		}
		c.panelURL = url
	})
	return c.panelURL
}

// Creates the Docker network and attaches self when containerized
func (c *Client) EnsureNetwork() error {
	ctx := context.Background()

	// List existing networks
	networks, err := c.docker.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	// Check if network already exists
	exists := false
	for _, net := range networks {
		if net.Name == c.config.NetworkName {
			exists = true
			break
		}
	}

	if !exists {
		// Creates network so Docker allocates the subnet automatically
		createOpts := network.CreateOptions{
			Driver: "bridge",
			Labels: map[string]string{
				"discopanel.managed": "true",
			},
		}

		if _, err = c.docker.NetworkCreate(ctx, c.config.NetworkName, createOpts); err != nil {
			return fmt.Errorf("failed to create network: %w", err)
		}
	}

	c.attachSelfToNetwork(ctx)
	return nil
}

// Connects panel to bridge network and registers DNS alias
func (c *Client) attachSelfToNetwork(ctx context.Context) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return
	}

	hostname, err := os.Hostname()
	if err != nil {
		return
	}

	// Docker sets the container hostname to its short ID
	info, err := c.docker.ContainerInspect(ctx, hostname)
	if err != nil {
		c.log.Debug("Could not inspect own container %s: %v", hostname, err)
		return
	}

	if info.HostConfig != nil && info.HostConfig.NetworkMode.IsHost() {
		return
	}

	endpoint := &network.EndpointSettings{Aliases: []string{PanelNetworkAlias}}

	if ep, ok := info.NetworkSettings.Networks[c.config.NetworkName]; ok {
		if slices.Contains(ep.Aliases, PanelNetworkAlias) {
			return
		}
		if err := c.docker.NetworkDisconnect(ctx, c.config.NetworkName, info.ID, false); err != nil {
			c.log.Warn("Could not detach own container to register panel alias: %v", err)
			return
		}
	}

	if err := c.docker.NetworkConnect(ctx, c.config.NetworkName, info.ID, endpoint); err != nil {
		c.log.Error("Failed to attach DiscoPanel container to network %s: %v", c.config.NetworkName, err)
		return
	}

	c.log.Info("Attached DiscoPanel container to network %s as %s", c.config.NetworkName, PanelNetworkAlias)
}

// Builds Docker environment variables from ServerProperties annotations
func buildEnvFromConfig(config *v1.ServerProperties) []string {
	var env []string
	m := config.ProtoReflect()
	for _, p := range protometa.Props(m.Descriptor()) {
		if p.Meta.Env == "" || p.Meta.Env == "-" {
			continue
		}
		value, set := protometa.ScalarString(m, p.Field)
		if !set {
			continue
		}
		// Empty strings stay out, zero numbers and bools go in
		if value == "" && p.Field.Kind() == protoreflect.StringKind {
			continue
		}
		env = append(env, fmt.Sprintf("%s=%s", p.Meta.Env, value))
	}
	return env
}
