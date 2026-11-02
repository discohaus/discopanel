package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Gate double mirroring the lifecycle slot rules
type fakeGate struct {
	mu       sync.Mutex
	paused   map[string]bool
	starting map[string]bool
	startErr error

	asks   atomic.Int32 // StartServer calls
	starts atomic.Int32 // starts that claimed the slot
	wakes  atomic.Int32
}

func newFakeGate() *fakeGate {
	return &fakeGate{paused: map[string]bool{}, starting: map[string]bool{}}
}

func (g *fakeGate) SleepingInfo(id string) (*SleepingServer, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused[id] {
		return nil, false
	}
	return &SleepingServer{Motd: "§bsleeping\n§r§7join to wake it up", MaxPlayers: 12}, true
}

func (g *fakeGate) WakeServer(_ context.Context, id string) error {
	g.wakes.Add(1)
	g.mu.Lock()
	g.paused[id] = false
	g.mu.Unlock()
	return nil
}

// Only the first ask claims, later asks ride along
func (g *fakeGate) StartServer(id string) error {
	g.asks.Add(1)
	if g.startErr != nil {
		return g.startErr
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.starting[id] {
		g.starting[id] = true
		g.starts.Add(1)
	}
	return nil
}

// Stopped server route the way desiredRoute builds it
func wakeRoute(wakeable bool) Route {
	version, _ := mcproto.NewestVersionForProtocol(772)
	wake := wakeable
	return Route{
		ServerID:   "srv-lazy",
		OwnerKind:  OwnerServer,
		OwnerID:    "srv-lazy",
		Protocol:   v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:   "lazy.example.com",
		State:      v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE,
		Wakeable:   wakeable,
		Motd:       offlineMOTD(&v1.ServerProperties{EnableWakeOnConnect: &wake}, wakeable),
		MaxPlayers: 12,
		Favicon:    "data:image/png;base64,AAAA",
		McVersion:  version,
		McProtocol: 772,
	}
}

// Lobby fleet entry for the stopped wakeable server
func wakeTarget() family.Target {
	version, _ := mcproto.NewestVersionForProtocol(772)
	return family.Target{
		ID: "srv-lazy", Name: "lazy", Hostname: "lazy.example.com",
		Port: 25565, Version: version, Protocol: 772, Wakeable: true,
	}
}

// Plain socket, no lobby, with a gate wired
func gatedSocket(t *testing.T, gate ServerGate, routes ...Route) *ListenerSocket {
	t.Helper()
	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New(), Gate: gate})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes(routes)
	return sock
}

// Flattens a decoded chat component into plain text
func componentPlain(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case map[string]any:
		out, _ := c["text"].(string)
		if extras, ok := c["extra"].([]any); ok {
			for _, e := range extras {
				out += componentPlain(e)
			}
		}
		return out
	case []any:
		out := ""
		for _, e := range c {
			out += componentPlain(e)
		}
		return out
	}
	return ""
}

// Logs in and returns the login disconnect text
func loginKickText(t *testing.T, addr, hostname string) string {
	t.Helper()
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))
	if err := mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   hostname,
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var start bytes.Buffer
	mcproto.WriteVarInt(&start, 0x00)
	packet.WriteString(&start, "Steve")
	packet.WriteUUID(&start, [16]byte{9})
	if err := packet.WriteFrame(client, start.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	br := bufio.NewReader(client)
	frame, err := packet.ReadFrame(br)
	if err != nil {
		t.Fatalf("disconnect read failed %v", err)
	}
	r := bytes.NewReader(frame)
	if pid, err := mcproto.ReadVarInt(r); err != nil || pid != 0x00 {
		t.Fatalf("expected login disconnect, got id %d err %v", pid, err)
	}
	reason, err := packet.ReadString(r)
	if err != nil {
		t.Fatalf("reason unreadable %v", err)
	}
	var component any
	if err := json.Unmarshal([]byte(reason), &component); err != nil {
		t.Fatalf("reason json invalid %v", err)
	}
	return componentPlain(component)
}

