package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/hub"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
)

// Client keepalive cadence and patience
const (
	shimKeepAliveEvery = 10 * time.Second
	shimKeepAliveGrace = 30 * time.Second
)

// Live lobby presence the shim mirrors
type HubPuppet interface {
	Events() <-chan family.Event
	Move(pos family.Pos) error
	Chat(text string) error
	EntityID() int32
	Spawn() family.Pos
	Close()
}

// Dials one puppet into the lobby
type PuppetDialer func(ctx context.Context, addr, name string, protocol int32) (HubPuppet, error)

// Hub world source and bake cache guarded together
type hubState struct {
	mu      sync.Mutex
	source  func() *hub.Grid
	last    *hub.Grid
	bundles map[int32][][]byte
}

// Installs the callback resolving the hub grid
func (sh *ShimRuntime) SetGridSource(source func() *hub.Grid) {
	sh.hub.mu.Lock()
	sh.hub.source = source
	sh.hub.last = nil
	sh.hub.bundles = nil
	sh.hub.mu.Unlock()
}

// Fresh grid under the lock, bakes reset on change
func (sh *ShimRuntime) gridLocked() *hub.Grid {
	if sh.hub.source == nil {
		return nil
	}
	grid := sh.hub.source()
	if grid != sh.hub.last {
		sh.hub.last = grid
		sh.hub.bundles = make(map[int32][][]byte)
	}
	return grid
}

// Installs the puppet dialer once available
func (sh *ShimRuntime) SetPuppetDialer(dial PuppetDialer) {
	sh.dialMu.Lock()
	sh.puppetDial = dial
	sh.dialMu.Unlock()
}

func (sh *ShimRuntime) puppetDialer() PuppetDialer {
	sh.dialMu.Lock()
	defer sh.dialMu.Unlock()
	return sh.puppetDial
}

// Grid snapshot when one is installed
func (sh *ShimRuntime) hubGrid() *hub.Grid {
	sh.hub.mu.Lock()
	defer sh.hub.mu.Unlock()
	return sh.gridLocked()
}

// Bakes or reuses chunks for one protocol
func (sh *ShimRuntime) bundleFor(codec family.Codec, protocol int32) ([][]byte, error) {
	sh.hub.mu.Lock()
	defer sh.hub.mu.Unlock()
	grid := sh.gridLocked()
	if grid == nil {
		return nil, fmt.Errorf("no hub grid installed")
	}
	if cached, ok := sh.hub.bundles[protocol]; ok {
		return cached, nil
	}
	baked, err := codec.BakeChunks(grid, protocol)
	if err != nil {
		return nil, err
	}
	sh.hub.bundles[protocol] = baked
	return baked, nil
}

// Mirrors one legacy client through a lobby puppet
// Reports false when this client can't be hosted
func (sh *ShimRuntime) serveCrossFamily(s *ListenerSocket, clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket, route Route, login *mcproto.LoginStart, stats *RouteStats) bool {
	protocol := int32(handshake.ProtocolVersion)
	codec := family.Lookup(protocol)
	if codec == nil {
		return false
	}
	dial := sh.puppetDialer()
	if dial == nil {
		return false
	}
	if sh.hubGrid() == nil {
		return false
	}

	clientConn.SetDeadline(time.Now().Add(shimLoginTimeout))
	ctx, cancel := context.WithTimeout(s.ctx, shimLoginTimeout)
	result, err := sh.auth.Authenticate(ctx, br, clientConn, protocol, login)
	cancel()
	if err != nil {
		sh.logger.Info("Hub auth refused %s from %s: %v", login.Name, clientConn.RemoteAddr(), err)
		if result != nil {
			kickStream(result.W, handshake, kickAuthFailed())
		} else {
			s.kick(clientConn, handshake, kickAuthFailed())
		}
		return true
	}

	profile := family.PlayerEntry{Name: result.Name}
	if result.Profile != nil {
		if id, err := mojang.UUIDBytes(result.Profile.ID); err == nil {
			profile.UUID = id
		}
		profile.Properties = result.Profile.Properties
	} else {
		profile.UUID = mojang.OfflineUUID(result.Name)
	}

	bundle, err := sh.bundleFor(codec, protocol)
	if err != nil {
		sh.logger.Error("Hub bake failed for protocol %d: %v", protocol, err)
		kickStream(result.W, handshake, kickNotAccepting())
		return true
	}

	dialCtx, dialCancel := context.WithTimeout(s.ctx, shimLoginTimeout)
	pup, err := dial(dialCtx, route.BackendAddr(), result.Name, route.McProtocol)
	dialCancel()
	if err != nil {
		sh.logger.Error("Hub puppet dial failed for %s: %v", result.Name, err)
		kickStream(result.W, handshake, kickNotAccepting())
		return true
	}
	defer pup.Close()

	offset := codec.YOffset(protocol)
	spawn := offsetPosDown(pup.Spawn(), offset)
	join := family.JoinData{
		Profile:    profile,
		EntityID:   pup.EntityID(),
		Spawn:      spawn,
		ViewChunks: bundle,
		SpawnBlock: [3]int{int(spawn.X), int(spawn.Y), int(spawn.Z)},
	}
	sess, err := codec.NewSession(result.R, result.W, protocol, join)
	if err != nil {
		sh.logger.Info("Hub join failed for %s: %v", result.Name, err)
		return true
	}

	clientConn.SetDeadline(time.Time{})
	stats.ActiveConns.Add(1)
	sh.runHubSession(s, clientConn, sess, offset, result.R, pup, route, result.Name)
	stats.ActiveConns.Add(-1)
	return true
}

