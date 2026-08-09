package family

// Play state ids for one modern protocol group
type ModernIDs struct {
	// Clientbound
	JoinGame         int32
	KeepAliveCB      int32
	ChunkData        int32
	UnloadChunk      int32
	DisconnectCB     int32
	PlayerInfoUpdate int32
	PlayerInfoRemove int32
	AddEntity        int32
	EntityTeleport   int32
	EntityPosSync    int32
	EntityPos        int32
	EntityPosRot     int32
	EntityRot        int32
	EntityHeadLook   int32
	RemoveEntities   int32
	SyncPlayerPos    int32
	SystemChat       int32
	PlayerChat       int32
	SpawnPosition    int32
	Abilities        int32
	TimeUpdate       int32
	BlockUpdate      int32
	Transfer         int32
	GameEvent        int32
	SetCenterChunk   int32
	SetRenderDist    int32
	BlockEntityData  int32

	// Serverbound
	KeepAliveSB     int32
	PlayerPos       int32
	PlayerPosRot    int32
	PlayerRot       int32
	PlayerStatus    int32
	ChatSB          int32
	ClientInfoSB    int32
	PluginMessageSB int32
	TeleportConfirm int32
	ClientCommand   int32

	// Config state ids for the group
	CfgFinishCB     int32
	CfgRegistryData int32
	CfgFeatureFlags int32
	CfgKnownPacks   int32
	CfgFinishAckSB  int32

	// Registry facts riding along with the group
	PlayerTypeID int32
	// Chunk block entity id for beacons in the group
	BeaconEntity int32
	// Player spawn packet id when the era has one
	SpawnPlayer int32
	// Movement flags widen at this group when true
	WideTeleport bool
	// Heightmaps ride as varint arrays when true
	VarIntHeightmaps bool
	// Chunk data arrays drop their length prefix when true
	UnprefixedSections bool
	// Serverbound chat carries the checksum byte when true
	ChatChecksum bool
	// Login success carries the strict handling flag when true
	StrictFlag bool
	// Spawn velocity rides the packed vec when true
	LpVelocity bool
	// Spawn position carries dimension and pitch when true
	GlobalRespawn bool
	// Time rides world clock updates when true
	ClockTime bool
	// Registries use the attribute shape when true
	AttribRegistries bool
	// Login play carries the online mode flag when true
	LoginOnlineFlag bool
	// Login success carries a session uuid when true
	LoginSessionID bool
	// Config registries ride one compound when true
	RegistryCompound bool
	// Join game names the dimension type when true
	DimTypeString bool
	// Chat and kick text ride json strings when true
	JSONText bool
	// Join game carries the codec, no config state
	NoConfigPhase bool
	// Network nbt still carries named roots when true
	NamedNBT bool
	// Position syncs end with a dismount flag when true
	SyncDismount bool
	// Sign lines live in four text rows when true
	SignTextRows bool
	// Player info rides the action list shape when true
	OldPlayerInfo bool
	// Light data opens with a trust flag when true
	TrustEdges bool
	// Chat rides the old chat packet when true
	LegacyChatPacket bool
	// System chat ends with a type id when true
	ChatTypeVarInt bool
	// Login success carries no properties when true
	LoginNoProps bool
	// Chunks use masks with biome arrays when true
	BiomeIntArray bool
	// Section and light masks are varints when true
	VarIntMask bool
	// Spawn position carries no angle when true
	SpawnPosNoAngle bool
	// Join game repeats the dimension inline when true
	DimensionInline bool
	// Join game lacks simulation distance when true
	NoSimDistance bool
	// Separate light packet id when the era has one
	UpdateLight int32
}

// Stamps the config ids shared by 766 and up
func init() {
	for _, ids := range []*ModernIDs{&modern766, &modern768, &modern770, &modern773, &modern775} {
		ids.CfgFinishCB = 0x03
		ids.CfgRegistryData = 0x07
		ids.CfgFeatureFlags = 0x0c
		ids.CfgKnownPacks = 0x0e
		ids.CfgFinishAckSB = 0x03
	}
}

