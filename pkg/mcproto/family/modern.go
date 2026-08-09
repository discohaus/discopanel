package family

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/hub"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Login ids the codec speaks after auth
const (
	modernLoginSuccess = 0x02
	modernLoginAck     = 0x03
)

// Client config frames tolerated before the ack
const maxConfigFrames = 32

// Game event id starting the chunk wait
const gameEventStartChunks = 13

// Modern family codec covering 766 through 772
type modernCodec struct{}

// Codec registers itself at package load
func init() {
	Register(modernCodec{})
}

// Protocol numbers this codec speaks
func (modernCodec) Protocols() []int32 {
	var out []int32
	for p := int32(ModernFloor); p <= ModernCeiling; p++ {
		out = append(out, p)
	}
	return out
}

// Shallow legacy worlds lift the hub above zero
func (modernCodec) YOffset(protocol int32) int {
	if protocol <= 756 {
		return legacyYOffset
	}
	return 0
}

// Bakes framed chunk packets for one grid
func (modernCodec) BakeChunks(grid *hub.Grid, protocol int32) ([][]byte, error) {
	return bakeModern(grid, protocol)
}

// Runs login tail then config then the join burst
func (modernCodec) NewSession(r io.Reader, w io.Writer, protocol int32, join JoinData) (Session, error) {
	ids := ModernIDsFor(protocol)
	if ids == nil {
		return nil, fmt.Errorf("no modern ids for protocol %d", protocol)
	}
	s := &modernSession{r: r, w: w, protocol: protocol, ids: ids, pos: join.Spawn}
	if err := s.finishLogin(join.Profile); err != nil {
		return nil, err
	}
	if !ids.NoConfigPhase {
		if err := s.runConfig(); err != nil {
			return nil, err
		}
	}
	if err := s.joinWorld(join); err != nil {
		return nil, err
	}
	return s, nil
}

// One modern client joined into the hub
type modernSession struct {
	r        io.Reader
	w        io.Writer
	protocol int32
	ids      *ModernIDs

	teleportID int32

	posMu sync.Mutex
	pos   Pos
}

// Frames one body onto the client stream
func (s *modernSession) send(body []byte) error {
	return packet.WriteFrame(s.w, body)
}

// Sends login success and eats the acknowledge
func (s *modernSession) finishLogin(profile PlayerEntry) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, modernLoginSuccess)
	packet.WriteUUID(&body, profile.UUID)
	packet.WriteString(&body, profile.Name)
	if !s.ids.LoginNoProps {
		mcproto.WriteVarInt(&body, mcproto.VarInt(len(profile.Properties)))
		for _, p := range profile.Properties {
			packet.WriteString(&body, p.Name)
			packet.WriteString(&body, p.Value)
			if p.Signature != "" {
				packet.WriteBool(&body, true)
				packet.WriteString(&body, p.Signature)
			} else {
				packet.WriteBool(&body, false)
			}
		}
	}
	if s.ids.StrictFlag {
		packet.WriteBool(&body, false)
	}
	if s.ids.LoginSessionID {
		var session [16]byte
		if _, err := rand.Read(session[:]); err != nil {
			return err
		}
		packet.WriteUUID(&body, session)
	}
	if err := s.send(body.Bytes()); err != nil {
		return err
	}

	// Codec era clients jump straight into play
	if s.ids.NoConfigPhase {
		return nil
	}

	frame, err := packet.ReadFrame(s.r)
	if err != nil {
		return fmt.Errorf("login acknowledge read failed: %w", err)
	}
	pid, err := mcproto.ReadVarInt(bytes.NewReader(frame))
	if err != nil {
		return err
	}
	if int32(pid) != modernLoginAck {
		return fmt.Errorf("expected login acknowledge, got %d", pid)
	}
	return nil
}

