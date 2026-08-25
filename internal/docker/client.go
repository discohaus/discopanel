package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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
	models "github.com/nickheyer/discopanel/internal/db"
	"github.com/nickheyer/discopanel/internal/minecraft"
	"github.com/nickheyer/discopanel/pkg/logger"
	v1 "github.com/nickheyer/discopanel/pkg/proto/discopanel/v1"
)

const (
	// Docker images manifest URL from itzg/docker-minecraft-server repo
	dockerImagesURL = "https://raw.githubusercontent.com/itzg/docker-minecraft-server/refs/heads/master/images.json"

	// Cache for 1 hour
	dockerImagesCacheDuration = time.Hour

	// Default Minecraft server port inside containers
	DefaultMinecraftPort = 25565

	// Default RCON port inside containers
	DefaultRCONPort = 25575

	// Offset added to game port for RCON host binding
	RCONPortOffset = 10
)

type ContainerStats struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryUsage float64 `json:"memory_usage"` // in MB
	MemoryLimit float64 `json:"memory_limit"` // in MB
}

type DockerImageTag struct {
	Tag           string   `json:"tag"`           // Docker tag name (e.g., "latest", "java21", etc.)
	Java          string   `json:"java"`          // Java version number
	Distribution  string   `json:"distribution"`  // Linux distribution (ubuntu, alpine, oracle)
	JVM           string   `json:"jvm"`           // JVM type (hotspot, graalvm)
	Architectures []string `json:"architectures"` // Supported architectures
	Deprecated    bool     `json:"deprecated"`    // Whether this tag is deprecated
	LTS           bool     `json:"lts"`           // Whether this is an LTS version
	JDK           bool     `json:"jdk"`           // Whether this includes JDK
	Notes         string   `json:"notes"`         // Additional notes about the tag
}

// Cached docker images data
type dockerImagesCache struct {
	mu            sync.RWMutex
	images        []DockerImageTag
	lastFetchTime time.Time
}

var dockerCache = &dockerImagesCache{}

// Converts a container-internal path to a host path.
// When DISCOPANEL_HOST_DATA_PATH is not set (running on host), it returns the path unchanged.
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

// Fetches the docker images manifest from itzg
func fetchDockerImages() ([]DockerImageTag, error) {
	// Check cache first
	dockerCache.mu.RLock()
	if len(dockerCache.images) > 0 && time.Since(dockerCache.lastFetchTime) < dockerImagesCacheDuration {
		images := dockerCache.images
		dockerCache.mu.RUnlock()
		return images, nil
	}
	dockerCache.mu.RUnlock()

	// Fetch new manifest
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(dockerImagesURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch docker images manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch docker images manifest: status code %d", resp.StatusCode)
	}

	var images []DockerImageTag
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		return nil, fmt.Errorf("failed to decode docker images manifest: %w", err)
	}

	// Update cache
	dockerCache.mu.Lock()
	dockerCache.images = images
	dockerCache.lastFetchTime = time.Now()
	dockerCache.mu.Unlock()

	return images, nil
}

// Gets ideal docker tag for a given Minecraft version + mod loader
func GetOptimalDockerTag(mcVersion string, modLoader models.ModLoader, preferGraalVM bool) string {
	javaVersion := GetRequiredJavaVersion(mcVersion, modLoader)
	if javaVersion == "0" || javaVersion == "" {
		// Could not determine Java version, use stable
		return "stable"
	}

	// Fetch Docker images from API
	images, err := fetchDockerImages()
	if err != nil {
		// Could not fetch Docker images, use stable
		return "stable"
	}

	// Find matching tag
	for _, tag := range images {
		if tag.Java == javaVersion && !tag.Deprecated {
			if preferGraalVM && strings.Contains(tag.Tag, "graalvm") {
				return tag.Tag
			}
			// Return first matching non-special tag (not graalvm, alpine, or jdk)
			if !strings.Contains(tag.Tag, "graalvm") && !strings.Contains(tag.Tag, "alpine") && !strings.Contains(tag.Tag, "jdk") {
				return tag.Tag
			}
		}
	}

	// No matching tag found, construct one
	return fmt.Sprintf("java%s", javaVersion)
}

