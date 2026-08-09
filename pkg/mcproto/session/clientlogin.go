package session

import (
	"bytes"
	"fmt"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Login clientbound ids for the modern group
const (
	clientLoginDisconnect    = 0x00
	clientLoginEncryption    = 0x01
	clientLoginSuccess       = 0x02
	clientLoginCompress      = 0x03
	clientLoginPluginRequest = 0x04
	clientLoginCookieRequest = 0x05
)

// Login serverbound ids for the modern group
const (
	serverLoginStart          = 0x00
	serverLoginPluginResponse = 0x02
	serverLoginAcknowledged   = 0x03
	serverLoginCookieResponse = 0x04
)

// Oldest protocol the puppet login speaks
const ClientLoginFloor = 766

// Streams and identity settled by a client login
type ClientLoginResult struct {
	UUID      [16]byte
	Name      string
	Threshold int
}

// Joins an offline backend up to the config switch
func LoginAsClient(conn io.ReadWriter, protocol int32, host string, port uint16, name string, id [16]byte) (*ClientLoginResult, error) {
	if protocol < ClientLoginFloor {
		return nil, fmt.Errorf("puppet login needs protocol %d plus, got %d", ClientLoginFloor, protocol)
	}

	err := mcproto.WriteHandshakePacket(conn, &mcproto.HandshakePacket{
		ProtocolVersion: mcproto.VarInt(protocol),
		ServerAddress:   host,
		ServerPort:      port,
		NextState:       mcproto.NextStateLogin,
	})
	if err != nil {
		return nil, fmt.Errorf("handshake write failed: %w", err)
	}

	var start bytes.Buffer
	mcproto.WriteVarInt(&start, serverLoginStart)
	packet.WriteString(&start, name)
	packet.WriteUUID(&start, id)
	if err := packet.WriteFrame(conn, start.Bytes()); err != nil {
		return nil, fmt.Errorf("login start write failed: %w", err)
	}

	threshold := -1
	for {
		frame, err := packet.ReadFrameZ(conn, threshold)
		if err != nil {
			return nil, fmt.Errorf("login frame read failed: %w", err)
		}
		buf := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return nil, err
		}

		switch pid {
		case clientLoginDisconnect:
			reason, _ := packet.ReadString(buf)
			return nil, fmt.Errorf("backend refused login: %s", reason)

		case clientLoginEncryption:
			return nil, fmt.Errorf("backend wants encryption, needs offline mode")

		case clientLoginCompress:
			t, err := mcproto.ReadVarInt(buf)
			if err != nil {
				return nil, err
			}
			threshold = int(t)

		case clientLoginPluginRequest:
			msgID, err := mcproto.ReadVarInt(buf)
			if err != nil {
				return nil, err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, serverLoginPluginResponse)
			mcproto.WriteVarInt(&resp, msgID)
			packet.WriteBool(&resp, false)
			if err := packet.WriteFrameZ(conn, resp.Bytes(), threshold); err != nil {
				return nil, err
			}

		case clientLoginCookieRequest:
			key, err := packet.ReadString(buf)
			if err != nil {
				return nil, err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, serverLoginCookieResponse)
			packet.WriteString(&resp, key)
			packet.WriteBool(&resp, false)
			if err := packet.WriteFrameZ(conn, resp.Bytes(), threshold); err != nil {
				return nil, err
			}

		case clientLoginSuccess:
			resultID, err := packet.ReadUUID(buf)
			if err != nil {
				return nil, err
			}
			resultName, err := packet.ReadString(buf)
			if err != nil {
				return nil, err
			}
			var ack bytes.Buffer
			mcproto.WriteVarInt(&ack, serverLoginAcknowledged)
			if err := packet.WriteFrameZ(conn, ack.Bytes(), threshold); err != nil {
				return nil, err
			}
			return &ClientLoginResult{UUID: resultID, Name: resultName, Threshold: threshold}, nil

		default:
			return nil, fmt.Errorf("unexpected login packet %d", pid)
		}
	}
}