// Feeds registries then waits for the finish ack
func (s *modernSession) runConfig() error {
	if s.ids.CfgKnownPacks >= 0 {
		var packs bytes.Buffer
		mcproto.WriteVarInt(&packs, mcproto.VarInt(s.ids.CfgKnownPacks))
		mcproto.WriteVarInt(&packs, 0)
		if err := s.send(packs.Bytes()); err != nil {
			return err
		}
	}

	var flags bytes.Buffer
	mcproto.WriteVarInt(&flags, mcproto.VarInt(s.ids.CfgFeatureFlags))
	mcproto.WriteVarInt(&flags, 1)
	packet.WriteString(&flags, "minecraft:vanilla")
	if err := s.send(flags.Bytes()); err != nil {
		return err
	}

	if s.ids.RegistryCompound {
		var reg bytes.Buffer
		mcproto.WriteVarInt(&reg, mcproto.VarInt(s.ids.CfgRegistryData))
		if err := packet.WriteNetworkNBT(&reg, registryCompoundNBT(s.protocol)); err != nil {
			return err
		}
		if err := s.send(reg.Bytes()); err != nil {
			return err
		}
	} else {
		regs, err := hubRegistries(s.protocol)
		if err != nil {
			return err
		}
		for _, reg := range regs {
			if err := s.send(reg); err != nil {
				return err
			}
		}
	}

	var fin bytes.Buffer
	mcproto.WriteVarInt(&fin, mcproto.VarInt(s.ids.CfgFinishCB))
	if err := s.send(fin.Bytes()); err != nil {
		return err
	}

	// Client info and pack answers pass silently
	for range maxConfigFrames {
		frame, err := packet.ReadFrame(s.r)
		if err != nil {
			return fmt.Errorf("config read failed: %w", err)
		}
		pid, err := mcproto.ReadVarInt(bytes.NewReader(frame))
		if err != nil {
			return err
		}
		if int32(pid) == s.ids.CfgFinishAckSB {
			return nil
		}
	}
	return fmt.Errorf("client never acknowledged config")
}

// Sends the full join burst around baked chunks
func (s *modernSession) joinWorld(join JoinData) error {
	var err error
	if s.ids.NoConfigPhase {
		err = s.sendCodecJoin(join)
	} else {
		err = s.sendModernJoin(join)
	}
	if err != nil {
		return err
	}

	// Chunk waits only exist for batched eras
	if s.protocol >= 764 {
		var gev bytes.Buffer
		mcproto.WriteVarInt(&gev, mcproto.VarInt(s.ids.GameEvent))
		gev.WriteByte(gameEventStartChunks)
		packet.WriteNum(&gev, float32(0))
		if err := s.send(gev.Bytes()); err != nil {
			return err
		}
	}

	var center bytes.Buffer
	mcproto.WriteVarInt(&center, mcproto.VarInt(s.ids.SetCenterChunk))
	mcproto.WriteVarInt(&center, mcproto.VarInt(chunkCoord(join.Spawn.X)))
	mcproto.WriteVarInt(&center, mcproto.VarInt(chunkCoord(join.Spawn.Z)))
	if err := s.send(center.Bytes()); err != nil {
		return err
	}

	for _, frame := range join.ViewChunks {
		if err := s.send(frame); err != nil {
			return err
		}
	}

	var spawn bytes.Buffer
	mcproto.WriteVarInt(&spawn, mcproto.VarInt(s.ids.SpawnPosition))
	if s.ids.GlobalRespawn {
		packet.WriteString(&spawn, "minecraft:overworld")
	}
	packet.WriteNum(&spawn, packet.PositionNew(join.SpawnBlock[0], join.SpawnBlock[1], join.SpawnBlock[2]))
	if !s.ids.SpawnPosNoAngle {
		packet.WriteNum(&spawn, float32(0))
	}
	if s.ids.GlobalRespawn {
		packet.WriteNum(&spawn, float32(0))
	}
	if err := s.send(spawn.Bytes()); err != nil {
		return err
	}

	var abilities bytes.Buffer
	mcproto.WriteVarInt(&abilities, mcproto.VarInt(s.ids.Abilities))
	abilities.WriteByte(0)
	packet.WriteNum(&abilities, float32(0.05))
	packet.WriteNum(&abilities, float32(0.1))
	if err := s.send(abilities.Bytes()); err != nil {
		return err
	}

	// Hub nights stay frozen at midnight
	var clock bytes.Buffer
	mcproto.WriteVarInt(&clock, mcproto.VarInt(s.ids.TimeUpdate))
	packet.WriteNum(&clock, int64(0))
	switch {
	case s.ids.ClockTime:
		// One frozen update for the overworld clock
		mcproto.WriteVarInt(&clock, 1)
		mcproto.WriteVarInt(&clock, 0)
		packet.WriteVarLong(&clock, 18000)
		packet.WriteNum(&clock, float32(0))
		packet.WriteNum(&clock, float32(0))
	case s.protocol >= 768:
		packet.WriteNum(&clock, int64(18000))
		packet.WriteBool(&clock, false)
	default:
		// Negative time keeps the old client frozen
		packet.WriteNum(&clock, int64(-18000))
	}
	if err := s.send(clock.Bytes()); err != nil {
		return err
	}

	if err := s.sendPlayerAdd(join.Profile); err != nil {
		return err
	}

	return s.writeSyncPos(join.Spawn)
}

