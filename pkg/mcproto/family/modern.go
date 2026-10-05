package family

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sync"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Login ids the codec speaks after auth
const (
	modernLoginSuccess = 0x02
	modernLoginAck     = 0x03
)

// Client config frames tolerated before the ack
const maxConfigFrames = 64

// Game event id starting the chunk wait
const gameEventStartChunks = 13

// Modern family codec covering 754 through 776
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
func (modernCodec) BakeChunks(grid *Grid, protocol int32) ([][]byte, error) {
	return bakeModern(grid, protocol)
}

// Runs login tail then config then the join burst
// Failures send the fail note in the phase they died in
func (modernCodec) NewSession(r io.Reader, w io.Writer, protocol int32, join JoinData) (Session, error) {
	ids := ModernIDsFor(protocol)
	if ids == nil {
		return nil, fmt.Errorf("no modern ids for protocol %d", protocol)
	}
	s := &modernSession{r: r, w: w, protocol: protocol, ids: ids, pos: join.Spawn}
	if err := s.finishLogin(join.Profile); err != nil {
		s.failLogin(join.FailNote)
		return nil, err
	}
	if !ids.NoConfigPhase {
		if err := s.runConfig(join); err != nil {
			if !errors.Is(err, ErrHandedOff) && join.FailNote != "" {
				WriteConfigDisconnect(s.w, protocol, join.FailNote)
			}
			return nil, err
		}
	}
	if err := s.joinWorld(join); err != nil {
		if join.FailNote != "" {
			s.Disconnect(join.FailNote)
		}
		return nil, err
	}
	return s, nil
}

// Login phase failure screen, best effort
func (s *modernSession) failLogin(note string) {
	if note == "" {
		return
	}
	raw, err := json.Marshal(map[string]string{"text": note})
	if err != nil {
		return
	}
	mcproto.WriteLoginDisconnectJSON(s.w, raw)
}