// Ids for protocol 754, masks still ride varints
var legacy754 = ModernIDs{
	JoinGame: 0x24, KeepAliveCB: 0x1f, ChunkData: 0x20, UnloadChunk: 0x1c,
	DisconnectCB: 0x19, PlayerInfoUpdate: 0x32, PlayerInfoRemove: -1,
	AddEntity: 0x00, SpawnPlayer: 0x04, EntityTeleport: 0x56, EntityPosSync: -1,
	EntityPos: 0x27, EntityPosRot: 0x28, EntityRot: 0x29, EntityHeadLook: 0x3a,
	RemoveEntities: 0x36, SyncPlayerPos: 0x34, SystemChat: 0x0e, PlayerChat: -1,
	SpawnPosition: 0x42, Abilities: 0x30, TimeUpdate: 0x4e, BlockUpdate: 0x0b,
	Transfer: -1, GameEvent: 0x1d, SetCenterChunk: 0x40, SetRenderDist: 0x41,
	BlockEntityData: 0x09, UpdateLight: 0x23,
	KeepAliveSB: 0x10, PlayerPos: 0x12, PlayerPosRot: 0x13, PlayerRot: 0x14,
	PlayerStatus: 0x15, ChatSB: 0x03, ClientInfoSB: 0x05, PluginMessageSB: 0x0b,
	TeleportConfirm: 0x00, ClientCommand: 0x04,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	NoConfigPhase: true, NamedNBT: true, JSONText: true, SignTextRows: true,
	OldPlayerInfo: true, TrustEdges: true, LegacyChatPacket: true, LoginNoProps: true,
	BiomeIntArray: true, VarIntMask: true, SpawnPosNoAngle: true,
	DimensionInline: true, NoSimDistance: true,
}

// Ids for protocols 755 and 756, light rides alone
var legacy755 = ModernIDs{
	JoinGame: 0x26, KeepAliveCB: 0x21, ChunkData: 0x22, UnloadChunk: 0x1d,
	DisconnectCB: 0x1a, PlayerInfoUpdate: 0x36, PlayerInfoRemove: -1,
	AddEntity: 0x00, SpawnPlayer: 0x04, EntityTeleport: 0x61, EntityPosSync: -1,
	EntityPos: 0x29, EntityPosRot: 0x2a, EntityRot: 0x2b, EntityHeadLook: 0x3e,
	RemoveEntities: 0x3a, SyncPlayerPos: 0x38, SystemChat: 0x0f, PlayerChat: -1,
	SpawnPosition: 0x4b, Abilities: 0x32, TimeUpdate: 0x58, BlockUpdate: 0x0c,
	Transfer: -1, GameEvent: 0x1e, SetCenterChunk: 0x49, SetRenderDist: 0x4a,
	BlockEntityData: 0x0a, UpdateLight: 0x25,
	KeepAliveSB: 0x0f, PlayerPos: 0x11, PlayerPosRot: 0x12, PlayerRot: 0x13,
	PlayerStatus: 0x14, ChatSB: 0x03, ClientInfoSB: 0x05, PluginMessageSB: 0x0a,
	TeleportConfirm: 0x00, ClientCommand: 0x04,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	NoConfigPhase: true, NamedNBT: true, JSONText: true, SignTextRows: true,
	SyncDismount: true, OldPlayerInfo: true, TrustEdges: true, LegacyChatPacket: true,
	LoginNoProps: true, BiomeIntArray: true, DimensionInline: true, NoSimDistance: true,
}

