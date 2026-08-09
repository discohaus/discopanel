package puppet

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Angle byte widened back to degrees
func angleDegrees(b byte) float32 {
	return float32(b) * 360.0 / 256.0
}

// Unpacks a modern packed block position
func unpackPosition(v uint64) (int, int, int) {
	x := int(int64(v) >> 38)
	y := int(int64(v) << 52 >> 52)
	z := int(int64(v) << 26 >> 38)
	return x, y, z
}

// Reads lobby packets until the connection dies
func (c *Client) readLoop() {
	defer close(c.events)
	defer c.Close()
	for {
		frame, err := packet.ReadFrameZ(c.conn, c.threshold)
		if err != nil {
			c.markFailed(err)
			return
		}
		c.handleFrame(frame)
	}
}

// Dispatches one play frame from the lobby
func (c *Client) handleFrame(frame []byte) {
	buf := bytes.NewReader(frame)
	pid, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	id := int32(pid)

	switch id {
	case c.ids.KeepAliveCB:
		var ka int64
		if packet.ReadNum(buf, &ka) == nil {
			var resp bytes.Buffer
			mcproto.WriteVarInt(&resp, mcproto.VarInt(c.ids.KeepAliveSB))
			packet.WriteNum(&resp, ka)
			c.send(resp.Bytes())
		}

	case c.ids.JoinGame:
		var eid int32
		if packet.ReadNum(buf, &eid) == nil {
			c.entityID = eid
		}

	case c.ids.SyncPlayerPos:
		c.handleSyncPos(buf)

	case c.ids.PlayerInfoUpdate:
		c.handlePlayerInfo(buf)

	case c.ids.PlayerInfoRemove:
		count, err := mcproto.ReadVarInt(buf)
		if err != nil || count < 0 || count > 1024 {
			return
		}
		for range int(count) {
			id, err := packet.ReadUUID(buf)
			if err != nil {
				return
			}
			if id != c.self {
				c.emit(family.EvPlayerRemove{UUID: id})
			}
		}

	case c.ids.AddEntity:
		c.handleAddEntity(buf)

	case c.ids.EntityPos:
		c.handleRelMove(buf, false)

	case c.ids.EntityPosRot:
		c.handleRelMove(buf, true)

	case c.ids.EntityRot:
		eid, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return
		}
		yaw, pitch, err := readAngles(buf)
		if err != nil {
			return
		}
		c.updateEntity(int32(eid), func(p *family.Pos) {
			p.Yaw, p.Pitch = yaw, pitch
		})

	case c.ids.EntityHeadLook:
		eid, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return
		}
		var head [1]byte
		if _, err := io.ReadFull(buf, head[:]); err != nil {
			return
		}
		c.updateEntity(int32(eid), func(p *family.Pos) {
			p.Yaw = angleDegrees(head[0])
		})

	case c.ids.EntityTeleport:
		c.handleAbsMove(buf, c.ids.WideTeleport)

	case c.ids.EntityPosSync:
		if c.ids.EntityPosSync >= 0 {
			c.handlePosSync(buf)
		}

	case c.ids.RemoveEntities:
		count, err := mcproto.ReadVarInt(buf)
		if err != nil || count < 0 || count > 4096 {
			return
		}
		removed := []int32{}
		for range int(count) {
			eid, err := mcproto.ReadVarInt(buf)
			if err != nil {
				return
			}
			if _, tracked := c.entities[int32(eid)]; tracked {
				removed = append(removed, int32(eid))
				delete(c.entities, int32(eid))
			}
		}
		if len(removed) > 0 {
			c.emit(family.EvEntityRemove{IDs: removed})
		}

	case c.ids.SystemChat:
		component, err := packet.ReadNetworkNBT(buf)
		if err != nil {
			return
		}
		overlay, err := packet.ReadBool(buf)
		if err != nil || overlay {
			return
		}
		if text := packet.ComponentText(component); text != "" {
			c.emit(family.EvChat{Text: text})
		}

	case c.ids.PlayerChat:
		c.handlePlayerChat(buf)

	case c.ids.Transfer:
		host, err := packet.ReadString(buf)
		if err != nil {
			return
		}
		port, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return
		}
		c.emit(family.EvTransfer{Host: host, Port: int(port)})

	case c.ids.DisconnectCB:
		component, err := packet.ReadNetworkNBT(buf)
		reason := "the lobby closed the connection"
		if err == nil {
			if text := packet.ComponentText(component); text != "" {
				reason = text
			}
		}
		c.emit(family.EvDisconnect{Reason: reason})
		c.Close()

	case c.ids.BlockUpdate:
		var raw uint64
		if packet.ReadNum(buf, &raw) != nil {
			return
		}
		state, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return
		}
		name, known := c.cfg.StateNames[int32(state)]
		if !known {
			return
		}
		x, y, z := unpackPosition(raw)
		c.emit(family.EvBlockChange{X: x, Y: y, Z: z, Block: name})

	case c.ids.BlockEntityData:
		var raw uint64
		if packet.ReadNum(buf, &raw) != nil {
			return
		}
		kind, err := mcproto.ReadVarInt(buf)
		if err != nil || int32(kind) != family.ModernSignEntity {
			return
		}
		data, err := packet.ReadNetworkNBT(buf)
		if err != nil {
			return
		}
		lines, ok := signLines(data, c.protocol)
		if !ok {
			return
		}
		x, y, z := unpackPosition(raw)
		c.emit(family.EvSignText{X: x, Y: y, Z: z, Lines: lines})
	}
}