// One modern client joined into the hub
type modernSession struct {
	r        io.Reader
	w        io.Writer
	protocol int32
	ids      *ModernIDs
	probes   []dialectProbe
	declared bool

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

// Declared clients get the refusal screen instead
// Config refusals ride config, later ones ride play
func (s *modernSession) refuseDeclared(join JoinData, config bool) error {
	if !s.declared || join.RefuseNote == "" {
		return nil
	}
	if config {
		if err := WriteConfigDisconnect(s.w, s.protocol, join.RefuseNote); err != nil {
			return err
		}
	} else if err := s.Disconnect(join.RefuseNote); err != nil {
		return err
	}
	return ErrHandedOff
}

// Feeds registries then waits for the finish ack
func (s *modernSession) runConfig(join JoinData) error {
	if err := s.runDialectFence(); err != nil {
		return err
	}
	if err := s.refuseDeclared(join, true); err != nil {
		return err
	}
	if regs, ok := syncedRegistrySet(s.protocol); ok && s.ids.CfgKnownPacks >= 0 {
		if err := s.runMirrorConfig(join, regs); err != nil {
			return err
		}
		// Late declarations past the ack refuse in play
		return s.refuseDeclared(join, false)
	}

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

	if err := s.sendConfigTags(); err != nil {
		return err
	}

	var fin bytes.Buffer
	mcproto.WriteVarInt(&fin, mcproto.VarInt(s.ids.CfgFinishCB))
	if err := s.send(fin.Bytes()); err != nil {
		return err
	}
	if err := s.awaitFinishAck(); err != nil {
		return err
	}
	// Late declarations past the ack refuse in play
	return s.refuseDeclared(join, false)
}

// Mirrors the client's own core pack through config
func (s *modernSession) runMirrorConfig(join JoinData, regs []syncedRegistry) error {
	var flags bytes.Buffer
	mcproto.WriteVarInt(&flags, mcproto.VarInt(s.ids.CfgFeatureFlags))
	mcproto.WriteVarInt(&flags, 1)
	packet.WriteString(&flags, "minecraft:vanilla")
	if err := s.send(flags.Bytes()); err != nil {
		return err
	}

	versions := mcproto.VersionNamesForProtocol(s.protocol)
	var packs bytes.Buffer
	mcproto.WriteVarInt(&packs, mcproto.VarInt(s.ids.CfgKnownPacks))
	mcproto.WriteVarInt(&packs, mcproto.VarInt(len(versions)))
	for _, v := range versions {
		packet.WriteString(&packs, "minecraft")
		packet.WriteString(&packs, "core")
		packet.WriteString(&packs, v)
	}
	if err := s.send(packs.Bytes()); err != nil {
		return err
	}

	confirmed, err := s.awaitKnownPacks(versions)
	if err != nil {
		return err
	}
	// Declarations landing with the packs still refuse
	if err := s.refuseDeclared(join, true); err != nil {
		return err
	}

	for _, reg := range regs {
		body, err := s.mirrorRegistryBody(reg, confirmed)
		if err != nil {
			return err
		}
		if body == nil {
			continue
		}
		if err := s.send(body); err != nil {
			return err
		}
	}

	if err := s.sendConfigTags(); err != nil {
		return err
	}

	var fin bytes.Buffer
	mcproto.WriteVarInt(&fin, mcproto.VarInt(s.ids.CfgFinishCB))
	if err := s.send(fin.Bytes()); err != nil {
		return err
	}
	return s.awaitFinishAck()
}

// Binds the tag names modern clients require
// Shared config ids cover every tagged era
func (s *modernSession) sendConfigTags() error {
	set := syncedTagSet(s.protocol)
	if len(set) == 0 {
		return nil
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(mcproto.CfgCBUpdateTags))
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(set)))
	for _, reg := range set {
		packet.WriteString(&body, reg.name)
		mcproto.WriteVarInt(&body, mcproto.VarInt(len(reg.tags)))
		for _, tag := range reg.tags {
			packet.WriteString(&body, tag.name)
			ids := make([]int32, 0, len(tag.entries))
			for _, entry := range tag.entries {
				if id, ok := syncedEntryID(s.protocol, reg.name, entry); ok {
					ids = append(ids, id)
				}
			}
			mcproto.WriteVarInt(&body, mcproto.VarInt(len(ids)))
			for _, id := range ids {
				mcproto.WriteVarInt(&body, mcproto.VarInt(id))
			}
		}
	}
	return s.send(body.Bytes())
}

// Builds one registry frame, names only when confirmed
func (s *modernSession) mirrorRegistryBody(reg syncedRegistry, confirmed bool) ([]byte, error) {
	var data func(string) packet.Tag
	if !confirmed {
		var err error
		data, err = s.fallbackRegistryData(reg.Name)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, nil
		}
	}

	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.CfgRegistryData))
	packet.WriteString(&body, reg.Name)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(reg.Entries)))
	for _, entry := range reg.Entries {
		packet.WriteString(&body, "minecraft:"+entry)
		if data == nil {
			packet.WriteBool(&body, false)
			continue
		}
		packet.WriteBool(&body, true)
		if err := packet.WriteNetworkNBT(&body, data(entry)); err != nil {
			return nil, err
		}
	}
	return body.Bytes(), nil
}

// Inline element source for pack refusing clients
func (s *modernSession) fallbackRegistryData(registry string) (func(string) packet.Tag, error) {
	switch registry {
	case "minecraft:dimension_type":
		tag := hubDimensionNBT()
		if s.ids.AttribRegistries {
			var err error
			if tag, err = packet.JSONToNBT([]byte(attribDimensionJSON)); err != nil {
				return nil, err
			}
		}
		return func(string) packet.Tag { return tag }, nil
	case "minecraft:worldgen/biome":
		tag := hubBiomeNBT()
		if s.ids.AttribRegistries {
			var err error
			if tag, err = packet.JSONToNBT([]byte(attribBiomeJSON)); err != nil {
				return nil, err
			}
		}
		return func(string) packet.Tag { return tag }, nil
	case "minecraft:chat_type":
		return func(string) packet.Tag { return chatTypeNBT() }, nil
	case "minecraft:damage_type":
		return damageTypeNBT, nil
	case "minecraft:world_clock":
		return func(string) packet.Tag { return packet.NBTCompound{} }, nil
	}
	return nil, nil
}

