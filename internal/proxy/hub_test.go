package proxy

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Session server trusting exactly one player
func hubSessionServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("username") != "Steve" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		json.NewEncoder(w).Encode(mojang.Profile{
			ID:   "069a79f444e94726a5befca90e38aaf5",
			Name: "Steve",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Socket with an enabled lobby and optional routes
func hubSocket(t *testing.T, online bool, targets []family.Target, routes []Route) (*ListenerSocket, *HubRuntime, *IntentTable) {
	t.Helper()
	intents := NewIntentTable()
	h, err := NewHubRuntime(online, logger.New(), intents)
	if err != nil {
		t.Fatalf("hub build failed %v", err)
	}
	t.Cleanup(h.Stop)
	if online {
		h.auth.SessionBase = hubSessionServer(t).URL
	}
	h.SetEnabled(true)
	h.SetTargets(targets)

	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Intents:    intents,
		Hub:        h,
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes(routes)
	return sock, h, intents
}

// Client half of the encryption dance for tests
func clientDance(t *testing.T, conn net.Conn) (io.Reader, io.Writer) {
	t.Helper()
	frame, err := packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("request read failed %v", err)
	}
	buf := bytes.NewReader(frame)
	if id, err := mcproto.ReadVarInt(buf); err != nil || id != 0x01 {
		t.Fatalf("request id = %d, err %v", id, err)
	}
	if _, err := packet.ReadString(buf); err != nil {
		t.Fatalf("server id failed %v", err)
	}
	pubDER, err := packet.ReadVarBytes(buf, 4096)
	if err != nil {
		t.Fatalf("pubkey failed %v", err)
	}
	token, err := packet.ReadVarBytes(buf, 4096)
	if err != nil {
		t.Fatalf("token failed %v", err)
	}

	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("pubkey parse failed %v", err)
	}
	pub := pubAny.(*rsa.PublicKey)
	secret := make([]byte, 16)
	rand.Read(secret)
	encSecret, _ := rsa.EncryptPKCS1v15(rand.Reader, pub, secret)
	encToken, _ := rsa.EncryptPKCS1v15(rand.Reader, pub, token)

	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x01)
	packet.WriteVarBytes(&body, encSecret)
	packet.WriteVarBytes(&body, encToken)
	if err := packet.WriteFrame(conn, body.Bytes()); err != nil {
		t.Fatalf("response failed %v", err)
	}

	cr, err := packet.NewCipherReader(conn, secret)
	if err != nil {
		t.Fatalf("cipher reader failed %v", err)
	}
	cw, err := packet.NewCipherWriter(conn, secret)
	if err != nil {
		t.Fatalf("cipher writer failed %v", err)
	}
	return cr, cw
}

// Confirms the first offered pack like a real client
func confirmPacks(w io.Writer, buf *bytes.Reader, ids *family.ModernIDs) error {
	count, err := mcproto.ReadVarInt(buf)
	if err != nil || count < 1 {
		return fmt.Errorf("bad known packs offer: %v", err)
	}
	ns, err := packet.ReadString(buf)
	if err != nil {
		return err
	}
	id, err := packet.ReadString(buf)
	if err != nil {
		return err
	}
	ver, err := packet.ReadString(buf)
	if err != nil {
		return err
	}
	var resp bytes.Buffer
	mcproto.WriteVarInt(&resp, mcproto.VarInt(ids.CfgKnownPacksSB))
	mcproto.WriteVarInt(&resp, 1)
	packet.WriteString(&resp, ns)
	packet.WriteString(&resp, id)
	packet.WriteString(&resp, ver)
	return packet.WriteFrame(w, resp.Bytes())
}

