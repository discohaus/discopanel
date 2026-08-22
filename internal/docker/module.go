package docker

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	shellparse "github.com/arkady-emelyanov/go-shellparse"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/discohaus/discopanel/internal/alias"
	models "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/files"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/proto"
)

// Creates a module container, optionally given sibling modules by name
func (c *Client) CreateModuleContainer(ctx context.Context, module *v1.Module, template *v1.ModuleTemplate, server *v1.Server, serverConfig *v1.ServerProperties, cfg *config.Config, siblingModules ...map[string]*v1.Module) (string, error) {
	// Determine the Docker image to use
	imageName := template.DockerImage
	if imageName == "" {
		return "", fmt.Errorf("module template has no Docker image configured")
	}

	if err := c.ensureImage(ctx, imageName, nil); err != nil {
		return "", err
	}

	// Build alias context for substitution (needed for env and volumes)
	aliasCtx := &alias.Context{
		Server:           server,
		ServerProperties: serverConfig,
		Module:           module,
		Config:           cfg,
	}
	// Add sibling modules for inter-module references
	if len(siblingModules) > 0 && siblingModules[0] != nil {
		aliasCtx.Modules = siblingModules[0]
	}

	// Build environment variables
	env := c.buildModuleEnv(module, server, aliasCtx)

	c.log.Debug("Creating container for module %s with image %s", module.Id, imageName)

	// Build exposed ports and port bindings from module.Ports
	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, port := range module.Ports {
		if port == nil || port.ContainerPort == 0 {
			continue
		}

		dockerProto := protometa.Name(models.TransportOf(port.Protocol))
		natPort := nat.Port(fmt.Sprintf("%d/%s", port.ContainerPort, dockerProto))
		exposedPorts[natPort] = struct{}{}

		// Binds host port only when proxy is disabled
		if port.HostPort > 0 && !port.ProxyEnabled {
			portBindings[natPort] = []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", port.HostPort)},
			}
		}

		c.log.Debug("Added port for module %s: %s (%d:%d/%s, proxy=%t)",
			module.Id, port.Name, port.HostPort, port.ContainerPort, port.Protocol, port.ProxyEnabled)
	}

	// Build mounts from module configuration only (frontend sends complete config)
	vols := c.resolveVolumes(module.VolumeOverrides, aliasCtx)
	if server != nil {
		resolveWorldSources(vols, server.DataPath)
	}

	vols, err := c.mountModuleCerts(module, template, cfg, vols)
	if err != nil {
		return "", err
	}

	// Pre-create bind sources, read-only ones must exist before create
	for _, vol := range vols {
		if vol.Type != "" && vol.Type != "bind" {
			continue
		}
		if !vol.CreateDir && !vol.ReadOnly {
			continue
		}
		if _, err := os.Stat(vol.Source); os.IsNotExist(err) {
			if err := os.MkdirAll(vol.Source, 0755); err != nil {
				c.log.Warn("Failed to pre-create mount directory %s: %v", vol.Source, err)
			}
		}
	}

	mounts := c.volumesToMounts(vols)

	siblings := map[string]*v1.Module{}
	if len(siblingModules) > 0 && siblingModules[0] != nil {
		siblings = siblingModules[0]
	}
	config := &container.Config{
		Image:        imageName,
		Env:          env,
		Tty:          true,
		AttachStdout: true,
		AttachStderr: true,
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"discopanel.module.id":          module.Id,
			"discopanel.module.name":        module.Name,
			"discopanel.module.server_id":   module.ServerId,
			"discopanel.module.template_id": module.TemplateId,
			"discopanel.managed":            "true",
			LabelModuleConfigHash:           c.DesiredModuleConfigHash(module, template, server, serverConfig, cfg, siblings),
		},
	}

	// Set uid + gid
	uid, gid := alias.Substitute(module.Uid, aliasCtx), alias.Substitute(module.Gid, aliasCtx)
	if uid != "" || gid != "" {
		config.User = fmt.Sprintf("%s:%s", uid, gid)
	}

	// Module command override takes precedence over template default
	cmd := module.CmdOverride
	if cmd == "" {
		cmd = template.DefaultCmd
	}
	if cmd != "" {
		// Parse command string into args using shell parsing lib
		cmdArgs, err := shellparse.StringToSlice(cmd)
		if err != nil {
			c.log.Warn("Failed to parse module command %q: %v, using as single arg", cmd, err)
			cmdArgs = []string{cmd}
		}
		config.Cmd = cmdArgs
		c.log.Debug("Setting container command for module %s: %v", module.Id, config.Cmd)
	}

	// Configure resources
	memory := int64(module.Memory)
	if memory == 0 {
		memory = 512 // Default 512MB
	}

	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		Mounts:       mounts,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		Resources: container.Resources{
			Memory:     memory * 1024 * 1024,
			MemorySwap: memory * 1024 * 1024,
		},
		LogConfig: container.LogConfig{
			Type:   "json-file",
			Config: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		ExtraHosts: []string{"host.docker.internal:host-gateway"},
	}

	// Sandboxed clients like Steam need user namespaces
	if len(template.DefaultSecurityOpt) > 0 {
		hostConfig.SecurityOpt = template.DefaultSecurityOpt
	}

	// Apply CPU limit if specified
	if module.CpuLimit > 0 {
		hostConfig.Resources.NanoCPUs = int64(module.CpuLimit * 1e9)
	}

	// Network configuration - same network as server for communication
	networkConfig := &network.NetworkingConfig{}
	if c.config.NetworkName != "" {
		networkConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			c.config.NetworkName: {},
		}
	}

	// Create the container
	resp, err := c.docker.ContainerCreate(
		ctx, config, hostConfig, networkConfig, nil,
		models.ModuleContainerName(module.Id),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create module container: %w", err)
	}

	return resp.ID, nil
}