// Pumps events and actions until either side ends
func (sh *ShimRuntime) runHubSession(s *ListenerSocket, clientConn net.Conn, sess family.Session, offset int, clientR io.Reader, pup HubPuppet, route Route, playerName string) {
	actions := make(chan family.Action, 64)
	readErr := make(chan error, 1)
	go func() {
		for {
			frame, err := packet.ReadFrame(clientR)
			if err != nil {
				readErr <- err
				return
			}
			act, err := sess.Decode(frame)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case actions <- act:
			case <-s.ctx.Done():
				return
			}
		}
	}()

	keepTicker := time.NewTicker(shimKeepAliveEvery)
	defer keepTicker.Stop()
	lastAck := time.Now()
	keepID := int64(0)

	for {
		select {
		case <-s.ctx.Done():
			sess.Disconnect("the panel is shutting down")
			return

		case err := <-readErr:
			sh.logger.Debug("Hub client %s left: %v", playerName, err)
			return

		case act := <-actions:
			switch a := act.(type) {
			case family.ActMove:
				pup.Move(offsetPosUp(a.Pos, offset))
			case family.ActChat:
				pup.Chat(a.Text)
			case family.ActKeepAlive:
				lastAck = time.Now()
			}

		case <-keepTicker.C:
			if time.Since(lastAck) > shimKeepAliveGrace {
				sh.logger.Debug("Hub client %s timed out", playerName)
				return
			}
			keepID++
			if err := sess.KeepAlive(keepID); err != nil {
				return
			}

		case ev, ok := <-pup.Events():
			if !ok {
				sess.Disconnect("the lobby closed the session")
				return
			}
			if done := sh.handleHubEvent(s, sess, ev, offset, route, playerName); done {
				return
			}
		}
	}
}

// Renders one puppet event, true ends the session
func (sh *ShimRuntime) handleHubEvent(s *ListenerSocket, sess family.Session, ev family.Event, offset int, route Route, playerName string) bool {
	switch e := ev.(type) {
	case family.EvTransfer:
		target, ok := s.lookupMCRoute(normalizeWireHostname(e.Host))
		if ok && sh.intents != nil {
			sh.intents.Put(playerName, target.ServerID, 0)
		}
		sess.Disconnect("your world is ready\nclick back then join again to hop over")
		return true

	case family.EvDisconnect:
		sess.Disconnect(e.Reason)
		return true

	case family.EvSpawnPlayer:
		e.Pos = offsetPosDown(e.Pos, offset)
		sess.Encode(e)

	case family.EvEntityMove:
		e.Pos = offsetPosDown(e.Pos, offset)
		sess.Encode(e)

	case family.EvTeleportSelf:
		e.Pos = offsetPosDown(e.Pos, offset)
		sess.Encode(e)

	case family.EvBlockChange:
		e.Y += offset
		sess.Encode(e)

	case family.EvSignText:
		e.Y += offset
		sess.Encode(e)

	default:
		sess.Encode(ev)
	}
	return false
}

// Lifts lobby coordinates into the client world
func offsetPosDown(p family.Pos, offset int) family.Pos {
	p.Y += float64(offset)
	return p
}

// Sinks client coordinates back into the lobby
func offsetPosUp(p family.Pos, offset int) family.Pos {
	p.Y -= float64(offset)
	return p
}
