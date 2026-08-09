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

// Fake lobby reading the join then echoing one line
func fakeLobby(t *testing.T, ln net.Listener, reply string) chan string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- "accept failed"
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := mcproto.ReadHandshakePacket(conn); err != nil {
			got <- "handshake failed"
			return
		}
		ls, err := mcproto.ReadLoginStart(conn)
		if err != nil {
			got <- "login start failed"
			return
		}
		probe := make([]byte, 5)
		if _, err := io.ReadFull(conn, probe); err != nil {
			got <- "probe read failed"
			return
		}
		conn.Write([]byte(reply))
		got <- ls.Name + ":" + string(probe)
	}()
	return got
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

// Online mediation carries cipher to plaintext both ways
func TestShimMediatedRelayOnline(t *testing.T) {
	sock, lobbyLn, _ := mediationSocket(t, true)
	lobbySaw := fakeLobby(t, lobbyLn, "PONG!")

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
	mcproto.WriteVarInt(&body, mcproto.VarInt(len("Steve")))
	body.WriteString("Steve")
	if err := mcproto.WriteFramed(client, body.Bytes()); err != nil {
		t.Fatalf("login start failed %v", err)
	}

	cr, cw := shimClientDance(t, client)

	if _, err := cw.Write([]byte("PING!")); err != nil {
		t.Fatalf("cipher write failed %v", err)
	}

	select {
	case saw := <-lobbySaw:
		if saw != "Steve:PING!" {
			t.Fatalf("lobby saw %q", saw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lobby never saw the join")
	}

	echo := make([]byte, 5)
	if _, err := io.ReadFull(cr, echo); err != nil {
		t.Fatalf("cipher read failed %v", err)
	}
	if string(echo) != "PONG!" {
		t.Fatalf("echo = %q", echo)
	}
}

// Offline mediation splices without any cipher
func TestShimMediatedRelayOffline(t *testing.T) {
	sock, lobbyLn, _ := mediationSocket(t, false)
	lobbySaw := fakeLobby(t, lobbyLn, "PONG!")

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(10 * time.Second))

	mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 772,
		ServerAddress:   "hub.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x00)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len("Steve")))
	body.WriteString("Steve")
	mcproto.WriteFramed(client, body.Bytes())

	client.Write([]byte("PING!"))

	select {
	case saw := <-lobbySaw:
		if saw != "Steve:PING!" {
			t.Fatalf("lobby saw %q", saw)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lobby never saw the join")
	}

	echo := make([]byte, 5)
	if _, err := io.ReadFull(client, echo); err != nil {
		t.Fatalf("read failed %v", err)
	}
	if string(echo) != "PONG!" {
		t.Fatalf("echo = %q", echo)
	}
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
