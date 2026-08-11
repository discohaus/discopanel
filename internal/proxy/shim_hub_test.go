package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/hub"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Tiny platform grid for hub join tests
const hubTestGrid = `{
	"version": 1,
	"spawn_x": 0.5, "spawn_y": -59, "spawn_z": 0.5,
	"min_y": -64,
	"fills": [
		{"x1": -4, "y1": -60, "z1": -4, "x2": 4, "y2": -60, "z2": 4, "block": "polished_diorite"}
	]
}`

// Login and config ids the fake lobby speaks
const (
	lobbyLoginStartID    = 0x00
	lobbyLoginSuccessID  = 0x02
	lobbyLoginAckID      = 0x03
	lobbyCfgClientInfo   = 0x00
	lobbyCfgFinish       = 0x03
	lobbyCfgFinishAck    = 0x03
	lobbyCfgKnownPacks   = 0x0e
	lobbyCfgKnownPacksSB = 0x07
)

// Confirms the first offered pack like a real client
func confirmFirstPack(w io.Writer, buf *bytes.Reader) error {
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
	mcproto.WriteVarInt(&resp, lobbyCfgKnownPacksSB)
	mcproto.WriteVarInt(&resp, 1)
	packet.WriteString(&resp, ns)
	packet.WriteString(&resp, id)
	packet.WriteString(&resp, ver)
	return packet.WriteFrame(w, resp.Bytes())
}

// Fake vanilla lobby hosting exactly one puppet
func fakeModernLobby(t *testing.T, ln net.Listener, protocol int32) chan error {
	t.Helper()
	ids := family.ModernIDsFor(protocol)
	done := make(chan error, 4)

	go func() {
		defer close(done)
		fail := func(err error) { done <- err }

		conn, err := ln.Accept()
		if err != nil {
			fail(fmt.Errorf("lobby accept: %w", err))
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		if _, err := mcproto.ReadHandshakePacket(conn); err != nil {
			fail(fmt.Errorf("lobby handshake: %w", err))
			return
		}
		frame, err := packet.ReadFrame(conn)
		if err != nil {
			fail(fmt.Errorf("login start read: %w", err))
			return
		}
		buf := bytes.NewReader(frame)
		if pid, _ := mcproto.ReadVarInt(buf); int32(pid) != lobbyLoginStartID {
			fail(fmt.Errorf("login start id %d", pid))
			return
		}
		name, err := packet.ReadString(buf)
		if err != nil {
			fail(fmt.Errorf("login name: %w", err))
			return
		}
		id, err := packet.ReadUUID(buf)
		if err != nil {
			fail(fmt.Errorf("login uuid: %w", err))
			return
		}

		var success bytes.Buffer
		mcproto.WriteVarInt(&success, lobbyLoginSuccessID)
		packet.WriteUUID(&success, id)
		packet.WriteString(&success, name)
		mcproto.WriteVarInt(&success, 0)
		if ids.StrictFlag {
			packet.WriteBool(&success, false)
		}
		if err := packet.WriteFrame(conn, success.Bytes()); err != nil {
			fail(err)
			return
		}

		frame, err = packet.ReadFrame(conn)
		if err != nil {
			fail(fmt.Errorf("login ack read: %w", err))
			return
		}
		buf = bytes.NewReader(frame)
		if pid, _ := mcproto.ReadVarInt(buf); int32(pid) != lobbyLoginAckID {
			fail(fmt.Errorf("login ack id %d", pid))
			return
		}

		var fin bytes.Buffer
		mcproto.WriteVarInt(&fin, lobbyCfgFinish)
		if err := packet.WriteFrame(conn, fin.Bytes()); err != nil {
			fail(err)
			return
		}
		for {
			frame, err = packet.ReadFrame(conn)
			if err != nil {
				fail(fmt.Errorf("config read: %w", err))
				return
			}
			buf = bytes.NewReader(frame)
			pid, _ := mcproto.ReadVarInt(buf)
			if int32(pid) == lobbyCfgFinishAck {
				break
			}
		}

		var join bytes.Buffer
		mcproto.WriteVarInt(&join, mcproto.VarInt(ids.JoinGame))
		packet.WriteNum(&join, int32(77))
		if err := packet.WriteFrame(conn, join.Bytes()); err != nil {
			fail(err)
			return
		}

		var park bytes.Buffer
		mcproto.WriteVarInt(&park, mcproto.VarInt(ids.SyncPlayerPos))
		mcproto.WriteVarInt(&park, 1)
		packet.WriteNum(&park, float64(0.5))
		packet.WriteNum(&park, float64(-59))
		packet.WriteNum(&park, float64(0.5))
		for range 3 {
			packet.WriteNum(&park, float64(0))
		}
		packet.WriteNum(&park, float32(180))
		packet.WriteNum(&park, float32(0))
		packet.WriteNum(&park, int32(0))
		if err := packet.WriteFrame(conn, park.Bytes()); err != nil {
			fail(err)
			return
		}

		// Puppet stays parked until the shim side ends
		for {
			if _, err := packet.ReadFrame(conn); err != nil {
				return
			}
		}
	}()
	return done
}

// Walks the shim join from login success onward
func hubShimWalk(r io.Reader, w io.Writer, protocol int32, hook func(pid int32, buf *bytes.Reader) error) (int, error) {
	ids := family.ModernIDsFor(protocol)

	frame, err := packet.ReadFrame(r)
	if err != nil {
		return 0, fmt.Errorf("login success read: %w", err)
	}
	if pid, _ := mcproto.ReadVarInt(bytes.NewReader(frame)); int32(pid) != lobbyLoginSuccessID {
		return 0, fmt.Errorf("login success id %d", pid)
	}
	var ack bytes.Buffer
	mcproto.WriteVarInt(&ack, lobbyLoginAckID)
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
		if hook != nil {
			if err := hook(int32(pid), buf); err != nil {
				return 0, err
			}
		}
		if int32(pid) == lobbyCfgKnownPacks {
			if err := confirmFirstPack(w, buf); err != nil {
				return 0, err
			}
		}
		if int32(pid) == lobbyCfgFinish {
			break
		}
	}
	var fin bytes.Buffer
	mcproto.WriteVarInt(&fin, lobbyCfgFinishAck)
	if err := packet.WriteFrame(w, fin.Bytes()); err != nil {
		return 0, err
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

// Plain client walking the shim rendered hub join
func hubShimClient(t *testing.T, addr string, protocol int32, hook func(pid int32, buf *bytes.Reader) error) (int, error) {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: mcproto.VarInt(protocol),
		ServerAddress:   "hub.example.com",
		ServerPort:      25565,
		NextState:       mcproto.NextStateLogin,
	}); err != nil {
		return 0, err
	}
	var start bytes.Buffer
	mcproto.WriteVarInt(&start, 0x00)
	packet.WriteString(&start, "Steve")
	packet.WriteUUID(&start, [16]byte{9})
	if err := packet.WriteFrame(conn, start.Bytes()); err != nil {
		return 0, err
	}

	return hubShimWalk(conn, conn, protocol, hook)
}