// Walks the native join from login success onward
func hubWalk(r io.Reader, w io.Writer, protocol int32) (int, error) {
	ids := family.ModernIDsFor(protocol)

	frame, err := packet.ReadFrame(r)
	if err != nil {
		return 0, fmt.Errorf("login success read: %w", err)
	}
	if pid, _ := mcproto.ReadVarInt(bytes.NewReader(frame)); int32(pid) != 0x02 {
		return 0, fmt.Errorf("login success id %d", pid)
	}

	if !ids.NoConfigPhase {
		var ack bytes.Buffer
		mcproto.WriteVarInt(&ack, 0x03)
		if err := packet.WriteFrame(w, ack.Bytes()); err != nil {
			return 0, err
		}
		for {
			frame, err = packet.ReadFrame(r)
			if err != nil {
				return 0, fmt.Errorf("config read: %w", err)
			}
			buf := bytes.NewReader(frame)
			pid, _ := mcproto.ReadVarInt(buf)
			if ids.CfgPingCB >= 0 && int32(pid) == ids.CfgPingCB {
				var id int32
				if err := packet.ReadNum(buf, &id); err != nil {
					return 0, err
				}
				var pong bytes.Buffer
				mcproto.WriteVarInt(&pong, mcproto.VarInt(ids.CfgPongSB))
				packet.WriteNum(&pong, id)
				if err := packet.WriteFrame(w, pong.Bytes()); err != nil {
					return 0, err
				}
			}
			if ids.CfgKnownPacks >= 0 && int32(pid) == ids.CfgKnownPacks {
				if err := confirmPacks(w, buf, ids); err != nil {
					return 0, err
				}
			}
			if int32(pid) == ids.CfgFinishCB {
				break
			}
		}
		var fin bytes.Buffer
		mcproto.WriteVarInt(&fin, mcproto.VarInt(ids.CfgFinishAckSB))
		if err := packet.WriteFrame(w, fin.Bytes()); err != nil {
			return 0, err
		}
	}

	chunks := 0
	for {
		frame, err = packet.ReadFrame(r)
		if err != nil {
			return chunks, fmt.Errorf("play read: %w", err)
		}
		pid, _ := mcproto.ReadVarInt(bytes.NewReader(frame))
		if int32(pid) == ids.ChunkData {
			chunks++
		}
		if int32(pid) == ids.SyncPlayerPos {
			return chunks, nil
		}
	}
}

// Dials the lobby and walks the whole join
func hubClient(t *testing.T, addr, hostname, name string, protocol int32) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: mcproto.VarInt(protocol),
		ServerAddress:   hostname,
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var start bytes.Buffer
	mcproto.WriteVarInt(&start, 0x00)
	packet.WriteString(&start, name)
	packet.WriteUUID(&start, [16]byte{9})
	if err := packet.WriteFrame(conn, start.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	chunks, err := hubWalk(conn, conn, protocol)
	if err != nil {
		t.Fatalf("hub walk failed %v", err)
	}
	return conn, chunks
}

// Dials and logs in up to the acknowledge
func loginTo(t *testing.T, addr, hostname, name string, protocol int32, intent mcproto.VarInt) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: mcproto.VarInt(protocol),
		ServerAddress:   hostname,
		ServerPort:      25565,
		NextState:       intent,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var start bytes.Buffer
	mcproto.WriteVarInt(&start, 0x00)
	packet.WriteString(&start, name)
	packet.WriteUUID(&start, [16]byte{9})
	if err := packet.WriteFrame(conn, start.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	frame, err := packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("login success read failed %v", err)
	}
	if pid, _ := mcproto.ReadVarInt(bytes.NewReader(frame)); int32(pid) != 0x02 {
		t.Fatalf("login success id %d", pid)
	}
	var ack bytes.Buffer
	mcproto.WriteVarInt(&ack, 0x03)
	if err := packet.WriteFrame(conn, ack.Bytes()); err != nil {
		t.Fatalf("login ack failed %v", err)
	}
	return conn
}