// Front text lines out of one sign entity
func signLines(data any, protocol int32) ([4]string, bool) {
	root, ok := data.(map[string]any)
	if !ok {
		return [4]string{}, false
	}
	front, ok := root["front_text"].(map[string]any)
	if !ok {
		return [4]string{}, false
	}
	raw, ok := front["messages"].([]any)
	if !ok {
		return [4]string{}, false
	}
	var lines [4]string
	for i, m := range raw {
		if i >= len(lines) {
			break
		}
		// Older groups wrap each line in json
		if s, isText := m.(string); isText && protocol < 770 {
			var comp any
			if json.Unmarshal([]byte(s), &comp) == nil {
				lines[i] = packet.ComponentText(comp)
				continue
			}
		}
		lines[i] = packet.ComponentText(m)
	}
	return lines, true
}

// Applies one sync and answers the teleport
func (c *Client) handleSyncPos(buf *bytes.Reader) {
	var teleportID mcproto.VarInt
	var x, y, z float64
	var yaw, pitch float32
	var flags int32
	var err error

	if c.ids.WideTeleport {
		if teleportID, err = mcproto.ReadVarInt(buf); err != nil {
			return
		}
		var vel float64
		if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
			return
		}
		for range 3 {
			if packet.ReadNum(buf, &vel) != nil {
				return
			}
		}
		if packet.ReadNum(buf, &yaw) != nil || packet.ReadNum(buf, &pitch) != nil {
			return
		}
		if packet.ReadNum(buf, &flags) != nil {
			return
		}
	} else {
		if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
			return
		}
		if packet.ReadNum(buf, &yaw) != nil || packet.ReadNum(buf, &pitch) != nil {
			return
		}
		var narrow [1]byte
		if _, err := io.ReadFull(buf, narrow[:]); err != nil {
			return
		}
		flags = int32(narrow[0])
		if teleportID, err = mcproto.ReadVarInt(buf); err != nil {
			return
		}
	}

	pos := c.pos
	if flags&0x01 != 0 {
		pos.X += x
	} else {
		pos.X = x
	}
	if flags&0x02 != 0 {
		pos.Y += y
	} else {
		pos.Y = y
	}
	if flags&0x04 != 0 {
		pos.Z += z
	} else {
		pos.Z = z
	}
	if flags&0x08 != 0 {
		pos.Yaw += yaw
	} else {
		pos.Yaw = yaw
	}
	if flags&0x10 != 0 {
		pos.Pitch += pitch
	} else {
		pos.Pitch = pitch
	}
	pos.OnGround = true
	c.pos = pos

	var confirm bytes.Buffer
	mcproto.WriteVarInt(&confirm, mcproto.VarInt(c.ids.TeleportConfirm))
	mcproto.WriteVarInt(&confirm, teleportID)
	c.send(confirm.Bytes())
	c.Move(pos)

	c.markReady()
	c.emit(family.EvTeleportSelf{Pos: pos})
}