// Polls until the lobby holds n waiters for the lazy server
func waitHeld(t *testing.T, h *HubRuntime, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.WaitingByServer()["srv-lazy"] != n {
		if time.Now().After(deadline) {
			t.Fatalf("lobby waiting = %d, want %d", h.WaitingByServer()["srv-lazy"], n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Status sample lines as one string
func sampleText(status map[string]any) string {
	players, _ := status["players"].(map[string]any)
	sample, _ := players["sample"].([]any)
	var lines []string
	for _, s := range sample {
		if m, ok := s.(map[string]any); ok {
			lines = append(lines, fmt.Sprint(m["name"]))
		}
	}
	return strings.Join(lines, "\n")
}

// Stopped wakeable servers answer the list without waking
func TestWakeStatusStoppedServer(t *testing.T) {
	cases := []struct {
		name     string
		wakeable bool
		motd     string
		sample   string
	}{
		{"wakeable", true, "join to press play", "join to start it up"},
		{"offline status only", false, "the server is stopped", "ask the owner to start it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newFakeGate()
			sock := gatedSocket(t, gate, wakeRoute(tc.wakeable))

			status := statusQuery(t, sock, "lazy.example.com")
			version, _ := status["version"].(map[string]any)
			if version["name"] != "offline" || version["protocol"] != float64(767) {
				t.Fatalf("version = %v, want offline echoing the client protocol", version)
			}
			if desc := componentPlain(status["description"]); !strings.Contains(desc, tc.motd) {
				t.Fatalf("motd = %q, want %q", desc, tc.motd)
			}
			players, _ := status["players"].(map[string]any)
			if players["max"] != float64(12) || players["online"] != float64(0) {
				t.Fatalf("players = %v, want 0/12", players)
			}
			if got := sampleText(status); !strings.Contains(got, tc.sample) {
				t.Fatalf("sample = %q, want %q", got, tc.sample)
			}
			if status["favicon"] != "data:image/png;base64,AAAA" {
				t.Fatalf("favicon = %v, want the server icon", status["favicon"])
			}
			if gate.asks.Load() != 0 {
				t.Fatal("status pings must never start the server")
			}
		})
	}
}

// Paused servers answer as sleeping and never touch the frozen backend
func TestWakeStatusPausedServer(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	t.Cleanup(func() { backendLn.Close() })
	var dialed atomic.Bool
	go func() {
		for {
			conn, err := backendLn.Accept()
			if err != nil {
				return
			}
			dialed.Store(true)
			conn.Close()
		}
	}()

	gate := newFakeGate()
	gate.paused["srv-lazy"] = true
	route := wakeRoute(true)
	route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE
	route.BackendHost = "127.0.0.1"
	route.BackendPort = backendLn.Addr().(*net.TCPAddr).Port
	sock := gatedSocket(t, gate, route)

	status := statusQuery(t, sock, "lazy.example.com")
	version, _ := status["version"].(map[string]any)
	if version["name"] != "zZz" {
		t.Fatalf("version = %v, want the sleeping card", version)
	}
	if got := sampleText(status); !strings.Contains(got, "this server is sleeping") {
		t.Fatalf("sample = %q, want the sleeping hint", got)
	}
	if gate.wakes.Load() != 0 || dialed.Load() {
		t.Fatal("status pings must not wake or dial a paused server")
	}
}

// A login unpauses first, then relays to the backend
func TestWakeLoginUnpausesThenRelays(t *testing.T) {
	sock, backendLn := mcTestSocket(t, false)
	gate := newFakeGate()
	gate.paused["srv-mc"] = true
	sock.SetGate(gate)

	data := roundTripMC(t, sock, backendLn, mcLoginBytes(t), false)
	assertRewrittenLogin(t, data, backendLn)
	if gate.wakes.Load() != 1 {
		t.Fatalf("wakes = %d, want 1", gate.wakes.Load())
	}
	if st := sock.StatsSnapshots()["srv-mc"]; st == nil || st.Wakes != 1 {
		t.Fatalf("route stats must count the wake: %+v", st)
	}
}

// Without the lobby a cold start login gets the rejoin screen
func TestWakeLoginColdStartWithoutLobby(t *testing.T) {
	gate := newFakeGate()
	sock := gatedSocket(t, gate, wakeRoute(true))

	text := loginKickText(t, sock.listener.Addr().String(), "lazy.example.com")
	if !strings.Contains(text, "the server is starting now") || !strings.Contains(text, "join again") {
		t.Fatalf("kick = %q, want the starting screen", text)
	}
	if gate.starts.Load() != 1 {
		t.Fatalf("starts = %d, want 1", gate.starts.Load())
	}
}

// Start refusals pick the right screen
func TestWakeLoginStartRefused(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"busy", fmt.Errorf("%w: stop in progress", ErrServerBusy), "the server is busy right now"},
		{"failed", errors.New("boom"), "the server couldn't start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newFakeGate()
			gate.startErr = tc.err
			sock := gatedSocket(t, gate, wakeRoute(true))
			if text := loginKickText(t, sock.listener.Addr().String(), "lazy.example.com"); !strings.Contains(text, tc.want) {
				t.Fatalf("kick = %q, want %q", text, tc.want)
			}
		})
	}
}

