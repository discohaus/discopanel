package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// Label marking images built on this machine
const localBuildLabel = "app.discopanel.build"

// Pings the daemon once per run and remembers the answer
func (r *Runner) dockerReady(ctx context.Context, st *runState) error {
	st.pingOnce.Do(func() {
		if r.docker == nil {
			st.pingErr = errors.New("docker client not configured")
			return
		}
		_, st.pingErr = r.docker.GetDockerClient().Ping(ctx)
	})
	return st.pingErr
}

// Socket path behind a unix docker host, empty otherwise
func socketPath(host string) string {
	if strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return ""
}

// Explains a daemon connection failure with a fix
func (r *Runner) explainDockerErr(c *check, err error) {
	host := r.cfg.Docker.Host
	sock := socketPath(host)
	lower := strings.ToLower(err.Error())
	kind := detectContainer()
	switch {
	case strings.Contains(lower, "permission denied"):
		detail := ""
		if uid, gid, ok := files.Owner(sock); ok {
			detail = fmt.Sprintf(" The socket is owned by uid %d gid %d, DiscoPanel runs as uid %d with groups %v.", uid, gid, os.Getuid(), groupsOrEmpty())
		}
		c.fail("Docker socket %s refused access (permission denied)", sock)
		c.fix("Add the DiscoPanel user to the socket's group (usually docker) and log in again, or run the panel as root."+detail+" In compose, run the container as root or add group_add with the socket's gid.", docsTroubleshooting)
	case strings.Contains(lower, "no such file"), strings.Contains(lower, "cannot find the file"):
		c.fail("Docker socket %s does not exist", sock)
		c.fix("Mount the host socket into the panel container (- /var/run/docker.sock:/var/run/docker.sock, add :z on SELinux hosts) or point docker.host at the daemon. Rootless docker listens on $XDG_RUNTIME_DIR/docker.sock, podman needs its API socket enabled (systemctl --user enable --now podman.socket).", docsConfiguration)
	case strings.Contains(lower, "connection refused"):
		c.fail("Docker daemon is not accepting connections at %s", host)
		fix := "Start the docker service (systemctl start docker) and check docker info on the host."
		if kind == containerLXC {
			fix += " Inside a Proxmox LXC enable Options > Features > nesting (and keyctl) first, then restart the container."
		}
		c.fix(fix, docsTroubleshooting)
	default:
		c.fail("Docker daemon unreachable at %s: %s", host, describeNetErr(err))
		c.fix("Verify docker.host (DISCOPANEL_DOCKER_HOST) and that the daemon runs. docker info on the host shows whether it is healthy.", docsConfiguration)
	}
}

// Supplementary group ids, empty when unavailable
func groupsOrEmpty() []int {
	groups, err := os.Getgroups()
	if err != nil {
		return nil
	}
	return groups
}

// Verifies the daemon answers and records its shape
func (r *Runner) checkDockerDaemon(ctx context.Context, c *check) {
	c.fact("docker_host", r.cfg.Docker.Host)
	if err := r.dockerReady(ctx, c.state); err != nil {
		r.explainDockerErr(c, err)
		return
	}
	cli := r.docker.GetDockerClient()
	info, err := cli.Info(ctx)
	if err != nil {
		c.warn("Daemon answers pings but info failed: %v", err)
		return
	}
	c.fact("version", info.ServerVersion)
	c.fact("os", info.OperatingSystem)
	c.fact("os_type", info.OSType)
	c.fact("kernel", info.KernelVersion)
	c.fact("arch", info.Architecture)
	c.fact("cgroup", info.CgroupDriver+" v"+info.CgroupVersion)
	c.fact("storage_driver", info.Driver)
	c.fact("cpus", info.NCPU)
	c.fact("memory", gib(info.MemTotal))
	c.fact("root_dir", info.DockerRootDir)
	c.fact("containers", info.Containers)
	c.fact("images", info.Images)
	rootless := slices.Contains(info.SecurityOptions, "name=rootless")
	c.fact("rootless", rootless)
	for _, w := range info.Warnings {
		c.note("daemon warning: %s", w)
	}
	if rootless {
		c.note("Rootless docker cannot bind ports below 1024 without net.ipv4.ip_unprivileged_port_start and lacks SYS_NICE for tick priority")
	}
	if !info.MemoryLimit {
		c.note("Kernel reports no memory limit support, server memory caps will not apply")
	}
	if info.OSType != "linux" {
		c.warn("Docker %s runs %s containers, DiscoPanel needs linux containers", info.ServerVersion, info.OSType)
		c.fix("Switch Docker Desktop to Linux containers.", "")
		return
	}
	c.pass("Docker %s on %s (%s), API %s", info.ServerVersion, info.OperatingSystem, info.Architecture, cli.ClientVersion())
}