// Declaration bytes for one neoforge style client
func probeDecl(protocol int32, optional bool) []byte {
	var d bytes.Buffer
	if protocol == 765 {
		mcproto.WriteVarInt(&d, 1)
		packet.WriteString(&d, "modded:chan")
		packet.WriteBool(&d, true)
		packet.WriteString(&d, "1")
		packet.WriteBool(&d, true)
		mcproto.WriteVarInt(&d, 1)
		packet.WriteBool(&d, optional)
		mcproto.WriteVarInt(&d, 0)
		return d.Bytes()
	}
	mcproto.WriteVarInt(&d, 1)
	mcproto.WriteVarInt(&d, 4)
	mcproto.WriteVarInt(&d, 1)
	packet.WriteString(&d, "modded:chan")
	packet.WriteString(&d, "1")
	packet.WriteBool(&d, true)
	mcproto.WriteVarInt(&d, 0)
	packet.WriteBool(&d, optional)
	return d.Bytes()
}

// Logs in then answers the fence with a declaration
func declaredLogin(t *testing.T, addr, hostname, name string, protocol int32, intent mcproto.VarInt, optional bool) net.Conn {
	t.Helper()
	conn := loginTo(t, addr, hostname, name, protocol, intent)
	ids := family.ModernIDsFor(protocol)

	frame, err := packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("probe read failed %v", err)
	}
	buf := bytes.NewReader(frame)
	if pid, _ := mcproto.ReadVarInt(buf); int32(pid) != ids.CfgPluginMsgCB {
		t.Fatalf("probe id %d", pid)
	}
	if channel, err := packet.ReadString(buf); err != nil || channel != "neoforge:register" {
		t.Fatalf("probe channel %q err %v", channel, err)
	}

	frame, err = packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("fence ping read failed %v", err)
	}
	buf = bytes.NewReader(frame)
	if pid, _ := mcproto.ReadVarInt(buf); int32(pid) != ids.CfgPingCB {
		t.Fatalf("fence ping id %d", pid)
	}
	var pingID int32
	if err := packet.ReadNum(buf, &pingID); err != nil {
		t.Fatalf("fence ping body failed %v", err)
	}

	var reply bytes.Buffer
	mcproto.WriteVarInt(&reply, mcproto.VarInt(ids.CfgPluginMsgSB))
	packet.WriteString(&reply, "neoforge:register")
	reply.Write(probeDecl(protocol, optional))
	if err := packet.WriteFrame(conn, reply.Bytes()); err != nil {
		t.Fatalf("declaration failed %v", err)
	}
	var pong bytes.Buffer
	mcproto.WriteVarInt(&pong, mcproto.VarInt(ids.CfgPongSB))
	packet.WriteNum(&pong, pingID)
	if err := packet.WriteFrame(conn, pong.Bytes()); err != nil {
		t.Fatalf("pong failed %v", err)
	}
	return conn
}

// Sends one absolute move as the client
func sendMove(t *testing.T, w io.Writer, protocol int32, x, y, z float64) {
	t.Helper()
	ids := family.ModernIDsFor(protocol)
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(ids.PlayerPos))
	packet.WriteNum(&body, x)
	packet.WriteNum(&body, y)
	packet.WriteNum(&body, z)
	body.WriteByte(0x01)
	if err := packet.WriteFrame(w, body.Bytes()); err != nil {
		t.Fatalf("move write failed %v", err)
	}
}

// Sends one chat line as the client
func sendChat(t *testing.T, w io.Writer, protocol int32, text string) {
	t.Helper()
	ids := family.ModernIDsFor(protocol)
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(ids.ChatSB))
	packet.WriteString(&body, text)
	if err := packet.WriteFrame(w, body.Bytes()); err != nil {
		t.Fatalf("chat write failed %v", err)
	}
}