// Non wakeable stopped servers never start on join
func TestWakeLoginNotWakeable(t *testing.T) {
	gate := newFakeGate()
	sock := gatedSocket(t, gate, wakeRoute(false))
	if text := loginKickText(t, sock.listener.Addr().String(), "lazy.example.com"); !strings.Contains(text, "this server is offline") {
		t.Fatalf("kick = %q, want the offline screen", text)
	}
	if gate.asks.Load() != 0 {
		t.Fatal("non wakeable server must not start")
	}
}

// Concurrent joins start once, wait in the lobby, then hop and relay
func TestWakeConcurrentJoinsHoldThenTransfer(t *testing.T) {
	gate := newFakeGate()
	sock, h, intents := hubSocket(t, false, []family.Target{wakeTarget()}, []Route{wakeRoute(true)})
	sock.SetGate(gate)
	h.SetGate(gate)
	addr := sock.listener.Addr().String()
	// Warm bake keeps slow race builds inside the login deadline
	if _, _, err := h.bundleFor(family.Lookup(772), 772); err != nil {
		t.Fatalf("bake failed %v", err)
	}

	const players = 5
	conns := make([]net.Conn, players)
	var wg sync.WaitGroup
	for i := range players {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conns[i], _ = hubClient(t, addr, "lazy.example.com", fmt.Sprintf("Player%d", i), 772)
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if gate.starts.Load() != 1 {
		t.Fatalf("starts = %d, want exactly one", gate.starts.Load())
	}
	if gate.asks.Load() != players {
		t.Fatalf("start asks = %d, want one per join", gate.asks.Load())
	}
	waitHeld(t, h, players)

	// Booting keeps everyone parked
	booting := wakeTarget()
	booting.Wakeable, booting.Waking = false, true
	h.SetTargets([]family.Target{booting})
	h.tick()
	if got := h.WaitingByServer()["srv-lazy"]; got != players {
		t.Fatalf("booting world released waiters, %d left", got)
	}

	// Backend comes up, the next tick hops every waiter
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	t.Cleanup(func() { backendLn.Close() })
	ready := wakeTarget()
	ready.Wakeable, ready.Running = false, true
	ready.Addr = backendLn.Addr().String()
	h.SetTargets([]family.Target{ready})
	sock.UpsertServerRoute(Route{
		ServerID:    "srv-lazy",
		OwnerKind:   OwnerServer,
		OwnerID:     "srv-lazy",
		Hostname:    "lazy.example.com",
		State:       v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
		BackendHost: "127.0.0.1",
		BackendPort: backendLn.Addr().(*net.TCPAddr).Port,
	})

	// Readiness probes land on the backend first
	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			conn, err := backendLn.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	h.tick()

	ids := family.ModernIDsFor(772)
	for i, conn := range conns {
		buf := readUntil(t, conn, ids.Transfer)
		host, err := packet.ReadString(buf)
		if err != nil || host != "lazy.example.com" {
			t.Fatalf("player %d transfer host = %q err %v", i, host, err)
		}
	}
	if _, ok := intents.Claim("Player0"); !ok {
		t.Fatal("hop must leave a rejoin claim")
	}

	// Transferred rejoin relays straight to the live backend
	var rejoin bytes.Buffer
	mcproto.WriteHandshakePacket(&rejoin, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "lazy.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateTransfer,
	})
	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("rejoin dial failed %v", err)
	}
	defer client.Close()
	client.Write(rejoin.Bytes())
	client.Write([]byte("login-start-payload"))
	client.(*net.TCPConn).CloseWrite()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case conn := <-accepted:
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			data, _ := io.ReadAll(conn)
			conn.Close()
			if len(data) == 0 {
				continue // readiness probe
			}
			hs, err := mcproto.ReadHandshakePacket(bytes.NewReader(data))
			if err != nil || hs.NextState != mcproto.NextStateTransfer {
				t.Fatalf("backend saw %v err %v, want the transfer handshake", hs, err)
			}
			if !bytes.HasSuffix(data, []byte("login-start-payload")) {
				t.Fatalf("login bytes lost in relay: %q", data)
			}
			return
		case <-deadline:
			t.Fatal("rejoin never reached the backend")
		}
	}
}