// Waits for the pack answer, echoing mirrorable traffic
func (s *modernSession) awaitKnownPacks(versions []string) (bool, error) {
	for range maxConfigFrames {
		frame, err := packet.ReadFrame(s.r)
		if err != nil {
			return false, fmt.Errorf("known packs read failed: %w", err)
		}
		rd := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return false, err
		}
		if int32(pid) == s.ids.CfgKnownPacksSB {
			count, err := mcproto.ReadVarInt(rd)
			if err != nil {
				return false, err
			}
			confirmed := false
			for range count {
				ns, err := packet.ReadString(rd)
				if err != nil {
					return false, err
				}
				id, err := packet.ReadString(rd)
				if err != nil {
					return false, err
				}
				ver, err := packet.ReadString(rd)
				if err != nil {
					return false, err
				}
				if ns == "minecraft" && id == "core" && slices.Contains(versions, ver) {
					confirmed = true
				}
			}
			return confirmed, nil
		}
		if err := s.mirrorConfigFrame(int32(pid), rd); err != nil {
			return false, err
		}
	}
	return false, nil
}

// Client info and pack answers pass silently
func (s *modernSession) awaitFinishAck() error {
	for range maxConfigFrames {
		frame, err := packet.ReadFrame(s.r)
		if err != nil {
			return fmt.Errorf("config read failed: %w", err)
		}
		rd := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return err
		}
		if int32(pid) == s.ids.CfgFinishAckSB {
			return nil
		}
		if err := s.mirrorConfigFrame(int32(pid), rd); err != nil {
			return err
		}
	}
	return fmt.Errorf("client never acknowledged config")
}

// Sends dialect queries then fences on a ping
func (s *modernSession) runDialectFence() error {
	probes := dialectProbes(s.protocol)
	if len(probes) == 0 || s.ids.CfgPingCB < 0 || s.ids.CfgPluginMsgCB < 0 {
		return nil
	}
	s.probes = probes
	for _, p := range probes {
		var msg bytes.Buffer
		mcproto.WriteVarInt(&msg, mcproto.VarInt(s.ids.CfgPluginMsgCB))
		packet.WriteString(&msg, p.query)
		msg.Write(p.body)
		if err := s.send(msg.Bytes()); err != nil {
			return err
		}
	}
	var ping bytes.Buffer
	mcproto.WriteVarInt(&ping, mcproto.VarInt(s.ids.CfgPingCB))
	packet.WriteNum(&ping, int32(0))
	if err := s.send(ping.Bytes()); err != nil {
		return err
	}
	for range maxConfigFrames {
		frame, err := packet.ReadFrame(s.r)
		if err != nil {
			return fmt.Errorf("dialect fence read failed: %w", err)
		}
		rd := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(rd)
		if err != nil {
			return err
		}
		if int32(pid) == s.ids.CfgPongSB {
			return nil
		}
		if err := s.mirrorConfigFrame(int32(pid), rd); err != nil {
			return err
		}
	}
	return fmt.Errorf("client never answered the fence ping")
}