// Installs the hub world and puppet dialer
func hubbedSocket(t *testing.T, online bool) (*ListenerSocket, net.Listener) {
	t.Helper()
	sock, lobbyLn, sh := mediationSocket(t, online)

	grid, err := hub.Parse([]byte(hubTestGrid))
	if err != nil {
		t.Fatalf("grid parse failed %v", err)
	}
	sh.SetGridSource(func() *hub.Grid { return grid })
	sh.SetPuppetDialer(dialLobbyPuppet)
	return sock, lobbyLn
}

// Drains the lobby side failing on any error
func expectLobbyClean(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lobby saw %v", err)
		}
	default:
	}
}

// Version mismatched client rides a puppet into the hub
func TestShimHubJoinOlderClient(t *testing.T) {
	sock, lobbyLn := hubbedSocket(t, false)
	lobbyDone := fakeModernLobby(t, lobbyLn, 772)

	chunks, err := hubShimClient(t, sock.listener.Addr().String(), 766, nil)
	if err != nil {
		t.Fatalf("shim client failed %v", err)
	}
	if chunks != 16 {
		t.Fatalf("client saw %d chunks, want 16", chunks)
	}
	expectLobbyClean(t, lobbyDone)
}

// Matched version clients mirror too, never splicing
func TestShimHubJoinMatchedProtocol(t *testing.T) {
	sock, lobbyLn := hubbedSocket(t, false)
	lobbyDone := fakeModernLobby(t, lobbyLn, 772)

	chunks, err := hubShimClient(t, sock.listener.Addr().String(), 772, nil)
	if err != nil {
		t.Fatalf("shim client failed %v", err)
	}
	if chunks != 16 {
		t.Fatalf("client saw %d chunks, want 16", chunks)
	}
	expectLobbyClean(t, lobbyDone)
}