// Join packet for config phase eras
func (s *modernSession) sendModernJoin(join JoinData) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.JoinGame))
	packet.WriteNum(&body, join.EntityID)
	packet.WriteBool(&body, false)
	mcproto.WriteVarInt(&body, 1)
	packet.WriteString(&body, "minecraft:overworld")
	mcproto.WriteVarInt(&body, 20)
	mcproto.WriteVarInt(&body, 8)
	mcproto.WriteVarInt(&body, 8)
	packet.WriteBool(&body, false)
	packet.WriteBool(&body, true)
	packet.WriteBool(&body, false)
	if s.ids.DimTypeString {
		packet.WriteString(&body, "minecraft:overworld")
	} else {
		mcproto.WriteVarInt(&body, 0)
	}
	packet.WriteString(&body, "minecraft:overworld")
	packet.WriteNum(&body, int64(0))
	body.WriteByte(2)
	body.WriteByte(0xff)
	packet.WriteBool(&body, false)
	packet.WriteBool(&body, true)
	packet.WriteBool(&body, false)
	mcproto.WriteVarInt(&body, 0)
	if s.protocol >= 768 {
		mcproto.WriteVarInt(&body, 63)
	}
	if s.ids.LoginOnlineFlag {
		packet.WriteBool(&body, false)
	}
	if s.protocol >= 766 {
		packet.WriteBool(&body, false)
	}
	return s.send(body.Bytes())
}

// Join packet carrying the registry codec inline
func (s *modernSession) sendCodecJoin(join JoinData) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.JoinGame))
	packet.WriteNum(&body, join.EntityID)
	packet.WriteBool(&body, false)
	body.WriteByte(2)
	body.WriteByte(0xff)
	mcproto.WriteVarInt(&body, 1)
	packet.WriteString(&body, "minecraft:overworld")
	if err := packet.WriteNBT(&body, "", dimensionCodecNBT(s.protocol)); err != nil {
		return err
	}
	if s.ids.DimensionInline {
		if err := packet.WriteNBT(&body, "", legacyDimensionNBT(s.protocol)); err != nil {
			return err
		}
	} else {
		packet.WriteString(&body, "minecraft:overworld")
	}
	packet.WriteString(&body, "minecraft:overworld")
	packet.WriteNum(&body, int64(0))
	mcproto.WriteVarInt(&body, 20)
	mcproto.WriteVarInt(&body, 8)
	if !s.ids.NoSimDistance {
		mcproto.WriteVarInt(&body, 8)
	}
	packet.WriteBool(&body, false)
	packet.WriteBool(&body, true)
	packet.WriteBool(&body, false)
	packet.WriteBool(&body, true)
	if s.protocol >= 759 {
		packet.WriteBool(&body, false)
	}
	if s.protocol >= 763 {
		mcproto.WriteVarInt(&body, 0)
	}
	return s.send(body.Bytes())
}

// Chunk coordinate under one absolute axis value
func chunkCoord(v float64) int32 {
	return int32(math.Floor(v)) >> 4
}