// Answers dialect replies and echoes the brand
func (s *modernSession) mirrorConfigFrame(pid int32, rd *bytes.Reader) error {
	if pid != s.ids.CfgPluginMsgSB || s.ids.CfgPluginMsgCB < 0 {
		return nil
	}
	channel, err := packet.ReadString(rd)
	if err != nil {
		return nil
	}
	rest, err := io.ReadAll(rd)
	if err != nil {
		return nil
	}
	for i, p := range s.probes {
		if p.query != channel {
			continue
		}
		s.probes = slices.Delete(s.probes, i, i+1)
		body, err := p.reshape(rest)
		if err != nil {
			return fmt.Errorf("dialect reply reshape failed: %w", err)
		}
		s.declared = true
		var ans bytes.Buffer
		mcproto.WriteVarInt(&ans, mcproto.VarInt(s.ids.CfgPluginMsgCB))
		packet.WriteString(&ans, p.answer)
		ans.Write(body)
		return s.send(ans.Bytes())
	}
	if channel != "minecraft:brand" {
		return nil
	}
	var echo bytes.Buffer
	mcproto.WriteVarInt(&echo, mcproto.VarInt(s.ids.CfgPluginMsgCB))
	packet.WriteString(&echo, channel)
	echo.Write(rest)
	return s.send(echo.Bytes())
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
		id, _ := syncedEntryID(s.protocol, "minecraft:dimension_type", "overworld")
		mcproto.WriteVarInt(&body, mcproto.VarInt(id))
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
		// Mask eras take the sign action byte with coords inside
		if s.ids.BiomeIntArray {
			body.WriteByte(9)
			if err := packet.WriteNBT(&body, "", signEntityNBT(e.X, e.Y, e.Z, e.Lines, s.ids)); err != nil {
				return err
			}
			return s.send(body.Bytes())
		}
		mcproto.WriteVarInt(&body, ModernSignEntity)
		if err := writeEraNBT(&body, s.ids, signTextNBT(e.Lines, s.ids)); err != nil {
			return err
		}
		return s.send(body.Bytes())

	case EvBeaconInit:
		var body bytes.Buffer
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.BlockEntityData))
		packet.WriteNum(&body, packet.PositionNew(e.X, e.Y, e.Z))
		// Mask eras take the beacon action byte with coords inside
		if s.ids.BiomeIntArray {
			body.WriteByte(3)
			entity := packet.NBTCompound{
				{Name: "x", Tag: packet.NBTInt(int32(e.X))},
				{Name: "y", Tag: packet.NBTInt(int32(e.Y))},
				{Name: "z", Tag: packet.NBTInt(int32(e.Z))},
				{Name: "id", Tag: packet.NBTString("minecraft:beacon")},
			}
			if err := packet.WriteNBT(&body, "", entity); err != nil {
				return err
			}
			return s.send(body.Bytes())
		}
		mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.BeaconEntity))
		if err := writeEraNBT(&body, s.ids, packet.NBTCompound{}); err != nil {
			return err
		}
		return s.send(body.Bytes())
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

// Sends the client to another address
func (s *modernSession) Transfer(host string, port int) (bool, error) {
	if s.ids.Transfer < 0 {
		return false, nil
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(s.ids.Transfer))
	packet.WriteString(&body, host)
	mcproto.WriteVarInt(&body, mcproto.VarInt(port))
	if err := s.send(body.Bytes()); err != nil {
		return true, err
	}
	return true, nil
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

// Sends a config phase disconnect screen
// Codec eras fall back to the play screen
func WriteConfigDisconnect(w io.Writer, protocol int32, reason string) error {
	ids := ModernIDsFor(protocol)
	if ids == nil {
		return nil
	}
	id := ids.CfgDisconnect
	if id < 0 {
		id = ids.DisconnectCB
	}
	if id < 0 {
		return nil
	}
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(id))
	if ids.JSONText {
		raw, err := json.Marshal(map[string]string{"text": reason})
		if err != nil {
			return err
		}
		packet.WriteString(&body, string(raw))
	} else if err := packet.WriteNetworkNBT(&body, packet.NBTString(reason)); err != nil {
		return err
	}
	return packet.WriteFrame(w, body.Bytes())
}
