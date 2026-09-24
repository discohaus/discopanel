package mcproto

// Config clientbound ids, stable for 766 and up
const (
	CfgCBCookieRequest = 0x00
	CfgCBPluginMessage = 0x01
	CfgCBDisconnect    = 0x02
	CfgCBFinish        = 0x03
	CfgCBKeepAlive     = 0x04
	CfgCBPing          = 0x05
	CfgCBRegistryData  = 0x07
	CfgCBAddRespack    = 0x09
	CfgCBFeatureFlags  = 0x0c
	CfgCBUpdateTags    = 0x0d
	CfgCBKnownPacks    = 0x0e
)

// Config serverbound ids, stable for 766 and up
const (
	CfgSBClientInfo     = 0x00
	CfgSBCookieResponse = 0x01
	CfgSBPluginMessage  = 0x02
	CfgSBFinishAck      = 0x03
	CfgSBKeepAlive      = 0x04
	CfgSBPong           = 0x05
	CfgSBRespackAnswer  = 0x06
	CfgSBKnownPacks     = 0x07
)