// Ids for protocols 757 and 758, chunks carry light
var legacy757 = ModernIDs{
	JoinGame: 0x26, KeepAliveCB: 0x21, ChunkData: 0x22, UnloadChunk: 0x1d,
	DisconnectCB: 0x1a, PlayerInfoUpdate: 0x36, PlayerInfoRemove: -1,
	AddEntity: 0x00, SpawnPlayer: 0x04, EntityTeleport: 0x62, EntityPosSync: -1,
	EntityPos: 0x29, EntityPosRot: 0x2a, EntityRot: 0x2b, EntityHeadLook: 0x3e,
	RemoveEntities: 0x3a, SyncPlayerPos: 0x38, SystemChat: 0x0f, PlayerChat: -1,
	SpawnPosition: 0x4b, Abilities: 0x32, TimeUpdate: 0x59, BlockUpdate: 0x0c,
	Transfer: -1, GameEvent: 0x1e, SetCenterChunk: 0x49, SetRenderDist: 0x4a,
	BlockEntityData: 0x0a,
	KeepAliveSB:     0x0f, PlayerPos: 0x11, PlayerPosRot: 0x12, PlayerRot: 0x13,
	PlayerStatus: 0x14, ChatSB: 0x03, ClientInfoSB: 0x05, PluginMessageSB: 0x0a,
	TeleportConfirm: 0x00, ClientCommand: 0x04,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	BeaconEntity:  13,
	NoConfigPhase: true, NamedNBT: true, JSONText: true, SignTextRows: true,
	SyncDismount: true, OldPlayerInfo: true, TrustEdges: true, LegacyChatPacket: true,
	LoginNoProps: true, DimensionInline: true,
}

// Ids for protocol 759, chat signing arrives
var legacy759 = ModernIDs{
	JoinGame: 0x23, KeepAliveCB: 0x1e, ChunkData: 0x1f, UnloadChunk: 0x1a,
	DisconnectCB: 0x17, PlayerInfoUpdate: 0x34, PlayerInfoRemove: -1,
	AddEntity: 0x00, SpawnPlayer: 0x02, EntityTeleport: 0x63, EntityPosSync: -1,
	EntityPos: 0x26, EntityPosRot: 0x27, EntityRot: 0x28, EntityHeadLook: 0x3c,
	RemoveEntities: 0x38, SyncPlayerPos: 0x36, SystemChat: 0x5f, PlayerChat: 0x30,
	SpawnPosition: 0x4a, Abilities: 0x2f, TimeUpdate: 0x59, BlockUpdate: 0x09,
	Transfer: -1, GameEvent: 0x1b, SetCenterChunk: 0x48, SetRenderDist: 0x49,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x11, PlayerPos: 0x13, PlayerPosRot: 0x14, PlayerRot: 0x15,
	PlayerStatus: 0x16, ChatSB: 0x04, ClientInfoSB: 0x07, PluginMessageSB: 0x0c,
	TeleportConfirm: 0x00, ClientCommand: 0x06,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	BeaconEntity:  13,
	NoConfigPhase: true, NamedNBT: true, JSONText: true, SignTextRows: true,
	SyncDismount: true, OldPlayerInfo: true, TrustEdges: true, ChatTypeVarInt: true,
}

// Ids for protocol 760, one more signing pass
var legacy760 = ModernIDs{
	JoinGame: 0x25, KeepAliveCB: 0x20, ChunkData: 0x21, UnloadChunk: 0x1c,
	DisconnectCB: 0x19, PlayerInfoUpdate: 0x37, PlayerInfoRemove: -1,
	AddEntity: 0x00, SpawnPlayer: 0x02, EntityTeleport: 0x66, EntityPosSync: -1,
	EntityPos: 0x28, EntityPosRot: 0x29, EntityRot: 0x2a, EntityHeadLook: 0x3f,
	RemoveEntities: 0x3b, SyncPlayerPos: 0x39, SystemChat: 0x62, PlayerChat: 0x33,
	SpawnPosition: 0x4d, Abilities: 0x31, TimeUpdate: 0x5c, BlockUpdate: 0x09,
	Transfer: -1, GameEvent: 0x1d, SetCenterChunk: 0x4b, SetRenderDist: 0x4c,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x12, PlayerPos: 0x14, PlayerPosRot: 0x15, PlayerRot: 0x16,
	PlayerStatus: 0x17, ChatSB: 0x05, ClientInfoSB: 0x08, PluginMessageSB: 0x0d,
	TeleportConfirm: 0x00, ClientCommand: 0x07,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	BeaconEntity:  13,
	NoConfigPhase: true, NamedNBT: true, JSONText: true, SignTextRows: true,
	SyncDismount: true, OldPlayerInfo: true, TrustEdges: true, ChatTypeVarInt: true,
}

