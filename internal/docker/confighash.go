package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"strings"

	"github.com/discohaus/discopanel/internal/alias"
	models "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/proto"
)

// Create-time config fingerprint, compared at start to trigger recreate
const LabelConfigHash = "discopanel.config.hash"

// Fingerprints create-time server inputs, excludes image which drifts separately
func (c *Client) DesiredConfigHash(server *v1.Server, serverConfig *v1.ServerProperties) string {
	h := sha256.New()
	w := func(parts ...string) {
		for _, p := range parts {
			_, _ = io.WriteString(h, p)
			_, _ = h.Write([]byte{0})
		}
	}

	// V3 added extra-port proxy flag which changes host binds
	w("v3")
	for _, e := range buildEnvFromConfig(serverConfig) {
		w("env", e)
	}

	useProxy := len(server.ProxyHostnames) > 0
	w("port", strconv.Itoa(int(server.Port)), strconv.Itoa(models.InContainerPort(server)), strconv.FormatBool(useProxy))
	for _, p := range server.AdditionalPorts {
		w("extra-port", strconv.Itoa(int(p.GetHostPort())), strconv.Itoa(int(p.GetContainerPort())), protometa.Name(p.GetProtocol()), strconv.FormatBool(p.GetProxyEnabled()))
	}

	w("data", TranslateToHostPath(server.DataPath))
	w("memory", strconv.Itoa(int(server.Memory)))
	w("dns", c.config.DNS)

	if server.DockerOverrides != nil {
		if raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(server.DockerOverrides); err == nil {
			w("overrides")
			_, _ = h.Write(raw)
		}
	}

	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Returns creation-time config hash, empty if predating the label
func (c *Client) ContainerConfigHash(ctx context.Context, containerID string) (string, error) {
	return c.containerLabel(ctx, containerID, LabelConfigHash)
}

// Create-time module fingerprint, compared to trigger recreate
const LabelModuleConfigHash = "discopanel.module.confighash"

// V1 fingerprints module create inputs, rotating token env excluded
func (c *Client) DesiredModuleConfigHash(module *v1.Module, template *v1.ModuleTemplate, server *v1.Server, serverConfig *v1.ServerProperties, cfg *config.Config, siblings map[string]*v1.Module) string {
	h := sha256.New()
	w := func(parts ...string) {
		for _, p := range parts {
			_, _ = io.WriteString(h, p)
			_, _ = h.Write([]byte{0})
		}
	}

	aliasCtx := &alias.Context{
		Server:           server,
		ServerProperties: serverConfig,
		Module:           module,
		Config:           cfg,
		Modules:          siblings,
	}

	w("v1", template.DockerImage)
	for _, e := range c.buildModuleEnv(module, server, aliasCtx) {
		if strings.HasPrefix(e, "DISCOPANEL_API_TOKEN=") {
			continue
		}
		w("env", e)
	}
	for _, p := range module.Ports {
		if p == nil {
			continue
		}
		w("port", strconv.Itoa(int(p.HostPort)), strconv.Itoa(int(p.ContainerPort)), protometa.Name(p.Protocol), strconv.FormatBool(p.ProxyEnabled))
	}
	vols := c.resolveModuleVolumes(module.VolumeOverrides, aliasCtx)
	if server != nil {
		resolveWorldSources(vols, server.DataPath)
	}
	for _, v := range vols {
		w("vol", v.Type, v.Source, v.Target, strconv.FormatBool(v.ReadOnly))
	}
	w("user", alias.Substitute(module.Uid, aliasCtx), alias.Substitute(module.Gid, aliasCtx))
	cmd := module.CmdOverride
	if cmd == "" {
		cmd = template.DefaultCmd
	}
	w("cmd", cmd)
	w("memory", strconv.Itoa(int(module.Memory)))
	w("cpu", strconv.FormatFloat(module.CpuLimit, 'f', -1, 64))
	w("network", c.config.NetworkName)
	for _, s := range template.DefaultSecurityOpt {
		w("secopt", s)
	}

	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Returns a module container's create-time hash label
func (c *Client) ModuleContainerConfigHash(ctx context.Context, containerID string) (string, error) {
	return c.containerLabel(ctx, containerID, LabelModuleConfigHash)
}

func (c *Client) containerLabel(ctx context.Context, containerID, label string) (string, error) {
	inspect, err := c.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if inspect.Config == nil {
		return "", nil
	}
	return inspect.Config.Labels[label], nil
}