// Scans frames until one packet id shows up
func readUntil(t *testing.T, r io.Reader, pid int32) *bytes.Reader {
	t.Helper()
	for range 256 {
		frame, err := packet.ReadFrame(r)
		if err != nil {
			t.Fatalf("scan for id %d failed %v", pid, err)
		}
		buf := bytes.NewReader(frame)
		got, err := mcproto.ReadVarInt(buf)
		if err != nil {
			continue
		}
		if int32(got) == pid {
			return buf
		}
	}
	t.Fatalf("packet id %d never arrived", pid)
	return nil
}

// One running fleet entry for gate tests
func runningTarget(name, hostname string, protocol int32) family.Target {
	version, _ := mcproto.NewestVersionForProtocol(protocol)
	return family.Target{
		ID:       "srv-" + name,
		Name:     name,
		Hostname: hostname,
		Port:     25565,
		Version:  version,
		Protocol: protocol,
		Running:  true,
	}
}

// Offline auth joins land on the plaza with chunks
func TestHubJoin(t *testing.T) {
	sock, _, _ := hubSocket(t, false, nil, nil)
	_, chunks := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	if chunks != 36 {
		t.Fatalf("client saw %d chunks, want 36", chunks)
	}
}

// Online joins ride the cipher through the whole walk
func TestHubJoinOnline(t *testing.T) {
	sock, _, _ := hubSocket(t, true, nil, nil)

	conn, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "lobby.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	packet.WriteString(&body, "Steve")
	packet.WriteUUID(&body, [16]byte{9})
	if err := packet.WriteFrame(conn, body.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	cr, cw := clientDance(t, conn)
	chunks, err := hubWalk(cr, cw, 772)
	if err != nil {
		t.Fatalf("hub walk failed %v", err)
	}
	if chunks != 36 {
		t.Fatalf("client saw %d chunks, want 36", chunks)
	}
}

// Registry compound eras join without known packs
func TestHubJoinLegacyCodec(t *testing.T) {
	sock, _, _ := hubSocket(t, false, nil, nil)
	_, chunks := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 765)
	if chunks != 36 {
		t.Fatalf("client saw %d chunks, want 36", chunks)
	}
}

// Codec floor kicks name the oldest spoken version
func TestHubUnsupportedVersionKicks(t *testing.T) {
	sock, _, _ := hubSocket(t, false, nil, nil)

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))

	mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 404,
		ServerAddress:   "lobby.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len("Steve")))
	body.WriteString("Steve")
	mcproto.WriteFramed(client, body.Bytes())

	reply, _ := io.ReadAll(client)
	if len(reply) == 0 {
		t.Fatal("unsupported version must kick, got silence")
	}
	reason := readKickReason(t, reply)
	if !strings.Contains(reason, "1.16.4") {
		t.Fatalf("kick reason misses floor version, got %q", reason)
	}
}

// Gate entry transfers back to the dialed address
func TestHubGateTransfer(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 772)
	sock, _, intents := hubSocket(t, false, []family.Target{target}, nil)

	conn, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	sendMove(t, conn, 772, -8, -60, -15)

	ids := family.ModernIDsFor(772)
	buf := readUntil(t, conn, ids.Transfer)
	host, err := packet.ReadString(buf)
	if err != nil {
		t.Fatalf("transfer host unreadable %v", err)
	}
	port, err := mcproto.ReadVarInt(buf)
	if err != nil {
		t.Fatalf("transfer port unreadable %v", err)
	}
	if host != "lobby.example.com" || int(port) != 25565 {
		t.Fatalf("transfer = %s:%d, want lobby.example.com:25565", host, port)
	}
	if id, ok := intents.Claim("steve"); !ok || id != "srv-alpha" {
		t.Fatalf("claim = %q/%v, want srv-alpha", id, ok)
	}
}

