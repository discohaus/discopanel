package diagnostics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/pkg/hub"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Probe containers wait this long for a cold image pull
const probeTimeout = 50 * time.Second

// Background pulls outlive the run that started them
const probePullBudget = 15 * time.Minute

// Marker files probes leave behind for a few seconds
const probeMarkerPrefix = ".discopanel-probe-"

// Output lines probes emit, everything else is noise
const probeLinePrefix = "DIAG "

// Default container user when no setting says otherwise
const defaultContainerUID = 1000

// Reported when no runtime image is ready for a probe
var errProbeImage = errors.New("probe image unavailable")

// Names every install must resolve from inside a container: the registry, the hub, and the upstream route in use
func containerDNSHosts() []string {
	hosts := []string{"ghcr.io", bareHost(hub.SupportHost())}
	for _, h := range hub.UpstreamHosts() {
		hosts = append(hosts, bareHost(h))
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

// Random suffix for marker files
func probeToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// Runtime image for probes, a pulled one wins
func (r *Runner) probeImage(ctx context.Context, st *runState) (string, error) {
	st.imageOnce.Do(func() {
		cli := r.docker.GetDockerClient()
		var candidates []string
		if servers, err := r.store.ListServers(ctx); err == nil {
			for _, s := range servers {
				candidates = append(candidates, r.docker.DesiredImage(s))
			}
		}
		for i := len(docker.SupportedJavaVersions) - 1; i >= 0; i-- {
			candidates = append(candidates, r.docker.RuntimeImage(docker.SupportedJavaVersions[i]))
		}
		for _, ref := range candidates {
			if _, err := cli.ImageInspect(ctx, ref); err == nil {
				st.image = ref
				return
			}
		}
		// Pull continues after the run gives up
		ref := r.docker.RuntimeImage(docker.SupportedJavaVersions[len(docker.SupportedJavaVersions)-1])
		done := make(chan error, 1)
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), probePullBudget)
			defer cancel()
			done <- r.docker.EnsureImage(bg, ref, nil)
		}()
		select {
		case err := <-done:
			if err != nil {
				st.imageErr = fmt.Errorf("%w: %s cannot be pulled, see docker.registry", errProbeImage, ref)
				return
			}
			st.image = ref
		case <-ctx.Done():
			st.imageErr = fmt.Errorf("%w: %s is still downloading, rerun diagnostics once it lands", errProbeImage, ref)
		}
	})
	return st.image, st.imageErr
}

// Runs one probe container and returns its tagged output lines
func (r *Runner) runProbe(ctx context.Context, c *check, name string, opts docker.OneShotOptions) ([]string, error) {
	image, err := r.probeImage(ctx, c.state)
	if err != nil {
		return nil, err
	}
	opts.Image = image
	opts.Name = "discopanel-diag-" + name
	opts.Labels = map[string]string{"discopanel.managed": "true", "discopanel.diagnostic": name}
	c.fact("probe_image", image)
	var mu sync.Mutex
	var lines []string
	err = r.docker.RunOneShot(ctx, opts, func(line string) {
		if rest, ok := strings.CutPrefix(line, probeLinePrefix); ok {
			mu.Lock()
			lines = append(lines, rest)
			mu.Unlock()
		}
	})
	mu.Lock()
	defer mu.Unlock()
	return lines, err
}

// Skips on missing image, fails on daemon refusals
func (r *Runner) probeFailed(c *check, err error) {
	if errors.Is(err, errProbeImage) {
		c.skip("%v", err)
		return
	}
	c.fail("Probe container could not run: %s", strings.TrimSpace(err.Error()))
	c.fix("A server container would fail the same way. The daemon error above says why, docker run --rm on the host with the same image reproduces it.", docsTroubleshooting)
}

