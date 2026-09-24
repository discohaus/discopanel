package family

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Vanilla overworld vertical bounds
const (
	modernMinY     = -64
	modernSections = 24
)

// Light sections pad the world by two
const modernLightSections = modernSections + 2

// Sign block entity type id, stable in the group
const ModernSignEntity = 7

// Heightmap entries fit this world in nine bits
const modernHeightBits = 9

// First group storing sign lines as plain nbt text
const modernSNBTFloor = 770

// Bakes framed modern chunk packets for the hub
func bakeModern(grid *Grid, protocol int32) ([][]byte, error) {
	ids := ModernIDsFor(protocol)
	if ids == nil {
		return nil, fmt.Errorf("no modern ids for protocol %d", protocol)
	}

	cx1, cz1, cx2, cz2 := grid.ChunkRange()
	cx1, cz1, cx2, cz2 = cx1-1, cz1-1, cx2+1, cz2+1

	var frames [][]byte
	for cx := cx1; cx <= cx2; cx++ {
		for cz := cz1; cz <= cz2; cz++ {
			if ids.BiomeIntArray {
				chunk, light, err := bakeLegacyChunk(grid, protocol, ids, cx, cz)
				if err != nil {
					return nil, err
				}
				frames = append(frames, chunk, light)
				continue
			}
			frame, err := bakeModernChunk(grid, protocol, ids, cx, cz)
			if err != nil {
				return nil, err
			}
			frames = append(frames, frame)
		}
	}
	return frames, nil
}

// Bakes one chunk with light and block entities
func bakeModernChunk(grid *Grid, protocol int32, ids *ModernIDs, cx, cz int) ([]byte, error) {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(ids.ChunkData))
	packet.WriteNum(&body, int32(cx))
	packet.WriteNum(&body, int32(cz))

	heights := columnHeights(grid, cx, cz)
	if err := writeHeightmaps(&body, ids, heights); err != nil {
		return nil, err
	}

	var sections bytes.Buffer
	for s := range modernSections {
		if err := writeModernSection(&sections, grid, protocol, ids, cx, cz, modernMinY+s*16); err != nil {
			return nil, err
		}
	}
	mcproto.WriteVarInt(&body, mcproto.VarInt(sections.Len()))
	body.Write(sections.Bytes())

	signs := chunkSigns(grid, cx, cz)
	beacons := chunkBeacons(grid, cx, cz)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(signs)+len(beacons)))
	for _, sg := range signs {
		body.WriteByte(byte((sg.X&15)<<4 | sg.Z&15))
		packet.WriteNum(&body, int16(sg.Y))
		mcproto.WriteVarInt(&body, ModernSignEntity)
		if err := writeEraNBT(&body, ids, signTextNBT(sg.Lines, ids)); err != nil {
			return nil, err
		}
	}
	for _, pos := range beacons {
		body.WriteByte(byte((pos[0]&15)<<4 | pos[2]&15))
		packet.WriteNum(&body, int16(pos[1]))
		mcproto.WriteVarInt(&body, mcproto.VarInt(ids.BeaconEntity))
		if err := writeEraNBT(&body, ids, packet.NBTCompound{}); err != nil {
			return nil, err
		}
	}

	if ids.TrustEdges {
		packet.WriteBool(&body, true)
	}
	writeModernLight(&body)
	return body.Bytes(), nil
}

// Shallow legacy worlds and their vertical lift
const (
	legacyYOffset       = 64
	legacySections      = 16
	legacyLightSections = legacySections + 2
	legacyLightMask     = uint64(1)<<legacyLightSections - 1
)