// Verifies the managed bridge and panel attachment
func (r *Runner) checkDockerNetwork(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	name := r.docker.NetworkName()
	c.fact("network", name)
	cli := r.docker.GetDockerClient()
	nw, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		if docker.IsNotFound(err) {
			c.fail("Managed network %s does not exist", name)
			c.fix("DiscoPanel creates it at startup. Look for 'Failed to ensure Docker network' in the panel log, then run docker network create "+name+" by hand if the daemon refuses.", "")
			return
		}
		c.warn("Could not inspect network %s: %v", name, err)
		return
	}
	c.fact("driver", nw.Driver)
	var subnets []string
	for _, ipam := range nw.IPAM.Config {
		subnets = append(subnets, ipam.Subnet)
		if ipam.Gateway != "" {
			c.fact("gateway", ipam.Gateway)
		}
	}
	c.fact("subnets", strings.Join(subnets, ", "))
	c.fact("attached_containers", len(nw.Containers))

	self, err := c.selfContainer(ctx)
	switch {
	case errors.Is(err, docker.ErrNotContainerized):
		c.pass("Network %s exists (%s %s), panel runs outside docker", name, nw.Driver, strings.Join(subnets, " "))
		return
	case err != nil:
		c.warn("Network %s exists but the panel's own container could not be inspected: %v", name, err)
		c.fix("The panel finds itself through /proc/self/mountinfo and the docker socket. If the socket belongs to a different daemon than the one running the panel, modules and agents cannot reach it by alias, set docker.agent_url.", docsConfiguration)
		return
	}
	if self.HostConfig != nil && self.HostConfig.NetworkMode.IsHost() {
		c.pass("Network %s exists, panel uses host networking", name)
		return
	}
	ep, attached := self.NetworkSettings.Networks[name]
	switch {
	case !attached:
		c.warn("Panel container is not attached to %s", name)
		c.fix("The panel attaches itself at startup, restart it and check the log for 'Failed to attach'. Alternatively add the panel service to "+name+" in compose, or use network_mode: host.", docsConfiguration)
	case !slices.Contains(ep.Aliases, docker.PanelNetworkAlias):
		c.warn("Panel is on %s without the %s alias", name, docker.PanelNetworkAlias)
		c.fix("Restart the panel so it re-registers the alias, modules resolve the panel through it.", "")
	default:
		c.pass("Network %s exists and the panel is attached as %s", name, docker.PanelNetworkAlias)
	}
}

// Host ports the panel container publishes for tcp
func publishedTCPPorts(ports nat.PortMap) map[int]bool {
	out := map[int]bool{}
	for port, bindings := range ports {
		if port.Proto() != "tcp" {
			continue
		}
		for _, b := range bindings {
			if p, err := strconv.Atoi(b.HostPort); err == nil {
				out[p] = true
			}
		}
	}
	return out
}

// Ports the panel container must publish in bridge mode
func (r *Runner) neededPorts(ctx context.Context) map[int]string {
	needed := map[int]string{}
	if p, err := strconv.Atoi(r.cfg.Server.Port); err == nil {
		needed[p] = "panel"
	}
	if r.proxy == nil || !r.proxy.Enabled() {
		return needed
	}
	listeners, err := r.store.ListProxyListeners(ctx)
	if err != nil {
		return needed
	}
	for _, l := range listeners {
		if l.Enabled && int(l.Port) > 0 {
			if _, ok := needed[int(l.Port)]; !ok {
				needed[int(l.Port)] = "listener " + l.Name
			}
		}
	}
	return needed
}

// Bridge mode panels must publish panel and listener ports
func (r *Runner) checkPublishedPorts(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	self, err := c.selfContainer(ctx)
	switch {
	case errors.Is(err, docker.ErrNotContainerized):
		c.skip("Panel runs outside docker, nothing to publish")
		return
	case err != nil:
		c.skip("Could not inspect the panel container: %v", err)
		return
	}
	if self.HostConfig != nil && self.HostConfig.NetworkMode.IsHost() {
		c.pass("Host network mode, every listener port is reachable without publishing")
		return
	}
	published := publishedTCPPorts(self.NetworkSettings.Ports)
	if self.HostConfig != nil {
		for p := range publishedTCPPorts(self.HostConfig.PortBindings) {
			published[p] = true
		}
	}
	needed := r.neededPorts(ctx)
	var missing []string
	for port, label := range needed {
		if !published[port] {
			missing = append(missing, fmt.Sprintf("%d (%s)", port, label))
		}
	}
	sort.Strings(missing)
	var have []string
	for p := range published {
		have = append(have, strconv.Itoa(p))
	}
	sort.Strings(have)
	c.fact("published", strings.Join(have, ", "))
	if len(missing) > 0 {
		c.warn("%s not published from the panel container: %s", plural(len(missing), "port is", "ports are"), strings.Join(missing, ", "))
		c.fix("Bridge mode only exposes ports listed under ports: in compose (e.g. \"25565:25565\"). Add each listener port there, or switch to network_mode: host as the example compose recommends. Skip this if a tunnel module (playit) carries player traffic.", docsProxy)
		return
	}
	c.pass("All %d needed ports are published", len(needed))
}