// Waits end when the start dies or the server goes away
func TestWakeHoldExpires(t *testing.T) {
	cases := []struct {
		name    string
		targets func() []family.Target
		age     time.Duration
		want    string
	}{
		{"start failed", func() []family.Target { return []family.Target{wakeTarget()} }, hubWakeGrace + time.Second, "trouble starting"},
		{"server deleted", func() []family.Target { return nil }, 0, "that world is gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := newFakeGate()
			sock, h, _ := hubSocket(t, false, []family.Target{wakeTarget()}, []Route{wakeRoute(true)})
			sock.SetGate(gate)
			h.SetGate(gate)

			conn, _ := hubClient(t, sock.listener.Addr().String(), "lazy.example.com", "Steve", 772)
			waitHeld(t, h, 1)

			// Fresh waits on a not yet booting world survive the grace
			h.tick()
			if h.WaitingByServer()["srv-lazy"] != 1 {
				t.Fatal("wait dropped inside the grace window")
			}

			h.mu.Lock()
			for _, m := range h.members {
				m.pendingAt = m.pendingAt.Add(-tc.age)
			}
			h.mu.Unlock()
			h.SetTargets(tc.targets())
			h.tick()
			if n := h.WaitingByServer()["srv-lazy"]; n != 0 {
				t.Fatalf("wait still held, %d waiting", n)
			}

			ids := family.ModernIDsFor(772)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				buf := readUntil(t, conn, ids.SystemChat)
				component, err := packet.ReadNetworkNBT(buf)
				if err != nil {
					continue
				}
				if strings.Contains(packet.ComponentText(component), tc.want) {
					return
				}
			}
			t.Fatalf("member never heard %q", tc.want)
		})
	}
}

// Joins during boot wait in the lobby instead of timing out
func TestWakeStartingBackendHeldInLobby(t *testing.T) {
	// Closed port stands in for a booting java process
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed %v", err)
	}
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()

	route := wakeRoute(true)
	route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING
	route.BackendHost = "127.0.0.1"
	route.BackendPort = port
	booting := wakeTarget()
	booting.Wakeable, booting.Waking = false, true

	sock, h, _ := hubSocket(t, false, []family.Target{booting}, []Route{route})
	conn, chunks := hubClient(t, sock.listener.Addr().String(), "lazy.example.com", "Steve", 772)
	if conn == nil || chunks == 0 {
		t.Fatal("booting join must land in the lobby")
	}
	waitHeld(t, h, 1)
}