// Typing a menu number transfers back to the dialed address
func TestHubChatNumberHop(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 772)
	sock, _, intents := hubSocket(t, false, []family.Target{target}, nil)

	conn, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	sendChat(t, conn, 772, "1")

	buf := readUntil(t, conn, family.ModernIDsFor(772).Transfer)
	host, err := packet.ReadString(buf)
	if err != nil || host != "lobby.example.com" {
		t.Fatalf("transfer host = %q err %v, want lobby.example.com", host, err)
	}
	if id, ok := intents.Claim("steve"); !ok || id != "srv-alpha" {
		t.Fatalf("claim = %q/%v, want srv-alpha", id, ok)
	}
}

// Version gaps warn in chat instead of bouncing
func TestHubVersionGapWarns(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 766)
	sock, _, _ := hubSocket(t, false, []family.Target{target}, nil)

	conn, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	sendChat(t, conn, 772, "alpha")

	ids := family.ModernIDsFor(772)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frame, err := packet.ReadFrame(conn)
		if err != nil {
			t.Fatalf("frame read failed %v", err)
		}
		buf := bytes.NewReader(frame)
		pid, _ := mcproto.ReadVarInt(buf)
		if int32(pid) == ids.Transfer {
			t.Fatal("mismatched version must never transfer")
		}
		if int32(pid) != ids.SystemChat {
			continue
		}
		component, err := packet.ReadNetworkNBT(buf)
		if err != nil {
			continue
		}
		if strings.Contains(packet.ComponentText(component), "runs minecraft") {
			return
		}
	}
	t.Fatal("version warning never arrived")
}

