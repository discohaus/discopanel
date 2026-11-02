package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Unconnected ping as a bedrock client sends it
func raknetPingPacket(id byte, sentAt uint64) []byte {
	b := []byte{id}
	b = binary.BigEndian.AppendUint64(b, sentAt)
	b = append(b, raknetMagic...)
	return binary.BigEndian.AppendUint64(b, 0xc0ffee)
}

// Open connection request 1 padded to an mtu probe
func raknetJoinPacket(mtu int) []byte {
	b := []byte{raknetOpenRequest1}
	b = append(b, raknetMagic...)
	b = append(b, 11)
	return append(b, make([]byte, mtu-len(b))...)
}

func TestClassifyRakNet(t *testing.T) {
	badMagic := raknetJoinPacket(64)
	badMagic[3] ^= 0xff
	badPing := raknetPingPacket(raknetUnconnectedPing, 1)
	badPing[12] ^= 0xff
	openReq2 := raknetJoinPacket(64)
	openReq2[0] = 0x07

	cases := []struct {
		name string
		b    []byte
		want raknetKind
	}{
		{"empty", nil, raknetOther},
		{"join", raknetJoinPacket(1400), raknetJoin},
		{"join at the floor", raknetJoinPacket(raknetJoinMinLen), raknetJoin},
		{"join under the floor", raknetJoinPacket(raknetJoinMinLen - 1), raknetOther},
		{"join smallest mtu probe", raknetJoinPacket(576 - 48), raknetJoin},
		{"join truncated", raknetJoinPacket(18)[:17], raknetOther},
		{"join bad magic", badMagic, raknetOther},
		{"open request 2", openReq2, raknetOther},
		{"ping", raknetPingPacket(raknetUnconnectedPing, 7), raknetPing},
		{"ping open connections", raknetPingPacket(raknetUnconnectedPingOpen, 7), raknetPing},
		{"ping truncated", raknetPingPacket(raknetUnconnectedPing, 7)[:32], raknetOther},
		{"ping bad magic", badPing, raknetOther},
		{"connected frame", append([]byte{0x84}, raknetMagic...), raknetOther},
		{"java handshake", []byte{0x10, 0x00, 0xf8, 0x05}, raknetOther},
		{"garbage", bytes.Repeat([]byte{0x05}, 1400), raknetOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRakNet(tc.b); got != tc.want {
				t.Fatalf("classifyRakNet = %v, want %v", got, tc.want)
			}
		})
	}
}

// Splits a pong into echoed time, guid and status fields
func parsePong(t *testing.T, b []byte) (sentAt uint64, guid int64, fields []string) {
	t.Helper()
	if len(b) < 35 || b[0] != raknetUnconnectedPong {
		t.Fatalf("not a pong: % x", b)
	}
	if !bytes.Equal(b[17:33], raknetMagic) {
		t.Fatal("pong lacks the offline magic")
	}
	n := int(binary.BigEndian.Uint16(b[33:35]))
	if len(b) != 35+n {
		t.Fatalf("pong length %d, status says %d", len(b), n)
	}
	status := string(b[35:])
	if !strings.HasPrefix(status, "MCPE;") || !strings.HasSuffix(status, ";") {
		t.Fatalf("status shape wrong: %q", status)
	}
	return binary.BigEndian.Uint64(b[1:9]), int64(binary.BigEndian.Uint64(b[9:17])), strings.Split(strings.TrimSuffix(status, ";"), ";")
}

func TestBedrockPong(t *testing.T) {
	pong := bedrockPong(raknetPingPacket(raknetUnconnectedPing, 424242), 99, "Lazy;World\n§7offline", 20, 19132)
	sentAt, guid, fields := parsePong(t, pong)
	if sentAt != 424242 || guid != 99 {
		t.Fatalf("pong echoed %d guid %d", sentAt, guid)
	}
	if len(fields) != 12 {
		t.Fatalf("status has %d fields: %q", len(fields), fields)
	}
	if fields[1] != "Lazy,World" || fields[7] != "§7offline" {
		t.Fatalf("motd lines wrong: %q / %q", fields[1], fields[7])
	}
	if fields[4] != "0" || fields[5] != "20" || fields[6] != "99" || fields[10] != "19132" {
		t.Fatalf("player, guid or port fields wrong: %q", fields)
	}
}

