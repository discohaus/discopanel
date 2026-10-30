package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

const (
	keepAliveEvery = 10 * time.Second
	burstDelay     = 3 * time.Second
	settleAfter    = 6 * time.Second
	sessionLimit   = 5 * time.Minute
)

// Serves the hub codec to real clients for manual checks
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 25599, "listen port")
	worlds := fs.Int("worlds", 3, "pretend worlds on the plaza")
	once := fs.Bool("once", false, "exit after the first session, nonzero when it failed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l, err := net.Listen("tcp", ":"+strconv.Itoa(*port))
	if err != nil {
		return err
	}
	defer l.Close()
	logf("serving hub codec on %s, point a client at it", l.Addr())
	return serveHub(context.Background(), l, *worlds, *once)
}

// Accepts clients until the context ends or one session finishes with once
func serveHub(ctx context.Context, l net.Listener, worlds int, once bool) error {
	h := newHubServer(worlds)
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		outcome, handled := h.handle(conn)
		if once && handled {
			return outcome
		}
	}
}

// Stand in plaza with cached bakes per protocol
type hubServer struct {
	targets []family.Target
	mu      sync.Mutex
	grids   map[int32]*family.Grid
	bundles map[int32][][]byte
}

func newHubServer(worlds int) *hubServer {
	h := &hubServer{grids: map[int32]*family.Grid{}, bundles: map[int32][][]byte{}}
	moods := []func(t *family.Target){
		func(t *family.Target) { t.Running = true; t.Online = 2 },
		func(t *family.Target) { t.Wakeable = true },
		func(t *family.Target) { t.Waking = true },
		func(t *family.Target) {},
	}
	for i := range worlds {
		t := family.Target{
			ID:       fmt.Sprintf("world-%d", i+1),
			Name:     fmt.Sprintf("World %d", i+1),
			Hostname: fmt.Sprintf("world%d.example", i+1),
			Port:     25565,
			Version:  "26.3",
		}
		moods[i%len(moods)](&t)
		h.targets = append(h.targets, t)
	}
	family.SortTargets(h.targets)
	return h
}

func (h *hubServer) bake(codec family.Codec, protocol int32) ([][]byte, *family.Grid, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if grid, ok := h.grids[protocol]; ok {
		return h.bundles[protocol], grid, nil
	}
	grid := family.BuildHub(h.targets, protocol)
	bundle, err := codec.BakeChunks(grid, protocol)
	if err != nil {
		return nil, nil, err
	}
	h.grids[protocol] = grid
	h.bundles[protocol] = bundle
	return bundle, grid, nil
}

// Answers one connection, reporting whether a login session ran
func (h *hubServer) handle(conn net.Conn) (error, bool) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(conn)
	hs, err := mcproto.ReadHandshakePacket(br)
	if err != nil {
		logf("%s: bad handshake: %v", conn.RemoteAddr(), err)
		return nil, false
	}
	protocol := int32(hs.ProtocolVersion)
	if hs.NextState == mcproto.NextStateStatus {
		h.status(conn, br, protocol)
		return nil, false
	}
	login, err := mcproto.ReadLoginStart(br)
	if err != nil {
		logf("%s: bad login start: %v", conn.RemoteAddr(), err)
		return nil, false
	}
	codec := family.Lookup(protocol)
	if codec == nil {
		name, _ := mcproto.NewestVersionForProtocol(protocol)
		logf("%s: %s uses protocol %d which has no codec", conn.RemoteAddr(), login.Name, protocol)
		mcproto.WriteLoginDisconnect(conn, fmt.Sprintf("no hub codec for protocol %d %s", protocol, name))
		return fmt.Errorf("protocol %d has no codec", protocol), true
	}
	logf("%s: %s logging in with protocol %d", conn.RemoteAddr(), login.Name, protocol)
	return h.session(conn, br, codec, protocol, login.Name), true
}

// Status answer echoing the client protocol as compatible
func (h *hubServer) status(conn net.Conn, br *bufio.Reader, protocol int32) {
	if _, err := packet.ReadFrame(br); err != nil {
		return
	}
	status := map[string]any{
		"version":     map[string]any{"name": "mcprotogen", "protocol": protocol},
		"players":     map[string]any{"max": 8, "online": 0},
		"description": map[string]any{"text": "mcprotogen hub"},
	}
	raw, _ := json.Marshal(status)
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0)
	packet.WriteString(&body, string(raw))
	packet.WriteFrame(conn, body.Bytes())
	ping, err := packet.ReadFrame(br)
	if err != nil {
		return
	}
	packet.WriteFrame(conn, ping)
}