// Adds one listed player to the tab list
func (s *modernSession) sendPlayerAdd(entry PlayerEntry) error {
	if s.ids.OldPlayerInfo {
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.PlayerInfoUpdate))
		mcproto.WriteVarInt(&body, 0)
		mcproto.WriteVarInt(&body, 1)
		packet.WriteUUID(&body, entry.UUID)
		packet.WriteString(&body, entry.Name)
		mcproto.WriteVarInt(&body, mcproto.VarInt(len(entry.Properties)))
		for _, p := range entry.Properties {
			packet.WriteString(&body, p.Name)
			packet.WriteString(&body, p.Value)
			if p.Signature != "" {
				packet.WriteBool(&body, true)
				packet.WriteString(&body, p.Signature)
			} else {
				packet.WriteBool(&body, false)
			}
		}
		mcproto.WriteVarInt(&body, 2)
		mcproto.WriteVarInt(&body, 0)
		packet.WriteBool(&body, false)
		if s.protocol >= 759 {
			// Signing era entries end with no key data
			packet.WriteBool(&body, false)
		}
		return s.send(body.Bytes())
	}

	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.PlayerInfoUpdate))
	body.WriteByte(0x09)
	mcproto.WriteVarInt(&body, 1)
	packet.WriteUUID(&body, entry.UUID)
	packet.WriteString(&body, entry.Name)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(entry.Properties)))
	for _, p := range entry.Properties {
		packet.WriteString(&body, p.Name)
		packet.WriteString(&body, p.Value)
		if p.Signature != "" {
			packet.WriteBool(&body, true)
			packet.WriteString(&body, p.Signature)
		} else {
			packet.WriteBool(&body, false)
		}
	}
	packet.WriteBool(&body, true)
	return s.send(body.Bytes())
}

// Sends one absolute position sync to the client
func (s *modernSession) writeSyncPos(pos Pos) error {
	s.teleportID++
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.SyncPlayerPos))
	if s.ids.WideTeleport {
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.teleportID))
		packet.WriteNum(&body, pos.X)
		packet.WriteNum(&body, pos.Y)
		packet.WriteNum(&body, pos.Z)
		for range 3 {
			packet.WriteNum(&body, float64(0))
		}
		packet.WriteNum(&body, pos.Yaw)
		packet.WriteNum(&body, pos.Pitch)
		packet.WriteNum(&body, int32(0))
	} else {
		packet.WriteNum(&body, pos.X)
		packet.WriteNum(&body, pos.Y)
		packet.WriteNum(&body, pos.Z)
		packet.WriteNum(&body, pos.Yaw)
		packet.WriteNum(&body, pos.Pitch)
		body.WriteByte(0)
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.teleportID))
		if s.ids.SyncDismount {
			packet.WriteBool(&body, false)
		}
	}
	return s.send(body.Bytes())
}