// Explains a registry failure seen by the daemon
func registryRemedy(err error) (string, string) {
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "lookup"), strings.Contains(lower, "name resolution"):
		return "DNS lookup failed inside the daemon", "The Docker daemon resolves names on its own, not through the panel. Fix /etc/resolv.conf on the host, or set \"dns\": [\"1.1.1.1\"] in /etc/docker/daemon.json and restart docker."
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline"), strings.Contains(lower, "i/o"):
		return "timed out", "Outbound HTTPS from the daemon is blocked or slow. Check the host firewall and any HTTP proxy the docker service needs (systemd drop-in with HTTPS_PROXY)."
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return "TLS certificate rejected", "A TLS intercepting proxy or a wrong system clock breaks registry TLS. Install the intercepting CA on the host or fix the clock (timedatectl set-ntp true)."
	case strings.Contains(lower, "denied"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "toomanyrequests"), strings.Contains(lower, "429"):
		return "registry refused the request", "Anonymous pulls from ghcr.io are allowed, so this is a rate limit or a proxy in between. Wait a few minutes and retry, or docker login ghcr.io with a GitHub token."
	}
	return err.Error(), "Run docker pull on the host to see the daemon's own error message."
}

// Asks the daemon to resolve the runtime image on ghcr
func (r *Runner) checkRegistry(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	ref := r.docker.RuntimeImage(docker.SupportedJavaVersions[len(docker.SupportedJavaVersions)-1])
	c.fact("image", ref)
	dist, err := r.docker.GetDockerClient().DistributionInspect(ctx, ref, "")
	if err != nil {
		reason, fix := registryRemedy(err)
		c.fail("Daemon cannot reach the registry for %s: %s", ref, reason)
		c.fix(fix, docsTroubleshooting)
		return
	}
	c.fact("digest", dist.Descriptor.Digest.String())
	c.pass("Daemon can pull from %s", registryHost(ref))
}

// Registry hostname of an image reference
func registryHost(ref string) string {
	host, _, _ := strings.Cut(ref, "/")
	return host
}

// Compares local images against the registry for staleness
func (r *Runner) checkImages(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	owners := map[string][]string{}
	add := func(ref, owner string) {
		if ref == "" {
			return
		}
		owners[ref] = append(owners[ref], owner)
	}
	add(r.docker.RuntimeImage(docker.SupportedJavaVersions[len(docker.SupportedJavaVersions)-1]), "default runtime")
	if servers, err := r.store.ListServers(ctx); err == nil {
		for _, s := range servers {
			add(r.docker.DesiredImage(s), "server "+s.Name)
		}
	}
	if modules, err := r.store.ListModules(ctx); err == nil {
		for _, m := range modules {
			if tpl, err := r.store.GetModuleTemplate(ctx, m.TemplateId); err == nil {
				add(tpl.DockerImage, "module "+m.Name)
			}
		}
	}

	type verdict struct {
		ref    string
		state  string
		reason string
	}
	refs := make([]string, 0, len(owners))
	for ref := range owners {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	results := make([]verdict, len(refs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	cli := r.docker.GetDockerClient()
	for i, ref := range refs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			v := verdict{ref: ref}
			img, err := cli.ImageInspect(ctx, ref)
			switch {
			case err != nil:
				v.state = "absent"
			case img.Config != nil && img.Config.Labels[localBuildLabel] == "local":
				v.state = "local"
			default:
				dist, err := cli.DistributionInspect(ctx, ref, "")
				if err != nil {
					v.state = "unknown"
					v.reason, _ = registryRemedy(err)
					break
				}
				remote := dist.Descriptor.Digest.String()
				current := slices.ContainsFunc(img.RepoDigests, func(d string) bool {
					return strings.HasSuffix(d, "@"+remote)
				})
				if current {
					v.state = "current"
				} else {
					v.state = "outdated"
				}
			}
			results[i] = v
		})
	}
	wg.Wait()

	var outdated, unknown []string
	counts := map[string]int{}
	for _, v := range results {
		counts[v.state]++
		line := fmt.Sprintf("%s: %s (%s)", v.ref, v.state, strings.Join(owners[v.ref], ", "))
		if v.reason != "" {
			line += " " + v.reason
		}
		c.note("%s", line)
		switch v.state {
		case "outdated":
			outdated = append(outdated, v.ref)
		case "unknown":
			unknown = append(unknown, v.ref)
		}
	}
	c.fact("current", counts["current"])
	c.fact("outdated", counts["outdated"])
	c.fact("not_pulled", counts["absent"])
	c.fact("local_builds", counts["local"])

	switch {
	case len(outdated) > 0:
		c.warn("%s newer builds in the registry: %s", plural(len(outdated), "image has", "images have"), abbreviate(outdated, 3))
		c.fix("Images refresh in the background within an hour of a server start. Restart the affected servers, or Recreate modules, to switch to the new build. Manual: docker pull <image>.", docsModules)
	case len(unknown) == len(refs):
		c.skip("Registry unreachable for every image, see docker.registry")
	case len(unknown) > 0:
		c.warn("Could not compare %s against the registry", plural(len(unknown), "image", "images"))
	default:
		c.pass("%s current, %d not pulled yet", plural(counts["current"], "image", "images"), counts["absent"])
	}
}
