package proxy

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Claims burn exactly once
func TestIntentTableClaimOnce(t *testing.T) {
	table := NewIntentTable()
	table.Put("Steve", "srv-a", time.Minute)
	if id, ok := table.Claim("steve"); !ok || id != "srv-a" {
		t.Fatalf("claim = %q/%v, want srv-a", id, ok)
	}
	if _, ok := table.Claim("steve"); ok {
		t.Fatal("second claim must miss")
	}
}

// Names match without case
func TestIntentTableCaseInsensitive(t *testing.T) {
	table := NewIntentTable()
	table.Put("StEvE", "srv-a", time.Minute)
	if _, ok := table.Claim("sTeVe"); !ok {
		t.Fatal("case blind claim must hit")
	}
}

// Expired claims never fire
func TestIntentTableExpiry(t *testing.T) {
	table := NewIntentTable()
	table.Put("Steve", "srv-a", 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if _, ok := table.Claim("steve"); ok {
		t.Fatal("expired claim must miss")
	}
}

// Blank keys never store
func TestIntentTableIgnoresBlanks(t *testing.T) {
	table := NewIntentTable()
	table.Put("", "srv-a", time.Minute)
	table.Put("Steve", "", time.Minute)
	if _, ok := table.Claim(""); ok {
		t.Fatal("blank player must miss")
	}
	if _, ok := table.Claim("steve"); ok {
		t.Fatal("blank server must miss")
	}
}

// Lobby enabled socket with a second plain target route
// Also a sole route decoy so unmatched names reach the lobby
func hubIntentSocket(t *testing.T, targetProto int32, targetVersion string) (*ListenerSocket, net.Listener, *IntentTable) {
	t.Helper()
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("target listen failed %v", err)
	}
	t.Cleanup(func() { targetLn.Close() })
	decoyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("decoy listen failed %v", err)
	}
	t.Cleanup(func() { decoyLn.Close() })

	sock, _, intents := hubSocket(t, false, nil, []Route{
		{
			ServerID:    "srv-target",
			OwnerKind:   OwnerServer,
			OwnerID:     "srv-target",
			Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
			Hostname:    "target.example.com",
			State:       v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
			BackendHost: "127.0.0.1",
			BackendPort: targetLn.Addr().(*net.TCPAddr).Port,
			McVersion:   targetVersion,
			McProtocol:  targetProto,
		},
		{
			ServerID:    "srv-decoy",
			OwnerKind:   OwnerServer,
			OwnerID:     "srv-decoy",
			Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
			Hostname:    "decoy.example.com",
			State:       v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
			BackendHost: "127.0.0.1",
			BackendPort: decoyLn.Addr().(*net.TCPAddr).Port,
		},
	})
	return sock, targetLn, intents
}