// First word of each line keyed to the rest
func probeFields(lines []string) map[string]string {
	out := map[string]string{}
	for _, line := range lines {
		k, v, _ := strings.Cut(line, " ")
		if _, dup := out[k]; !dup {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// Host side of the data dir from panel mounts
func (r *Runner) hostPathHint(ctx context.Context, c *check) (string, error) {
	self, err := c.selfContainer(ctx)
	if err != nil {
		return "", err
	}
	if self.HostConfig != nil {
		c.fact("panel_network_mode", string(self.HostConfig.NetworkMode))
	}
	dataDir := path.Clean(filepath.ToSlash(r.cfg.Storage.DataDir))
	m := bestMount(self.Mounts, dataDir)
	if m == nil {
		c.note("The panel container has no volume covering %s", dataDir)
		return "", nil
	}
	c.fact("panel_volume", m.Source+" -> "+m.Destination)
	rel := strings.TrimPrefix(dataDir, path.Clean(m.Destination))
	return path.Clean(m.Source + rel), nil
}

// Remedy for a data mount the daemon cannot see correctly
func (r *Runner) hostPathRemedy(c *check, dataDir, hostPath, expected string, containerized bool) {
	hostEnv := os.Getenv("DISCOPANEL_HOST_DATA_PATH")
	switch {
	case containerized && hostEnv == "":
		want := expected
		if want == "" {
			want = "<host folder mounted at " + dataDir + ">"
		}
		c.fix(fmt.Sprintf("The panel runs in a container, so server containers need the host side of %s. Set DISCOPANEL_HOST_DATA_PATH=%s in the panel's environment (identical to the volume source in compose) and recreate the panel container.", dataDir, want), docsConfiguration)
	case containerized && expected != "" && expected != hostPath:
		c.fix(fmt.Sprintf("DISCOPANEL_HOST_DATA_PATH is %s but the panel's data volume comes from %s. Set it to %s and keep DISCOPANEL_DATA_DIR equal to storage.data_dir (%s).", hostEnv, expected, expected, dataDir), docsConfiguration)
	case containerized:
		c.fix(fmt.Sprintf("Docker mounted %s but that is not the folder the panel writes. Check the volume line for %s in compose and make DISCOPANEL_HOST_DATA_PATH match its host side exactly.", hostPath, dataDir), docsConfiguration)
	default:
		c.fix(fmt.Sprintf("The Docker daemon does not share this filesystem. It mounted %s while the panel writes %s. Causes: a remote daemon (docker.host over tcp), a socket shared from the host into an LXC, or Docker Desktop file sharing that excludes the path. Set DISCOPANEL_HOST_DATA_PATH to the path as the daemon's host sees it, or run the panel where the daemon runs.", hostPath, dataDir), docsConfiguration)
	}
}

// Proves server containers see the data directory the panel writes
func (r *Runner) checkDataMount(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	dataDir := r.cfg.Storage.DataDir
	hostPath := docker.TranslateToHostPath(dataDir)
	c.fact("data_dir", dataDir)
	c.fact("host_data_path", hostPath)
	c.fact("host_data_path_env", os.Getenv("DISCOPANEL_HOST_DATA_PATH"))
	c.fact("data_dir_env", os.Getenv("DISCOPANEL_DATA_DIR"))

	expected, selfErr := r.hostPathHint(ctx, c)
	containerized := !errors.Is(selfErr, docker.ErrNotContainerized)
	if selfErr != nil && containerized {
		c.note("Panel container not inspectable: %v", selfErr)
	}
	c.fact("panel_in_container", containerized)
	if expected != "" {
		c.fact("panel_volume_source", expected)
	}

	token := probeToken()
	markerName := probeMarkerPrefix + token
	marker := filepath.Join(dataDir, markerName)
	if err := os.WriteFile(marker, []byte(token), 0o644); err != nil {
		c.fail("Panel cannot write a marker into %s: %v", dataDir, err)
		c.fix(chownRemedy([]string{dataDir}), docsTroubleshooting)
		return
	}
	defer os.Remove(marker)

	script := fmt.Sprintf(`echo "DIAG id $(id -u):$(id -g)"
if [ -f /data/%[1]s ]; then echo "DIAG marker $(cat /data/%[1]s)"; else echo "DIAG marker missing"; echo "DIAG entries $(ls -A /data 2>/dev/null | head -8 | tr '\n' ' ')"; fi`, markerName)
	lines, err := r.runProbe(ctx, c, "data-mount", docker.OneShotOptions{
		Cmd:        []string{"sh", "-c", script},
		DataPath:   dataDir,
		StrictData: true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "bind source path does not exist") {
			c.fail("Docker's host has no directory %s to mount into server containers", hostPath)
			r.hostPathRemedy(c, dataDir, hostPath, expected, containerized)
			return
		}
		r.probeFailed(c, err)
		return
	}
	got := probeFields(lines)
	c.fact("container_user", got["id"])
	switch got["marker"] {
	case token:
		if hostPath == dataDir {
			c.pass("Server containers see %s exactly as the panel does", dataDir)
		} else {
			c.pass("Server containers see %s at host path %s", dataDir, hostPath)
		}
	case "missing":
		c.fail("A container mounting %s does not see the panel's files", hostPath)
		if e := got["entries"]; e != "" {
			c.note("Container saw: %s", e)
		} else {
			c.note("Container saw an empty directory")
		}
		r.hostPathRemedy(c, dataDir, hostPath, expected, containerized)
	default:
		c.fail("A container mounting %s reads a different marker than the panel wrote", hostPath)
		r.hostPathRemedy(c, dataDir, hostPath, expected, containerized)
	}
}

// Uid and gid a server container drops to, default 1000
func (r *Runner) containerIDs(ctx context.Context, serverID string, global *v1.ServerProperties) (int, int) {
	uid, gid := defaultContainerUID, defaultContainerUID
	pick := func(p *v1.ServerProperties) {
		if p == nil {
			return
		}
		if p.Uid != nil && *p.Uid > 0 {
			uid = int(*p.Uid)
		}
		if p.Gid != nil && *p.Gid > 0 {
			gid = int(*p.Gid)
		}
	}
	pick(global)
	if props, err := r.store.GetServerProperties(ctx, serverID); err == nil {
		pick(props)
	}
	return uid, gid
}

// One server folder as a probe container reaches it
type folderTarget struct {
	server *v1.Server
	inner  string
}

// Servers sharing a mount root and a container user
type folderGroup struct {
	uid, gid int
	root     string
	targets  []folderTarget
}

// Root pass mirroring the runtime, writes then chowns
const folderRootScript = `M=".discopanel-probe-$1"; U="$2"; G="$3"; shift 3
for d in "$@"; do
  if [ ! -d "$d" ]; then echo "DIAG $d|missing"; continue; fi
  if ! touch "$d/$M" 2>/dev/null; then echo "DIAG $d|root-denied|owner=$(stat -c %u:%g "$d" 2>/dev/null)|mode=$(stat -c %a "$d" 2>/dev/null)"; continue; fi
  if chown "$U:$G" "$d/$M" 2>/dev/null; then C=1; else C=0; fi
  rm -f "$d/$M"
  echo "DIAG $d|chown=$C|owner=$(stat -c %u:%g "$d" 2>/dev/null)|mode=$(stat -c %a "$d" 2>/dev/null)"
done`

// User pass run as the server uid, writes only
const folderUserScript = `M=".discopanel-probe-$1"; shift 1
for d in "$@"; do
  if [ ! -d "$d" ]; then echo "DIAG $d|missing"; continue; fi
  if touch "$d/$M" 2>/dev/null; then rm -f "$d/$M"; echo "DIAG $d|write=1"; else echo "DIAG $d|write=0"; fi
done`

// What both passes learned about one folder
type folderOutcome struct {
	state  string
	fields map[string]string
}

// Proves server containers can own and write their folders
func (r *Runner) checkServerWrite(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	servers, err := r.store.ListServers(ctx)
	if err != nil {
		c.skip("Could not list servers: %v", err)
		return
	}
	if len(servers) == 0 {
		c.skip("No servers yet")
		return
	}
	global, _, _ := r.store.GetGlobalSettings(ctx)
	dataDir := r.cfg.Storage.DataDir

	groups := map[string]*folderGroup{}
	var keys []string
	missing := 0
	for _, s := range servers {
		if s.DataPath == "" {
			continue
		}
		if _, err := os.Stat(s.DataPath); err != nil {
			missing++
			c.note("%s: %s missing, recreated on next start", s.Name, s.DataPath)
			continue
		}
		uid, gid := r.containerIDs(ctx, s.Id, global)
		root, inner := s.DataPath, "/data"
		if rel, err := filepath.Rel(dataDir, s.DataPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			root, inner = dataDir, path.Join("/data", filepath.ToSlash(rel))
		}
		key := fmt.Sprintf("%d:%d|%s", uid, gid, root)
		g := groups[key]
		if g == nil {
			g = &folderGroup{uid: uid, gid: gid, root: root}
			groups[key] = g
			keys = append(keys, key)
		}
		g.targets = append(g.targets, folderTarget{server: s, inner: inner})
	}
	sort.Strings(keys)

	var rootDenied, userDenied, unseen, ok []string
	var fixHost []string
	probed := 0
	for i, key := range keys {
		g := groups[key]
		rootArgs := []string{"sh", "-c", folderRootScript, "sh", probeToken(), strconv.Itoa(g.uid), strconv.Itoa(g.gid)}
		byInner := map[string]folderTarget{}
		for _, t := range g.targets {
			rootArgs = append(rootArgs, t.inner)
			byInner[t.inner] = t
		}
		lines, err := r.runProbe(ctx, c, fmt.Sprintf("folders-%d-root", i), docker.OneShotOptions{
			Cmd:        rootArgs,
			DataPath:   g.root,
			StrictData: true,
		})
		if err != nil {
			if strings.Contains(err.Error(), "bind source path does not exist") {
				c.skip("Docker's host has no directory %s, see docker.data_mount", docker.TranslateToHostPath(g.root))
				return
			}
			r.probeFailed(c, err)
			return
		}
		outcomes := map[string]folderOutcome{}
		userArgs := []string{"sh", "-c", folderUserScript, "sh", probeToken()}
		for _, line := range lines {
			inner, state, fields := parseFolderLine(line)
			if _, known := byInner[inner]; !known {
				continue
			}
			outcomes[inner] = folderOutcome{state: state, fields: fields}
			if state == "probed" {
				if g.uid == 0 {
					fields["write"] = "1"
				} else {
					userArgs = append(userArgs, inner)
				}
			}
		}
		// Second container runs as the server user, no privilege drop inside
		if g.uid != 0 && len(userArgs) > 5 {
			userLines, err := r.runProbe(ctx, c, fmt.Sprintf("folders-%d-user", i), docker.OneShotOptions{
				Cmd:        userArgs,
				DataPath:   g.root,
				StrictData: true,
				User:       fmt.Sprintf("%d:%d", g.uid, g.gid),
			})
			if err != nil {
				r.probeFailed(c, err)
				return
			}
			for _, line := range userLines {
				inner, _, fields := parseFolderLine(line)
				if o, found := outcomes[inner]; found {
					o.fields["write"] = fields["write"]
				}
			}
		}
		for _, t := range g.targets {
			o, found := outcomes[t.inner]
			if !found {
				continue
			}
			name := t.server.Name
			hostPath := docker.TranslateToHostPath(t.server.DataPath)
			fields := o.fields
			switch {
			case o.state == "missing":
				unseen = append(unseen, name)
				c.note("%s: not visible inside the container at %s", name, hostPath)
				continue
			case o.state == "root-denied":
				rootDenied = append(rootDenied, name)
				c.note("%s: container root cannot write %s (owner %s mode %s)", name, hostPath, fields["owner"], fields["mode"])
			case fields["write"] == "1":
				ok = append(ok, name)
				c.note("%s: writable as %d:%d (owner %s mode %s)", name, g.uid, g.gid, fields["owner"], fields["mode"])
			case fields["chown"] == "1":
				ok = append(ok, name)
				c.note("%s: owner %s now, runtime takes ownership as %d:%d at start", name, fields["owner"], g.uid, g.gid)
			default:
				userDenied = append(userDenied, name)
				fixHost = append(fixHost, fmt.Sprintf("chown -R %d:%d %s", g.uid, g.gid, hostPath))
				c.note("%s: container runs as %d:%d, cannot write or take ownership of %s (owner %s mode %s)", name, g.uid, g.gid, hostPath, fields["owner"], fields["mode"])
			}
			probed++
		}
	}
	c.fact("servers_probed", probed)

	switch {
	case len(unseen) > 0:
		c.fail("Containers do not see %s through the data mount: %s", plural(len(unseen), "server folder", "server folders"), abbreviate(unseen, 4))
		c.fix("The host side of the data directory is wrong, so servers would start with an empty folder. Fix docker.data_mount first.", docsConfiguration)
	case len(rootDenied) > 0:
		c.fail("Containers cannot write %s even as root: %s", plural(len(rootDenied), "server folder", "server folders"), abbreviate(rootDenied, 4))
		c.fix("The mount itself refuses writes. On SELinux hosts (Fedora, RHEL) label the data folder with chcon -Rt container_file_t <folder>. NFS with root_squash and read-only mounts behave the same, and rootless Docker maps container root to your user, so the host folder must be writable by that user.", docsTroubleshooting)
	case len(userDenied) > 0:
		c.fail("%s cannot be written by the server container: %s", plural(len(userDenied), "server folder", "server folders"), abbreviate(userDenied, 4))
		c.fix(fmt.Sprintf("The container cannot chown the folder and the server user cannot write it. On the host run: %s. Or set uid and gid in the server's settings to the folder's owner.", strings.Join(fixHost, "; ")), docsTroubleshooting)
	case probed == 0 && missing > 0:
		c.info("%d server folders are missing and will be recreated on start", missing)
	case probed == 0:
		c.skip("No server folders to probe")
	default:
		c.pass("All %d probed server folders are writable by their containers", probed)
	}
}

// Splits a folder probe line into path, state, and fields
func parseFolderLine(line string) (string, string, map[string]string) {
	parts := strings.Split(line, "|")
	fields := map[string]string{}
	if len(parts) < 2 {
		return parts[0], "", fields
	}
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(p, "=")
		fields[k] = v
	}
	state := parts[1]
	if strings.Contains(state, "=") {
		state = "probed"
	}
	return parts[0], state, fields
}

// Script resolving names the way a server container does
const dnsProbeScript = `for h in "$@"; do
  if a=$(timeout 5 getent hosts "$h" 2>/dev/null); then echo "DIAG ok $h ${a%% *}"; else echo "DIAG fail $h"; fi
done
echo "DIAG resolv $(sed -n 's/^nameserver[[:space:]]*//p' /etc/resolv.conf | tr '\n' ' ')"`

// Resolves upstream names from inside a container
func (r *Runner) checkContainerDNS(ctx context.Context, c *check) {
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	c.fact("docker_dns", r.cfg.Docker.DNS)
	c.fact("network", r.docker.NetworkName())
	hosts := containerDNSHosts()
	args := append([]string{"sh", "-c", dnsProbeScript, "sh"}, hosts...)
	lines, err := r.runProbe(ctx, c, "dns", docker.OneShotOptions{
		Cmd:     args,
		Network: r.docker.NetworkName(),
	})
	if err != nil {
		r.probeFailed(c, err)
		return
	}
	var failed []string
	resolvers := ""
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "ok":
			c.note("%s: %s", fields[1], strings.Join(fields[2:], " "))
		case "fail":
			failed = append(failed, fields[1])
			c.note("%s: no answer", fields[1])
		case "resolv":
			resolvers = strings.Join(fields[1:], ", ")
		}
	}
	c.fact("container_resolvers", resolvers)

	embedded := strings.Contains(resolvers, "127.0.0.11")
	switch {
	case len(failed) == len(hosts):
		c.fail("Containers cannot resolve any of %d hostnames", len(hosts))
		if embedded {
			c.fix("Docker's embedded DNS (127.0.0.11) forwards to the resolvers in the host's /etc/resolv.conf, and those do not answer from inside containers (a loopback systemd-resolved stub, or a firewall dropping container traffic on port 53). Set docker.dns (DISCOPANEL_DOCKER_DNS=1.1.1.1) for server containers and \"dns\": [\"1.1.1.1\"] in /etc/docker/daemon.json for the rest, then restart docker.", docsTroubleshooting)
		} else {
			c.fix("The resolvers containers received ("+orEmpty(resolvers)+") do not answer. Set docker.dns (DISCOPANEL_DOCKER_DNS=1.1.1.1) or fix dns in /etc/docker/daemon.json, and check the host firewall allows container traffic out on port 53.", docsTroubleshooting)
		}
	case len(failed) > 0:
		c.warn("Containers failed to resolve %s: %s", plural(len(failed), "hostname", "hostnames"), strings.Join(failed, ", "))
		c.fix("A filtering or flaky resolver inside containers. Set docker.dns (DISCOPANEL_DOCKER_DNS=1.1.1.1) so servers get a known good one.", docsTroubleshooting)
	default:
		c.pass("Containers resolve all %d hostnames via %s", len(hosts), orEmpty(resolvers))
	}
}