// Renders one event as client packets
func (s *modernSession) Encode(ev Event) error {
	switch e := ev.(type) {
	case EvPlayerAdd:
		return s.sendPlayerAdd(e.Entry)

	case EvPlayerRemove:
		var body bytes.Buffer
		if s.ids.OldPlayerInfo {
			mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.PlayerInfoUpdate))
			mcproto.WriteVarInt(&body, 4)
			mcproto.WriteVarInt(&body, 1)
			packet.WriteUUID(&body, e.UUID)
			return s.send(body.Bytes())
		}
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.PlayerInfoRemove))
		mcproto.WriteVarInt(&body, 1)
		packet.WriteUUID(&body, e.UUID)
		return s.send(body.Bytes())

	case EvSpawnPlayer:
		if s.ids.SpawnPlayer > 0 {
			var body bytes.Buffer
			mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.SpawnPlayer))
			mcproto.WriteVarInt(&body, mcproto.VarInt(e.EntityID))
			packet.WriteUUID(&body, e.UUID)
			packet.WriteNum(&body, e.Pos.X)
			packet.WriteNum(&body, e.Pos.Y)
			packet.WriteNum(&body, e.Pos.Z)
			body.WriteByte(packet.Angle(e.Pos.Yaw))
			body.WriteByte(packet.Angle(e.Pos.Pitch))
			return s.send(body.Bytes())
		}
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.AddEntity))
		mcproto.WriteVarInt(&body, mcproto.VarInt(e.EntityID))
		packet.WriteUUID(&body, e.UUID)
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.PlayerTypeID))
		packet.WriteNum(&body, e.Pos.X)
		packet.WriteNum(&body, e.Pos.Y)
		packet.WriteNum(&body, e.Pos.Z)
		if s.ids.LpVelocity {
			packet.WriteLpVec3Zero(&body)
			body.WriteByte(packet.Angle(e.Pos.Pitch))
			body.WriteByte(packet.Angle(e.Pos.Yaw))
			body.WriteByte(packet.Angle(e.Pos.Yaw))
			mcproto.WriteVarInt(&body, 0)
		} else {
			body.WriteByte(packet.Angle(e.Pos.Pitch))
			body.WriteByte(packet.Angle(e.Pos.Yaw))
			body.WriteByte(packet.Angle(e.Pos.Yaw))
			mcproto.WriteVarInt(&body, 0)
			for range 3 {
				packet.WriteNum(&body, int16(0))
			}
		}
		return s.send(body.Bytes())

	case EvEntityMove:
		if err := s.sendEntityMove(e); err != nil {
			return err
		}
		var head bytes.Buffer
		mcproto.WriteVarInt(&head, mcproto.VarInt(s.ids.EntityHeadLook))
		mcproto.WriteVarInt(&head, mcproto.VarInt(e.EntityID))
		head.WriteByte(packet.Angle(e.Pos.Yaw))
		return s.send(head.Bytes())

	case EvEntityRemove:
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.RemoveEntities))
		mcproto.WriteVarInt(&body, mcproto.VarInt(len(e.IDs)))
		for _, id := range e.IDs {
			mcproto.WriteVarInt(&body, mcproto.VarInt(id))
		}
		return s.send(body.Bytes())

	case EvChat:
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.SystemChat))
		if err := s.writeText(&body, e.Text); err != nil {
			return err
		}
		switch {
		case s.ids.LegacyChatPacket:
			// System slot with a blank sender
			body.WriteByte(1)
			packet.WriteUUID(&body, [16]byte{})
		case s.ids.ChatTypeVarInt:
			mcproto.WriteVarInt(&body, 1)
		default:
			packet.WriteBool(&body, false)
		}
		return s.send(body.Bytes())

	case EvTeleportSelf:
		s.posMu.Lock()
		s.pos = e.Pos
		s.posMu.Unlock()
		return s.writeSyncPos(e.Pos)

	case EvBlockChange:
		state, ok := ModernStateID(s.protocol, e.Block)
		if !ok {
			return nil
		}
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.BlockUpdate))
		packet.WriteNum(&body, packet.PositionNew(e.X, e.Y, e.Z))
		mcproto.WriteVarInt(&body, mcproto.VarInt(state))
		return s.send(body.Bytes())

	case EvSignText:
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.BlockEntityData))
		packet.WriteNum(&body, packet.PositionNew(e.X, e.Y, e.Z))
		mcproto.WriteVarInt(&body, ModernSignEntity)
		if err := writeEraNBT(&body, s.ids, signTextNBT(e.Lines, s.ids)); err != nil {
			return err
		}
		return s.send(body.Bytes())

	case EvDisconnect:
		return s.Disconnect(e.Reason)
	}
	return nil
}

// Moves one remote player absolutely
func (s *modernSession) sendEntityMove(e EvEntityMove) error {
	var body bytes.Buffer
	if s.ids.EntityPosSync >= 0 {
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.EntityPosSync))
		mcproto.WriteVarInt(&body, mcproto.VarInt(e.EntityID))
		packet.WriteNum(&body, e.Pos.X)
		packet.WriteNum(&body, e.Pos.Y)
		packet.WriteNum(&body, e.Pos.Z)
		for range 3 {
			packet.WriteNum(&body, float64(0))
		}
		packet.WriteNum(&body, e.Pos.Yaw)
		packet.WriteNum(&body, e.Pos.Pitch)
		packet.WriteBool(&body, e.Pos.OnGround)
	} else {
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.EntityTeleport))
		mcproto.WriteVarInt(&body, mcproto.VarInt(e.EntityID))
		packet.WriteNum(&body, e.Pos.X)
		packet.WriteNum(&body, e.Pos.Y)
		packet.WriteNum(&body, e.Pos.Z)
		body.WriteByte(packet.Angle(e.Pos.Yaw))
		body.WriteByte(packet.Angle(e.Pos.Pitch))
		packet.WriteBool(&body, e.Pos.OnGround)
	}
	return s.send(body.Bytes())
}

