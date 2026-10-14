package diagnostics

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// Container flavors the panel may run inside
const (
	containerDocker = "docker"
	containerPodman = "podman"
	containerLXC    = "lxc"
	containerNspawn = "systemd-nspawn"
	containerWSL    = "wsl"
)

// Names of the container flavor, empty on bare metal
func detectContainer() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return containerDocker
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return containerPodman
	}
	if data, err := os.ReadFile("/proc/1/environ"); err == nil {
		for _, kv := range bytes.Split(data, []byte{0}) {
			if k, v, ok := strings.Cut(string(kv), "="); ok && k == "container" && v != "" {
				return v
			}
		}
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil && strings.Contains(string(data), "/lxc/") {
		return containerLXC
	}
	if data, err := os.ReadFile("/proc/version"); err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft") {
		return containerWSL
	}
	return ""
}

// True when a container runtime wrote resolv.conf
func dockerLike(kind string) bool {
	return kind == containerDocker || kind == containerPodman
}

// Env names whose values never leave the host
var secretEnvMarkers = []string{"SECRET", "PASSWORD", "TOKEN", "API_KEY", "APIKEY"}

// Panel env overrides with secrets masked, sorted
func panelEnvOverrides() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(k, "DISCOPANEL_") && k != "APP_VERSION" {
			continue
		}
		upper := strings.ToUpper(k)
		for _, marker := range secretEnvMarkers {
			if strings.Contains(upper, marker) {
				v = "REDACTED"
				break
			}
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// Reports os, container, user, and paths as facts
func (r *Runner) checkEnvironment(ctx context.Context, c *check) {
	c.fact("os", runtime.GOOS)
	c.fact("arch", runtime.GOARCH)
	c.fact("go", runtime.Version())
	c.fact("panel_version", r.version)
	if host, err := os.Hostname(); err == nil {
		c.fact("hostname", host)
	}
	kind := detectContainer()
	if kind == "" {
		c.fact("container", "none")
	} else {
		c.fact("container", kind)
	}

	who := "unknown user"
	if runtime.GOOS != "windows" {
		c.fact("uid", os.Getuid())
		c.fact("gid", os.Getgid())
		if groups, err := os.Getgroups(); err == nil {
			c.fact("groups", fmt.Sprint(groups))
		}
		who = fmt.Sprintf("uid %d", os.Getuid())
		if os.Getuid() == 0 {
			who = "root"
		}
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		c.fact("user", u.Username)
		if runtime.GOOS == "windows" {
			who = u.Username
		}
	}

	c.fact("bind", net.JoinHostPort(r.cfg.Server.Host, r.cfg.Server.Port))
	c.fact("data_dir", r.cfg.Storage.DataDir)
	c.fact("backup_dir", r.cfg.Storage.BackupDir)
	c.fact("temp_dir", r.cfg.Storage.TempDir)
	c.fact("database", r.cfg.Database.Path)
	if r.cfg.Logging.Enabled {
		c.fact("log_file", r.cfg.Logging.FilePath)
	}
	c.fact("docker_host", r.cfg.Docker.Host)

	if overrides := panelEnvOverrides(); len(overrides) > 0 {
		c.note("Environment overrides:")
		for _, kv := range overrides {
			c.note("  %s", kv)
		}
	}

	where := "directly on the host"
	switch kind {
	case containerDocker, containerPodman:
		where = "inside a " + kind + " container"
	case containerLXC:
		where = "inside an LXC container"
		c.note("Docker inside an LXC needs nesting enabled on the container (Proxmox: Options > Features)")
	case containerNspawn:
		where = "inside a systemd-nspawn container"
	case containerWSL:
		where = "inside WSL"
		c.note("WSL networking sits behind its own NAT, port forwarding needs netsh portproxy rules on Windows")
	case "":
	default:
		where = "inside a " + kind + " container"
	}
	c.info("DiscoPanel %s running %s on %s/%s as %s", r.version, where, runtime.GOOS, runtime.GOARCH, who)
}

// Longest mount whose destination contains p
func bestMount(mounts []container.MountPoint, p string) *container.MountPoint {
	var best *container.MountPoint
	for i := range mounts {
		m := &mounts[i]
		dest := path.Clean(m.Destination)
		if p != dest && !strings.HasPrefix(p, dest+"/") {
			continue
		}
		if best == nil || len(dest) > len(path.Clean(best.Destination)) {
			best = m
		}
	}
	return best
}
