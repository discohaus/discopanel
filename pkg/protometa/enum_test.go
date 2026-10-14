package protometa

import (
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Annotated strings must come back from the descriptor
func TestEnumMetadataFromDescriptor(t *testing.T) {
	if got := Label(v1.ServerStatus_SERVER_STATUS_PAUSED); got != "Sleeping" {
		t.Errorf("paused label reads %q", got)
	}
	if got := Desc(v1.ServerStatus_SERVER_STATUS_PAUSED); got == "" {
		t.Error("paused desc reads empty")
	}
	if got := TypeLabel(v1.ServerStatus_SERVER_STATUS_RUNNING); got != "Server Status" {
		t.Errorf("type label reads %q", got)
	}
	if got := Label(v1.ModLoader_MOD_LOADER_FORGE); got != "Minecraft Forge" {
		t.Errorf("forge label reads %q", got)
	}
}

// Names derive from value names when not annotated
func TestEnumNameDerivation(t *testing.T) {
	if got := Name(v1.ServerStatus_SERVER_STATUS_RUNNING); got != "running" {
		t.Errorf("running name reads %q", got)
	}
	back, ok := FromName[v1.ServerStatus]("running")
	if !ok || back != v1.ServerStatus_SERVER_STATUS_RUNNING {
		t.Errorf("running parsed to %v ok=%v", back, ok)
	}
	if _, ok := FromName[v1.ServerStatus]("nonsense"); ok {
		t.Error("unknown name parsed to a status")
	}
}
