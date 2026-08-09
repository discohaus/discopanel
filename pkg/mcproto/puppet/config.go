// Package puppet joins the lobby as a headless client
package puppet

import (
	"bytes"
	"fmt"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Config clientbound ids, stable across the modern group
const (
	cfgCBCookieRequest = 0x00
	cfgCBPluginMessage = 0x01
	cfgCBDisconnect    = 0x02
	cfgCBFinish        = 0x03
	cfgCBKeepAlive     = 0x04
	cfgCBPing          = 0x05
	cfgCBRegistryData  = 0x07
	cfgCBAddRespack    = 0x09
	cfgCBFeatureFlags  = 0x0c
	cfgCBUpdateTags    = 0x0d
	cfgCBKnownPacks    = 0x0e
)

// Config serverbound ids, stable across the modern group
const (
	cfgSBClientInfo     = 0x00
	cfgSBCookieResponse = 0x01
	cfgSBPluginMessage  = 0x02
	cfgSBFinishAck      = 0x03
	cfgSBKeepAlive      = 0x04
	cfgSBPong           = 0x05
	cfgSBRespackAnswer  = 0x06
	cfgSBKnownPacks     = 0x07
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
	if err := writeClientInfo(c.conn, c.threshold, cfgSBClientInfo, c.protocol); err != nil {
		return fmt.Errorf("client info failed: %w", err)
	}
	var brand bytes.Buffer
	mcproto.WriteVarInt(&brand, cfgSBPluginMessage)
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
		case cfgCBFinish:
			var ack bytes.Buffer
			mcproto.WriteVarInt(&ack, cfgSBFinishAck)
			return packet.WriteFrameZ(c.conn, ack.Bytes(), c.threshold)

		case cfgCBDisconnect:
			return fmt.Errorf("lobby dropped the puppet during config")

		case cfgCBKeepAlive:
			var id int64
			if err := packet.ReadNum(buf, &id); err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, cfgSBKeepAlive)
			packet.WriteNum(&resp, id)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case cfgCBPing:
			var id int32
			if err := packet.ReadNum(buf, &id); err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, cfgSBPong)
			packet.WriteNum(&resp, id)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case cfgCBKnownPacks:
			// Echo keeps registries on the client side
			rest := make([]byte, buf.Len())
			io.ReadFull(buf, rest)
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, cfgSBKnownPacks)
			resp.Write(rest)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case cfgCBCookieRequest:
			key, err := packet.ReadString(buf)
			if err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, cfgSBCookieResponse)
			packet.WriteString(&resp, key)
			packet.WriteBool(&resp, false)
			if err := packet.WriteFrameZ(c.conn, resp.Bytes(), c.threshold); err != nil {
				return err
			}

		case cfgCBAddRespack:
			id, err := packet.ReadUUID(buf)
			if err != nil {
				return err
			}
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, cfgSBRespackAnswer)
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