// Ids for protocol 761, join game carries the codec
var legacy761 = ModernIDs{
	JoinGame: 0x24, KeepAliveCB: 0x1f, ChunkData: 0x20, UnloadChunk: 0x1b,
	DisconnectCB: 0x17, PlayerInfoUpdate: 0x36, PlayerInfoRemove: 0x35,
	AddEntity: 0x00, SpawnPlayer: 0x02, EntityTeleport: 0x64, EntityPosSync: -1,
	EntityPos: 0x27, EntityPosRot: 0x28, EntityRot: 0x29, EntityHeadLook: 0x3e,
	RemoveEntities: 0x3a, SyncPlayerPos: 0x38, SystemChat: 0x60, PlayerChat: 0x31,
	SpawnPosition: 0x4c, Abilities: 0x30, TimeUpdate: 0x5a, BlockUpdate: 0x09,
	Transfer: -1, GameEvent: 0x1c, SetCenterChunk: 0x4a, SetRenderDist: 0x4b,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x11, PlayerPos: 0x13, PlayerPosRot: 0x14, PlayerRot: 0x15,
	PlayerStatus: 0x16, ChatSB: 0x05, ClientInfoSB: 0x07, PluginMessageSB: 0x0c,
	TeleportConfirm: 0x00, ClientCommand: 0x06,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	BeaconEntity:  14,
	NoConfigPhase: true, NamedNBT: true, JSONText: true,
	SyncDismount: true, SignTextRows: true,
}

// Ids for protocols 762 and 763, codec still in join
var legacy762 = ModernIDs{
	JoinGame: 0x28, KeepAliveCB: 0x23, ChunkData: 0x24, UnloadChunk: 0x1e,
	DisconnectCB: 0x1a, PlayerInfoUpdate: 0x3a, PlayerInfoRemove: 0x39,
	AddEntity: 0x01, SpawnPlayer: 0x03, EntityTeleport: 0x68, EntityPosSync: -1,
	EntityPos: 0x2b, EntityPosRot: 0x2c, EntityRot: 0x2d, EntityHeadLook: 0x42,
	RemoveEntities: 0x3e, SyncPlayerPos: 0x3c, SystemChat: 0x64, PlayerChat: 0x35,
	SpawnPosition: 0x50, Abilities: 0x34, TimeUpdate: 0x5e, BlockUpdate: 0x0a,
	Transfer: -1, GameEvent: 0x1f, SetCenterChunk: 0x4e, SetRenderDist: 0x4f,
	BlockEntityData: 0x08,
	KeepAliveSB:     0x12, PlayerPos: 0x14, PlayerPosRot: 0x15, PlayerRot: 0x16,
	PlayerStatus: 0x17, ChatSB: 0x05, ClientInfoSB: 0x08, PluginMessageSB: 0x0d,
	TeleportConfirm: 0x00, ClientCommand: 0x07,
	CfgFinishCB: -1, CfgRegistryData: -1, CfgFeatureFlags: -1,
	CfgKnownPacks: -1, CfgFinishAckSB: -1,
	BeaconEntity:  14,
	NoConfigPhase: true, NamedNBT: true, JSONText: true,
}