// Runs one joined client through every encoder the hub uses
func (h *hubServer) session(conn net.Conn, br *bufio.Reader, codec family.Codec, protocol int32, name string) error {
	bundle, grid, err := h.bake(codec, protocol)
	if err != nil {
		return fmt.Errorf("bake failed: %w", err)
	}
	offset := codec.YOffset(protocol)
	spawn := family.Pos{X: grid.SpawnX, Y: grid.SpawnY + float64(offset), Z: grid.SpawnZ, Yaw: grid.SpawnYaw, OnGround: true}
	join := family.JoinData{
		Profile:    family.PlayerEntry{UUID: mojang.OfflineUUID(name), Name: name},
		EntityID:   1,
		Spawn:      spawn,
		ViewChunks: bundle,
		SpawnBlock: [3]int{int(spawn.X), int(spawn.Y), int(spawn.Z)},
		RefuseNote: "mcprotogen refuses declared clients",
		FailNote:   "mcprotogen join failed",
	}
	started := time.Now()
	sess, err := codec.NewSession(br, conn, protocol, join)
	if err != nil {
		return fmt.Errorf("join failed after %s: %w", time.Since(started).Round(time.Millisecond), err)
	}
	conn.SetDeadline(time.Now().Add(sessionLimit))
	logf("%s joined in %s", name, time.Since(started).Round(time.Millisecond))

	actions := make(chan family.Action, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			frame, err := packet.ReadFrame(br)
			if err != nil {
				readErr <- err
				return
			}
			act, err := sess.Decode(frame)
			if err != nil {
				readErr <- err
				return
			}
			actions <- act
		}
	}()

	keepAlive := time.NewTicker(keepAliveEvery)
	defer keepAlive.Stop()
	burst := time.After(burstDelay)
	var settle <-chan time.Time
	moved, keepAlives := 0, 0
	burstDone := false
	for {
		select {
		case act := <-actions:
			switch a := act.(type) {
			case family.ActMove:
				moved++
				if moved == 1 {
					logf("%s is moving, the world rendered", name)
				}
			case family.ActKeepAlive:
				keepAlives++
			case family.ActChat:
				logf("%s says %q", name, a.Text)
				if err := sess.Encode(family.EvChat{Text: "§7echo: " + a.Text}); err != nil {
					return err
				}
			}
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				err = errors.New("client closed the connection")
			}
			return fmt.Errorf("%s left after %s: %w (moved %d, keepalives %d, burst done %v)", name, time.Since(started).Round(time.Millisecond), err, moved, keepAlives, burstDone)
		case <-keepAlive.C:
			if err := sess.KeepAlive(time.Now().UnixMilli()); err != nil {
				return err
			}
		case <-burst:
			if err := h.burst(sess, grid, spawn, offset); err != nil {
				return fmt.Errorf("event burst failed: %w", err)
			}
			burstDone = true
			settle = time.After(settleAfter)
		case <-settle:
			if moved == 0 {
				return fmt.Errorf("%s never moved, the client did not finish loading", name)
			}
			logf("%s survived every encoder, disconnecting (moved %d, keepalives %d)", name, moved, keepAlives)
			sess.Disconnect("mcprotogen verified this protocol")
			return nil
		}
	}
}

// Fires every event the lobby can emit at one client
func (h *hubServer) burst(sess family.Session, grid *family.Grid, spawn family.Pos, offset int) error {
	other := family.PlayerEntry{UUID: mojang.OfflineUUID("mcprotogen-buddy"), Name: "buddy"}
	pos := spawn
	pos.X += 2
	steps := []struct {
		label string
		ev    family.Event
	}{
		{"player add", family.EvPlayerAdd{Entry: other}},
		{"spawn player", family.EvSpawnPlayer{EntityID: 2, UUID: other.UUID, Pos: pos}},
		{"entity move", family.EvEntityMove{EntityID: 2, Pos: family.Pos{X: pos.X + 1, Y: pos.Y, Z: pos.Z + 1, Yaw: 90, OnGround: true}}},
		{"chat", family.EvChat{Text: "§ahello from mcprotogen"}},
		{"block change", family.EvBlockChange{X: 1, Y: int(spawn.Y) - 1, Z: 1, Block: "lime_stained_glass"}},
		{"sign text", signEvent(grid, offset)},
		{"beacon init", beaconEvent(grid, offset)},
		{"teleport self", family.EvTeleportSelf{Pos: spawn}},
		{"entity remove", family.EvEntityRemove{IDs: []int32{2}}},
		{"player remove", family.EvPlayerRemove{UUID: other.UUID}},
	}
	for _, s := range steps {
		if s.ev == nil {
			continue
		}
		if err := sess.Encode(s.ev); err != nil {
			return fmt.Errorf("%s: %w", s.label, err)
		}
		logf("sent %s", s.label)
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func signEvent(grid *family.Grid, offset int) family.Event {
	if len(grid.Signs) == 0 {
		return nil
	}
	s := grid.Signs[0]
	return family.EvSignText{X: s.X, Y: s.Y + offset, Z: s.Z, Lines: [4]string{"mcprotogen", "sign", "update", "ok"}}
}

func beaconEvent(grid *family.Grid, offset int) family.Event {
	for pos, name := range grid.Blocks() {
		if base, _ := family.SplitState(name); base == "beacon" {
			return family.EvBeaconInit{X: pos[0], Y: pos[1] + offset, Z: pos[2]}
		}
	}
	return nil
}