// Transferless eras get the claim and rejoin note
func TestHubPre766DisconnectClaims(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 765)
	sock, _, intents := hubSocket(t, false, []family.Target{target}, nil)

	conn, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 765)
	sendChat(t, conn, 765, "1")

	readUntil(t, conn, family.ModernIDsFor(765).DisconnectCB)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if id, ok := intents.Claim("steve"); ok {
			if id != "srv-alpha" {
				t.Fatalf("claim = %q, want srv-alpha", id)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("claim never landed")
}

// Members in the plaza see each other appear
func TestHubPresence(t *testing.T) {
	sock, _, _ := hubSocket(t, false, nil, nil)

	connA, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	connB, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Alexis", 772)

	ids := family.ModernIDsFor(772)
	// The newcomer sees the earlier member spawn
	readUntil(t, connB, ids.AddEntity)
	// The earlier member sees the newcomer spawn
	readUntil(t, connA, ids.AddEntity)

	sendMove(t, connA, 772, 2, -60, 2)
	readUntil(t, connB, ids.EntityPosSync)
}

// Held logins transfer once their world answers
func TestHubHoldTransfersWhenReady(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	t.Cleanup(func() { backendLn.Close() })
	go func() {
		for {
			conn, err := backendLn.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	version, _ := mcproto.NewestVersionForProtocol(772)
	waking := family.Target{
		ID: "srv-smp", Name: "smp", Hostname: "smp.example.com",
		Port: 25565, Version: version, Protocol: 772, Waking: true,
	}
	route := Route{
		ServerID:   "srv-smp",
		OwnerKind:  OwnerServer,
		OwnerID:    "srv-smp",
		Protocol:   v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:   "smp.example.com",
		State:      v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING,
		McVersion:  version,
		McProtocol: 772,
	}
	sock, h, _ := hubSocket(t, false, []family.Target{waking}, []Route{route})

	conn, chunks := hubClient(t, sock.listener.Addr().String(), "smp.example.com", "Steve", 772)
	if chunks != 36 {
		t.Fatalf("held client saw %d chunks, want 36", chunks)
	}

	ready := waking
	ready.Running, ready.Waking = true, false
	ready.Addr = backendLn.Addr().String()
	h.SetTargets([]family.Target{ready})
	h.tick()

	buf := readUntil(t, conn, family.ModernIDsFor(772).Transfer)
	host, err := packet.ReadString(buf)
	if err != nil || host != "smp.example.com" {
		t.Fatalf("transfer host = %q err %v, want smp.example.com", host, err)
	}
}

// Modded plaza joins get the address list screen
func TestHubPlazaRefusalListsAddresses(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 772)
	sock, _, _ := hubSocket(t, false, []family.Target{target}, nil)
	for _, protocol := range []int32{765, 772} {
		conn := declaredLogin(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", protocol, mcproto.NextStateLogin, false)
		buf := readUntil(t, conn, family.ModernIDsFor(protocol).CfgDisconnect)
		rest, err := io.ReadAll(buf)
		if err != nil {
			t.Fatalf("refusal body failed for %d: %v", protocol, err)
		}
		if !bytes.Contains(rest, []byte("play.example.com")) {
			t.Fatalf("refusal for %d lists no address", protocol)
		}
	}
}

// Lobby answers pings for unrouted hostnames
func TestHubStatusCard(t *testing.T) {
	target := runningTarget("alpha", "play.example.com", 772)
	sock, _, _ := hubSocket(t, false, []family.Target{target}, nil)

	conn, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "nowhere.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateStatus,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var req bytes.Buffer
	mcproto.WriteVarInt(&req, 0x00)
	if err := mcproto.WriteFramed(conn, req.Bytes()); err != nil {
		t.Fatalf("status request failed %v", err)
	}

	frame, err := packet.ReadFrame(conn)
	if err != nil {
		t.Fatalf("status response failed %v", err)
	}
	buf := bytes.NewReader(frame)
	if pid, _ := mcproto.ReadVarInt(buf); pid != 0x00 {
		t.Fatalf("status id = %d", pid)
	}
	payload, err := packet.ReadString(buf)
	if err != nil {
		t.Fatalf("status json unreadable %v", err)
	}
	if !strings.Contains(payload, "Disco") {
		t.Fatalf("status misses the lobby brand: %s", payload)
	}
	if !strings.Contains(payload, "alpha") {
		t.Fatalf("status misses the fleet sample: %s", payload)
	}
}

// Disabled lobby keeps the classic empty kick
func TestHubDisabledKeepsUnknownHostKick(t *testing.T) {
	sock, h, _ := hubSocket(t, false, nil, nil)
	h.SetEnabled(false)

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))

	mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "nowhere.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	packet.WriteString(&body, "Steve")
	packet.WriteUUID(&body, [16]byte{9})
	packet.WriteFrame(client, body.Bytes())

	reply, _ := io.ReadAll(client)
	if len(reply) == 0 {
		t.Fatal("disabled lobby must kick, got silence")
	}
	reason := readKickReason(t, reply)
	if !strings.Contains(reason, "nowhere.example.com") {
		t.Fatalf("kick reason misses hostname, got %q", reason)
	}
}

// Full lobbies kick before the login dance starts
func TestHubFullKicks(t *testing.T) {
	sock, h, _ := hubSocket(t, false, nil, nil)
	h.mu.Lock()
	for i := range hubMaxMembers {
		id := int64(i + 1000)
		h.members[id] = &hubMember{id: id}
	}
	h.mu.Unlock()

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))

	mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "lobby.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	packet.WriteString(&body, "Steve")
	packet.WriteUUID(&body, [16]byte{9})
	packet.WriteFrame(client, body.Bytes())

	reply, _ := io.ReadAll(client)
	if len(reply) == 0 {
		t.Fatal("full lobby must kick, got silence")
	}
	if !strings.Contains(readKickReason(t, reply), "full") {
		t.Fatal("kick reason misses the full note")
	}
}

// Same account joining again drops the older session
func TestHubDoubleJoinDropsOld(t *testing.T) {
	sock, _, _ := hubSocket(t, false, nil, nil)

	connA, _ := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	connB, chunks := hubClient(t, sock.listener.Addr().String(), "lobby.example.com", "Steve", 772)
	if chunks != 36 {
		t.Fatalf("newer join saw %d chunks, want 36", chunks)
	}

	readUntil(t, connA, family.ModernIDsFor(772).DisconnectCB)
	_ = connB
}
