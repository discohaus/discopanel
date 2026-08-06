package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Http sniff decides on method prefixes even split
func TestSniffHTTP(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		split bool
		want  bool
	}{
		{"get request", []byte("GET / HTTP/1.1\r\n"), false, true},
		{"h2c preface", []byte("PRI * HTTP/2.0\r\n"), false, true},
		{"split post", []byte("POST /join HTTP/1.1\r\n"), true, true},
		{"mc handshake bytes", []byte{0x10, 0x00, 0xff, 0x0c}, false, false},
		{"method lookalike", []byte("GETX / HTTP/1.1\r\n"), false, false},
		{"eof mid method", []byte("PO"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			go func() {
				if tc.split {
					for _, b := range tc.bytes {
						client.Write([]byte{b})
						time.Sleep(time.Millisecond)
					}
				} else {
					client.Write(tc.bytes)
				}
				client.Close()
			}()
			br := bufio.NewReaderSize(server, mcproto.MaxHandshakeLength)
			// Callers peek before sniffing, mirror that
			if _, err := br.Peek(1); err != nil && tc.want {
				t.Fatalf("first byte peek failed %v", err)
			}
			if got := sniffHTTP(br); got != tc.want {
				t.Fatalf("sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

// Socket with one online mc route to a captured backend
func mcTestSocket(t *testing.T, preserveHost bool) (*ListenerSocket, net.Listener) {
	t.Helper()
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed %v", err)
	}
	t.Cleanup(func() { backendLn.Close() })

	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes([]Route{{
		ServerID:     "srv-mc",
		OwnerKind:    OwnerServer,
		OwnerID:      "srv-mc",
		Protocol:     v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:     "play.example.com",
		State:        v1.ProxyRouteState_PROXY_ROUTE_STATE_ONLINE,
		BackendHost:  "127.0.0.1",
		BackendPort:  backendLn.Addr().(*net.TCPAddr).Port,
		PreserveHost: preserveHost,
	}})
	return sock, backendLn
}

// Login handshake bytes plus a fake login start payload
func mcLoginBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	err := mcproto.WriteHandshakePacket(&buf, &mcproto.HandshakePacket{
		ProtocolVersion: 767,
		ServerAddress:   "play.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	if err != nil {
		t.Fatalf("handshake build failed %v", err)
	}
	buf.WriteString("login-start-payload")
	return buf.Bytes()
}

// Carries backend bytes or the accept failure
type backendRead struct {
	data []byte
	err  error
}

// Sends bytes and returns what the backend saw
func roundTripMC(t *testing.T, sock *ListenerSocket, backendLn net.Listener, sent []byte, split bool) []byte {
	t.Helper()
	got := make(chan backendRead, 1)
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			got <- backendRead{err: err}
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, _ := io.ReadAll(conn)
		got <- backendRead{data: data}
	}()

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("client dial failed %v", err)
	}
	defer client.Close()
	if split {
		for _, b := range sent {
			if _, err := client.Write([]byte{b}); err != nil {
				t.Fatalf("split write failed %v", err)
			}
			time.Sleep(time.Millisecond)
		}
	} else {
		if _, err := client.Write(sent); err != nil {
			t.Fatalf("write failed %v", err)
		}
	}
	client.(*net.TCPConn).CloseWrite()

	select {
	case res := <-got:
		if res.err != nil {
			t.Fatalf("backend accept failed %v", res.err)
		}
		return res.data
	case <-time.After(10 * time.Second):
		t.Fatal("backend never saw the connection")
		return nil
	}
}

// Backend must see a valid rewritten handshake then the payload
func assertRewrittenLogin(t *testing.T, data []byte, backendLn net.Listener) {
	t.Helper()
	r := bytes.NewReader(data)
	handshake, err := mcproto.ReadHandshakePacket(r)
	if err != nil {
		t.Fatalf("backend handshake unreadable %v", err)
	}
	if handshake.ServerAddress != "localhost" {
		t.Fatalf("default forward must rewrite the address, got %q", handshake.ServerAddress)
	}
	if int(handshake.ServerPort) != backendLn.Addr().(*net.TCPAddr).Port {
		t.Fatalf("handshake must carry the backend port, got %d", handshake.ServerPort)
	}
	if handshake.NextState != mcproto.NextStateLogin {
		t.Fatalf("next state must survive, got %d", handshake.NextState)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "login-start-payload" {
		t.Fatalf("payload must follow intact, got %q", rest)
	}
}

// Login dispatch rewrites the handshake and keeps the payload
func TestMCLoginDispatchReachesBackend(t *testing.T) {
	sock, backendLn := mcTestSocket(t, false)
	sent := mcLoginBytes(t)

	data := roundTripMC(t, sock, backendLn, sent, false)
	assertRewrittenLogin(t, data, backendLn)

	stats := sock.StatsSnapshots()["srv-mc"]
	if stats == nil || stats.Logins != 1 || stats.TotalConnections != 1 {
		t.Fatalf("login counters wrong: %+v", stats)
	}
}

// Preserved host forwards the login bytes untouched
func TestMCLoginPreserveHostReplaysBytes(t *testing.T) {
	sock, backendLn := mcTestSocket(t, true)
	sent := mcLoginBytes(t)

	data := roundTripMC(t, sock, backendLn, sent, false)
	r := bytes.NewReader(data)
	handshake, err := mcproto.ReadHandshakePacket(r)
	if err != nil {
		t.Fatalf("backend handshake unreadable %v", err)
	}
	if handshake.ServerAddress != "play.example.com" {
		t.Fatalf("preserve host must keep the address, got %q", handshake.ServerAddress)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "login-start-payload" {
		t.Fatalf("payload must follow intact, got %q", rest)
	}
}

// Byte split handshakes cross the sniff boundary intact
func TestMCLoginSplitHandshake(t *testing.T) {
	sock, backendLn := mcTestSocket(t, false)
	sent := mcLoginBytes(t)

	data := roundTripMC(t, sock, backendLn, sent, true)
	assertRewrittenLogin(t, data, backendLn)
}

// Runs a status query and returns the parsed json
func statusQuery(t *testing.T, sock *ListenerSocket, hostname string) map[string]any {
	t.Helper()
	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("client dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))

	err = mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 767,
		ServerAddress:   hostname,
		ServerPort:      25565,
		NextState:       mcproto.NextStateStatus,
	})
	if err != nil {
		t.Fatalf("handshake write failed %v", err)
	}
	if err := mcproto.WriteFramed(client, []byte{0x00}); err != nil {
		t.Fatalf("status request write failed %v", err)
	}

	br := bufio.NewReader(client)
	length, err := mcproto.ReadVarInt(br)
	if err != nil {
		t.Fatalf("response length unreadable %v", err)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(br, data); err != nil {
		t.Fatalf("response body unreadable %v", err)
	}
	r := bytes.NewReader(data)
	packetID, err := mcproto.ReadVarInt(r)
	if err != nil || packetID != 0x00 {
		t.Fatalf("expected status response, got id %d err %v", packetID, err)
	}
	jsonLen, err := mcproto.ReadVarInt(r)
	if err != nil {
		t.Fatalf("json length unreadable %v", err)
	}
	jsonBytes := make([]byte, jsonLen)
	if _, err := io.ReadFull(r, jsonBytes); err != nil {
		t.Fatalf("json body unreadable %v", err)
	}
	var status map[string]any
	if err := json.Unmarshal(jsonBytes, &status); err != nil {
		t.Fatalf("status json invalid %v", err)
	}
	return status
}

// Offline synthetic status carries the route icon
func TestSyntheticStatusCarriesRouteIcon(t *testing.T) {
	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes([]Route{{
		ServerID:   "srv-icon",
		OwnerKind:  OwnerServer,
		OwnerID:    "srv-icon",
		Protocol:   v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:   "play.example.com",
		State:      v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE,
		Motd:       "resting",
		MaxPlayers: 7,
		Favicon:    "data:image/png;base64,AAAA",
	}})

	status := statusQuery(t, sock, "play.example.com")
	if got := status["favicon"]; got != "data:image/png;base64,AAAA" {
		t.Fatalf("favicon = %v, want the route icon", got)
	}
	desc, _ := status["description"].(map[string]any)
	if desc == nil || desc["text"] != "resting" {
		t.Fatalf("description = %v, want the route motd", status["description"])
	}
	players, _ := status["players"].(map[string]any)
	if players == nil || players["max"] != float64(7) {
		t.Fatalf("players = %v, want max 7", status["players"])
	}
}

// Offline non wakeable logins get the offline screen
func TestOfflineLoginDisconnects(t *testing.T) {
	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })
	sock.SetRoutes([]Route{{
		ServerID:  "srv-off",
		OwnerKind: OwnerServer,
		OwnerID:   "srv-off",
		Protocol:  v1.ModuleProtocol_MODULE_PROTOCOL_MINECRAFT,
		Hostname:  "play.example.com",
		State:     v1.ProxyRouteState_PROXY_ROUTE_STATE_OFFLINE,
	}})

	client, err := net.Dial("tcp", sock.listener.Addr().String())
	if err != nil {
		t.Fatalf("client dial failed %v", err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	err = mcproto.WriteHandshakePacket(client, &mcproto.HandshakePacket{
		ProtocolVersion: 767,
		ServerAddress:   "play.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	})
	if err != nil {
		t.Fatalf("handshake write failed %v", err)
	}

	br := bufio.NewReader(client)
	length, err := mcproto.ReadVarInt(br)
	if err != nil {
		t.Fatalf("disconnect length unreadable %v", err)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(br, data); err != nil {
		t.Fatalf("disconnect body unreadable %v", err)
	}
	r := bytes.NewReader(data)
	packetID, err := mcproto.ReadVarInt(r)
	if err != nil || packetID != 0x00 {
		t.Fatalf("expected login disconnect, got id %d err %v", packetID, err)
	}
	reasonLen, err := mcproto.ReadVarInt(r)
	if err != nil {
		t.Fatalf("reason length unreadable %v", err)
	}
	reason := make([]byte, reasonLen)
	if _, err := io.ReadFull(r, reason); err != nil {
		t.Fatalf("reason body unreadable %v", err)
	}
	var component map[string]any
	if err := json.Unmarshal(reason, &component); err != nil {
		t.Fatalf("reason json invalid %v", err)
	}
	flat, _ := component["text"].(string)
	if extras, ok := component["extra"].([]any); ok {
		for _, extra := range extras {
			if part, ok := extra.(map[string]any); ok {
				if txt, ok := part["text"].(string); ok {
					flat += txt
				}
			}
		}
	}
	if !strings.Contains(flat, "this server is offline") {
		t.Fatalf("reason = %q, want the offline screen", flat)
	}
}

// Routeless status replies fall back to the avatar icon
func TestSyntheticStatusFallsBackToAvatar(t *testing.T) {
	sock := NewListenerSocket(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	if err := sock.Start(); err != nil {
		t.Fatalf("socket start failed %v", err)
	}
	t.Cleanup(func() { sock.Stop() })

	status := statusQuery(t, sock, "nobody.example.com")
	if got := status["favicon"]; got != minecraft.DefaultFavicon() {
		t.Fatalf("favicon must be the avatar fallback, got %v", got)
	}
}
