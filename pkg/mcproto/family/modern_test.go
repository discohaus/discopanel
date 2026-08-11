package family

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/hub"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Small hub crossing one chunk border
const testGridJSON = `{
	"version": 1,
	"spawn_x": 8.5, "spawn_y": -59, "spawn_z": 8.5,
	"min_y": -64,
	"fills": [
		{"x1": 0, "y1": -60, "z1": 0, "x2": 18, "y2": -60, "z2": 18, "block": "minecraft:polished_diorite"},
		{"x1": 2, "y1": -59, "z1": 2, "x2": 4, "y2": -59, "z2": 4, "block": "lime_stained_glass"},
		{"x1": 3, "y1": -58, "z1": 3, "x2": 3, "y2": -58, "z2": 3, "block": "oak_wall_sign[facing=north]"},
		{"x1": 6, "y1": -59, "z1": 6, "x2": 6, "y2": -59, "z2": 6, "block": "beacon"},
		{"x1": 7, "y1": -59, "z1": 7, "x2": 7, "y2": -59, "z2": 7, "block": "polished_blackstone_brick_wall"},
		{"x1": 8, "y1": -59, "z1": 8, "x2": 8, "y2": -59, "z2": 8, "block": "amethyst_cluster"}
	],
	"signs": [
		{"x": 3, "y": -58, "z": 3, "facing": "north", "wall": true, "lines": ["Hub", "", "", ""]}
	]
}`

func testGrid(t *testing.T) *hub.Grid {
	t.Helper()
	grid, err := hub.Parse([]byte(testGridJSON))
	if err != nil {
		t.Fatalf("grid parse failed %v", err)
	}
	return grid
}

// Every modern protocol resolves to the codec
func TestModernCodecRegistered(t *testing.T) {
	for p := int32(ModernFloor); p <= ModernCeiling; p++ {
		if Lookup(p) == nil {
			t.Fatalf("protocol %d has no codec", p)
		}
	}
	if Lookup(ModernFloor-1) != nil || Lookup(ModernCeiling+1) != nil {
		t.Fatal("codec leaked outside its range")
	}
}

func readN(t *testing.T, r *bytes.Reader, n int) []byte {
	t.Helper()
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		t.Fatalf("short read of %d bytes %v", n, err)
	}
	return out
}

func readVarInt(t *testing.T, r *bytes.Reader) int32 {
	t.Helper()
	v, err := mcproto.ReadVarInt(r)
	if err != nil {
		t.Fatalf("varint read failed %v", err)
	}
	return int32(v)
}

// Reads era nbt, named roots carry a label
func readEraNBT(t *testing.T, r *bytes.Reader, ids *ModernIDs) any {
	t.Helper()
	if ids.NamedNBT {
		typ, err := r.ReadByte()
		if err != nil {
			t.Fatalf("nbt type read failed %v", err)
		}
		var nameLen uint16
		if err := binary.Read(r, binary.BigEndian, &nameLen); err != nil {
			t.Fatalf("nbt name len failed %v", err)
		}
		readN(t, r, int(nameLen))
		v, err := packet.ReadNetworkNBT(io.MultiReader(bytes.NewReader([]byte{typ}), r))
		if err != nil {
			t.Fatalf("named nbt failed %v", err)
		}
		return v
	}
	v, err := packet.ReadNetworkNBT(r)
	if err != nil {
		t.Fatalf("nbt failed %v", err)
	}
	return v
}

// Walks one paletted container off the reader
func walkContainer(t *testing.T, r *bytes.Reader, ids *ModernIDs, entries int, minBits int) {
	t.Helper()
	bits, err := r.ReadByte()
	if err != nil {
		t.Fatalf("bits read failed %v", err)
	}
	if bits == 0 {
		readVarInt(t, r)
		if !ids.UnprefixedSections {
			if n := readVarInt(t, r); n != 0 {
				t.Fatalf("single container carries %d longs", n)
			}
		}
		return
	}
	if int(bits) < minBits || bits > 8 {
		t.Fatalf("container bits %d out of range", bits)
	}
	palette := readVarInt(t, r)
	for range palette {
		readVarInt(t, r)
	}
	perLong := 64 / int(bits)
	longs := (entries + perLong - 1) / perLong
	if !ids.UnprefixedSections {
		if n := int(readVarInt(t, r)); n != longs {
			t.Fatalf("long count %d, computed %d", n, longs)
		}
	}
	readN(t, r, longs*8)
}