func TestBedrockWakeAction(t *testing.T) {
	cases := []struct {
		status   v1.ServerStatus
		wake     bool
		sleeping bool
		want     bedrockAction
	}{
		{v1.ServerStatus_SERVER_STATUS_STOPPED, true, false, bedrockStart},
		{v1.ServerStatus_SERVER_STATUS_ERROR, true, false, bedrockStart},
		{v1.ServerStatus_SERVER_STATUS_STOPPING, true, false, bedrockStart},
		{v1.ServerStatus_SERVER_STATUS_PAUSED, true, true, bedrockResume},
		{v1.ServerStatus_SERVER_STATUS_RUNNING, true, false, bedrockNone},
		{v1.ServerStatus_SERVER_STATUS_STARTING, true, false, bedrockNone},
		{v1.ServerStatus_SERVER_STATUS_PROVISIONING, true, false, bedrockNone},
		{v1.ServerStatus_SERVER_STATUS_STOPPED, false, false, bedrockNone},
		{v1.ServerStatus_SERVER_STATUS_PAUSED, false, true, bedrockNone},
	}
	for _, tc := range cases {
		if got := bedrockWakeAction(tc.status, tc.wake, tc.sleeping); got != tc.want {
			t.Errorf("bedrockWakeAction(%v, wake=%v, sleeping=%v) = %v, want %v", tc.status, tc.wake, tc.sleeping, got, tc.want)
		}
	}
}

func TestWakeLimiterSingleFlightAndCooldown(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newWakeLimiter(5 * time.Second)
	l.now = func() time.Time { return now }

	if !l.begin("a") {
		t.Fatal("first attempt must run")
	}
	if l.begin("a") {
		t.Fatal("second attempt while one runs must wait")
	}
	if !l.begin("b") {
		t.Fatal("other servers are independent")
	}
	l.end("a")
	now = now.Add(4 * time.Second)
	if l.begin("a") {
		t.Fatal("attempt inside the cooldown must be dropped")
	}
	now = now.Add(time.Second)
	if !l.begin("a") {
		t.Fatal("attempt after the cooldown must run")
	}

	// Long attempts still hold past the window
	now = now.Add(time.Minute)
	if l.begin("a") {
		t.Fatal("in flight attempt must block even after the window")
	}
}

// Join bursts reach the gate once per window
func TestBedrockWakeBurstStartsOnce(t *testing.T) {
	m, store := testManager(t)
	gate := newFakeGate()
	m.SetServerGate(gate)
	seedServer(t, store, "srv-bedrock", nil)
	wakeProps(t, store, "srv-bedrock", true)

	stats := &RouteStats{}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.bedrockWake("srv-bedrock", stats)
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for gate.asks.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := gate.asks.Load(); n != 1 {
		t.Fatalf("gate asked %d times, want 1", n)
	}
	// Counted like java wakes
	deadline = time.Now().Add(2 * time.Second)
	for stats.Wakes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := stats.Wakes.Load(); n != 1 {
		t.Fatalf("route wakes = %d, want 1", n)
	}
}

// Properties row carrying only the wake flag
func wakeProps(t *testing.T, store *db.Store, id string, wake bool) {
	t.Helper()
	if err := store.CreateServerProperties(context.Background(), &v1.ServerProperties{
		Id: id, ServerId: id, EnableWakeOnConnect: &wake,
	}); err != nil {
		t.Fatalf("properties create failed %v", err)
	}
}