// Parses info updates and surfaces new players
func (c *Client) handlePlayerInfo(buf *bytes.Reader) {
	var actions [1]byte
	if _, err := io.ReadFull(buf, actions[:]); err != nil {
		return
	}
	count, err := mcproto.ReadVarInt(buf)
	if err != nil || count < 0 || count > 1024 {
		return
	}

	for range int(count) {
		id, err := packet.ReadUUID(buf)
		if err != nil {
			return
		}
		entry := family.PlayerEntry{UUID: id}
		added := false

		if actions[0]&0x01 != 0 {
			name, err := packet.ReadString(buf)
			if err != nil {
				return
			}
			entry.Name = name
			propCount, err := mcproto.ReadVarInt(buf)
			if err != nil || propCount < 0 || propCount > 64 {
				return
			}
			for range int(propCount) {
				pname, err := packet.ReadString(buf)
				if err != nil {
					return
				}
				pvalue, err := packet.ReadString(buf)
				if err != nil {
					return
				}
				signed, err := packet.ReadBool(buf)
				if err != nil {
					return
				}
				prop := mojang.Property{Name: pname, Value: pvalue}
				if signed {
					if prop.Signature, err = packet.ReadString(buf); err != nil {
						return
					}
				}
				entry.Properties = append(entry.Properties, prop)
			}
			added = true
		}
		if actions[0]&0x02 != 0 {
			present, err := packet.ReadBool(buf)
			if err != nil {
				return
			}
			if present {
				if _, err := packet.ReadUUID(buf); err != nil {
					return
				}
				var expiry int64
				if packet.ReadNum(buf, &expiry) != nil {
					return
				}
				if _, err := packet.ReadVarBytes(buf, 512); err != nil {
					return
				}
				if _, err := packet.ReadVarBytes(buf, 4096); err != nil {
					return
				}
			}
		}
		if actions[0]&0x04 != 0 {
			if _, err := mcproto.ReadVarInt(buf); err != nil {
				return
			}
		}
		if actions[0]&0x08 != 0 {
			if _, err := packet.ReadBool(buf); err != nil {
				return
			}
		}
		if actions[0]&0x10 != 0 {
			if _, err := mcproto.ReadVarInt(buf); err != nil {
				return
			}
		}
		if actions[0]&0x20 != 0 {
			present, err := packet.ReadBool(buf)
			if err != nil {
				return
			}
			if present {
				if _, err := packet.ReadNetworkNBT(buf); err != nil {
					return
				}
			}
		}
		if actions[0]&0x40 != 0 && c.protocol >= 768 {
			if _, err := mcproto.ReadVarInt(buf); err != nil {
				return
			}
		}
		if actions[0]&0x80 != 0 && c.protocol >= 769 {
			if _, err := packet.ReadBool(buf); err != nil {
				return
			}
		}

		if added && id != c.self {
			c.emit(family.EvPlayerAdd{Entry: entry})
		}
	}
}

// Surfaces player entities appearing in the hub
func (c *Client) handleAddEntity(buf *bytes.Reader) {
	eid, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	id, err := packet.ReadUUID(buf)
	if err != nil {
		return
	}
	entityType, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	var x, y, z float64
	if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
		return
	}
	if c.ids.LpVelocity {
		if _, _, _, err := packet.ReadLpVec3(buf); err != nil {
			return
		}
	}
	var pitchB, yawB [1]byte
	if _, err := io.ReadFull(buf, pitchB[:]); err != nil {
		return
	}
	if _, err := io.ReadFull(buf, yawB[:]); err != nil {
		return
	}

	if int32(entityType) != c.ids.PlayerTypeID || id == c.self {
		return
	}
	pos := family.Pos{
		X: x, Y: y, Z: z,
		Yaw: angleDegrees(yawB[0]), Pitch: angleDegrees(pitchB[0]),
	}
	c.entities[int32(eid)] = pos
	c.emit(family.EvSpawnPlayer{EntityID: int32(eid), UUID: id, Pos: pos})
}