// Bakes one shallow chunk and its light frame
func bakeLegacyChunk(grid *Grid, protocol int32, ids *ModernIDs, cx, cz int) ([]byte, []byte, error) {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, mcproto.VarInt(ids.ChunkData))
	packet.WriteNum(&body, int32(cx))
	packet.WriteNum(&body, int32(cz))

	if ids.VarIntMask {
		packet.WriteBool(&body, true)
		mcproto.WriteVarInt(&body, 0xffff)
	} else {
		mcproto.WriteVarInt(&body, 1)
		packet.WriteNum(&body, uint64(0xffff))
	}

	heights := columnHeights(grid, cx, cz)
	if err := writeHeightmaps(&body, ids, heights); err != nil {
		return nil, nil, err
	}

	// One plains biome cell for the whole column
	mcproto.WriteVarInt(&body, 1024)
	for range 1024 {
		mcproto.WriteVarInt(&body, 0)
	}

	var sections bytes.Buffer
	for s := range legacySections {
		if err := writeLegacySection(&sections, grid, protocol, cx, cz, s*16-legacyYOffset); err != nil {
			return nil, nil, err
		}
	}
	mcproto.WriteVarInt(&body, mcproto.VarInt(sections.Len()))
	body.Write(sections.Bytes())

	signs := chunkSigns(grid, cx, cz)
	beacons := chunkBeacons(grid, cx, cz)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(signs)+len(beacons)))
	for _, sg := range signs {
		entity := signEntityNBT(sg.X, sg.Y+legacyYOffset, sg.Z, sg.Lines, ids)
		if err := packet.WriteNBT(&body, "", entity); err != nil {
			return nil, nil, err
		}
	}
	for _, pos := range beacons {
		entity := packet.NBTCompound{
			{Name: "x", Tag: packet.NBTInt(int32(pos[0]))},
			{Name: "y", Tag: packet.NBTInt(int32(pos[1] + legacyYOffset))},
			{Name: "z", Tag: packet.NBTInt(int32(pos[2]))},
			{Name: "id", Tag: packet.NBTString("minecraft:beacon")},
		}
		if err := packet.WriteNBT(&body, "", entity); err != nil {
			return nil, nil, err
		}
	}

	var light bytes.Buffer
	mcproto.WriteVarInt(&light, mcproto.VarInt(ids.UpdateLight))
	mcproto.WriteVarInt(&light, mcproto.VarInt(cx))
	mcproto.WriteVarInt(&light, mcproto.VarInt(cz))
	packet.WriteBool(&light, true)
	if ids.VarIntMask {
		mcproto.WriteVarInt(&light, mcproto.VarInt(legacyLightMask))
		mcproto.WriteVarInt(&light, 0)
		mcproto.WriteVarInt(&light, 0)
		mcproto.WriteVarInt(&light, mcproto.VarInt(legacyLightMask))
	} else {
		writeBitset(&light, legacyLightMask)
		writeBitset(&light, 0)
		writeBitset(&light, 0)
		writeBitset(&light, legacyLightMask)
	}
	bright := bytes.Repeat([]byte{0xff}, 2048)
	if !ids.VarIntMask {
		mcproto.WriteVarInt(&light, legacyLightSections)
	}
	for range legacyLightSections {
		mcproto.WriteVarInt(&light, 2048)
		light.Write(bright)
	}
	if !ids.VarIntMask {
		mcproto.WriteVarInt(&light, 0)
	}

	return body.Bytes(), light.Bytes(), nil
}

// Writes one block only section for shallow worlds
func writeLegacySection(w *bytes.Buffer, grid *Grid, protocol int32, cx, cz, yBase int) error {
	var states [4096]int32
	var palette []int32
	index := map[int32]int{}
	count := int16(0)

	for ly := range 16 {
		for lz := range 16 {
			for lx := range 16 {
				name := grid.BlockAt(cx*16+lx, yBase+ly, cz*16+lz)
				state := int32(0)
				if name != "" {
					id, ok := ModernStateID(protocol, name)
					if !ok {
						return fmt.Errorf("block %q not in modern palette", name)
					}
					state = id
					count++
				}
				states[ly*256+lz*16+lx] = state
				if _, seen := index[state]; !seen {
					index[state] = len(palette)
					palette = append(palette, state)
				}
			}
		}
	}

	packet.WriteNum(w, count)

	if len(palette) == 1 {
		w.WriteByte(0)
		mcproto.WriteVarInt(w, mcproto.VarInt(palette[0]))
		mcproto.WriteVarInt(w, 0)
		return nil
	}
	bits := packet.BitsFor(len(palette), 4)
	if bits > 8 {
		return fmt.Errorf("section palette too big at %d entries", len(palette))
	}
	w.WriteByte(byte(bits))
	mcproto.WriteVarInt(w, mcproto.VarInt(len(palette)))
	for _, id := range palette {
		mcproto.WriteVarInt(w, mcproto.VarInt(id))
	}
	values := make([]uint32, len(states))
	for i, s := range states {
		values[i] = uint32(index[s])
	}
	longs := packet.PackPadded(values, bits)
	mcproto.WriteVarInt(w, mcproto.VarInt(len(longs)))
	for _, l := range longs {
		packet.WriteNum(w, l)
	}
	return nil
}