// Gets required Java version for a Minecraft version
func GetRequiredJavaVersion(mcVersion string, modLoader models.ModLoader) string {
	// Fetch the Java version from the Minecraft version metadata
	javaVersion, err := minecraft.GetJavaVersion(mcVersion)
	if err != nil {
		// If we can't determine the Java version, return 0 to indicate error
		return "0"
	}
	return javaVersion
}

type ClientConfig struct {
	APIVersion  string
	NetworkName string
	RegistryURL string
	DNS         string
	Labels      map[string]string
}

type ContainerLogStreamer interface {
	StartStreaming(containerID string) error
	StopStreaming(containerID string)
	MigrateSubscribers(oldContainerID, newContainerID string)
}

type Client struct {
	docker      *client.Client
	config      ClientConfig
	logStreamer ContainerLogStreamer
	log         *logger.Logger
}

// Auto manage streams at the client level when set
func (c *Client) SetLogStreamer(ls ContainerLogStreamer) {
	c.logStreamer = ls
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

	c := &Client{docker: docker, log: log}
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

// ApplyOverrides applies DockerOverrides to container and host configs
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

func (c *Client) CreateContainer(ctx context.Context, server *models.Server, serverConfig *models.ServerConfig) (string, error) {
	// Use server's DockerImage if specified, otherwise determine based on version and loader
	var imageName string
	if server.DockerImage != "" {
		imageName = "itzg/minecraft-server:" + server.DockerImage
	} else {
		imageName = getDockerImage(server.ModLoader, server.MCVersion)
	}

	// Try pulling latest
	if err := c.pullImage(ctx, imageName); err != nil {
		return "", fmt.Errorf("failed to pull image: %w", err)
	}

	// Build environment variables
	env := buildEnvFromConfig(serverConfig)

	// Determine container port - proxy servers always use default port internally
	useProxy := server.ProxyHostname != ""
	lazyServer := useProxy && serverConfig.EnableAutostop != nil && *serverConfig.EnableAutostop && serverConfig.EnableLazyServer != nil && *serverConfig.EnableLazyServer
	containerPort := server.Port
	if useProxy {
		containerPort = DefaultMinecraftPort
		// Override SERVER_PORT env var for proxy servers
		filtered := make([]string, 0, len(env))
		for _, e := range env {
			if !strings.HasPrefix(e, "SERVER_PORT=") {
				filtered = append(filtered, e)
			}
		}
		env = append(filtered, fmt.Sprintf("SERVER_PORT=%d", DefaultMinecraftPort))
	}

	c.log.Debug("Creating container for server %s with image %s", server.ID, imageName)

	// Build exposed ports
	exposedPorts := nat.PortSet{
		nat.Port(fmt.Sprintf("%d/tcp", containerPort)):   struct{}{},
		nat.Port(fmt.Sprintf("%d/tcp", DefaultRCONPort)): struct{}{},
	}
	for _, port := range server.AdditionalPorts {
		protocol := port.GetProtocol()
		if protocol == "" {
			protocol = "tcp"
		}
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
		protocol := port.GetProtocol()
		if protocol == "" {
			protocol = "tcp"
		}
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
		Image:        imageName,
		Env:          env,
		Tty:          true,
		AttachStdout: true,
		AttachStderr: true,
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"discopanel.server.id":      server.ID,
			"discopanel.server.name":    server.Name,
			"discopanel.server.loader":  string(server.ModLoader),
			"discopanel.server.version": server.MCVersion,
			"discopanel.managed":        "true",
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
		},
		LogConfig: container.LogConfig{
			Type:   "json-file",
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

	// Lazy servers rely on auto-stop leaving the container stopped until the
	// proxy receives a player login and explicitly starts it again.
	if lazyServer {
		hostConfig.RestartPolicy = container.RestartPolicy{Name: "no"}
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
		fmt.Sprintf("discopanel-server-%s", server.ID),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	if err := c.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return err
	}

	// Start log streaming if configured
	if c.logStreamer != nil {
		if err := c.logStreamer.StartStreaming(containerID); err != nil {
			c.log.Warn("Failed to start log streaming for container %s: %v", containerID, err)
		}
	}

	return nil
}

// StopContainer stops a container. Returns (containerFound, error).
// If container doesn't exist, returns (false, nil) so caller can clean up stale references.
func (c *Client) StopContainer(ctx context.Context, containerID string) (bool, error) {
	// Stop log streaming before stopping container
	if c.logStreamer != nil {
		c.logStreamer.StopStreaming(containerID)
	}

	// First try graceful stop with a short timeout
	timeout := 5 // seconds
	err := c.docker.ContainerStop(ctx, containerID, container.StopOptions{
		Timeout: &timeout,
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

// Stops and starts a container with an optional delay between operations
func (c *Client) RestartContainer(ctx context.Context, containerID string, delay time.Duration) error {
	if _, err := c.StopContainer(ctx, containerID); err != nil {
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

// Stops, removes, and creates a new container - Returns new container ID and whether it was running before
func (c *Client) RecreateContainer(ctx context.Context, oldContainerID string, server *models.Server, serverConfig *models.ServerConfig) (*RecreateContainerResult, error) {
	result := &RecreateContainerResult{}

	// Check if container was running before we stop it
	if oldContainerID != "" {
		status, err := c.GetContainerStatus(ctx, oldContainerID)
		if err != nil {
			// Container may not exist, that's ok - continue with creation
			c.log.Debug("Container %s not found during recreation: %v", oldContainerID, err)
		} else if status == models.StatusRunning || status == models.StatusUnhealthy {
			result.WasRunning = true
			if _, err := c.StopContainer(ctx, oldContainerID); err != nil {
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
	newContainerID, err := c.CreateContainer(ctx, server, serverConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	result.NewContainerID = newContainerID

	// Migrate log subscribers from old to new container
	if c.logStreamer != nil && oldContainerID != "" {
		c.logStreamer.MigrateSubscribers(oldContainerID, newContainerID)
	}

	// Start if it was running before
	if result.WasRunning {
		if err := c.StartContainer(ctx, newContainerID); err != nil {
			return result, fmt.Errorf("failed to start new container: %w", err)
		}
	}

	return result, nil
}

func (c *Client) GetContainerStatus(ctx context.Context, containerID string) (models.ServerStatus, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return models.StatusError, err
	}

	switch inspect.State.Status {
	case "running":
		// Check health status if available
		if inspect.State.Health != nil {
			switch inspect.State.Health.Status {
			case "healthy":
				return models.StatusRunning, nil
			case "starting":
				return models.StatusStarting, nil
			case "unhealthy":
				// Server process isn't responding
				return models.StatusUnhealthy, nil
			default:
				// No health status or unknown, assume running
				return models.StatusRunning, nil
			}
		}
		return models.StatusRunning, nil
	case "restarting":
		return models.StatusStarting, nil
	case "exited", "dead":
		return models.StatusStopped, nil
	case "created", "paused", "removing":
		return models.StatusStopped, nil
	default:
		return models.StatusError, nil
	}
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

	// Get memory usage in MB (excluding cache)
	memoryUsage := float64(stats.MemoryStats.Usage-stats.MemoryStats.Stats["cache"]) / 1024 / 1024
	memoryLimit := float64(stats.MemoryStats.Limit) / 1024 / 1024

	return &ContainerStats{
		CPUPercent:  cpuPercent,
		MemoryUsage: memoryUsage,
		MemoryLimit: memoryLimit,
	}, nil
}

// Runs shell command, script, or executable inside the container and returns the output
func (c *Client) Exec(ctx context.Context, containerID string, execCmd []string) (string, error) {
	// Create exec configuration
	execConfig := container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Cmd:          execCmd, //[]string{"rcon-cli", command},
	}

	// Create exec instance
	execResp, err := c.docker.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec: %w", err)
	}

	// Attach to exec instance
	attachResp, err := c.docker.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer attachResp.Close()

	// Read output using stdcopy to demultiplex the stream
	var outputBuf bytes.Buffer
	_, err = stdcopy.StdCopy(&outputBuf, &outputBuf, attachResp.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to read exec output: %w", err)
	}

	// Check exec exit code
	inspectResp, err := c.docker.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect exec: %w", err)
	}

	if inspectResp.ExitCode != 0 {
		return "", fmt.Errorf("command failed with exit code %d: %s", inspectResp.ExitCode, outputBuf.String())
	}

	return outputBuf.String(), nil
}

// ExecCommand executes a command inside the container and returns the output
func (c *Client) ExecCommand(ctx context.Context, containerID string, command string) (string, error) {
	return c.Exec(ctx, containerID, []string{"rcon-cli", command})
}

func (c *Client) pullImage(ctx context.Context, imageName string) error {
	reader, err := c.docker.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Read the output to ensure the pull completes
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return fmt.Errorf("failed to complete image pull for %s: %w", imageName, err)
	}

	return nil
}

func (c *Client) GetDockerImages() []DockerImageTag {
	images, err := fetchDockerImages()
	if err != nil {
		c.log.Error("Failed to fetch docker images: %v", err)
		return []DockerImageTag{}
	}

	// Filter out deprecated and dedup
	seen := make(map[string]bool)
	var activeImages []DockerImageTag
	for _, img := range images {
		if !img.Deprecated && !seen[img.Tag] {
			seen[img.Tag] = true
			activeImages = append(activeImages, img)
		}
	}
	return activeImages
}

func getDockerImage(loader models.ModLoader, mcVersion string) string {
	_ = loader
	// itzg/minecraft-server supports all mod loaders through environment variables
	// We use Java version specific tags for better compatibility
	return "itzg/minecraft-server:" + GetOptimalDockerTag(mcVersion, loader, false)
}

// Creates the Docker network if it doesn't exist - attaches itself to that network when applicable
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
		// Create network - let Docker allocate subnet from its configured default-address-pools
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

// Connects discopanel to its own bridge network if running as container
// NOTE: Only really needed for bridge mode though
func (c *Client) attachSelfToNetwork(ctx context.Context) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return
	}

	hostname, err := os.Hostname()
	if err != nil {
		return
	}

	// Docker sets the container hostname to its short ID by default
	info, err := c.docker.ContainerInspect(ctx, hostname)
	if err != nil {
		c.log.Debug("Could not inspect own container %s: %v", hostname, err)
		return
	}

	if info.HostConfig != nil && info.HostConfig.NetworkMode.IsHost() {
		return
	}

	if _, ok := info.NetworkSettings.Networks[c.config.NetworkName]; ok {
		return
	}

	if err := c.docker.NetworkConnect(ctx, c.config.NetworkName, info.ID, nil); err != nil {
		c.log.Error("Failed to attach DiscoPanel container to network %s: %v", c.config.NetworkName, err)
		return
	}

	c.log.Info("Attached DiscoPanel container to network %s", c.config.NetworkName)
}

// Builds Docker environment variables from ServerConfig struct
func buildEnvFromConfig(config *models.ServerConfig) []string {
	env := []string{
		"DUMP_SERVER_PROPERTIES=true",
	}

	configValue := reflect.ValueOf(config).Elem()
	configType := configValue.Type()

	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		envTag := field.Tag.Get("env")

		// Skip fields without env tags
		if envTag == "" || envTag == "-" {
			continue
		}

		fieldValue := configValue.Field(i)

		// Handle pointer types
		if fieldValue.Kind() == reflect.Pointer {
			// Skip if nil
			if fieldValue.IsNil() {
				continue
			}
			// Dereference the pointer
			fieldValue = fieldValue.Elem()
		}

		// Handle different field types
		switch fieldValue.Kind() {
		case reflect.String:
			if str := fieldValue.String(); str != "" {
				env = append(env, fmt.Sprintf("%s=%s", envTag, str))
			}
		case reflect.Int, reflect.Int32, reflect.Int64:
			// Always include int values (even 0) when the field is explicitly set
			env = append(env, fmt.Sprintf("%s=%d", envTag, fieldValue.Int()))
		case reflect.Bool:
			// Always include bool values when the field is explicitly set
			env = append(env, fmt.Sprintf("%s=%v", envTag, fieldValue.Bool()))
		}
	}

	return env
}
