// Package family renders hub sessions per protocol family
package family

import (
	"errors"
	"io"

	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
)

// Absolute position with facing
type Pos struct {
	X, Y, Z    float64
	Yaw, Pitch float32
	OnGround   bool
}

// One tab list entry with its profile
type PlayerEntry struct {
	UUID       [16]byte
	Name       string
	Properties []mojang.Property
}

// Everything a codec needs to join one client
type JoinData struct {
	Profile    PlayerEntry
	EntityID   int32
	Spawn      Pos
	ViewChunks [][]byte
	SpawnBlock [3]int
	// Screen shown to clients answering the fence
	RefuseNote string
	// Screen shown when the join dies partway
	FailNote string
}

// Session ended inside config on purpose
var ErrHandedOff = errors.New("client handed off during config")

// One normalized lobby happening for the client
type Event interface{ isEvent() }

// Player list gained an entry
type EvPlayerAdd struct{ Entry PlayerEntry }

// Player list lost an entry
type EvPlayerRemove struct{ UUID [16]byte }

// Another player appeared in the world
type EvSpawnPlayer struct {
	EntityID int32
	UUID     [16]byte
	Pos      Pos
}

// Another player moved, absolute coordinates
type EvEntityMove struct {
	EntityID int32
	Pos      Pos
}

// Entities left the world
type EvEntityRemove struct{ IDs []int32 }

// Chat or system line for the client
type EvChat struct{ Text string }

// Client got moved by the lobby
type EvTeleportSelf struct{ Pos Pos }

// One block changed in the hub
type EvBlockChange struct {
	X, Y, Z int
	Block   string
}

// Sign text changed on one hub plaque
type EvSignText struct {
	X, Y, Z int
	Lines   [4]string
}

// Beacon appeared and needs its entity to beam
type EvBeaconInit struct {
	X, Y, Z int
}

func (EvPlayerAdd) isEvent()    {}
func (EvPlayerRemove) isEvent() {}
func (EvSpawnPlayer) isEvent()  {}
func (EvEntityMove) isEvent()   {}
func (EvEntityRemove) isEvent() {}
func (EvChat) isEvent()         {}
func (EvTeleportSelf) isEvent() {}
func (EvBlockChange) isEvent()  {}
func (EvSignText) isEvent()     {}
func (EvBeaconInit) isEvent()   {}

// One normalized client wish for the lobby
type Action interface{ isAction() }

// Client moved or turned
type ActMove struct{ Pos Pos }

// Client spoke in chat
type ActChat struct{ Text string }

// Client answered a keepalive
type ActKeepAlive struct{ ID int64 }

// Frame the session ignores on purpose
type ActNone struct{}

func (ActMove) isAction()      {}
func (ActChat) isAction()      {}
func (ActKeepAlive) isAction() {}
func (ActNone) isAction()      {}

// Wire renderer for one protocol family
type Codec interface {
	// Protocol numbers this codec speaks
	Protocols() []int32
	// Runs login tail, config phase, and join sequence
	// Returns the live session holding per client state
	NewSession(r io.Reader, w io.Writer, protocol int32, join JoinData) (Session, error)
	// Bakes framed chunk packets for one grid
	BakeChunks(grid *Grid, protocol int32) ([][]byte, error)
	// World height shift for shallow legacy worlds
	YOffset(protocol int32) int
}

// One joined client the lobby renders into
type Session interface {
	// Renders one event as client packets
	Encode(ev Event) error
	// Turns one client frame into an action
	// Unknown packets come back as ActNone never errors
	Decode(frame []byte) (Action, error)
	// Sends a keepalive probe
	KeepAlive(id int64) error
	// Sends a play state disconnect
	Disconnect(reason string) error
	// Sends the client to another address
	// Reports false when the era lacks transfer
	Transfer(host string, port int) (bool, error)
}

var registry = map[int32]Codec{}

// Registers a codec for every protocol it speaks
func Register(c Codec) {
	for _, p := range c.Protocols() {
		registry[p] = c
	}
}

// Finds the codec speaking one protocol
func Lookup(protocol int32) Codec {
	return registry[protocol]
}

// Protocol numbers with a registered codec
func SupportedProtocols() []int32 {
	out := make([]int32, 0, len(registry))
	for p := range registry {
		out = append(out, p)
	}
	return out
}
