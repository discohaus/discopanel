package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/mcdata"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

const (
	serverStartTimeout = 5 * time.Minute
	serverStopTimeout  = 90 * time.Second
	captureTimeout     = 2 * time.Minute
	captureName        = "mcprotogen"
)

// Everything the vanilla config phase hands a confirming client
type capture struct {
	FeatureFlags []string
	Registries   []mcdata.Registry
	Tags         []mcdata.TagRegistry
}

// Boots a throwaway vanilla server and captures its config phase
func captureVanilla(ctx context.Context, javaBin, jar, runDir string, r *reports, protocol int32, version string, logf func(string, ...any)) (*capture, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if err := writeServerFiles(runDir, port); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, javaBin, "-Xms512M", "-Xmx1536M", "-jar", jar, "--nogui")
	cmd.Dir = runDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("vanilla server: %w", err)
	}
	ready := make(chan error, 1)
	var recent []string
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		signalled := false
		for sc.Scan() {
			line := sc.Text()
			recent = append(recent, line)
			if len(recent) > 30 {
				recent = recent[1:]
			}
			if !signalled && strings.Contains(line, "Done (") {
				signalled = true
				ready <- nil
			}
		}
		if !signalled {
			ready <- fmt.Errorf("server exited before ready\n%s", strings.Join(recent, "\n"))
		}
	}()
	defer func() {
		io.WriteString(stdin, "stop\n")
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(serverStopTimeout):
			cancel()
			<-done
		}
	}()
	logf("vanilla %s starting on port %d", version, port)
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
	case <-time.After(serverStartTimeout):
		return nil, fmt.Errorf("vanilla server took over %s to start\n%s", serverStartTimeout, strings.Join(recent, "\n"))
	}
	logf("vanilla %s ready, capturing configuration", version)
	return captureConfig(port, r, protocol, version)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Offline flat world, no compression, nothing else running