// Ids for protocols 764, config phase without known packs
var legacy764 = ModernIDs{
	JoinGame: 0x29, KeepAliveCB: 0x24, ChunkData: 0x25, UnloadChunk: 0x1f,
	DisconnectCB: 0x1b, PlayerInfoUpdate: 0x3c, PlayerInfoRemove: 0x3b,
	AddEntity: 0x01, EntityTeleport: 0x6b, EntityPosSync: -1,
	EntityPos: 0x2c, EntityPosRot: 0x2d, EntityRot: 0x2e, EntityHeadLook: 0x44,
	RemoveEntities: 0x40, SyncPlayerPos: 0x3e, SystemChat: 0x67, PlayerChat: 0x37,
	SpawnPosition: 0x52, Abilities: 0x36, TimeUpdate: 0x60, BlockUpdate: 0x09,
	Transfer: -1, GameEvent: 0x20, SetCenterChunk: 0x50, SetRenderDist: 0x51,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x14, PlayerPos: 0x16, PlayerPosRot: 0x17, PlayerRot: 0x18,
	PlayerStatus: 0x19, ChatSB: 0x05, ClientInfoSB: 0x09, PluginMessageSB: 0x0f,
	TeleportConfirm: 0x00, ClientCommand: 0x08,
	CfgFinishCB: 0x02, CfgRegistryData: 0x05, CfgFeatureFlags: 0x07,
	CfgKnownPacks: -1, CfgFinishAckSB: 0x02,
	PlayerTypeID: 122, BeaconEntity: 14,
	RegistryCompound: true, DimTypeString: true, JSONText: true,
}

// Ids for protocol 765, chat text turned to nbt
var legacy765 = ModernIDs{
	JoinGame: 0x29, KeepAliveCB: 0x24, ChunkData: 0x25, UnloadChunk: 0x1f,
	DisconnectCB: 0x1b, PlayerInfoUpdate: 0x3c, PlayerInfoRemove: 0x3b,
	AddEntity: 0x01, EntityTeleport: 0x6d, EntityPosSync: -1,
	EntityPos: 0x2c, EntityPosRot: 0x2d, EntityRot: 0x2e, EntityHeadLook: 0x46,
	RemoveEntities: 0x40, SyncPlayerPos: 0x3e, SystemChat: 0x69, PlayerChat: 0x37,
	SpawnPosition: 0x54, Abilities: 0x36, TimeUpdate: 0x62, BlockUpdate: 0x09,
	Transfer: -1, GameEvent: 0x20, SetCenterChunk: 0x52, SetRenderDist: 0x53,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x15, PlayerPos: 0x17, PlayerPosRot: 0x18, PlayerRot: 0x19,
	PlayerStatus: 0x1a, ChatSB: 0x05, ClientInfoSB: 0x09, PluginMessageSB: 0x10,
	TeleportConfirm: 0x00, ClientCommand: 0x08,
	CfgFinishCB: 0x02, CfgRegistryData: 0x05, CfgFeatureFlags: 0x07,
	CfgKnownPacks: -1, CfgFinishAckSB: 0x02,
	PlayerTypeID: 124, BeaconEntity: 14,
	RegistryCompound: true, DimTypeString: true,
}

// Ids for protocols 766 and 767
var modern766 = ModernIDs{
	JoinGame: 0x2b, KeepAliveCB: 0x26, ChunkData: 0x27, UnloadChunk: 0x21,
	DisconnectCB: 0x1d, PlayerInfoUpdate: 0x3e, PlayerInfoRemove: 0x3d,
	AddEntity: 0x01, EntityTeleport: 0x70, EntityPosSync: -1,
	EntityPos: 0x2e, EntityPosRot: 0x2f, EntityRot: 0x30, EntityHeadLook: 0x48,
	RemoveEntities: 0x42, SyncPlayerPos: 0x40, SystemChat: 0x6c, PlayerChat: 0x39,
	SpawnPosition: 0x56, Abilities: 0x38, TimeUpdate: 0x64, BlockUpdate: 0x09,
	Transfer: 0x73, GameEvent: 0x22, SetCenterChunk: 0x54, SetRenderDist: 0x55,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x18, PlayerPos: 0x1a, PlayerPosRot: 0x1b, PlayerRot: 0x1c,
	PlayerStatus: 0x1d, ChatSB: 0x06, ClientInfoSB: 0x0a, PluginMessageSB: 0x12,
	TeleportConfirm: 0x00, ClientCommand: 0x09,
	PlayerTypeID: 128, BeaconEntity: 14, StrictFlag: true,
}