// Script fetching one page over bash's tcp device
const agentProbeScript = `H="$1"; P="$2"
out=$(timeout 8 bash -c 'exec 3<>/dev/tcp/'"$H"'/'"$P"' || exit 7; printf "GET / HTTP/1.0\r\nHost: '"$H"'\r\nUser-Agent: discopanel-probe\r\n\r\n" >&3; IFS= read -r -t 5 line <&3 || exit 8; printf "%s" "$line"' 2>&1); rc=$?
echo "DIAG rc $rc"; echo "DIAG out $out"; echo "DIAG ip $(getent hosts "$H" 2>/dev/null | head -1)"`

// Verifies the address runtime agents dial, from a container
func (r *Runner) checkAgentURL(ctx context.Context, c *check) {
	if isLoopbackHost(r.cfg.Server.Host) {
		c.fail("server.host is %s, containers cannot reach the panel", r.cfg.Server.Host)
		c.fix("Set server.host to 0.0.0.0 (DISCOPANEL_SERVER_HOST) so the panel answers on every interface.", docsConfiguration)
		return
	}
	if err := r.dockerReady(ctx, c.state); err != nil {
		c.skip("Docker unavailable, see docker.daemon")
		return
	}
	agentURL := r.cfg.Docker.AgentURL
	if agentURL == "" {
		resolved, err := r.docker.PanelAgentURL(ctx, r.cfg.Server.Port)
		if err != nil {
			c.warn("Could not work out the panel address containers use: %v", err)
			c.fix("Set docker.agent_url (DISCOPANEL_DOCKER_AGENT_URL) to a URL containers can reach, for example http://<lan-ip>:"+r.cfg.Server.Port, docsConfiguration)
			return
		}
		agentURL = resolved
		c.fact("source", "auto detected")
	} else {
		c.fact("source", "docker.agent_url")
	}
	c.fact("agent_url", agentURL)
	parsed, err := url.Parse(agentURL)
	if err != nil || parsed.Hostname() == "" {
		c.fail("Agent URL %q is not a valid URL", agentURL)
		c.fix("Set docker.agent_url to http://host:port form.", docsConfiguration)
		return
	}
	host := parsed.Hostname()
	if isLoopbackHost(host) {
		c.fail("Agent URL %s points at loopback, unreachable from containers", agentURL)
		c.fix("Set docker.agent_url to the panel's LAN address or the managed network gateway, for example http://<lan-ip>:"+r.cfg.Server.Port, docsConfiguration)
		return
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}

	lines, err := r.runProbe(ctx, c, "agent", docker.OneShotOptions{
		Cmd:     []string{"sh", "-c", agentProbeScript, "sh", host, port},
		Network: r.docker.NetworkName(),
	})
	if err != nil {
		r.probeFailed(c, err)
		return
	}
	got := probeFields(lines)
	c.fact("resolved", got["ip"])
	out := got["out"]
	lower := strings.ToLower(out)
	switch {
	case got["rc"] == "0" && strings.HasPrefix(out, "HTTP/"):
		c.pass("Containers reach the panel at %s (%s)", agentURL, out)
	case got["rc"] == "8" && parsed.Scheme == "https":
		c.pass("Containers connect to %s, TLS not verified by the probe", agentURL)
	case got["rc"] == "8":
		c.warn("Containers connect to %s but nothing HTTP answered", agentURL)
		c.fix("Something else listens on that port. Point docker.agent_url at the panel's port ("+r.cfg.Server.Port+").", docsConfiguration)
	case strings.Contains(lower, "name or service not known"), strings.Contains(lower, "temporary failure"), strings.Contains(lower, "invalid argument"):
		c.fail("Containers cannot resolve %s", host)
		switch {
		case host == docker.PanelNetworkAlias:
			c.fix("The panel alias only exists on the managed network, and the panel is not attached with it. See docker.network, or set docker.agent_url to the panel's LAN address.", docsConfiguration)
		case host == "host.docker.internal":
			c.fix("Server containers do not get host.docker.internal on Linux. Set docker.agent_url to the panel's LAN address or the managed network gateway (docker network inspect "+r.docker.NetworkName()+" shows it).", docsConfiguration)
		default:
			c.fix("Containers cannot resolve that name, see docker.container_dns, or use an IP address in docker.agent_url.", docsConfiguration)
		}
	case strings.Contains(lower, "connection refused"):
		c.fail("Containers reach %s but the panel port %s is closed there", host, port)
		c.fix("The panel is not listening on that address. server.host must be 0.0.0.0, and the port must match server.port ("+r.cfg.Server.Port+"). In bridge mode the panel container must publish the port for gateway addresses to work.", docsConfiguration)
	case strings.Contains(lower, "no route"), strings.Contains(lower, "timed out"), got["rc"] == "124":
		c.fail("Containers cannot connect to %s (%s)", agentURL, orEmpty(strings.TrimSpace(out)))
		c.fix("The host firewall drops traffic from the docker bridge to the panel. ufw: ufw allow in on docker0 (or the managed bridge) to any port "+port+". firewalld: add the bridge interface to the trusted zone. Runtime agents, consoles, and modules need this path.", docsTroubleshooting)
	default:
		c.fail("Containers cannot reach %s: %s", agentURL, orEmpty(strings.TrimSpace(out)))
		c.fix("Set docker.agent_url (DISCOPANEL_DOCKER_AGENT_URL) to an address that works from inside a container, such as http://<lan-ip>:"+r.cfg.Server.Port+".", docsConfiguration)
	}
}