func writeServerFiles(dir string, port int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	props := strings.Join([]string{
		"online-mode=false",
		"white-list=false",
		"enforce-whitelist=false",
		"network-compression-threshold=-1",
		"server-ip=127.0.0.1",
		"server-port=" + strconv.Itoa(port),
		"level-type=minecraft:flat",
		"generate-structures=false",
		"spawn-protection=0",
		"max-players=2",
		"view-distance=2",
		"simulation-distance=2",
		"enable-rcon=false",
		"enable-query=false",
		"sync-chunk-writes=false",
		"motd=mcprotogen",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("eula=true\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "server.properties"), []byte(props), 0o644)
}

// Packet ids the capture speaks, resolved from the reports
type captureIDs struct {
	loginFinished, loginDisconnect, loginHello, loginCompression int32
	loginAck                                                     int32
	cfgCustomPayload, cfgFeatures, cfgKnownPacks, cfgRegistry    int32
	cfgTags, cfgFinish, cfgKeepAlive, cfgPing, cfgDisconnect     int32
	cfgCookieRequest                                             int32
	cfgFinishAck, cfgKnownPacksSB, cfgKeepAliveSB, cfgPong       int32
	cfgCookieResponse                                            int32
}

// Wire ids of 1.20.5 through 1.21.1, jars without packet reports
var preReportIDs = captureIDs{
	loginFinished: 0x02, loginDisconnect: 0x00, loginHello: 0x01, loginCompression: 0x03,
	loginAck:         0x03,
	cfgCustomPayload: 0x01, cfgFeatures: 0x0c, cfgKnownPacks: 0x0e, cfgRegistry: 0x07,
	cfgTags: 0x0d, cfgFinish: 0x03, cfgKeepAlive: 0x04, cfgPing: 0x05, cfgDisconnect: 0x02,
	cfgCookieRequest: 0x00,
	cfgFinishAck:     0x03, cfgKnownPacksSB: 0x07, cfgKeepAliveSB: 0x04, cfgPong: 0x05,
	cfgCookieResponse: 0x01,
}

func (r *reports) captureIDs() (*captureIDs, error) {
	if len(r.packets) == 0 {
		ids := preReportIDs
		return &ids, nil
	}
	ids := &captureIDs{}
	need := func(dst *int32, state, dir string, names ...string) error {
		for _, name := range names {
			if id, ok := r.packets[state][dir]["minecraft:"+name]; ok {
				*dst = id
				return nil
			}
		}
		return fmt.Errorf("packets.json lacks %s/%s %s", state, dir, names[0])
	}
	steps := []error{
		need(&ids.loginFinished, "login", "clientbound", "login_finished", "game_profile"),
		need(&ids.loginDisconnect, "login", "clientbound", "login_disconnect"),
		need(&ids.loginHello, "login", "clientbound", "hello"),
		need(&ids.loginCompression, "login", "clientbound", "login_compression"),
		need(&ids.loginAck, "login", "serverbound", "login_acknowledged"),
		need(&ids.cfgCustomPayload, "configuration", "clientbound", "custom_payload"),
		need(&ids.cfgFeatures, "configuration", "clientbound", "update_enabled_features"),
		need(&ids.cfgKnownPacks, "configuration", "clientbound", "select_known_packs"),
		need(&ids.cfgRegistry, "configuration", "clientbound", "registry_data"),
		need(&ids.cfgTags, "configuration", "clientbound", "update_tags"),
		need(&ids.cfgFinish, "configuration", "clientbound", "finish_configuration"),
		need(&ids.cfgKeepAlive, "configuration", "clientbound", "keep_alive"),
		need(&ids.cfgPing, "configuration", "clientbound", "ping"),
		need(&ids.cfgDisconnect, "configuration", "clientbound", "disconnect"),
		need(&ids.cfgCookieRequest, "configuration", "clientbound", "cookie_request"),
		need(&ids.cfgFinishAck, "configuration", "serverbound", "finish_configuration"),
		need(&ids.cfgKnownPacksSB, "configuration", "serverbound", "select_known_packs"),
		need(&ids.cfgKeepAliveSB, "configuration", "serverbound", "keep_alive"),
		need(&ids.cfgPong, "configuration", "serverbound", "pong"),
		need(&ids.cfgCookieResponse, "configuration", "serverbound", "cookie_response"),
	}
	for _, err := range steps {
		if err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// Joins as a vanilla client and records the configuration phase
func captureConfig(port int, r *reports, protocol int32, version string) (*capture, error) {
	ids, err := r.captureIDs()
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(captureTimeout))

	var hs bytes.Buffer
	mcproto.WriteVarInt(&hs, 0)
	mcproto.WriteVarInt(&hs, mcproto.VarInt(protocol))
	packet.WriteString(&hs, "127.0.0.1")
	packet.WriteNum(&hs, uint16(port))
	mcproto.WriteVarInt(&hs, mcproto.NextStateLogin)
	if err := packet.WriteFrame(conn, hs.Bytes()); err != nil {
		return nil, err
	}
	var start bytes.Buffer
	mcproto.WriteVarInt(&start, 0)
	packet.WriteString(&start, captureName)
	packet.WriteUUID(&start, mojang.OfflineUUID(captureName))
	if err := packet.WriteFrame(conn, start.Bytes()); err != nil {
		return nil, err
	}

	br := bufio.NewReader(conn)
	for {
		pid, rd, err := readPacket(br)
		if err != nil {
			return nil, fmt.Errorf("login: %w", err)
		}
		if pid == ids.loginFinished {
			break
		}
		switch pid {
		case ids.loginDisconnect:
			reason, _ := packet.ReadString(rd)
			return nil, fmt.Errorf("server refused login: %s", reason)
		case ids.loginHello:
			return nil, fmt.Errorf("server demands encryption, online-mode is on")
		case ids.loginCompression:
			return nil, fmt.Errorf("server enabled compression, threshold setting was ignored")
		}
	}
	if err := sendPacket(conn, ids.loginAck, nil); err != nil {
		return nil, err
	}

	out := &capture{}
	for {
		pid, rd, err := readPacket(br)
		if err != nil {
			return nil, fmt.Errorf("configuration: %w", err)
		}
		switch pid {
		case ids.cfgFeatures:
			if out.FeatureFlags, err = readStrings(rd); err != nil {
				return nil, err
			}
		case ids.cfgKnownPacks:
			reply, confirmed, err := confirmKnownPacks(rd, version)
			if err != nil {
				return nil, err
			}
			if !confirmed {
				return nil, fmt.Errorf("server offered no minecraft:core %s pack", version)
			}
			if err := sendPacket(conn, ids.cfgKnownPacksSB, reply); err != nil {
				return nil, err
			}
		case ids.cfgRegistry:
			reg, err := readRegistry(rd)
			if err != nil {
				return nil, err
			}
			out.Registries = append(out.Registries, reg)
		case ids.cfgTags:
			if out.Tags, err = readTags(rd); err != nil {
				return nil, err
			}
		case ids.cfgKeepAlive:
			var id int64
			if err := packet.ReadNum(rd, &id); err != nil {
				return nil, err
			}
			var body bytes.Buffer
			packet.WriteNum(&body, id)
			if err := sendPacket(conn, ids.cfgKeepAliveSB, body.Bytes()); err != nil {
				return nil, err
			}
		case ids.cfgPing:
			var id int32
			if err := packet.ReadNum(rd, &id); err != nil {
				return nil, err
			}
			var body bytes.Buffer
			packet.WriteNum(&body, id)
			if err := sendPacket(conn, ids.cfgPong, body.Bytes()); err != nil {
				return nil, err
			}
		case ids.cfgCookieRequest:
			key, err := packet.ReadString(rd)
			if err != nil {
				return nil, err
			}
			var body bytes.Buffer
			packet.WriteString(&body, key)
			packet.WriteBool(&body, false)
			if err := sendPacket(conn, ids.cfgCookieResponse, body.Bytes()); err != nil {
				return nil, err
			}
		case ids.cfgDisconnect:
			return nil, fmt.Errorf("server disconnected the capture client during configuration")
		case ids.cfgFinish:
			if err := sendPacket(conn, ids.cfgFinishAck, nil); err != nil {
				return nil, err
			}
			if len(out.Registries) == 0 {
				return nil, fmt.Errorf("server sent no registry data")
			}
			if len(out.Tags) == 0 {
				return nil, fmt.Errorf("server sent no tags")
			}
			return out, nil
		}
	}
}

func readPacket(br *bufio.Reader) (int32, *bytes.Reader, error) {
	frame, err := packet.ReadFrame(br)
	if err != nil {
		return 0, nil, err
	}
	rd := bytes.NewReader(frame)
	pid, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return 0, nil, err
	}
	return int32(pid), rd, nil
}

func sendPacket(w io.Writer, id int32, body []byte) error {
	var buf bytes.Buffer
	mcproto.WriteVarInt(&buf, mcproto.VarInt(id))
	buf.Write(body)
	return packet.WriteFrame(w, buf.Bytes())
}

func readStrings(rd *bytes.Reader) ([]string, error) {
	n, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for range n {
		s, err := packet.ReadString(rd)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Echoes the offered packs, reporting whether the core pack matched
func confirmKnownPacks(rd *bytes.Reader, version string) ([]byte, bool, error) {
	n, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return nil, false, err
	}
	var reply bytes.Buffer
	mcproto.WriteVarInt(&reply, n)
	confirmed := false
	for range n {
		ns, err := packet.ReadString(rd)
		if err != nil {
			return nil, false, err
		}
		id, err := packet.ReadString(rd)
		if err != nil {
			return nil, false, err
		}
		ver, err := packet.ReadString(rd)
		if err != nil {
			return nil, false, err
		}
		if ns == "minecraft" && id == "core" && ver == version {
			confirmed = true
		}
		packet.WriteString(&reply, ns)
		packet.WriteString(&reply, id)
		packet.WriteString(&reply, ver)
	}
	return reply.Bytes(), confirmed, nil
}

// One registry data packet, entries kept in wire order
func readRegistry(rd *bytes.Reader) (mcdata.Registry, error) {
	name, err := packet.ReadString(rd)
	if err != nil {
		return mcdata.Registry{}, err
	}
	n, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return mcdata.Registry{}, err
	}
	reg := mcdata.Registry{Name: name}
	for range n {
		entry, err := packet.ReadString(rd)
		if err != nil {
			return mcdata.Registry{}, err
		}
		hasData, err := packet.ReadBool(rd)
		if err != nil {
			return mcdata.Registry{}, err
		}
		if hasData {
			return mcdata.Registry{}, fmt.Errorf("registry %s sent %s inline, the core pack was not confirmed", name, entry)
		}
		reg.Entries = append(reg.Entries, entry)
	}
	return reg, nil
}

// The update tags payload in wire order
func readTags(rd *bytes.Reader) ([]mcdata.TagRegistry, error) {
	n, err := mcproto.ReadVarInt(rd)
	if err != nil {
		return nil, err
	}
	out := make([]mcdata.TagRegistry, 0, n)
	for range n {
		name, err := packet.ReadString(rd)
		if err != nil {
			return nil, err
		}
		reg := mcdata.TagRegistry{Name: name}
		count, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return nil, err
		}
		for range count {
			tag, err := packet.ReadString(rd)
			if err != nil {
				return nil, err
			}
			size, err := mcproto.ReadVarInt(rd)
			if err != nil {
				return nil, err
			}
			t := mcdata.Tag{Name: tag, IDs: make([]int32, 0, size)}
			for range size {
				id, err := mcproto.ReadVarInt(rd)
				if err != nil {
					return nil, err
				}
				t.IDs = append(t.IDs, int32(id))
			}
			reg.Tags = append(reg.Tags, t)
		}
		out = append(out, reg)
	}
	if rd.Len() != 0 {
		return nil, fmt.Errorf("update tags left %d trailing bytes", rd.Len())
	}
	return out, nil
}