// Ids for protocols 768 and 769
var modern768 = ModernIDs{
	JoinGame: 0x2c, KeepAliveCB: 0x27, ChunkData: 0x28, UnloadChunk: 0x22,
	DisconnectCB: 0x1d, PlayerInfoUpdate: 0x40, PlayerInfoRemove: 0x3f,
	AddEntity: 0x01, EntityTeleport: 0x77, EntityPosSync: 0x20,
	EntityPos: 0x2f, EntityPosRot: 0x30, EntityRot: 0x32, EntityHeadLook: 0x4d,
	RemoveEntities: 0x47, SyncPlayerPos: 0x42, SystemChat: 0x73, PlayerChat: 0x3b,
	SpawnPosition: 0x5b, Abilities: 0x3a, TimeUpdate: 0x6b, BlockUpdate: 0x09,
	Transfer: 0x7a, GameEvent: 0x23, SetCenterChunk: 0x58, SetRenderDist: 0x59,
	BlockEntityData: 0x07,
	KeepAliveSB:     0x1a, PlayerPos: 0x1c, PlayerPosRot: 0x1d, PlayerRot: 0x1e,
	PlayerStatus: 0x1f, ChatSB: 0x07, ClientInfoSB: 0x0c, PluginMessageSB: 0x14,
	TeleportConfirm: 0x00, ClientCommand: 0x0a,
	PlayerTypeID: 148, BeaconEntity: 15, WideTeleport: true,
}

// Ids for protocols 770 through 772
var modern770 = ModernIDs{
	JoinGame: 0x2b, KeepAliveCB: 0x26, ChunkData: 0x27, UnloadChunk: 0x21,
	DisconnectCB: 0x1c, PlayerInfoUpdate: 0x3f, PlayerInfoRemove: 0x3e,
	AddEntity: 0x01, EntityTeleport: 0x76, EntityPosSync: 0x1f,
	EntityPos: 0x2e, EntityPosRot: 0x2f, EntityRot: 0x31, EntityHeadLook: 0x4c,
	RemoveEntities: 0x46, SyncPlayerPos: 0x41, SystemChat: 0x72, PlayerChat: 0x3a,
	SpawnPosition: 0x5a, Abilities: 0x39, TimeUpdate: 0x6a, BlockUpdate: 0x08,
	Transfer: 0x7a, GameEvent: 0x22, SetCenterChunk: 0x57, SetRenderDist: 0x58,
	BlockEntityData: 0x06,
	KeepAliveSB:     0x1b, PlayerPos: 0x1d, PlayerPosRot: 0x1e, PlayerRot: 0x1f,
	PlayerStatus: 0x20, ChatSB: 0x08, ClientInfoSB: 0x0d, PluginMessageSB: 0x15,
	TeleportConfirm: 0x00, ClientCommand: 0x0b,
	PlayerTypeID: 148, BeaconEntity: 15, WideTeleport: true, VarIntHeightmaps: true,
	UnprefixedSections: true, ChatChecksum: true,
}

// Ids for protocols 773 and 774
var modern773 = ModernIDs{
	JoinGame: 0x30, KeepAliveCB: 0x2b, ChunkData: 0x2c, UnloadChunk: 0x25,
	DisconnectCB: 0x20, PlayerInfoUpdate: 0x44, PlayerInfoRemove: 0x43,
	AddEntity: 0x01, EntityTeleport: 0x7b, EntityPosSync: 0x23,
	EntityPos: 0x33, EntityPosRot: 0x34, EntityRot: 0x36, EntityHeadLook: 0x51,
	RemoveEntities: 0x4b, SyncPlayerPos: 0x46, SystemChat: 0x77, PlayerChat: 0x3f,
	SpawnPosition: 0x5f, Abilities: 0x3e, TimeUpdate: 0x6f, BlockUpdate: 0x08,
	Transfer: 0x7f, GameEvent: 0x26, SetCenterChunk: 0x5c, SetRenderDist: 0x5d,
	BlockEntityData: 0x06,
	KeepAliveSB:     0x1b, PlayerPos: 0x1d, PlayerPosRot: 0x1e, PlayerRot: 0x1f,
	PlayerStatus: 0x20, ChatSB: 0x08, ClientInfoSB: 0x0d, PluginMessageSB: 0x15,
	TeleportConfirm: 0x00, ClientCommand: 0x0b,
	PlayerTypeID: 151, BeaconEntity: 15, WideTeleport: true, VarIntHeightmaps: true,
	UnprefixedSections: true, ChatChecksum: true, LpVelocity: true, GlobalRespawn: true,
}

