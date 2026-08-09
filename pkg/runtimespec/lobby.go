package runtimespec

import "path/filepath"

// Builtin template id marking a lobby module
const LobbyTemplateID = "builtin-lobby"

// Hub grid file the lobby module writes
const HubGridFileName = "hub-grid.json"

// Hub grid path under one server data dir
func HubGridPath(dataPath string) string {
	return filepath.Join(dataPath, StateDir, HubGridFileName)
}
