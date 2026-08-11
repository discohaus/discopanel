package proxy

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Session server trusting exactly one player
func shimSessionServer(t *testing.T) *httptest.Server {
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

// Socket with a shim mediated hub route
func mediationSocket(t *testing.T, online bool) (*ListenerSocket, net.Listener, *ShimRuntime) {
	t.Helper()
	lobbyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("lobby listen failed %v", err)
	}
	t.Cleanup(func() { lobbyLn.Close() })

	intents := NewIntentTable()
	sh, err := NewShimRuntime(online, logger.New(), intents)
	if err != nil {
		t.Fatalf("shim build failed %v", err)
	}
	if online {
		sh.auth.SessionBase = shimSessionServer(t).URL
	}

	sock := NewListenerSocket(&Config{
		ListenAddr: "127.0.0.1:0",
		Logger:     logger.New(),
		Intents:    intents,
		Shim:       sh,
	})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })

	sock.SetRoutes([]Route{{
		ServerID:    "srv-hub",
		OwnerKind:   OwnerServer,
		OwnerID:     "srv-hub",
		Protocol:    v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:    "hub.example.com",
		State:       v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
		BackendHost: "127.0.0.1",
		BackendPort: lobbyLn.Addr().(*net.TCPAddr).Port,
		McVersion:   "1.21.8",
		McProtocol:  772,
		LobbyShim:   true,
	}})
	return sock, lobbyLn, sh
}

// Client half of the encryption dance for tests
func shimClientDance(t *testing.T, conn net.Conn) (io.Reader, io.Writer) {
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

// Online hub join rides the mirror through the cipher
func TestShimHubJoinOnline(t *testing.T) {
	sock, lobbyLn := hubbedSocket(t, true)
	lobbyDone := fakeModernLobby(t, lobbyLn, 772)

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))

	if err := mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "hub.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	}); err != nil {
		t.Fatalf("handshake failed %v", err)
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	packet.WriteString(&body, "Steve")
	packet.WriteUUID(&body, [16]byte{9})
	if err := packet.WriteFrame(client, body.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	cr, cw := shimClientDance(t, client)

	chunks, err := hubShimWalk(cr, cw, 772, nil)
	if err != nil {
		t.Fatalf("shim client failed %v", err)
	}
	if chunks != 16 {
		t.Fatalf("client saw %d chunks, want 16", chunks)
	}
	expectLobbyClean(t, lobbyDone)
}

// Unsupported versions get the lobby version kick
func TestShimUnsupportedFamilyKicks(t *testing.T) {
	sock, _, _ := mediationSocket(t, true)

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))

	mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 404,
		ServerAddress:   "hub.example.com",
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
		t.Fatal("unsupported family must kick, got silence")
	}
	reason := readKickReason(t, reply)
	if !bytes.Contains([]byte(reason), []byte("1.21.8")) {
		t.Fatalf("kick reason misses version, got %q", reason)
	}
}