// Fresh rows decide, flags and busy refusals included
func TestWakeForBedrockGating(t *testing.T) {
	cases := []struct {
		name    string
		status  v1.ServerStatus
		wake    bool
		paused  bool
		busy    bool
		asks    int32
		resumes int32
	}{
		{"stopped wakeable", v1.ServerStatus_SERVER_STATUS_STOPPED, true, false, false, 1, 0},
		{"stopped not wakeable", v1.ServerStatus_SERVER_STATUS_STOPPED, false, false, false, 0, 0},
		{"paused wakeable", v1.ServerStatus_SERVER_STATUS_PAUSED, true, true, false, 0, 1},
		{"paused not wakeable", v1.ServerStatus_SERVER_STATUS_PAUSED, false, true, false, 0, 0},
		{"running", v1.ServerStatus_SERVER_STATUS_RUNNING, true, false, false, 0, 0},
		{"stopping is busy", v1.ServerStatus_SERVER_STATUS_STOPPING, true, false, true, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, store := testManager(t)
			gate := newFakeGate()
			if tc.busy {
				gate.startErr = errors.Join(ErrServerBusy, errors.New("stop in progress"))
			}
			gate.paused["srv"] = tc.paused
			m.SetServerGate(gate)
			seedServer(t, store, "srv", func(s *v1.Server) { s.Status = tc.status })
			wakeProps(t, store, "srv", tc.wake)

			m.wakeForBedrock("srv")
			if got := gate.asks.Load(); got != tc.asks {
				t.Fatalf("start asks = %d, want %d", got, tc.asks)
			}
			if got := gate.wakes.Load(); got != tc.resumes {
				t.Fatalf("resumes = %d, want %d", got, tc.resumes)
			}
		})
	}

	// Missing rows and gates are quiet no-ops
	m, _ := testManager(t)
	m.wakeForBedrock("ghost")
	m.SetServerGate(newFakeGate())
	m.wakeForBedrock("ghost")
}

