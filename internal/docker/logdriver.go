package docker

import (
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
)

// Driver used when docker.log_driver is left empty on Docker
const defaultLogDriver = "local"

// Driver used on Podman, which has no "local" log driver
const podmanLogDriver = "json-file"

// Log driver for containers without one of their own. The configured
// docker.log_driver wins; otherwise "local", or "json-file" on Podman.
// Detection is cached once it succeeds and retried while the engine is
// unreachable.
func (c *Client) DefaultLogDriver() string {
	if c.config.LogDriver != "" {
		return c.config.LogDriver
	}
	c.logDriverMu.Lock()
	defer c.logDriverMu.Unlock()
	if c.autoLogDriver != "" {
		return c.autoLogDriver
	}
	if c.docker == nil {
		return defaultLogDriver
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := c.docker.ServerVersion(ctx)
	if err != nil {
		return defaultLogDriver
	}
	c.autoLogDriver = logDriverFor(v)
	return c.autoLogDriver
}

// Picks the default driver for an engine's /version answer
func logDriverFor(v types.Version) string {
	if isPodman(v) {
		return podmanLogDriver
	}
	return defaultLogDriver
}

// Podman's Docker-compatible API names itself in the platform or components
func isPodman(v types.Version) bool {
	if strings.Contains(strings.ToLower(v.Platform.Name), "podman") {
		return true
	}
	for _, comp := range v.Components {
		if strings.Contains(strings.ToLower(comp.Name), "podman") {
			return true
		}
	}
	return false
}