// Ids for protocols 775 and 776
var modern775 = ModernIDs{
	JoinGame: 0x31, KeepAliveCB: 0x2c, ChunkData: 0x2d, UnloadChunk: 0x25,
	DisconnectCB: 0x20, PlayerInfoUpdate: 0x46, PlayerInfoRemove: 0x45,
	AddEntity: 0x01, EntityTeleport: 0x7d, EntityPosSync: 0x23,
	EntityPos: 0x35, EntityPosRot: 0x36, EntityRot: 0x38, EntityHeadLook: 0x53,
	RemoveEntities: 0x4d, SyncPlayerPos: 0x48, SystemChat: 0x79, PlayerChat: 0x41,
	SpawnPosition: 0x61, Abilities: 0x40, TimeUpdate: 0x71, BlockUpdate: 0x08,
	Transfer: 0x81, GameEvent: 0x26, SetCenterChunk: 0x5e, SetRenderDist: 0x5f,
	BlockEntityData: 0x06,
	KeepAliveSB:     0x1c, PlayerPos: 0x1e, PlayerPosRot: 0x1f, PlayerRot: 0x20,
	PlayerStatus: 0x21, ChatSB: 0x09, ClientInfoSB: 0x0e, PluginMessageSB: 0x16,
	TeleportConfirm: 0x00, ClientCommand: 0x0c,
	PlayerTypeID: 155, BeaconEntity: 15, WideTeleport: true, VarIntHeightmaps: true,
	UnprefixedSections: true, ChatChecksum: true, LpVelocity: true, GlobalRespawn: true,
	ClockTime: true, AttribRegistries: true,
}

// Group table for one modern protocol
func ModernIDsFor(protocol int32) *ModernIDs {
	switch protocol {
	case 754:
		return &legacy754
	case 755, 756:
		return &legacy755
	case 757, 758:
		return &legacy757
	case 759:
		return &legacy759
	case 760:
		return &legacy760
	case 761:
		return &legacy761
	case 762:
		ids := legacy762
		ids.SignTextRows = true
		return &ids
	case 763:
		return &legacy762
	case 764:
		return &legacy764
	case 765:
		return &legacy765
	case 766, 767:
		return &modern766
	case 768:
		ids := modern768
		ids.PlayerTypeID = 148
		return &ids
	case 769:
		ids := modern768
		ids.PlayerTypeID = 147
		return &ids
	case 770:
		ids := modern770
		ids.PlayerTypeID = 148
		return &ids
	case 771, 772:
		ids := modern770
		ids.PlayerTypeID = 149
		return &ids
	case 773:
		ids := modern773
		ids.PlayerTypeID = 151
		return &ids
	case 774:
		ids := modern773
		ids.PlayerTypeID = 155
		return &ids
	case 775:
		ids := modern775
		ids.PlayerTypeID = 155
		return &ids
	case 776:
		ids := modern775
		ids.PlayerTypeID = 156
		ids.LoginOnlineFlag = true
		ids.LoginSessionID = true
		return &ids
	default:
		return nil
	}
}

// Oldest protocol the codec speaks
const ModernFloor = 754

// Newest protocol the modern family speaks
const ModernCeiling = 776

// Puppets must speak the transfer era themselves
const PuppetFloor = 766