// Applies one relative move to a tracked entity
func (c *Client) handleRelMove(buf *bytes.Reader, withLook bool) {
	eid, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	var dx, dy, dz int16
	if packet.ReadNum(buf, &dx) != nil || packet.ReadNum(buf, &dy) != nil || packet.ReadNum(buf, &dz) != nil {
		return
	}
	var yaw, pitch float32
	hasLook := false
	if withLook {
		if yaw, pitch, err = readAngles(buf); err != nil {
			return
		}
		hasLook = true
	}
	c.updateEntity(int32(eid), func(p *family.Pos) {
		p.X += float64(dx) / 4096.0
		p.Y += float64(dy) / 4096.0
		p.Z += float64(dz) / 4096.0
		if hasLook {
			p.Yaw, p.Pitch = yaw, pitch
		}
	})
}

// Applies one absolute move to a tracked entity
func (c *Client) handleAbsMove(buf *bytes.Reader, wide bool) {
	eid, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	var x, y, z float64
	if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
		return
	}
	var yaw, pitch float32
	if wide {
		var vel float64
		for range 3 {
			if packet.ReadNum(buf, &vel) != nil {
				return
			}
		}
		if packet.ReadNum(buf, &yaw) != nil || packet.ReadNum(buf, &pitch) != nil {
			return
		}
	} else {
		if yaw, pitch, err = readAngles(buf); err != nil {
			return
		}
	}
	c.updateEntity(int32(eid), func(p *family.Pos) {
		p.X, p.Y, p.Z = x, y, z
		p.Yaw, p.Pitch = yaw, pitch
	})
}

// Applies one position sync to a tracked entity
func (c *Client) handlePosSync(buf *bytes.Reader) {
	c.handleAbsMove(buf, true)
}

// Extracts speaker and text from signed chat
func (c *Client) handlePlayerChat(buf *bytes.Reader) {
	if _, err := packet.ReadUUID(buf); err != nil {
		return
	}
	if _, err := mcproto.ReadVarInt(buf); err != nil {
		return
	}
	signed, err := packet.ReadBool(buf)
	if err != nil {
		return
	}
	if signed {
		sig := make([]byte, 256)
		if _, err := io.ReadFull(buf, sig); err != nil {
			return
		}
	}
	message, err := packet.ReadString(buf)
	if err != nil {
		return
	}
	var ts, salt int64
	if packet.ReadNum(buf, &ts) != nil || packet.ReadNum(buf, &salt) != nil {
		return
	}
	prevCount, err := mcproto.ReadVarInt(buf)
	if err != nil || prevCount < 0 || prevCount > 20 {
		return
	}
	for range int(prevCount) {
		msgID, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return
		}
		if msgID == 0 {
			sig := make([]byte, 256)
			if _, err := io.ReadFull(buf, sig); err != nil {
				return
			}
		}
	}
	hasUnsigned, err := packet.ReadBool(buf)
	if err != nil {
		return
	}
	if hasUnsigned {
		if _, err := packet.ReadNetworkNBT(buf); err != nil {
			return
		}
	}
	filterType, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return
	}
	if filterType == 2 {
		bits, err := mcproto.ReadVarInt(buf)
		if err != nil || bits < 0 || bits > 128 {
			return
		}
		var long int64
		for range int(bits) {
			if packet.ReadNum(buf, &long) != nil {
				return
			}
		}
	}
	chatType, err := mcproto.ReadVarInt(buf)
	if err != nil || chatType == 0 {
		return
	}
	senderName, err := packet.ReadNetworkNBT(buf)
	if err != nil {
		return
	}

	name := packet.ComponentText(senderName)
	if name == "" || name == c.cfg.Name {
		return
	}
	c.emit(family.EvChat{Text: "<" + name + "> " + message})
}

// Mutates one tracked entity then reports it
func (c *Client) updateEntity(eid int32, mutate func(*family.Pos)) {
	pos, tracked := c.entities[eid]
	if !tracked {
		return
	}
	mutate(&pos)
	c.entities[eid] = pos
	c.emit(family.EvEntityMove{EntityID: eid, Pos: pos})
}

// Extracts two angle bytes as degrees
func readAngles(buf *bytes.Reader) (float32, float32, error) {
	var raw [2]byte
	if _, err := io.ReadFull(buf, raw[:]); err != nil {
		return 0, 0, err
	}
	return angleDegrees(raw[0]), angleDegrees(raw[1]), nil
}