// Desired routes keep stopped servers joinable only when asked
func TestDesiredRouteWakeOnConnect(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		status   v1.ServerStatus
		wake     *bool
		offline  *bool
		want     bool
		wakeable bool
	}{
		{"stopped plain", v1.ServerStatus_SERVER_STATUS_STOPPED, nil, nil, false, false},
		{"stopped wake", v1.ServerStatus_SERVER_STATUS_STOPPED, &yes, nil, true, true},
		{"stopped offline status", v1.ServerStatus_SERVER_STATUS_STOPPED, &no, &yes, true, false},
		{"errored wake", v1.ServerStatus_SERVER_STATUS_ERROR, &yes, nil, true, true},
		{"stopping wake", v1.ServerStatus_SERVER_STATUS_STOPPING, &yes, nil, true, true},
	}
	m, store := testManager(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("srv-%d", i)
			server := seedServer(t, store, id, func(s *v1.Server) {
				s.Status = tc.status
				s.MaxPlayers = 9
			})
			if err := store.CreateServerProperties(context.Background(), &v1.ServerProperties{
				Id: id, ServerId: id, EnableWakeOnConnect: tc.wake, EnableOfflineStatus: tc.offline,
			}); err != nil {
				t.Fatalf("properties create failed %v", err)
			}
			route, want, err := m.desiredRoute(context.Background(), server)
			if err != nil {
				t.Fatalf("desired route failed %v", err)
			}
			if want != tc.want {
				t.Fatalf("want route = %v, expected %v", want, tc.want)
			}
			if !want {
				return
			}
			if route.State != v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE || route.Wakeable != tc.wakeable {
				t.Fatalf("route = %v wakeable %v, want offline wakeable %v", route.State, route.Wakeable, tc.wakeable)
			}
			if route.BackendHost != "" || route.MaxPlayers != 9 || route.Favicon == "" {
				t.Fatalf("offline route shape wrong: %+v", route)
			}
		})
	}
}

// Versions the lobby can't host keep riding the dial retry during boot
func TestWakeStartingUnsupportedProtocolRetriesDial(t *testing.T) {
	// Port reserved now, backend only listens once java is up
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	route := wakeRoute(true)
	route.State = v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING
	route.BackendHost = "127.0.0.1"
	route.BackendPort = port
	route.McProtocol, route.McVersion = 0, ""
	booting := wakeTarget()
	booting.Wakeable, booting.Waking = false, true
	sock, h, _ := hubSocket(t, false, []family.Target{booting}, []Route{route})

	const oldProtocol = 404
	if family.Lookup(oldProtocol) != nil {
		t.Fatalf("protocol %d must be one the lobby can't speak", oldProtocol)
	}

	got := make(chan []byte, 1)
	go func() {
		time.Sleep(time.Second)
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
		if err != nil {
			got <- nil
			return
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, _ := io.ReadAll(conn)
		got <- data
	}()

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	var buf bytes.Buffer
	mcproto.WriteHandshakePacket(&buf, &mcproto.HandshakePacket{
		ProtocolVersion: oldProtocol,
		ServerAddress:   "lazy.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	buf.WriteString("login-start-payload")
	client.Write(buf.Bytes())
	client.(*net.TCPConn).CloseWrite()

	select {
	case data := <-got:
		hs, err := mcproto.ReadHandshakePacket(bytes.NewReader(data))
		if err != nil || int(hs.ProtocolVersion) != oldProtocol {
			t.Fatalf("backend saw %v err %v, want the old client's handshake", hs, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("unsupported client was never relayed once the backend came up")
	}
	if h.WaitingByServer()["srv-lazy"] != 0 {
		t.Fatal("unsupported client must not be parked in the lobby")
	}
}

// Portal picks refused as busy say so instead of reporting a failure
func TestWakeHubBusyStartMessage(t *testing.T) {
	gate := newFakeGate()
	gate.startErr = fmt.Errorf("%w: stop in progress", ErrServerBusy)
	sock, h, _ := hubSocket(t, false, []family.Target{wakeTarget()}, nil)
	sock.SetGate(gate)
	h.SetGate(gate)

	conn, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	sendChat(t, conn, 772, "lazy")

	ids := family.ModernIDsFor(772)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		buf := readUntil(t, conn, ids.SystemChat)
		component, err := packet.ReadNetworkNBT(buf)
		if err != nil {
			continue
		}
		text := packet.ComponentText(component)
		if strings.Contains(text, "trouble starting") {
			t.Fatalf("busy start reported as a failure: %q", text)
		}
		if strings.Contains(text, "that world is busy") {
			if gate.asks.Load() != 1 {
				t.Fatalf("start asks = %d, want 1", gate.asks.Load())
			}
			if n := h.WaitingByServer()["srv-lazy"]; n != 0 {
				t.Fatalf("busy start must release the wait, %d waiting", n)
			}
			return
		}
	}
	t.Fatal("busy message never arrived")
}