// Walks one whole baked chunk frame
// Returns how many block entities rode along
func walkChunkFrame(t *testing.T, frame []byte, ids *ModernIDs) int {
	t.Helper()
	r := bytes.NewReader(frame)
	if pid := readVarInt(t, r); pid != ids.ChunkData {
		t.Fatalf("packet id %d, want %d", pid, ids.ChunkData)
	}
	readN(t, r, 8)

	if ids.VarIntHeightmaps {
		maps := readVarInt(t, r)
		if maps != 2 {
			t.Fatalf("heightmap count %d", maps)
		}
		for range maps {
			readVarInt(t, r)
			longs := readVarInt(t, r)
			readN(t, r, int(longs)*8)
		}
	} else {
		readEraNBT(t, r, ids)
	}

	size := int(readVarInt(t, r))
	sections := bytes.NewReader(readN(t, r, size))
	for range modernSections {
		readN(t, sections, 2)
		walkContainer(t, sections, ids, 4096, 4)
		walkContainer(t, sections, ids, 64, 1)
	}
	if sections.Len() != 0 {
		t.Fatalf("sections leave %d stray bytes", sections.Len())
	}

	signs := 0
	entities := readVarInt(t, r)
	for range entities {
		readN(t, r, 3)
		kind := readVarInt(t, r)
		switch kind {
		case ModernSignEntity:
			signs++
		case ids.BeaconEntity:
		default:
			t.Fatalf("block entity kind %d", kind)
		}
		readEraNBT(t, r, ids)
	}

	if ids.TrustEdges {
		readN(t, r, 1)
	}
	for range 4 {
		longs := readVarInt(t, r)
		readN(t, r, int(longs)*8)
	}
	sky := readVarInt(t, r)
	for range sky {
		if n := readVarInt(t, r); n != 2048 {
			t.Fatalf("sky array length %d", n)
		}
		readN(t, r, 2048)
	}
	if block := readVarInt(t, r); block != 0 {
		t.Fatalf("block light arrays %d", block)
	}
	if r.Len() != 0 {
		t.Fatalf("frame leaves %d stray bytes", r.Len())
	}
	return signs
}

// Walks one shallow chunk frame completely
func walkLegacyChunkFrame(t *testing.T, frame []byte, ids *ModernIDs) int {
	t.Helper()
	r := bytes.NewReader(frame)
	if pid := readVarInt(t, r); pid != ids.ChunkData {
		t.Fatalf("packet id %d, want %d", pid, ids.ChunkData)
	}
	readN(t, r, 8)
	if ids.VarIntMask {
		readN(t, r, 1)
		readVarInt(t, r)
	} else {
		longs := readVarInt(t, r)
		readN(t, r, int(longs)*8)
	}
	readEraNBT(t, r, ids)
	biomes := readVarInt(t, r)
	if biomes != 1024 {
		t.Fatalf("biome cells %d", biomes)
	}
	for range biomes {
		readVarInt(t, r)
	}
	size := int(readVarInt(t, r))
	sections := bytes.NewReader(readN(t, r, size))
	for range legacySections {
		readN(t, sections, 2)
		walkContainer(t, sections, ids, 4096, 4)
	}
	if sections.Len() != 0 {
		t.Fatalf("sections leave %d stray bytes", sections.Len())
	}
	signs := 0
	entities := readVarInt(t, r)
	for range entities {
		root := readEraNBT(t, r, ids).(map[string]any)
		if root["id"] == "minecraft:sign" {
			signs++
		}
	}
	if r.Len() != 0 {
		t.Fatalf("frame leaves %d stray bytes", r.Len())
	}
	return signs
}