// Builds environment variables for a module container
func (c *Client) buildModuleEnv(module *v1.Module, server *v1.Server, aliasCtx *alias.Context) []string {
	env := make([]string, 0)

	// Add DiscoPanel context variables, global modules have no server
	if server != nil {
		env = append(env,
			fmt.Sprintf("DISCOPANEL_SERVER_ID=%s", server.Id),
			fmt.Sprintf("DISCOPANEL_SERVER_NAME=%s", server.Name),
			fmt.Sprintf("DISCOPANEL_SERVER_HOST=discopanel-server-%s", server.Id),
			fmt.Sprintf("DISCOPANEL_SERVER_PORT=%d", models.InContainerPort(server)),
		)
	}
	env = append(env,
		fmt.Sprintf("DISCOPANEL_MODULE_ID=%s", module.Id),
		fmt.Sprintf("DISCOPANEL_MODULE_NAME=%s", module.Name),
	)

	// Deploy-agnostic panel URL, later env overrides still win
	if aliasCtx != nil && aliasCtx.Config != nil {
		env = append(env, fmt.Sprintf("DISCOPANEL_URL=%s", c.ModulePanelURL(aliasCtx.Config.Server.Port)))
	}

	// Add module API token if available
	if module.TokenPlaintext != "" {
		env = append(env, fmt.Sprintf("DISCOPANEL_API_TOKEN=%s", module.TokenPlaintext))
	}

	// Adds env vars sorted for a stable config hash
	for _, key := range slices.Sorted(maps.Keys(module.EnvOverrides)) {
		resolvedValue := alias.Substitute(module.EnvOverrides[key], aliasCtx)
		env = append(env, fmt.Sprintf("%s=%s", key, resolvedValue))
	}

	return env
}

// Repoints default world binds at the server's real world dir
func resolveWorldSources(vols []*v1.VolumeMount, dataPath string) {
	worldDir, err := files.FindWorldDir(dataPath)
	if err != nil || worldDir == "" {
		return
	}
	declared := filepath.Join(dataPath, "world")
	if filepath.Clean(worldDir) == declared {
		return
	}
	for i := range vols {
		if vols[i].Type != "" && vols[i].Type != "bind" {
			continue
		}
		src := filepath.Clean(vols[i].Source)
		if src == declared {
			vols[i].Source = worldDir
		} else if strings.HasPrefix(src, declared+string(filepath.Separator)) {
			vols[i].Source = filepath.Join(worldDir, src[len(declared):])
		}
	}
}

// Writes the stored cert pair and mounts it read only
func (c *Client) mountModuleCerts(module *v1.Module, template *v1.ModuleTemplate, cfg *config.Config, vols []*v1.VolumeMount) ([]*v1.VolumeMount, error) {
	if template.CertMountPath == "" || module.CertPem == "" || module.KeyPem == "" {
		return vols, nil
	}
	// Parent stays private, mounted dir stays container readable
	base := models.ModuleDataDir(cfg.Storage.DataDir, module.Id)
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, fmt.Errorf("failed to create module data dir: %w", err)
	}
	dir := filepath.Join(base, "certs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create module cert dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte(module.CertPem), 0644); err != nil {
		return nil, fmt.Errorf("failed to write module certificate: %w", err)
	}
	// Any container uid must read the key through the mount
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), []byte(module.KeyPem), 0644); err != nil {
		return nil, fmt.Errorf("failed to write module key: %w", err)
	}
	return append(vols, &v1.VolumeMount{Source: dir, Target: template.CertMountPath, ReadOnly: true}), nil
}

// Clones volume mounts and substitutes aliases in paths
func (c *Client) resolveVolumes(vols []*v1.VolumeMount, aliasCtx *alias.Context) []*v1.VolumeMount {
	resolved := make([]*v1.VolumeMount, 0, len(vols))
	for _, vol := range vols {
		if vol == nil {
			continue
		}
		clone := proto.Clone(vol).(*v1.VolumeMount)
		clone.Source = alias.Substitute(clone.Source, aliasCtx)
		clone.Target = alias.Substitute(clone.Target, aliasCtx)
		resolved = append(resolved, clone)
	}
	return resolved
}

// Volume mounts to Docker mount specs
func (c *Client) volumesToMounts(volumes []*v1.VolumeMount) []mount.Mount {
	var mounts []mount.Mount

	for _, vol := range volumes {
		mountType := mount.TypeBind
		if vol.Type == "volume" {
			mountType = mount.TypeVolume
		}

		// Skip mounts with empty source or target
		if vol.Source == "" || vol.Target == "" {
			c.log.Warn("Skipping volume mount with empty source or target: source=%q, target=%q", vol.Source, vol.Target)
			continue
		}

		// Translates bind mount sources to host paths in-container
		source := vol.Source
		m := mount.Mount{
			Type:     mountType,
			Source:   source,
			Target:   vol.Target,
			ReadOnly: vol.ReadOnly,
		}
		if mountType == mount.TypeBind {
			m.Source = TranslateToHostPath(source)
			m.BindOptions = &mount.BindOptions{CreateMountpoint: true}
		}

		mounts = append(mounts, m)
	}

	return mounts
}