// Turns one client frame into an action
func (s *modernSession) Decode(frame []byte) (Action, error) {
	buf := bytes.NewReader(frame)
	pid, err := mcproto.ReadVarInt(buf)
	if err != nil {
		return ActNone{}, nil
	}

	switch int32(pid) {
	case s.ids.KeepAliveSB:
		var id int64
		if packet.ReadNum(buf, &id) != nil {
			return ActNone{}, nil
		}
		return ActKeepAlive{ID: id}, nil

	case s.ids.PlayerPos:
		var x, y, z float64
		if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
			return ActNone{}, nil
		}
		ground, ok := readGround(buf)
		if !ok {
			return ActNone{}, nil
		}
		return ActMove{Pos: s.mergePos(func(p *Pos) {
			p.X, p.Y, p.Z, p.OnGround = x, y, z, ground
		})}, nil

	case s.ids.PlayerPosRot:
		var x, y, z float64
		var yaw, pitch float32
		if packet.ReadNum(buf, &x) != nil || packet.ReadNum(buf, &y) != nil || packet.ReadNum(buf, &z) != nil {
			return ActNone{}, nil
		}
		if packet.ReadNum(buf, &yaw) != nil || packet.ReadNum(buf, &pitch) != nil {
			return ActNone{}, nil
		}
		ground, ok := readGround(buf)
		if !ok {
			return ActNone{}, nil
		}
		return ActMove{Pos: s.mergePos(func(p *Pos) {
			p.X, p.Y, p.Z, p.Yaw, p.Pitch, p.OnGround = x, y, z, yaw, pitch, ground
		})}, nil

	case s.ids.PlayerRot:
		var yaw, pitch float32
		if packet.ReadNum(buf, &yaw) != nil || packet.ReadNum(buf, &pitch) != nil {
			return ActNone{}, nil
		}
		ground, ok := readGround(buf)
		if !ok {
			return ActNone{}, nil
		}
		return ActMove{Pos: s.mergePos(func(p *Pos) {
			p.Yaw, p.Pitch, p.OnGround = yaw, pitch, ground
		})}, nil

	case s.ids.ChatSB:
		text, err := packet.ReadString(buf)
		if err != nil || text == "" {
			return ActNone{}, nil
		}
		if len(text) > 256 {
			text = text[:256]
		}
		return ActChat{Text: text}, nil
	}

	return ActNone{}, nil
}

// Applies one client move onto the tracked pose
func (s *modernSession) mergePos(apply func(*Pos)) Pos {
	s.posMu.Lock()
	defer s.posMu.Unlock()
	apply(&s.pos)
	return s.pos
}

// Ground flag byte shared by every move packet
func readGround(buf *bytes.Reader) (bool, bool) {
	b, err := buf.ReadByte()
	if err != nil {
		return false, false
	}
	return b&0x01 != 0, true
}

// Sends a keepalive probe
func (s *modernSession) KeepAlive(id int64) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.KeepAliveCB))
	packet.WriteNum(&body, id)
	return s.send(body.Bytes())
}

// Sends a play state disconnect
func (s *modernSession) Disconnect(reason string) error {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.DisconnectCB))
	if err := s.writeText(&body, reason); err != nil {
		return err
	}
	return s.send(body.Bytes())
}

// Writes one text component in the group shape
func (s *modernSession) writeText(w *bytes.Buffer, text string) error {
	if s.ids.JSONText {
		raw, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			return err
		}
		return packet.WriteString(w, string(raw))
	}
	return packet.WriteNetworkNBT(w, packet.NBTString(text))
}