// Walks one standalone light frame completely
func walkLegacyLightFrame(t *testing.T, frame []byte, ids *ModernIDs) {
	t.Helper()
	r := bytes.NewReader(frame)
	if pid := readVarInt(t, r); pid != ids.UpdateLight {
		t.Fatalf("light id %d, want %d", pid, ids.UpdateLight)
	}
	readVarInt(t, r)
	readVarInt(t, r)
	readN(t, r, 1)
	if ids.VarIntMask {
		for range 4 {
			readVarInt(t, r)
		}
	} else {
		for range 4 {
			longs := readVarInt(t, r)
			readN(t, r, int(longs)*8)
		}
		if n := readVarInt(t, r); n != legacyLightSections {
			t.Fatalf("sky array count %d", n)
		}
	}
	for range legacyLightSections {
		if n := readVarInt(t, r); n != 2048 {
			t.Fatalf("sky array length %d", n)
		}
		readN(t, r, 2048)
	}
	if !ids.VarIntMask {
		if n := readVarInt(t, r); n != 0 {
			t.Fatalf("block array count %d", n)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("light leaves %d stray bytes", r.Len())
	}
}

// Baked frames parse cleanly for every group shape
func TestBakeModernChunkStructure(t *testing.T) {
	grid := testGrid(t)
	for _, protocol := range []int32{754, 755, 757, 759, 761, 762, 763, 764, 765, 766, 768, 770, 773, 775, 776} {
		ids := ModernIDsFor(protocol)
		frames, err := bakeModern(grid, protocol)
		if err != nil {
			t.Fatalf("bake %d failed %v", protocol, err)
		}
		want := 16
		if ids.BiomeIntArray {
			want = 32
		}
		if len(frames) != want {
			t.Fatalf("bake %d made %d frames, want %d", protocol, len(frames), want)
		}
		signs := 0
		if ids.BiomeIntArray {
			for i := 0; i < len(frames); i += 2 {
				signs += walkLegacyChunkFrame(t, frames[i], ids)
				walkLegacyLightFrame(t, frames[i+1], ids)
			}
		} else {
			for _, frame := range frames {
				signs += walkChunkFrame(t, frame, ids)
			}
		}
		if signs != 1 {
			t.Fatalf("bake %d carries %d signs, want 1", protocol, signs)
		}
	}
}

// Unknown palette blocks refuse the whole bake
func TestBakeModernRejectsUnknownBlock(t *testing.T) {
	grid, err := hub.Parse([]byte(`{
		"version": 1,
		"fills": [{"x1":0,"y1":0,"z1":0,"x2":0,"y2":0,"z2":0,"block":"command_block"}]
	}`))
	if err != nil {
		t.Fatalf("grid parse failed %v", err)
	}
	if _, err := bakeModern(grid, 766); err == nil {
		t.Fatal("unknown block must fail the bake")
	}
}

// Reads frames until one matches the wanted id
func awaitPacket(r io.Reader, want int32) (*bytes.Reader, error) {
	for range 128 {
		frame, err := packet.ReadFrame(r)
		if err != nil {
			return nil, err
		}
		buf := bytes.NewReader(frame)
		pid, err := mcproto.ReadVarInt(buf)
		if err != nil {
			return nil, err
		}
		if int32(pid) == want {
			return buf, nil
		}
	}
	return nil, fmt.Errorf("packet %d never arrived", want)
}

// Config exchange half of the fake client
func walkFakeConfig(conn net.Conn, protocol int32, ids *ModernIDs) error {
	wantRegs := 3
	if ids.ClockTime {
		wantRegs = 4
	}
	if ids.RegistryCompound {
		wantRegs = 1
	}
	if synced, ok := syncedRegistrySet(protocol); ok && ids.CfgKnownPacks >= 0 {
		wantRegs = len(synced)
	}
	regs := 0
	for {
		frame, err := packet.ReadFrame(conn)
		if err != nil {
			return fmt.Errorf("config read: %w", err)
		}
		buf := bytes.NewReader(frame)
		pid, _ := mcproto.ReadVarInt(buf)
		if int32(pid) == ids.CfgKnownPacks {
			if err := answerKnownPacks(conn, ids, buf); err != nil {
				return err
			}
		}
		if int32(pid) == ids.CfgRegistryData {
			regs++
		}
		if int32(pid) == ids.CfgFinishCB {
			break
		}
	}
	if regs != wantRegs {
		return fmt.Errorf("saw %d registries, want %d", regs, wantRegs)
	}
	var fin bytes.Buffer
	mcproto.WriteVarInt(&fin, mcproto.VarInt(ids.CfgFinishAckSB))
	return packet.WriteFrame(conn, fin.Bytes())
}

// Confirms the first offered pack like a real client
func answerKnownPacks(conn net.Conn, ids *ModernIDs, buf *bytes.Reader) error {
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
	mcproto.WriteVarInt(&resp, mcproto.VarInt(ids.CfgKnownPacksSB))
	mcproto.WriteVarInt(&resp, 1)
	packet.WriteString(&resp, ns)
	packet.WriteString(&resp, id)
	packet.WriteString(&resp, ver)
	return packet.WriteFrame(conn, resp.Bytes())
}

// Fake vanilla client walking login config and join
func runFakeClient(conn net.Conn, protocol int32, done chan<- error) {
	defer close(done)
	fail := func(err error) { done <- err }
	ids := ModernIDsFor(protocol)

	frame, err := packet.ReadFrame(conn)
	if err != nil {
		fail(fmt.Errorf("login success read: %w", err))
		return
	}
	buf := bytes.NewReader(frame)
	if pid, _ := mcproto.ReadVarInt(buf); int32(pid) != modernLoginSuccess {
		fail(fmt.Errorf("login success id %d", pid))
		return
	}

	if !ids.NoConfigPhase {
		var ack bytes.Buffer
		mcproto.WriteVarInt(&ack, modernLoginAck)
		if err := packet.WriteFrame(conn, ack.Bytes()); err != nil {
			fail(err)
			return
		}
		if err := walkFakeConfig(conn, protocol, ids); err != nil {
			fail(err)
			return
		}
	}

	chunks := 0
	for {
		frame, err := packet.ReadFrame(conn)
		if err != nil {
			fail(fmt.Errorf("play read: %w", err))
			return
		}
		buf := bytes.NewReader(frame)
		pid, _ := mcproto.ReadVarInt(buf)
		if int32(pid) == ids.ChunkData {
			chunks++
		}
		if int32(pid) == ids.SyncPlayerPos {
			break
		}
	}
	if chunks != 16 {
		fail(fmt.Errorf("saw %d chunks, want 16", chunks))
		return
	}

	sysChat, err := awaitPacket(conn, ids.SystemChat)
	if err != nil {
		fail(err)
		return
	}
	var component any
	if ids.JSONText {
		raw, err := packet.ReadString(sysChat)
		if err != nil {
			fail(fmt.Errorf("chat json: %w", err))
			return
		}
		if err := json.Unmarshal([]byte(raw), &component); err != nil {
			fail(fmt.Errorf("chat json parse: %w", err))
			return
		}
	} else if component, err = packet.ReadNetworkNBT(sysChat); err != nil {
		fail(fmt.Errorf("chat nbt: %w", err))
		return
	}
	if text := packet.ComponentText(component); text != "hello hub" {
		fail(fmt.Errorf("chat text %q", text))
		return
	}

	alive, err := awaitPacket(conn, ids.KeepAliveCB)
	if err != nil {
		fail(err)
		return
	}
	var id int64
	if packet.ReadNum(alive, &id) != nil || id != 7 {
		fail(fmt.Errorf("keepalive id %d", id))
		return
	}
}

// Full join handshake against a fake client
func TestModernSessionJoinAndEvents(t *testing.T) {
	for _, protocol := range []int32{754, 755, 757, 759, 760, 761, 763, 764, 765, 766, 772, 773, 776} {
		server, client := net.Pipe()
		deadline := time.Now().Add(10 * time.Second)
		server.SetDeadline(deadline)
		client.SetDeadline(deadline)

		done := make(chan error, 8)
		go runFakeClient(client, protocol, done)

		grid := testGrid(t)
		codec := Lookup(protocol)
		frames, err := codec.BakeChunks(grid, protocol)
		if err != nil {
			t.Fatalf("bake failed %v", err)
		}
		join := JoinData{
			Profile:    PlayerEntry{Name: "Steve", UUID: [16]byte{1}},
			EntityID:   42,
			Spawn:      Pos{X: 8.5, Y: -59, Z: 8.5},
			ViewChunks: frames,
			SpawnBlock: [3]int{8, -59, 8},
		}
		sess, err := codec.NewSession(server, server, protocol, join)
		if err != nil {
			t.Fatalf("session %d failed %v", protocol, err)
		}

		if err := sess.Encode(EvChat{Text: "hello hub"}); err != nil {
			t.Fatalf("chat encode failed %v", err)
		}
		if err := sess.KeepAlive(7); err != nil {
			t.Fatalf("keepalive failed %v", err)
		}

		if err := <-done; err != nil {
			t.Fatalf("client %d saw %v", protocol, err)
		}
		server.Close()
		client.Close()
	}
}

// Sign events render as block entity updates
func TestModernSessionSignEncode(t *testing.T) {
	for _, protocol := range []int32{766, 772} {
		var out bytes.Buffer
		s := &modernSession{w: &out, protocol: protocol, ids: ModernIDsFor(protocol)}
		lines := [4]string{"World", "☽ dreaming", "ᴛᴏᴜᴄʜ ᴛᴏ ᴡᴀᴋᴇ", "1.21.8"}
		if err := s.Encode(EvSignText{X: 3, Y: -57, Z: -14, Lines: lines}); err != nil {
			t.Fatalf("encode failed %v", err)
		}

		frame, err := packet.ReadFrame(&out)
		if err != nil {
			t.Fatalf("frame read failed %v", err)
		}
		r := bytes.NewReader(frame)
		if pid := readVarInt(t, r); pid != s.ids.BlockEntityData {
			t.Fatalf("packet id %d", pid)
		}
		var raw uint64
		if packet.ReadNum(r, &raw) != nil || raw != packet.PositionNew(3, -57, -14) {
			t.Fatalf("position %d", raw)
		}
		if kind := readVarInt(t, r); kind != ModernSignEntity {
			t.Fatalf("entity kind %d", kind)
		}
		data, err := packet.ReadNetworkNBT(r)
		if err != nil {
			t.Fatalf("nbt parse failed %v", err)
		}
		front := data.(map[string]any)["front_text"].(map[string]any)
		first, _ := front["messages"].([]any)[0].(string)
		want := "World"
		if protocol < modernSNBTFloor {
			want = `"World"`
		}
		if first != want {
			t.Fatalf("protocol %d line %q, want %q", protocol, first, want)
		}
	}
}

// Client frames decode into normalized actions
func TestModernSessionDecode(t *testing.T) {
	for _, protocol := range []int32{754, 757, 761, 764, 766, 772, 775} {
		ids := ModernIDsFor(protocol)
		s := &modernSession{protocol: protocol, ids: ids, pos: Pos{X: 1, Y: 2, Z: 3}}

		var move bytes.Buffer
		mcproto.WriteVarInt(&move, mcproto.VarInt(ids.PlayerPosRot))
		packet.WriteNum(&move, float64(10))
		packet.WriteNum(&move, float64(-59))
		packet.WriteNum(&move, float64(12))
		packet.WriteNum(&move, float32(90))
		packet.WriteNum(&move, float32(10))
		move.WriteByte(1)
		act, err := s.Decode(move.Bytes())
		if err != nil {
			t.Fatalf("move decode failed %v", err)
		}
		got, ok := act.(ActMove)
		if !ok || got.Pos.X != 10 || got.Pos.Yaw != 90 || !got.Pos.OnGround {
			t.Fatalf("move action %+v", act)
		}

		var rot bytes.Buffer
		mcproto.WriteVarInt(&rot, mcproto.VarInt(ids.PlayerRot))
		packet.WriteNum(&rot, float32(45))
		packet.WriteNum(&rot, float32(0))
		rot.WriteByte(0)
		act, _ = s.Decode(rot.Bytes())
		turned, ok := act.(ActMove)
		if !ok || turned.Pos.X != 10 || turned.Pos.Yaw != 45 {
			t.Fatalf("rot keeps position, got %+v", act)
		}

		var chat bytes.Buffer
		mcproto.WriteVarInt(&chat, mcproto.VarInt(ids.ChatSB))
		packet.WriteString(&chat, "hi")
		act, _ = s.Decode(chat.Bytes())
		if said, ok := act.(ActChat); !ok || said.Text != "hi" {
			t.Fatalf("chat action %+v", act)
		}

		var alive bytes.Buffer
		mcproto.WriteVarInt(&alive, mcproto.VarInt(ids.KeepAliveSB))
		packet.WriteNum(&alive, int64(99))
		act, _ = s.Decode(alive.Bytes())
		if ka, ok := act.(ActKeepAlive); !ok || ka.ID != 99 {
			t.Fatalf("keepalive action %+v", act)
		}

		var junk bytes.Buffer
		mcproto.WriteVarInt(&junk, 0x7f)
		junk.WriteByte(0xaa)
		act, err = s.Decode(junk.Bytes())
		if err != nil {
			t.Fatalf("junk decode errored %v", err)
		}
		if _, ok := act.(ActNone); !ok {
			t.Fatalf("junk action %+v", act)
		}
	}
}