// Loopback relay with a recording wake hook
func bedrockRelay(t *testing.T, route Route) (*UDPProxy, *net.UDPConn, chan string) {
	t.Helper()
	wakes := make(chan string, 16)
	up := NewUDPProxy(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Wake:       func(id string, _ *RouteStats) { wakes <- id },
	})
	if err := up.Start(); err != nil {
		t.Fatalf("relay start failed %v", err)
	}
	t.Cleanup(func() { up.Stop() })
	up.SetRoute(route)

	client, err := net.DialUDP("udp", nil, up.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("client dial failed %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return up, client, wakes
}

func noWake(t *testing.T, wakes chan string) {
	t.Helper()
	select {
	case id := <-wakes:
		t.Fatalf("unexpected wake for %s", id)
	case <-time.After(100 * time.Millisecond):
	}
}

func wantWake(t *testing.T, wakes chan string, id string) {
	t.Helper()
	select {
	case got := <-wakes:
		if got != id {
			t.Fatalf("woke %s, want %s", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("join did not ask for a wake")
	}
}

// Live gateway relays every datagram and wakes only on joins
func TestUDPRelayWakesOnBedrockJoin(t *testing.T) {
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	got := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := backend.ReadFromUDP(buf)
			if err != nil {
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()

	_, client, wakes := bedrockRelay(t, Route{
		ServerID:     "geyser-port-19132-udp",
		BackendHost:  "127.0.0.1",
		BackendPort:  backend.LocalAddr().(*net.UDPAddr).Port,
		WakeServerID: "srv-lazy",
	})
	forwarded := func(want []byte) {
		t.Helper()
		select {
		case b := <-got:
			if !bytes.Equal(b, want) {
				t.Fatalf("backend got % x, want % x", b, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("datagram was not relayed")
		}
	}

	ping := raknetPingPacket(raknetUnconnectedPing, 1)
	client.Write(ping)
	forwarded(ping)
	noWake(t, wakes)

	junk := []byte("not raknet")
	client.Write(junk)
	forwarded(junk)
	noWake(t, wakes)

	join := raknetJoinPacket(1400)
	client.Write(join)
	forwarded(join)
	wantWake(t, wakes, "srv-lazy")
}

// Relays without a wake target never call the hook
func TestUDPRelayWithoutWakeTarget(t *testing.T) {
	_, client, wakes := bedrockRelay(t, Route{
		ServerID:    "voice-udp",
		BackendHost: "127.0.0.1",
		BackendPort: freePort(t),
	})
	client.Write(raknetJoinPacket(1400))
	noWake(t, wakes)
}

// Backendless relay pongs pings and turns joins into wakes
func TestUDPWakeRelayAnswersWhileAsleep(t *testing.T) {
	up, client, wakes := bedrockRelay(t, Route{
		ServerID:     "srv-lazy-port-19132-udp",
		WakeServerID: "srv-lazy",
		State:        v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE,
		Wakeable:     true,
		Motd:         "Lazy\n§7offline, join to wake it",
		MaxPlayers:   10,
	})
	if route, ok := up.Route(); !ok || route.WakeServerID != "srv-lazy" {
		t.Fatalf("wake relay must stay visible as a route: %+v", route)
	}

	client.Write(raknetPingPacket(raknetUnconnectedPingOpen, 77))
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("no pong from sleeping relay %v", err)
	}
	sentAt, _, fields := parsePong(t, buf[:n])
	if sentAt != 77 || fields[1] != "Lazy" || fields[5] != "10" {
		t.Fatalf("pong wrong: %d %q", sentAt, fields)
	}
	noWake(t, wakes)

	client.Write(raknetJoinPacket(1400))
	wantWake(t, wakes, "srv-lazy")
	up.sessionsMu.Lock()
	sessions := len(up.sessions)
	up.sessionsMu.Unlock()
	if sessions != 0 {
		t.Fatalf("backendless relay opened %d sessions", sessions)
	}
	if pings := up.StatsSnapshot().StatusPings; pings != 1 {
		t.Fatalf("status pings = %d, want 1", pings)
	}
}

// Sleeping wakeable servers keep their bedrock relays up, other udp is untouched
func TestSyncListenersBedrockWakeRelays(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()
	mustSync(t, m)

	pluginPort, voicePort, pausedPort := freePort(t), freePort(t), freePort(t)
	geyserPort, legacyPort, voiceModPort, stoppedGeyserPort := freePort(t), freePort(t), freePort(t), freePort(t)
	udp := func(port int, name string, bedrock bool) *v1.NetworkPort {
		return &v1.NetworkPort{
			Name: name, HostPort: int32(port), ContainerPort: 19132,
			Protocol: v1.ModuleProtocol_MODULE_PROTOCOL_UDP, ProxyEnabled: true, Bedrock: bedrock,
		}
	}
	plugin := seedServer(t, store, "srv-plugin", func(s *v1.Server) {
		s.ContainerId = "c-plugin"
		s.AdditionalPorts = []*v1.NetworkPort{udp(pluginPort, "Bedrock", true), udp(voicePort, "Voice", false)}
	})
	wakeProps(t, store, plugin.Id, true)
	paused := seedServer(t, store, "srv-paused", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_PAUSED
		s.ContainerId = "c-paused"
		s.AdditionalPorts = []*v1.NetworkPort{udp(pausedPort, "Bedrock", true)}
	})
	wakeProps(t, store, paused.Id, true)
	parent := seedServer(t, store, "srv-parent", func(s *v1.Server) { s.ContainerId = "c-parent" })
	wakeProps(t, store, parent.Id, true)
	running := v1.ModuleStatus_MODULE_STATUS_RUNNING
	if err := store.CreateModule(ctx,
		&v1.Module{
			Id: "mod-geyser", Name: "Geyser", ServerId: parent.Id, ContainerId: "c-geyser",
			Status: running, Ports: []*v1.NetworkPort{udp(geyserPort, "Bedrock", true)},
		},
		&v1.Module{
			Id: "mod-geyser-legacy", Name: "Old Geyser", ServerId: parent.Id, ContainerId: "c-legacy", TemplateId: GeyserTemplateID,
			Status: running, Ports: []*v1.NetworkPort{udp(legacyPort, "Bedrock", false)},
		},
		&v1.Module{
			Id: "mod-voice", Name: "Voice", ServerId: parent.Id, ContainerId: "c-voice",
			Status: running, Ports: []*v1.NetworkPort{udp(voiceModPort, "Voice", false)},
		},
		&v1.Module{
			Id: "mod-geyser-off", Name: "Geyser Off", ServerId: parent.Id, AutoStart: true,
			Status: v1.ModuleStatus_MODULE_STATUS_STOPPED, Ports: []*v1.NetworkPort{udp(stoppedGeyserPort, "Bedrock", true)},
		},
	); err != nil {
		t.Fatalf("module create failed %v", err)
	}
	m.ipCacheMu.Lock()
	for id, ip := range map[string]string{"c-geyser": "172.18.0.9", "c-legacy": "172.18.0.10", "c-voice": "172.18.0.11", "c-paused": "172.18.0.12"} {
		m.ipCache[id] = ipEntry{ip: ip, at: time.Now()}
	}
	m.ipCacheMu.Unlock()

	mustSync(t, m)

	relay := func(port int) Route {
		t.Helper()
		up := lockedUDPSock(m, port)
		if up == nil || !up.IsRunning() {
			t.Fatalf("relay on %d must run", port)
		}
		route, ok := up.Route()
		if !ok {
			t.Fatalf("relay on %d has no route", port)
		}
		return route
	}

	// Marked plugin port on a stopped server answers as a wake relay
	r := relay(pluginPort)
	if r.WakeServerID != plugin.Id || r.BackendHost != "" || !r.Wakeable || r.State != v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE {
		t.Fatalf("plugin wake relay wrong: %+v", r)
	}
	if !strings.Contains(r.Motd, "join to wake") {
		t.Fatalf("plugin wake relay motd %q", r.Motd)
	}
	// Unmarked udp on the same server gets no responder
	if lockedUDPSock(m, voicePort) != nil {
		t.Fatal("voice port of a stopped server must not get a wake relay")
	}

	// Paused plugin relays live but answers pings itself
	r = relay(pausedPort)
	if r.WakeServerID != paused.Id || r.BackendHost != "172.18.0.12" || !r.Sleeping || r.Motd == "" {
		t.Fatalf("paused plugin relay wrong: %+v", r)
	}

	// Running gateways relay live and wake their parent, old geyser rows included
	for _, port := range []int{geyserPort, legacyPort} {
		if r := relay(port); r.WakeServerID != parent.Id || r.BackendHost == "" || r.Wakeable || r.Sleeping {
			t.Fatalf("gateway relay wrong: %+v", r)
		}
	}
	// Other udp modules never wake anything
	if r := relay(voiceModPort); r.WakeServerID != "" {
		t.Fatalf("voice module must not wake its server: %+v", r)
	}

	// Stopped autostart gateway still takes the join
	r = relay(stoppedGeyserPort)
	if r.WakeServerID != parent.Id || r.BackendHost != "" || r.OwnerID != "mod-geyser-off" {
		t.Fatalf("stopped gateway relay wrong: %+v", r)
	}

	// Turning wake off drops the responders
	off := false
	for _, id := range []string{plugin.Id, paused.Id, parent.Id} {
		cfg, err := store.GetServerProperties(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		cfg.EnableWakeOnConnect = &off
		if err := store.UpdateServerProperties(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	mustSync(t, m)
	if lockedUDPSock(m, pluginPort) != nil || lockedUDPSock(m, stoppedGeyserPort) != nil {
		t.Fatal("non wakeable servers must not keep wake relays")
	}
	if r := relay(geyserPort); r.WakeServerID != "" {
		t.Fatalf("gateway must not wake a non wakeable parent: %+v", r)
	}
	if r := relay(pausedPort); r.Sleeping || r.WakeServerID != "" {
		t.Fatalf("paused relay of a non wakeable server must just relay: %+v", r)
	}
}

// Long names are cut so a pong stays near ping size
func TestBedrockPongCapsStatus(t *testing.T) {
	long := strings.Repeat("é", 200) + "\n" + strings.Repeat("w", 200)
	pong := bedrockPong(raknetPingPacket(raknetUnconnectedPing, 1), bedrockGUID("route"), long, 100000, 65535)
	n := int(binary.BigEndian.Uint16(pong[33:35]))
	if n > bedrockPongMaxStatus {
		t.Fatalf("status is %d bytes, cap %d", n, bedrockPongMaxStatus)
	}
	if !utf8.Valid(pong[35:]) {
		t.Fatal("truncation split a rune")
	}
	_, _, fields := parsePong(t, pong)
	if len(fields) != 12 || fields[5] != "100000" || fields[10] != "65535" {
		t.Fatalf("fixed fields lost to truncation: %q", fields)
	}
	if len(pong) > 35+bedrockPongMaxStatus {
		t.Fatalf("pong is %d bytes", len(pong))
	}
}

// Same route, same guid across relay restarts
func TestBedrockGUIDStable(t *testing.T) {
	if bedrockGUID("srv-port-19132-udp") != bedrockGUID("srv-port-19132-udp") {
		t.Fatal("guid must be stable")
	}
	if bedrockGUID("a") == bedrockGUID("b") {
		t.Fatal("routes should differ")
	}
	if g := bedrockGUID("x"); g < 0 || g > 1<<32 {
		t.Fatalf("guid %d out of range", g)
	}
}

// Failed or crashed starts hold off joins, doubling to a cap
func TestWakeLimiterBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newWakeLimiter(time.Second)
	l.now = func() time.Time { return now }

	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, hold := range want {
		if !l.begin("s") {
			t.Fatalf("attempt %d must run once the hold passed", i)
		}
		l.end("s")
		l.fail("s")
		now = now.Add(hold - time.Millisecond)
		if l.begin("s") {
			t.Fatalf("attempt %d ran inside its %v hold", i, hold)
		}
		now = now.Add(time.Millisecond)
	}

	// A clean wake clears the hold
	l.succeed("s")
	l.fail("s")
	now = now.Add(10 * time.Second)
	if !l.begin("s") {
		t.Fatal("hold must restart from the base after a success")
	}
}

// Forged joins can't crash loop a server in error
func TestWakeForBedrockBacksOffErroredServer(t *testing.T) {
	m, store := testManager(t)
	gate := newFakeGate()
	m.SetServerGate(gate)
	seedServer(t, store, "srv", func(s *v1.Server) { s.Status = v1.ServerStatus_SERVER_STATUS_ERROR })
	wakeProps(t, store, "srv", true)

	if !m.bedrockWakes.begin("srv") || !m.wakeForBedrock("srv") {
		t.Fatal("first join must start the errored server")
	}
	m.bedrockWakes.end("srv")
	if gate.asks.Load() != 1 {
		t.Fatalf("asks = %d, want 1", gate.asks.Load())
	}
	m.bedrockWakes.now = func() time.Time { return time.Now().Add(6 * time.Second) }
	if m.bedrockWakes.begin("srv") {
		t.Fatal("errored server must be held past the plain cooldown")
	}

	// Failed starts back off too, stopped servers that start clear it
	m2, store2 := testManager(t)
	gate2 := newFakeGate()
	gate2.startErr = errors.New("provisioning failed")
	m2.SetServerGate(gate2)
	seedServer(t, store2, "srv", nil)
	wakeProps(t, store2, "srv", true)
	m2.wakeForBedrock("srv")
	m2.bedrockWakes.now = func() time.Time { return time.Now().Add(6 * time.Second) }
	if m2.bedrockWakes.begin("srv") {
		t.Fatal("failed start must hold later joins")
	}
	gate2.startErr = nil
	m2.bedrockWakes.succeed("srv")
	if !m2.bedrockWakes.begin("srv") {
		t.Fatal("success must clear the hold")
	}
}

func TestPongLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newPongLimiter()
	l.now = func() time.Time { return now }
	l.global.last = now

	// One network gets its burst, then waits for refill
	for i := range pongNetBurst {
		if !l.allow(net.IPv4(203, 0, 113, byte(i))) {
			t.Fatalf("pong %d inside the burst refused", i)
		}
	}
	if l.allow(net.IPv4(203, 0, 113, 200)) {
		t.Fatal("same /24 must share one bucket")
	}
	now = now.Add(time.Second)
	if !l.allow(net.IPv4(203, 0, 113, 7)) {
		t.Fatal("bucket must refill")
	}

	// Many networks hit the relay wide cap
	sent := 0
	for i := range 200 {
		if l.allow(net.IPv4(10, byte(i), 0, 1)) {
			sent++
		}
	}
	if sent > pongGlobalBurst {
		t.Fatalf("sent %d pongs in one instant, global burst %d", sent, pongGlobalBurst)
	}

	// IPv6 groups by /64
	l2 := newPongLimiter()
	a, b := net.ParseIP("2001:db8:1:2::1"), net.ParseIP("2001:db8:1:2:ffff::9")
	if pongNetKey(a) != pongNetKey(b) || pongNetKey(a) == pongNetKey(net.ParseIP("2001:db8:1:3::1")) {
		t.Fatal("ipv6 keys must group by /64")
	}
	for range pongNetBurst {
		l2.allow(a)
	}
	if l2.allow(b) {
		t.Fatal("same /64 must share one bucket")
	}
}

// Spoofed sources can't grow the network table past its bound
func TestPongLimiterBoundedTable(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newPongLimiter()
	l.now = func() time.Time { return now }
	for i := range pongNetMax + 500 {
		now = now.Add(time.Millisecond)
		l.allow(net.IPv4(byte(i>>16), byte(i>>8), byte(i), 1).To4())
		if len(l.nets) > pongNetMax {
			t.Fatalf("table grew to %d", len(l.nets))
		}
	}
}

// Paused plugins: relay answers pings, joins still reach the backend and wake
func TestUDPSleepingRelayAnswersPings(t *testing.T) {
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	got := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := backend.ReadFromUDP(buf)
			if err != nil {
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()

	_, client, wakes := bedrockRelay(t, Route{
		ServerID:     "srv-port-19132-udp",
		BackendHost:  "127.0.0.1",
		BackendPort:  backend.LocalAddr().(*net.UDPAddr).Port,
		WakeServerID: "srv",
		Sleeping:     true,
		Motd:         "Lazy\n§7join to wake",
		MaxPlayers:   5,
	})
	client.Write(raknetPingPacket(raknetUnconnectedPing, 3))
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("paused relay did not pong %v", err)
	}
	if _, guid, fields := parsePong(t, buf[:n]); fields[1] != "Lazy" || guid != bedrockGUID("srv-port-19132-udp") {
		t.Fatalf("pong wrong: %q guid %d", fields, guid)
	}
	select {
	case b := <-got:
		t.Fatalf("ping leaked to the frozen backend: % x", b)
	case <-time.After(100 * time.Millisecond):
	}

	client.Write(raknetJoinPacket(1400))
	wantWake(t, wakes, "srv")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("join must still reach the backend")
	}
}
