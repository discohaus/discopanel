// Package puppet joins the lobby as a headless client
package puppet

import (
	"bytes"
	"fmt"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Sends the client information body both states share
func writeClientInfo(w io.Writer, threshold int, packetID int32, protocol int32) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(packetID))
	packet.WriteString(&body, "en_us")
	body.WriteByte(8)
	mcproto.WriteVarInt(&body, 0)
	packet.WriteBool(&body, true)
	body.WriteByte(0x7F)
	mcproto.WriteVarInt(&body, 1)
	packet.WriteBool(&body, false)
	packet.WriteBool(&body, true)
	if protocol >= 768 {
		mcproto.WriteVarInt(&body, 2)
	}
	return packet.WriteFrameZ(w, body.Bytes(), threshold)
}

// Walks the config phase and returns at the play switch
func (c *Client) runConfig() error {
	if err := writeClientInfo(c.conn, c.threshold, mcproto.CfgSBClientInfo, c.protocol); err != nil {
		return fmt.Errorf("client info failed: %w", err)
	}
	var brand bytes.Buffer
	mcproto.WriteVarInt(&brand, mcproto.CfgSBPluginMessage)
	packet.WriteString(&brand, "minecraft:brand")
	packet.WriteString(&brand, "vanilla")
	if err := packet.WriteFrameZ(c.conn, brand.Bytes(), c.threshold); err != nil {
		return fmt.Errorf("brand failed: %w", err)
	}

	for {
		frame, err := packet.ReadFrameZ(c.conn, c.threshold)
		if err != nil {
			return fmt.Errorf("config read failed: %w", err)
		}
		buf := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return err
		}

		switch pid {
		case mcproto.CfgCBFinish:
			var ack bytes.Buffer
			mcproto.WriteVarInt(&ack, mcproto.CfgSBFinishAck)
			return packet.WriteFrameZ(c.conn, ack.Bytes(), c.threshold)

		case mcproto.CfgCBDisconnect:
			return fmt.Errorf("lobby dropped the puppet during config")

		case mcproto.CfgCBKeepAlive:
			var id int64
			if err := packet.ReadNum(buf, &id); err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.CfgSBKeepAlive)
			packet.WriteNum(&resp, id)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case mcproto.CfgCBPing:
			var id int32
			if err := packet.ReadNum(buf, &id); err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.CfgSBPong)
			packet.WriteNum(&resp, id)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case mcproto.CfgCBKnownPacks:
			// Echo keeps registries on the client side
			rest := make([]byte, buf.Len())
			io.ReadFull(buf, rest)
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.CfgSBKnownPacks)
			resp.Write(rest)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case mcproto.CfgCBCookieRequest:
			key, err := packet.ReadString(buf)
			if err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.CfgSBCookieResponse)
			packet.WriteString(&resp, key)
			packet.WriteBool(&resp, false)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case mcproto.CfgCBAddRespack:
			id, err := packet.ReadUUID(buf)
			if err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.CfgSBRespackAnswer)
			packet.WriteUUID(&resp, id)
			mcproto.WriteVarInt(&resp, 1)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		default:
			// Registries tags flags and friends pass silently
		}
	}
}