// Heightmap values for every column in one chunk
func columnHeights(grid *Grid, cx, cz int) [256]uint32 {
	var heights [256]uint32
	min, max := grid.Bounds()
	for lz := range 16 {
		for lx := range 16 {
			x, z := cx*16+lx, cz*16+lz
			for y := max[1]; y >= min[1]; y-- {
				if grid.BlockAt(x, y, z) != "" {
					heights[lz*16+lx] = uint32(y + 1 - modernMinY)
					break
				}
			}
		}
	}
	return heights
}

// Writes both heightmaps in the group shape
func writeHeightmaps(w *bytes.Buffer, ids *ModernIDs, heights [256]uint32) error {
	longs := packet.PackPadded(heights[:], modernHeightBits)

	if ids.VarIntHeightmaps {
		// World surface then motion blocking by type id
		mcproto.WriteVarInt(w, 2)
		for _, kind := range []int32{1, 4} {
			mcproto.WriteVarInt(w, mcproto.VarInt(kind))
			mcproto.WriteVarInt(w, mcproto.VarInt(len(longs)))
			for _, l := range longs {
				packet.WriteNum(w, l)
			}
		}
		return nil
	}

	arr := make(packet.NBTLongArray, len(longs))
	for i, l := range longs {
		arr[i] = int64(l)
	}
	root := packet.NBTCompound{
		{Name: "MOTION_BLOCKING", Tag: arr},
		{Name: "WORLD_SURFACE", Tag: arr},
	}
	return writeEraNBT(w, ids, root)
}

// Writes nbt named or nameless per the era
func writeEraNBT(w *bytes.Buffer, ids *ModernIDs, tag packet.Tag) error {
	if ids.NamedNBT {
		return packet.WriteNBT(w, "", tag)
	}
	return packet.WriteNetworkNBT(w, tag)
}

// Writes one section with its block and biome containers
func writeModernSection(w *bytes.Buffer, grid *Grid, protocol int32, ids *ModernIDs, cx, cz, yBase int) error {
	var states [4096]int32
	var palette []int32
	index := map[int32]int{}
	count := int16(0)

	for ly := range 16 {
		for lz := range 16 {
			for lx := range 16 {
				name := grid.BlockAt(cx*16+lx, yBase+ly, cz*16+lz)
				state := int32(0)
				if name != "" {
					id, ok := ModernStateID(protocol, name)
					if !ok {
						return fmt.Errorf("block %q not in modern palette", name)
					}
					state = id
					count++
				}
				states[ly*256+lz*16+lx] = state
				if _, seen := index[state]; !seen {
					index[state] = len(palette)
					palette = append(palette, state)
				}
			}
		}
	}

	packet.WriteNum(w, count)
	// Fluid eras count wet blocks, the plaza stays dry
	if ids.FluidCounts {
		packet.WriteNum(w, int16(0))
	}

	if len(palette) == 1 {
		w.WriteByte(0)
		mcproto.WriteVarInt(w, mcproto.VarInt(palette[0]))
		if !ids.UnprefixedSections {
			mcproto.WriteVarInt(w, 0)
		}
	} else {
		bits := packet.BitsFor(len(palette), 4)
		if bits > 8 {
			return fmt.Errorf("section palette too big at %d entries", len(palette))
		}
		w.WriteByte(byte(bits))
		mcproto.WriteVarInt(w, mcproto.VarInt(len(palette)))
		for _, id := range palette {
			mcproto.WriteVarInt(w, mcproto.VarInt(id))
		}
		values := make([]uint32, len(states))
		for i, s := range states {
			values[i] = uint32(index[s])
		}
		longs := packet.PackPadded(values, bits)
		if !ids.UnprefixedSections {
			mcproto.WriteVarInt(w, mcproto.VarInt(len(longs)))
		}
		for _, l := range longs {
			packet.WriteNum(w, l)
		}
	}

	// One plains biome fills the whole section
	w.WriteByte(0)
	mcproto.WriteVarInt(w, mcproto.VarInt(plainsBiomeID(protocol)))
	if !ids.UnprefixedSections {
		mcproto.WriteVarInt(w, 0)
	}
	return nil
}

