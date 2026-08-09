package family

import (
	"bytes"
	"fmt"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Damage types shared by every modern group
var modernDamageBase = []string{
	"arrow", "bad_respawn_point", "cactus", "cramming", "dragon_breath",
	"drown", "dry_out", "explosion", "fall", "falling_anvil",
	"falling_block", "falling_stalactite", "fireball", "fireworks",
	"fly_into_wall", "freeze", "generic", "generic_kill", "hot_floor",
	"in_fire", "in_wall", "indirect_magic", "lava", "lightning_bolt",
	"magic", "mob_attack", "mob_attack_no_aggro", "mob_projectile",
	"on_fire", "out_of_world", "outside_border", "player_attack",
	"player_explosion", "sonic_boom", "spit", "stalagmite", "starve",
	"sting", "sweet_berry_bush", "thorns", "thrown", "trident",
	"unattributed_fireball", "wither", "wither_skull",
}

// Later groups append their new damage types
// Deltas verified against vanilla data per version
func modernDamageTypes(protocol int32) []string {
	out := append([]string{}, modernDamageBase...)
	if protocol >= 767 {
		out = append(out, "campfire", "wind_charge")
	}
	if protocol >= 768 {
		out = append(out, "ender_pearl", "mace_smash")
	}
	if protocol >= 774 {
		out = append(out, "spear")
	}
	if protocol >= 776 {
		out = append(out, "sulfur_cube_hot")
	}
	if protocol < 766 {
		out = dropNames(out, "spit")
	}
	if protocol < 763 {
		out = dropNames(out, "generic_kill", "outside_border")
	}
	return out
}

// Removes names younger than the target group
func dropNames(list []string, gone ...string) []string {
	out := list[:0]
	for _, name := range list {
		keep := true
		for _, g := range gone {
			if name == g {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, name)
		}
	}
	return out
}

// Chat type entry for compound era clients
func chatTypeNBT() packet.Tag {
	decoration := func(key string) packet.Tag {
		return packet.NBTCompound{
			{Name: "translation_key", Tag: packet.NBTString(key)},
			{Name: "parameters", Tag: packet.NBTList{Elem: []packet.Tag{
				packet.NBTString("sender"), packet.NBTString("content"),
			}}},
		}
	}
	return packet.NBTCompound{
		{Name: "chat", Tag: decoration("chat.type.text")},
		{Name: "narration", Tag: decoration("chat.type.text.narrate")},
	}
}

// One registry list in the compound shape
func compoundRegistry(name string, entries []string, data func(string) packet.Tag) packet.NBTField {
	values := packet.NBTList{}
	for i, entry := range entries {
		values.Elem = append(values.Elem, packet.NBTCompound{
			{Name: "name", Tag: packet.NBTString("minecraft:" + entry)},
			{Name: "id", Tag: packet.NBTInt(int32(i))},
			{Name: "element", Tag: data(entry)},
		})
	}
	return packet.NBTField{Name: name, Tag: packet.NBTCompound{
		{Name: "type", Tag: packet.NBTString(name)},
		{Name: "value", Tag: values},
	}}
}

// Vanilla overworld captured from the 1.19.4 wire
const codecDimension762JSON = `{"monster_spawn_light_level": {"type": "minecraft:uniform", "value": {"max_inclusive": 7, "min_inclusive": 0}}, "infiniburn": "#minecraft:infiniburn_overworld", "effects": "minecraft:overworld", "ultrawarm": 0, "height": 384, "logical_height": 384, "natural": 1, "min_y": -64, "bed_works": 1, "coordinate_scale": 1, "piglin_safe": 0, "has_ceiling": 0, "has_skylight": 1, "ambient_light": 0, "monster_spawn_block_light_limit": 0, "has_raids": 1, "respawn_anchor_works": 0}`

// Vanilla overworld captured from the 1.19.2 wire
const codecDimension760JSON = `{"piglin_safe": 0, "natural": 1, "ambient_light": 0, "monster_spawn_block_light_limit": 0, "infiniburn": "#minecraft:infiniburn_overworld", "respawn_anchor_works": 0, "has_skylight": 1, "bed_works": 1, "effects": "minecraft:overworld", "has_raids": 1, "logical_height": 384, "coordinate_scale": 1, "monster_spawn_light_level": {"type": "minecraft:uniform", "value": {"min_inclusive": 0, "max_inclusive": 7}}, "min_y": -64, "ultrawarm": 0, "has_ceiling": 0, "height": 384}`

// Vanilla overworld captured from the 1.17 wire
const codecDimension755JSON = `{"piglin_safe": 0, "natural": 1, "ambient_light": 0, "infiniburn": "minecraft:infiniburn_overworld", "respawn_anchor_works": 0, "has_skylight": 1, "bed_works": 1, "effects": "minecraft:overworld", "has_raids": 1, "logical_height": 256, "coordinate_scale": 1, "min_y": 0, "has_ceiling": 0, "ultrawarm": 0, "height": 256}`

// Vanilla overworld captured from the 1.16.2 wire
const codecDimension754JSON = `{"bed_works": 1, "has_ceiling": 0, "coordinate_scale": 1, "piglin_safe": 0, "has_skylight": 1, "ultrawarm": 0, "infiniburn": "minecraft:infiniburn_overworld", "effects": "minecraft:overworld", "has_raids": 1, "ambient_light": 0, "logical_height": 256, "natural": 1, "respawn_anchor_works": 0}`

// Overworld element for one codec era
func legacyDimensionNBT(protocol int32) packet.Tag {
	var src string
	switch {
	case protocol >= 762:
		src = codecDimension762JSON
	case protocol >= 757:
		src = codecDimension760JSON
	case protocol >= 755:
		src = codecDimension755JSON
	default:
		src = codecDimension754JSON
	}
	tag, err := packet.JSONToNBT([]byte(src))
	if err != nil {
		return hubDimensionNBT()
	}
	return tag
}

// Plains element for one codec era
func legacyBiomeNBT(protocol int32) packet.Tag {
	if protocol >= 762 {
		return hubBiomeNBT()
	}
	// Older biomes name their rainfall outright
	out := packet.NBTCompound{
		{Name: "precipitation", Tag: packet.NBTString("rain")},
		{Name: "temperature", Tag: packet.NBTFloat(0.8)},
		{Name: "downfall", Tag: packet.NBTFloat(0.4)},
		{Name: "effects", Tag: packet.NBTCompound{
			{Name: "fog_color", Tag: packet.NBTInt(12638463)},
			{Name: "water_color", Tag: packet.NBTInt(4159204)},
			{Name: "water_fog_color", Tag: packet.NBTInt(329011)},
			{Name: "sky_color", Tag: packet.NBTInt(7907327)},
		}},
	}
	if protocol <= 756 {
		// Terrain era biomes still shape worldgen
		out = append(out,
			packet.NBTField{Name: "depth", Tag: packet.NBTFloat(0.125)},
			packet.NBTField{Name: "scale", Tag: packet.NBTFloat(0.05)},
			packet.NBTField{Name: "category", Tag: packet.NBTString("plains")},
		)
	}
	return out
}

// Chat entry with the 1.19 decoration shape
func chatType759NBT() packet.Tag {
	decorated := func(key string) packet.Tag {
		return packet.NBTCompound{
			{Name: "decoration", Tag: packet.NBTCompound{
				{Name: "translation_key", Tag: packet.NBTString(key)},
				{Name: "style", Tag: packet.NBTCompound{}},
				{Name: "parameters", Tag: packet.NBTList{Elem: []packet.Tag{
					packet.NBTString("sender"), packet.NBTString("content"),
				}}},
			}},
		}
	}
	narration := decorated("chat.type.text.narrate").(packet.NBTCompound)
	narration = append(packet.NBTCompound{{Name: "priority", Tag: packet.NBTString("chat")}}, narration...)
	return packet.NBTCompound{
		{Name: "chat", Tag: decorated("chat.type.text")},
		{Name: "narration", Tag: narration},
	}
}

// Registry codec compound for join game eras
func dimensionCodecNBT(protocol int32) packet.Tag {
	out := packet.NBTCompound{
		compoundRegistry("minecraft:dimension_type", []string{"overworld"},
			func(string) packet.Tag { return legacyDimensionNBT(protocol) }),
		compoundRegistry("minecraft:worldgen/biome", []string{"plains"},
			func(string) packet.Tag { return legacyBiomeNBT(protocol) }),
	}
	if protocol >= 759 {
		chat := chatTypeNBT
		if protocol == 759 {
			chat = chatType759NBT
		}
		out = append(out, compoundRegistry("minecraft:chat_type", []string{"chat"},
			func(string) packet.Tag { return chat() }))
	}
	if protocol >= 762 {
		out = append(out, compoundRegistry("minecraft:damage_type", modernDamageTypes(protocol), damageTypeNBT))
	}
	return out
}

// Whole registry compound for compound era configs
func registryCompoundNBT(protocol int32) packet.Tag {
	return packet.NBTCompound{
		compoundRegistry("minecraft:dimension_type", []string{"overworld"},
			func(string) packet.Tag { return hubDimensionNBT() }),
		compoundRegistry("minecraft:worldgen/biome", []string{"plains"},
			func(string) packet.Tag { return hubBiomeNBT() }),
		compoundRegistry("minecraft:chat_type", []string{"chat"},
			func(string) packet.Tag { return chatTypeNBT() }),
		compoundRegistry("minecraft:damage_type", modernDamageTypes(protocol), damageTypeNBT),
	}
}

// Overworld dimension sized for the hub bake
func hubDimensionNBT() packet.Tag {
	return packet.NBTCompound{
		{Name: "has_skylight", Tag: packet.NBTByte(1)},
		{Name: "has_ceiling", Tag: packet.NBTByte(0)},
		{Name: "ultrawarm", Tag: packet.NBTByte(0)},
		{Name: "natural", Tag: packet.NBTByte(1)},
		{Name: "coordinate_scale", Tag: packet.NBTDouble(1)},
		{Name: "bed_works", Tag: packet.NBTByte(1)},
		{Name: "respawn_anchor_works", Tag: packet.NBTByte(0)},
		{Name: "min_y", Tag: packet.NBTInt(modernMinY)},
		{Name: "height", Tag: packet.NBTInt(modernSections * 16)},
		{Name: "logical_height", Tag: packet.NBTInt(modernSections * 16)},
		{Name: "infiniburn", Tag: packet.NBTString("#minecraft:infiniburn_overworld")},
		{Name: "effects", Tag: packet.NBTString("minecraft:overworld")},
		{Name: "ambient_light", Tag: packet.NBTFloat(0)},
		{Name: "piglin_safe", Tag: packet.NBTByte(0)},
		{Name: "has_raids", Tag: packet.NBTByte(0)},
		{Name: "monster_spawn_light_level", Tag: packet.NBTInt(0)},
		{Name: "monster_spawn_block_light_limit", Tag: packet.NBTInt(0)},
	}
}

// Plains biome with stock overworld colors
func hubBiomeNBT() packet.Tag {
	return packet.NBTCompound{
		{Name: "has_precipitation", Tag: packet.NBTByte(1)},
		{Name: "temperature", Tag: packet.NBTFloat(0.8)},
		{Name: "downfall", Tag: packet.NBTFloat(0.4)},
		{Name: "effects", Tag: packet.NBTCompound{
			{Name: "fog_color", Tag: packet.NBTInt(12638463)},
			{Name: "water_color", Tag: packet.NBTInt(4159204)},
			{Name: "water_fog_color", Tag: packet.NBTInt(329011)},
			{Name: "sky_color", Tag: packet.NBTInt(7907327)},
		}},
	}
}

// Minimal damage body, death text never shows
func damageTypeNBT(name string) packet.Tag {
	return packet.NBTCompound{
		{Name: "message_id", Tag: packet.NBTString(name)},
		{Name: "scaling", Tag: packet.NBTString("when_caused_by_living_non_player")},
		{Name: "exhaustion", Tag: packet.NBTFloat(0)},
	}
}

// Vanilla overworld mirrored for attribute era clients
const attribDimensionJSON = `{"ambient_light": 0.0, "attributes": {"minecraft:audio/ambient_sounds": {"mood": {"block_search_extent": 8, "offset": 2.0, "sound": "minecraft:ambient.cave", "tick_delay": 6000}}, "minecraft:audio/background_music": {"creative": {"max_delay": 24000, "min_delay": 12000, "sound": "minecraft:music.creative"}, "default": {"max_delay": 24000, "min_delay": 12000, "sound": "minecraft:music.game"}}, "minecraft:gameplay/bed_rule": {"can_set_spawn": "always", "can_sleep": "when_dark", "error_message": {"translate": "block.minecraft.bed.no_sleep"}}, "minecraft:gameplay/nether_portal_spawns_piglin": true, "minecraft:gameplay/respawn_anchor_works": false, "minecraft:visual/ambient_light_color": "#0a0a0a", "minecraft:visual/cloud_color": "#ccffffff", "minecraft:visual/cloud_height": 192.33, "minecraft:visual/fog_color": "#c0d8ff", "minecraft:visual/sky_color": "#78a7ff"}, "coordinate_scale": 1.0, "default_clock": "minecraft:overworld", "has_ceiling": false, "has_ender_dragon_fight": false, "has_skylight": true, "height": 384, "infiniburn": "#minecraft:infiniburn_overworld", "logical_height": 384, "min_y": -64, "monster_spawn_block_light_limit": 0, "monster_spawn_light_level": {"type": "minecraft:uniform", "max_inclusive": 7, "min_inclusive": 0}, "timelines": "#minecraft:in_overworld"}`

// Vanilla plains mirrored for attribute era clients
const attribBiomeJSON = `{"attributes": {"minecraft:visual/sky_color": "#78a7ff"}, "downfall": 0.4, "effects": {"water_color": "#3f76e4"}, "has_precipitation": true, "temperature": 0.8}`

// Builds one registry data packet body
func registryBody(registry string, names []string, data func(string) packet.Tag) ([]byte, error) {
	var body bytes.Buffer
	mcproto.WriteVarInt(&body, 0x07)
	packet.WriteString(&body, registry)
	mcproto.WriteVarInt(&body, mcproto.VarInt(len(names)))
	for _, name := range names {
		packet.WriteString(&body, "minecraft:"+name)
		packet.WriteBool(&body, true)
		if err := packet.WriteNetworkNBT(&body, data(name)); err != nil {
			return nil, err
		}
	}
	return body.Bytes(), nil
}

// Registry payloads a modern join requires
func hubRegistries(protocol int32) ([][]byte, error) {
	ids := ModernIDsFor(protocol)
	if ids == nil {
		return nil, fmt.Errorf("no modern ids for protocol %d", protocol)
	}

	dimTag, biomeTag := hubDimensionNBT(), hubBiomeNBT()
	if ids.AttribRegistries {
		var err error
		if dimTag, err = packet.JSONToNBT([]byte(attribDimensionJSON)); err != nil {
			return nil, err
		}
		if biomeTag, err = packet.JSONToNBT([]byte(attribBiomeJSON)); err != nil {
			return nil, err
		}
	}

	var out [][]byte
	if ids.ClockTime {
		// Empty bodies keep every vanilla clock default
		clocks, err := registryBody("minecraft:world_clock", []string{"overworld", "the_end"},
			func(string) packet.Tag { return packet.NBTCompound{} })
		if err != nil {
			return nil, err
		}
		out = append(out, clocks)
	}
	dim, err := registryBody("minecraft:dimension_type", []string{"overworld"},
		func(string) packet.Tag { return dimTag })
	if err != nil {
		return nil, err
	}
	biome, err := registryBody("minecraft:worldgen/biome", []string{"plains"},
		func(string) packet.Tag { return biomeTag })
	if err != nil {
		return nil, err
	}
	damage, err := registryBody("minecraft:damage_type", modernDamageTypes(protocol), damageTypeNBT)
	if err != nil {
		return nil, err
	}
	return append(out, dim, biome, damage), nil
}
