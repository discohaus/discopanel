package session

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Offline backend walking the whole login script
func fakeOfflineBackend(t *testing.T, conn net.Conn, sendCompress bool) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		handshake, err := mcproto.ReadHandshakePacket(conn)
		if err != nil {
			done <- err
			return
		}
		if handshake.NextState != mcproto.NextStateLogin {
			done <- err
			return
		}
		ls, err := mcproto.ReadLoginStart(conn)
		if err != nil {
			done <- err
			return
		}

		threshold := -1
		if sendCompress {
			var compress bytes.Buffer
			mcproto.WriteVarInt(&compress, clientLoginCompress)
			mcproto.WriteVarInt(&compress, 256)
			if err := packet.WriteFrame(conn, compress.Bytes()); err != nil {
				done <- err
				return
			}
			threshold = 256
		}

		// Plugin request probes the response path
		var plugin bytes.Buffer
		mcproto.WriteVarInt(&plugin, clientLoginPluginRequest)
		mcproto.WriteVarInt(&plugin, 7)
		packet.WriteString(&plugin, "fml:handshake")
		if err := packet.WriteFrameZ(conn, plugin.Bytes(), threshold); err != nil {
			done <- err
			return
		}
		resp, err := packet.ReadFrameZ(conn, threshold)
		if err != nil {
			done <- err
			return
		}
		rb := bytes.NewReader(resp)
		if id, _ := mcproto.ReadVarInt(rb); id != serverLoginPluginResponse {
			done <- err
			return
		}

		var success bytes.Buffer
		mcproto.WriteVarInt(&success, clientLoginSuccess)
		packet.WriteUUID(&success, mojang.OfflineUUID(ls.Name))
		packet.WriteString(&success, ls.Name)
		mcproto.WriteVarInt(&success, 0)
		if err := packet.WriteFrameZ(conn, success.Bytes(), threshold); err != nil {
			done <- err
			return
		}

		ack, err := packet.ReadFrameZ(conn, threshold)
		if err != nil {
			done <- err
			return
		}
		ab := bytes.NewReader(ack)
		if id, _ := mcproto.ReadVarInt(ab); id != serverLoginAcknowledged {
			done <- err
			return
		}
		done <- nil
	}()
	return done
}

// Puppet login lands identity and threshold both modes
func TestLoginAsClient(t *testing.T) {
	for _, compress := range []bool{false, true} {
		server, client := net.Pipe()
		backendDone := fakeOfflineBackend(t, server, compress)

		client.SetDeadline(time.Now().Add(5 * time.Second))
		result, err := LoginAsClient(client, 772, "localhost", 25565, "Steve", mojang.OfflineUUID("Steve"))
		if err != nil {
			t.Fatalf("compress %v login failed %v", compress, err)
		}
		if result.Name != "Steve" {
			t.Fatalf("name = %q", result.Name)
		}
		wantThreshold := -1
		if compress {
			wantThreshold = 256
		}
		if result.Threshold != wantThreshold {
			t.Fatalf("threshold = %d, want %d", result.Threshold, wantThreshold)
		}
		if err := <-backendDone; err != nil {
			t.Fatalf("backend script failed %v", err)
		}
		server.Close()
		client.Close()
	}
}

// Old protocols get refused up front
func TestLoginAsClientFloor(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	if _, err := LoginAsClient(client, 340, "localhost", 25565, "Steve", mojang.OfflineUUID("Steve")); err == nil {
		t.Fatal("old protocol must fail")
	}
}