// Handshake plus a real login start for the hub
func hubJoinBytes(t *testing.T, hostname string, protocol int32, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	err := mcproto.WriteHandshakePacket(&buf, &mcproto.HandshakePacket{
		ProtocolVersion: mcproto.VarInt(protocol),
		ServerAddress:   hostname,
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	if err != nil {
		t.Fatalf("handshake build failed %v", err)
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(name)))
	body.WriteString(name)
	if err := mcproto.WriteFramed(&buf, body.Bytes()); err != nil {
		t.Fatalf("login start build failed %v", err)
	}
	return buf.Bytes()
}

// Claimed joins land on the target with login intact
func TestIntentRerouteReachesTarget(t *testing.T) {
	sock, targetLn, intents := hubIntentSocket(t, 772, "1.21.8")
	intents.Put("Steve", "srv-target", time.Minute)

	data := roundTripMC(t, sock, targetLn, hubJoinBytes(t, "hub.example.com", 772, "Steve"), false)

	r := bytes.NewReader(data)
	handshake, err := mcproto.ReadHandshakePacket(r)
	if err != nil {
		t.Fatalf("target handshake unreadable %v", err)
	}
	if int(handshake.ServerPort) != targetLn.Addr().(*net.TCPAddr).Port {
		t.Fatalf("handshake port = %d, want target", handshake.ServerPort)
	}
	ls, err := mcproto.ReadLoginStart(r)
	if err != nil {
		t.Fatalf("login start missing after reroute %v", err)
	}
	if ls.Name != "Steve" {
		t.Fatalf("login name = %q, want Steve", ls.Name)
	}

	stats := sock.StatsSnapshots()["srv-target"]
	if stats == nil || stats.Logins != 1 {
		t.Fatalf("target login counters wrong: %+v", stats)
	}
	if _, ok := intents.Claim("steve"); ok {
		t.Fatal("intent must burn on use")
	}
}

// Routed joins toward the claimed server burn the claim
func TestRoutedJoinBurnsClaim(t *testing.T) {
	sock, targetLn, intents := hubIntentSocket(t, 772, "1.21.8")
	intents.Put("Steve", "srv-target", time.Minute)

	data := roundTripMC(t, sock, targetLn, hubJoinBytes(t, "target.example.com", 772, "Steve"), false)

	r := bytes.NewReader(data)
	if _, err := mcproto.ReadHandshakePacket(r); err != nil {
		t.Fatalf("target handshake unreadable %v", err)
	}
	ls, err := mcproto.ReadLoginStart(r)
	if err != nil {
		t.Fatalf("login start missing after relay %v", err)
	}
	if ls.Name != "Steve" {
		t.Fatalf("login name = %q, want Steve", ls.Name)
	}
	if _, ok := intents.Claim("steve"); ok {
		t.Fatal("claim must burn on the routed join")
	}
}

// Routed joins elsewhere still honor the claim
func TestRoutedJoinHonorsClaimElsewhere(t *testing.T) {
	sock, targetLn, intents := hubIntentSocket(t, 772, "1.21.8")
	intents.Put("Steve", "srv-target", time.Minute)

	// Split writes prove the peek waits out fragments
	data := roundTripMC(t, sock, targetLn, hubJoinBytes(t, "decoy.example.com", 772, "Steve"), true)

	r := bytes.NewReader(data)
	handshake, err := mcproto.ReadHandshakePacket(r)
	if err != nil {
		t.Fatalf("target handshake unreadable %v", err)
	}
	if int(handshake.ServerPort) != targetLn.Addr().(*net.TCPAddr).Port {
		t.Fatalf("handshake port = %d, want target", handshake.ServerPort)
	}
	ls, err := mcproto.ReadLoginStart(r)
	if err != nil {
		t.Fatalf("login start missing after reroute %v", err)
	}
	if ls.Name != "Steve" {
		t.Fatalf("login name = %q, want Steve", ls.Name)
	}
	if _, ok := intents.Claim("steve"); ok {
		t.Fatal("claim must burn on use")
	}
}

// Unclaimed joins enter the native lobby instead
func TestNoIntentEntersLobby(t *testing.T) {
	sock, _, _ := hubIntentSocket(t, 772, "1.21.8")

	_, chunks := hubClient(t, sock.listener.Addr().String(), "hub.example.com", "Steve", 772)
	if chunks != 36 {
		t.Fatalf("lobby join saw %d chunks, want 36", chunks)
	}
}

// Sends bytes and returns the raw server reply
func clientExchange(t *testing.T, sock *ListenerSocket, sent []byte) []byte {
	t.Helper()
	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("client dial failed %v", err)
	}
	defer client.Close()
	if _, err := client.Write(sent); err != nil {
		t.Fatalf("write failed %v", err)
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, _ := io.ReadAll(client)
	return reply
}

// Reads one framed login disconnect reason
func readKickReason(t *testing.T, reply []byte) string {
	t.Helper()
	r := bytes.NewReader(reply)
	length, err := mcproto.ReadVarInt(r)
	if err != nil || length < 2 {
		t.Fatalf("kick frame unreadable, err %v", err)
	}
	packetID, err := mcproto.ReadVarInt(r)
	if err != nil || packetID != 0x00 {
		t.Fatalf("kick id = %d, err %v", packetID, err)
	}
	reason, err := mcproto.ReadString(r, 1<<16)
	if err != nil {
		t.Fatalf("kick reason unreadable %v", err)
	}
	return reason
}

// Wrong versions kick instead of dialing the target
func TestIntentVersionMismatchKicks(t *testing.T) {
	sock, _, intents := hubIntentSocket(t, 772, "1.21.8")
	intents.Put("Steve", "srv-target", time.Minute)

	reply := clientExchange(t, sock, hubJoinBytes(t, "hub.example.com", 340, "Steve"))
	reason := readKickReason(t, reply)
	if !strings.Contains(reason, "1.21.8") {
		t.Fatalf("kick reason misses version, got %q", reason)
	}
}

// Transfer state joins on dead routes still see a kick
func TestTransferStateDeadRouteKicks(t *testing.T) {
	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes([]Route{{
		ServerID:  "srv-dead",
		OwnerKind: OwnerServer,
		OwnerID:   "srv-dead",
		Protocol:  v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:  "dead.example.com",
		State:     v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
	}})

	var buf bytes.Buffer
	err := mcproto.WriteHandshakePacket(&buf, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "dead.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateTransfer,
	})
	if err != nil {
		t.Fatalf("handshake build failed %v", err)
	}

	reply := clientExchange(t, sock, buf.Bytes())
	if len(reply) == 0 {
		t.Fatal("transfer join must see a kick, got silence")
	}
	readKickReason(t, reply)
}