// Signs standing inside one chunk column
func chunkSigns(grid *Grid, cx, cz int) []Sign {
	var out []Sign
	for _, s := range grid.Signs {
		if s.X>>4 == cx && s.Z>>4 == cz {
			out = append(out, s)
		}
	}
	return out
}

// Beacon positions inside one chunk column
// Their block entities make the beams render
func chunkBeacons(grid *Grid, cx, cz int) [][3]int {
	var out [][3]int
	for pos, name := range grid.Blocks() {
		if pos[0]>>4 != cx || pos[2]>>4 != cz {
			continue
		}
		if base, _ := SplitState(name); base == "beacon" {
			out = append(out, pos)
		}
	}
	return out
}

// Glowing ink keeps plaques readable on dark wood
const signInk = "yellow"

// Sign text as a waxed block entity
func signTextNBT(lines [4]string, ids *ModernIDs) packet.Tag {
	if ids.SignTextRows {
		out := packet.NBTCompound{
			{Name: "Color", Tag: packet.NBTString(signInk)},
			{Name: "GlowingText", Tag: packet.NBTByte(1)},
		}
		for i, line := range lines {
			raw, _ := json.Marshal(map[string]string{"text": line})
			out = append(out, packet.NBTField{
				Name: "Text" + itoa(int32(i+1)),
				Tag:  packet.NBTString(string(raw)),
			})
		}
		return out
	}
	return packet.NBTCompound{
		{Name: "is_waxed", Tag: packet.NBTByte(1)},
		{Name: "front_text", Tag: signFace(lines, ids, true)},
		{Name: "back_text", Tag: signFace([4]string{}, ids, false)},
	}
}

// Sign entity nbt carrying coords for old eras
func signEntityNBT(x, y, z int, lines [4]string, ids *ModernIDs) packet.NBTCompound {
	entity := packet.NBTCompound{
		{Name: "x", Tag: packet.NBTInt(int32(x))},
		{Name: "y", Tag: packet.NBTInt(int32(y))},
		{Name: "z", Tag: packet.NBTInt(int32(z))},
		{Name: "id", Tag: packet.NBTString("minecraft:sign")},
	}
	return append(entity, signTextNBT(lines, ids).(packet.NBTCompound)...)
}

// One sign side with its four message lines
func signFace(lines [4]string, ids *ModernIDs, glow bool) packet.Tag {
	messages := packet.NBTList{}
	for _, line := range lines {
		text := line
		if !ids.VarIntHeightmaps {
			// Older groups still store lines as json
			raw, _ := json.Marshal(line)
			text = string(raw)
		}
		messages.Elem = append(messages.Elem, packet.NBTString(text))
	}
	ink, lit := "black", packet.NBTByte(0)
	if glow {
		ink, lit = signInk, packet.NBTByte(1)
	}
	return packet.NBTCompound{
		{Name: "has_glowing_text", Tag: lit},
		{Name: "color", Tag: packet.NBTString(ink)},
		{Name: "messages", Tag: messages},
	}
}

// Writes full bright sky light for the column
func writeModernLight(w *bytes.Buffer) {
	full := uint64(1)<<modernLightSections - 1
	writeBitset(w, full)
	writeBitset(w, 0)
	writeBitset(w, 0)
	writeBitset(w, full)

	bright := bytes.Repeat([]byte{0xff}, 2048)
	mcproto.WriteVarInt(w, modernLightSections)
	for range modernLightSections {
		mcproto.WriteVarInt(w, 2048)
		w.Write(bright)
	}
	mcproto.WriteVarInt(w, 0)
}

// Writes one long bitset in wire shape
func writeBitset(w *bytes.Buffer, bits uint64) {
	if bits == 0 {
		mcproto.WriteVarInt(w, 0)
		return
	}
	mcproto.WriteVarInt(w, 1)
	packet.WriteNum(w, bits)
}
