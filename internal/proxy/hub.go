package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/mcproto/mojang"
	"github.com/discohaus/discopanel/pkg/mcproto/packet"
	"github.com/discohaus/discopanel/pkg/mcproto/session"
	"github.com/discohaus/discopanel/pkg/minecraft"
)

// Login phase budget for one hub client
const hubLoginTimeout = 30 * time.Second

// Client keepalive cadence and patience
const (
	hubKeepAliveEvery = 10 * time.Second
	hubKeepAliveGrace = 30 * time.Second
)

// Cadence for counts, gate light, and readiness
const hubTickEvery = 5 * time.Second

// One backend probe budget per waking world
const hubReadyDial = 1500 * time.Millisecond

// Depth under bedrock that rescues a faller
const hubVoidDepth = 16

// Most clients the lobby hosts at once
const hubMaxMembers = 20

// Chat header matching the lobby wordmark
const hubChatHeader = "§f§lDisco§a§lPanel §8· §7lobby"

// Status hover lines the card lists at most
const hubSampleLines = 8

// Active relay conns per server for gate signs
type HubCounts func() map[string]int64

// Panel hosted lobby shared by every socket
type HubRuntime struct {
	auth    *session.ServerAuth
	logger  *logger.Logger
	intents *IntentTable

	done     chan struct{}
	stopOnce sync.Once

	countsMu sync.Mutex
	counts   HubCounts

	wakeMu sync.RWMutex
	wake   ServerGate

	mu         sync.Mutex
	enabled    bool
	onlineMode bool
	gen        int64
	joining    int
	targets    []family.Target
	gateBoxes  []family.GateBox
	online     map[string]int64
	grids      map[int32]*family.Grid
	bundles    map[int32][][]byte
	members    map[int64]*hubMember
	nextMember int64
	nextEntity int32
}

// Room signal fanned into one member loop
type memberSignal struct {
	evs  []family.Event
	hop  *family.Target
	drop string
}

// One client standing in the lobby
type hubMember struct {
	id          int64
	entityID    int32
	protocol    int32
	offset      int
	origHost    string
	origPort    int
	entry       family.PlayerEntry
	pos         family.Pos
	inGate      int
	pending     string
	pendingName string
	signals     chan memberSignal
	fate        chan memberSignal
}

// Builds the hub runtime with one shared keypair
func NewHubRuntime(online bool, log *logger.Logger, intents *IntentTable) (*HubRuntime, error) {
	auth, err := session.NewServerAuth(online)
	if err != nil {
		return nil, err
	}
	h := &HubRuntime{
		auth:       auth,
		logger:     log,
		intents:    intents,
		done:       make(chan struct{}),
		onlineMode: online,
		gateBoxes:  family.GateBoxes(0),
		online:     make(map[string]int64),
		grids:      make(map[int32]*family.Grid),
		bundles:    make(map[int32][][]byte),
		members:    make(map[int64]*hubMember),
	}
	go h.tickLoop()
	return h, nil
}

// Ends the tick loop for good
func (h *HubRuntime) Stop() {
	h.stopOnce.Do(func() { close(h.done) })
}

// Registers the wake gate for sleeping worlds
func (h *HubRuntime) SetGate(gate ServerGate) {
	h.wakeMu.Lock()
	h.wake = gate
	h.wakeMu.Unlock()
}

func (h *HubRuntime) getGate() ServerGate {
	h.wakeMu.RLock()
	defer h.wakeMu.RUnlock()
	return h.wake
}

// Installs the live counts source
func (h *HubRuntime) SetCounts(counts HubCounts) {
	h.countsMu.Lock()
	h.counts = counts
	h.countsMu.Unlock()
}

func (h *HubRuntime) countsFn() HubCounts {
	h.countsMu.Lock()
	defer h.countsMu.Unlock()
	return h.counts
}

// Turns the lobby on or off
func (h *HubRuntime) SetEnabled(on bool) {
	h.mu.Lock()
	h.enabled = on
	h.mu.Unlock()
}

// Reports whether the lobby answers joins
func (h *HubRuntime) Enabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled
}

// Turns mojang session checks on or off
func (h *HubRuntime) SetOnline(on bool) {
	h.mu.Lock()
	h.onlineMode = on
	h.mu.Unlock()
}

// Reports whether mojang session checks run
func (h *HubRuntime) OnlineMode() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.onlineMode
}

// Auth copy honoring the current online switch
func (h *HubRuntime) authFor() *session.ServerAuth {
	a := *h.auth
	h.mu.Lock()
	a.Online = h.onlineMode
	h.mu.Unlock()
	return &a
}

// Members standing in the lobby right now
func (h *HubRuntime) Population() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.members)
}

// Waiting members counted per pending server
func (h *HubRuntime) WaitingByServer() map[string]int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int32)
	for _, m := range h.members {
		if m.pending != "" {
			out[m.pending]++
		}
	}
	return out
}

// Replaces the fleet the gates offer
func (h *HubRuntime) SetTargets(targets []family.Target) {
	family.SortTargets(targets)
	h.mu.Lock()
	h.applyTargetsLocked(targets)
	h.mu.Unlock()
}

// Overlays counts then installs changed fleets
func (h *HubRuntime) applyTargetsLocked(targets []family.Target) {
	for i := range targets {
		targets[i].Online = int(h.online[targets[i].ID])
	}
	if slices.Equal(h.targets, targets) {
		return
	}
	h.targets = targets
	h.gateBoxes = family.GateBoxes(len(targets))
	h.refreshLooksLocked()
}

// Target standing in one gate slot
func (h *HubRuntime) slotTargetLocked(slot int) *family.Target {
	return h.targetAtLocked(family.TargetForSlot(slot, len(h.targets)))
}

// Target at one sorted fleet index
func (h *HubRuntime) targetAtLocked(i int) *family.Target {
	if i < 0 || i >= len(h.targets) {
		return nil
	}
	return &h.targets[i]
}

// Rebuilds room grids and streams every difference
// Members always see the same world fresh bakes get
func (h *HubRuntime) refreshLooksLocked() {
	h.gen++
	h.bundles = make(map[int32][][]byte)
	for protocol, old := range h.grids {
		fresh := family.BuildHub(h.targets, protocol)
		h.grids[protocol] = fresh
		evs := gridChangeEvents(old, fresh)
		if len(evs) == 0 {
			continue
		}
		for _, m := range h.members {
			if m.protocol != protocol {
				continue
			}
			h.signal(m, memberSignal{evs: evs})
		}
	}
}

// Grid differences rendered as room events
func gridChangeEvents(old, fresh *family.Grid) []family.Event {
	blocks, signs := family.DiffGrids(old, fresh)
	evs := make([]family.Event, 0, len(blocks)+len(signs))
	for _, b := range blocks {
		evs = append(evs, family.EvBlockChange{X: b.X, Y: b.Y, Z: b.Z, Block: b.Block})
		// Fresh beacons need entities before they beam
		if b.Block == "beacon" {
			evs = append(evs, family.EvBeaconInit{X: b.X, Y: b.Y, Z: b.Z})
		}
	}
	for _, s := range signs {
		evs = append(evs, family.EvSignText{X: s.X, Y: s.Y, Z: s.Z, Lines: s.Lines})
	}
	return evs
}

// Bakes or reuses grid and chunks for one protocol
// Slow bakes run off the lock, stale bakes stay uncached
func (h *HubRuntime) bundleFor(codec family.Codec, protocol int32) ([][]byte, *family.Grid, error) {
	h.mu.Lock()
	grid := h.grids[protocol]
	if grid != nil {
		if baked, ok := h.bundles[protocol]; ok {
			h.mu.Unlock()
			return baked, grid, nil
		}
	}
	if grid == nil {
		grid = family.BuildHub(h.targets, protocol)
		h.grids[protocol] = grid
	}
	gen := h.gen
	h.mu.Unlock()

	baked, err := codec.BakeChunks(grid, protocol)
	if err != nil {
		return nil, nil, err
	}

	h.mu.Lock()
	if h.gen == gen {
		h.bundles[protocol] = baked
	}
	h.mu.Unlock()
	return baked, grid, nil
}

// Hands one signal over without ever blocking
// Session ending signals ride their own reserved lane
func (h *HubRuntime) signal(m *hubMember, sig memberSignal) {
	if sig.hop != nil || sig.drop != "" {
		select {
		case m.fate <- sig:
		default:
		}
		return
	}
	select {
	case m.signals <- sig:
	default:
	}
}

// Chat line straight to one member
func (h *HubRuntime) tell(m *hubMember, line string) {
	h.signal(m, memberSignal{evs: []family.Event{family.EvChat{Text: line}}})
}

// Adds a member, reveals the room back to it
// Seen grid catches up to the current room look
func (h *HubRuntime) join(m *hubMember, seen *family.Grid) []family.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Membership swallows the join reservation
	if h.joining > 0 {
		h.joining--
	}
	h.members[m.id] = m
	var world []family.Event
	if cur := h.grids[m.protocol]; cur != nil && cur != seen {
		world = gridChangeEvents(seen, cur)
	}
	for _, o := range h.members {
		if o.id == m.id {
			continue
		}
		// Same account elsewhere gets dropped and hidden
		if o.entry.UUID == m.entry.UUID {
			h.signal(o, memberSignal{drop: "you joined the lobby from somewhere else"})
			continue
		}
		world = append(world,
			family.EvPlayerAdd{Entry: o.entry},
			family.EvSpawnPlayer{EntityID: o.entityID, UUID: o.entry.UUID, Pos: o.pos})
		h.signal(o, memberSignal{evs: []family.Event{
			family.EvPlayerAdd{Entry: m.entry},
			family.EvSpawnPlayer{EntityID: m.entityID, UUID: m.entry.UUID, Pos: m.pos},
		}})
	}
	return world
}

// Drops a member and clears it from the room
func (h *HubRuntime) leave(m *hubMember) {
	h.mu.Lock()
	// Tab entry survives while the same account remains
	keep := false
	delete(h.members, m.id)
	for _, o := range h.members {
		if o.entry.UUID == m.entry.UUID {
			keep = true
		}
	}
	for _, o := range h.members {
		evs := []family.Event{family.EvEntityRemove{IDs: []int32{m.entityID}}}
		if !keep {
			evs = append(evs, family.EvPlayerRemove{UUID: m.entry.UUID})
		}
		h.signal(o, memberSignal{evs: evs})
	}
	h.mu.Unlock()
}

// Applies one move, reports a freshly entered gate
func (h *HubRuntime) move(m *hubMember, pos family.Pos) *family.Target {
	h.mu.Lock()
	defer h.mu.Unlock()
	m.pos = pos
	for _, o := range h.members {
		if o.id == m.id {
			continue
		}
		h.signal(o, memberSignal{evs: []family.Event{family.EvEntityMove{EntityID: m.entityID, Pos: pos}}})
	}
	slot := -1
	for i, box := range h.gateBoxes {
		if box.Contains(pos.X, pos.Y, pos.Z) {
			slot = i
			break
		}
	}
	entered := slot >= 0 && slot != m.inGate
	m.inGate = slot
	if !entered {
		return nil
	}
	t := h.slotTargetLocked(slot)
	if t == nil {
		return nil
	}
	picked := *t
	return &picked
}

// Walks or types one member toward a target
func (h *HubRuntime) approach(m *hubMember, t *family.Target) {
	switch family.StateOf(t, m.protocol) {
	case family.StateVersionGap:
		mine, _ := mcproto.NewestVersionForProtocol(m.protocol)
		h.tell(m, fmt.Sprintf("§6%s runs minecraft %s §7and you're on %s", t.Name, t.Version, mine))
		return
	case family.StateRunning:
		h.signal(m, memberSignal{hop: t})
		return
	case family.StateOffline:
		h.tell(m, fmt.Sprintf("§8%s is offline §7ask whoever runs it to start it", t.Name))
		return
	}
	h.mu.Lock()
	held := m.pending == t.ID
	m.pending, m.pendingName = t.ID, t.Name
	h.mu.Unlock()
	if held || t.Waking {
		h.tell(m, fmt.Sprintf("§d%s is starting §7you'll hop over when it's ready", t.Name))
		return
	}
	h.tell(m, fmt.Sprintf("§dwaking %s §7the beam turns blue when it's ready", t.Name))
	go h.wakeTarget(t.ID)
}

// Wakes or cold starts one sleeping world
func (h *HubRuntime) wakeTarget(id string) {
	gate := h.getGate()
	if gate == nil {
		h.failPending(id)
		return
	}
	if _, sleeping := gate.SleepingInfo(id); sleeping {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := gate.WakeServer(ctx, id)
		cancel()
		if err != nil {
			h.logger.Error("Hub wake failed for server %s: %v", id, err)
			h.failPending(id)
		}
		return
	}
	if err := gate.StartServer(id); err != nil {
		h.logger.Error("Hub start failed for server %s: %v", id, err)
		h.failPending(id)
	}
}

// Clears one world's waiters after a failed wake
func (h *HubRuntime) failPending(id string) {
	h.mu.Lock()
	for _, m := range h.members {
		if m.pending != id {
			continue
		}
		m.pending, m.pendingName = "", ""
		h.tell(m, "§cthat world had trouble starting §7check discopanel to see why")
	}
	h.mu.Unlock()
}

// Runs counts, gate light, and readiness checks
func (h *HubRuntime) tickLoop() {
	ticker := time.NewTicker(hubTickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.tick()
		}
	}
}

// One pass of counts and pending readiness
func (h *HubRuntime) tick() {
	var fresh map[string]int64
	if counts := h.countsFn(); counts != nil {
		fresh = counts()
	}

	h.mu.Lock()
	if fresh != nil {
		h.online = fresh
		targets := slices.Clone(h.targets)
		h.applyTargetsLocked(targets)
	}
	waiting := make(map[string]family.Target)
	for _, m := range h.members {
		if m.pending == "" {
			continue
		}
		for _, t := range h.targets {
			if t.ID == m.pending && t.Running {
				waiting[t.ID] = t
			}
		}
	}
	h.mu.Unlock()

	ready := make(map[string]family.Target, len(waiting))
	for id, t := range waiting {
		// Unresolved addresses keep the member parked
		if t.Addr == "" {
			continue
		}
		conn, err := net.DialTimeout("tcp", t.Addr, hubReadyDial)
		if err != nil {
			continue
		}
		conn.Close()
		ready[id] = t
	}
	if len(ready) == 0 {
		return
	}

	h.mu.Lock()
	for _, m := range h.members {
		t, ok := ready[m.pending]
		if !ok {
			continue
		}
		m.pending, m.pendingName = "", ""
		picked := t
		h.signal(m, memberSignal{hop: &picked})
	}
	h.mu.Unlock()
}

// Chat menu listing every world for one member
func (h *HubRuntime) menuLines(protocol int32) []string {
	h.mu.Lock()
	targets := slices.Clone(h.targets)
	h.mu.Unlock()

	lines := []string{hubChatHeader}
	if len(targets) == 0 {
		return append(lines, "§7no worlds yet §8· §7create one in §fdisco§apanel")
	}
	for i, t := range targets {
		lines = append(lines, fmt.Sprintf("§7%d §8▸ §b%s §8· %s", i+1, t.Name, family.ChatStatus(&t, protocol)))
	}
	return append(lines, "§7walk through a gate or type a number to join")
}

// Matches typed text to one target
func (h *HubRuntime) matchTarget(text string) *family.Target {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n, err := strconv.Atoi(text); err == nil {
		if t := h.targetAtLocked(n - 1); t != nil {
			picked := *t
			return &picked
		}
		return nil
	}
	for _, t := range h.targets {
		if strings.EqualFold(t.Name, text) {
			picked := t
			return &picked
		}
	}
	return nil
}

// Routes one chat line to a pick or the room
func (h *HubRuntime) chat(m *hubMember, text string) {
	trimmed := strings.TrimSpace(text)
	if t := h.matchTarget(trimmed); t != nil {
		h.approach(m, t)
		return
	}
	switch strings.ToLower(trimmed) {
	case "menu", "lobby", "servers", "list", "help":
		for _, line := range h.menuLines(m.protocol) {
			h.tell(m, line)
		}
		return
	}
	line := fmt.Sprintf("§f<%s> %s", m.entry.Name, text)
	h.mu.Lock()
	for _, o := range h.members {
		h.signal(o, memberSignal{evs: []family.Event{family.EvChat{Text: line}}})
	}
	h.mu.Unlock()
}

// Server list card the lobby answers pings with
func (h *HubRuntime) statusCard() synthStatus {
	h.mu.Lock()
	population := len(h.members)
	targets := slices.Clone(h.targets)
	h.mu.Unlock()

	sample := make([]string, 0, hubSampleLines+1)
	for _, t := range targets {
		if len(sample) == hubSampleLines {
			break
		}
		sample = append(sample, fmt.Sprintf("§b%s §8· %s", t.Name, family.ChatStatus(&t, 0)))
	}
	if extra := len(targets) - hubSampleLines; extra > 0 {
		sample = append(sample, fmt.Sprintf("§7and %d more", extra))
	}
	if len(sample) == 0 {
		sample = []string{"§7no worlds yet", "§7create one in §fdisco§apanel"}
	}
	return synthStatus{
		desc:       hubChatHeader,
		version:    "DiscoPanel",
		maxPlayers: hubMaxMembers,
		online:     population,
		favicon:    minecraft.DefaultFavicon(),
		sample:     sample,
	}
}

// Lifts room coordinates into one client's world
func clientPos(p family.Pos, offset int) family.Pos {
	p.Y += float64(offset)
	return p
}

// Sinks client coordinates back into the room
func roomPos(p family.Pos, offset int) family.Pos {
	p.Y -= float64(offset)
	return p
}

// Serves one lobby join from login start onward
// Reports false when this client can't be hosted
func (h *HubRuntime) serve(s *ListenerSocket, clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket, login *mcproto.LoginStart, hold *family.Target) bool {
	protocol := int32(handshake.ProtocolVersion)
	codec := family.Lookup(protocol)
	if codec == nil || !h.Enabled() {
		return false
	}

	// Reservation holds the slot through the login dance
	h.mu.Lock()
	full := len(h.members)+h.joining >= hubMaxMembers
	if !full {
		h.joining++
	}
	h.mu.Unlock()
	if full {
		s.kick(clientConn, handshake, kickHubFull())
		return true
	}
	joined := false
	defer func() {
		if joined {
			return
		}
		h.mu.Lock()
		h.joining--
		h.mu.Unlock()
	}()

	clientConn.SetDeadline(time.Now().Add(hubLoginTimeout))
	ctx, cancel := context.WithTimeout(s.ctx, hubLoginTimeout)
	result, err := h.authFor().Authenticate(ctx, br, clientConn, protocol, login)
	cancel()
	if err != nil {
		h.logger.Info("Hub auth refused %s from %s: %v", login.Name, clientConn.RemoteAddr(), err)
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

	bundle, grid, err := h.bundleFor(codec, protocol)
	if err != nil {
		h.logger.Error("Hub bake failed for protocol %d: %v", protocol, err)
		kickStream(result.W, handshake, kickNotAccepting())
		return true
	}

	h.mu.Lock()
	h.nextMember++
	h.nextEntity++
	m := &hubMember{
		id:       h.nextMember,
		entityID: h.nextEntity,
		protocol: protocol,
		offset:   codec.YOffset(protocol),
		origHost: normalizeWireHostname(handshake.ServerAddress),
		origPort: int(handshake.ServerPort),
		entry:    profile,
		pos:      family.Pos{X: grid.SpawnX, Y: grid.SpawnY, Z: grid.SpawnZ, Yaw: grid.SpawnYaw, OnGround: true},
		inGate:   -1,
		signals:  make(chan memberSignal, 256),
		fate:     make(chan memberSignal, 1),
	}
	h.mu.Unlock()

	spawn := clientPos(m.pos, m.offset)
	join := family.JoinData{
		Profile:    profile,
		EntityID:   m.entityID,
		Spawn:      spawn,
		ViewChunks: bundle,
		SpawnBlock: [3]int{int(spawn.X), int(spawn.Y), int(spawn.Z)},
	}
	join.RefuseNote = h.plazaRefusalNote()
	join.FailNote = "the lobby couldn't finish your join\nplease try again"
	sess, err := codec.NewSession(result.R, result.W, protocol, join)
	if errors.Is(err, family.ErrHandedOff) {
		h.logger.Info("Hub handed off %s during config", result.Name)
		return true
	}
	if err != nil {
		h.logger.Info("Hub join failed for %s: %v", result.Name, err)
		return true
	}

	if hold != nil {
		m.pending, m.pendingName = hold.ID, hold.Name
	}

	clientConn.SetDeadline(time.Time{})
	joined = true
	world := h.join(m, grid)
	h.runMember(s, sess, m, result.R, world)
	h.leave(m)
	return true
}

// Refusal screen handing out every world address
func (h *HubRuntime) plazaRefusalNote() string {
	h.mu.Lock()
	targets := slices.Clone(h.targets)
	h.mu.Unlock()
	lines := []string{"§cYour client isn't compatible with this discopanel lobby"}
	if len(targets) == 0 {
		return lines[0]
	}
	lines = append(lines, "§7Connect directly:")
	for _, t := range targets {
		addr := t.Hostname
		if t.Port != 0 && t.Port != 25565 {
			addr = fmt.Sprintf("%s:%d", t.Hostname, t.Port)
		}
		lines = append(lines, fmt.Sprintf("§b%s§7: §f%s", t.Name, addr))
	}
	return strings.Join(lines, "\n")
}

// Greeting chat burst for one fresh member
func (h *HubRuntime) greeting(m *hubMember) []string {
	lines := h.menuLines(m.protocol)
	h.mu.Lock()
	pendingName := m.pendingName
	h.mu.Unlock()
	if pendingName != "" {
		lines = append(lines, fmt.Sprintf("§d%s is starting §7you'll hop over when it's ready", pendingName))
	}
	return lines
}

// Pumps actions and room signals until either side ends
func (h *HubRuntime) runMember(s *ListenerSocket, sess family.Session, m *hubMember, clientR io.Reader, world []family.Event) {
	// Close of gone frees a reader parked on a full lane
	gone := make(chan struct{})
	defer close(gone)
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
			case <-gone:
				return
			}
		}
	}()

	if done := h.deliver(sess, m, memberSignal{evs: world}); done {
		return
	}
	for _, line := range h.greeting(m) {
		sess.Encode(family.EvChat{Text: line})
	}

	keepTicker := time.NewTicker(hubKeepAliveEvery)
	defer keepTicker.Stop()
	lastAck := time.Now()
	keepID := int64(0)

	for {
		select {
		case <-s.ctx.Done():
			sess.Disconnect("the panel is shutting down")
			return

		case err := <-readErr:
			h.logger.Debug("Hub client %s left: %v", m.entry.Name, err)
			return

		case act := <-actions:
			switch a := act.(type) {
			case family.ActMove:
				pos := roomPos(a.Pos, m.offset)
				if pos.Y < family.SpawnY-hubVoidDepth {
					pos = family.Pos{X: family.SpawnX, Y: family.SpawnY, Z: family.SpawnZ, Yaw: family.SpawnYaw, OnGround: true}
					h.move(m, pos)
					h.deliver(sess, m, memberSignal{evs: []family.Event{family.EvTeleportSelf{Pos: pos}}})
					continue
				}
				if t := h.move(m, pos); t != nil {
					h.approach(m, t)
				}
			case family.ActChat:
				h.chat(m, a.Text)
			case family.ActKeepAlive:
				lastAck = time.Now()
			}

		case <-keepTicker.C:
			if time.Since(lastAck) > hubKeepAliveGrace {
				h.logger.Debug("Hub client %s timed out", m.entry.Name)
				return
			}
			keepID++
			if err := sess.KeepAlive(keepID); err != nil {
				return
			}

		case sig := <-m.fate:
			if done := h.deliver(sess, m, sig); done {
				return
			}

		case sig := <-m.signals:
			if done := h.deliver(sess, m, sig); done {
				return
			}
		}
	}
}

// Renders one signal, true ends the session
func (h *HubRuntime) deliver(sess family.Session, m *hubMember, sig memberSignal) bool {
	if sig.drop != "" {
		sess.Disconnect(sig.drop)
		return true
	}
	if sig.hop != nil {
		t := sig.hop
		sess.Encode(family.EvChat{Text: fmt.Sprintf("§dtaking you to §b%s", t.Name)})
		// Rejoin claim routes the client after it lands
		h.intents.Put(m.entry.Name, t.ID, 0)
		// Hop rides the address the client already reached
		host, port := m.origHost, m.origPort
		if host == "" {
			host, port = t.Hostname, t.Port
		}
		if ok, err := sess.Transfer(host, port); ok {
			if err != nil {
				h.logger.Error("Hub transfer failed for %s: %v", m.entry.Name, err)
			} else {
				h.logger.Info("Hub sent %s to %s", m.entry.Name, t.Name)
			}
			return true
		}
		// Transferless eras rejoin by hand instead
		sess.Disconnect("your world is ready\njoin again to hop over")
		return true
	}

	for _, ev := range sig.evs {
		switch e := ev.(type) {
		case family.EvSpawnPlayer:
			e.Pos = clientPos(e.Pos, m.offset)
			sess.Encode(e)
		case family.EvEntityMove:
			e.Pos = clientPos(e.Pos, m.offset)
			sess.Encode(e)
		case family.EvTeleportSelf:
			e.Pos = clientPos(e.Pos, m.offset)
			sess.Encode(e)
		case family.EvBlockChange:
			e.Y += m.offset
			sess.Encode(e)
		case family.EvSignText:
			e.Y += m.offset
			sess.Encode(e)
		case family.EvBeaconInit:
			e.Y += m.offset
			sess.Encode(e)
		default:
			sess.Encode(ev)
		}
	}
	return false
}

// Finds one target by server id
func (h *HubRuntime) targetByID(id string) *family.Target {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.targets {
		if t.ID == id {
			picked := t
			return &picked
		}
	}
	return nil
}

// Sends a kick over an already wrapped stream
func kickStream(w io.Writer, handshake *mcproto.HandshakePacket, screen minecraft.Text) {
	reason, err := json.Marshal(screen.Render(int(handshake.ProtocolVersion)))
	if err != nil {
		return
	}
	mcproto.WriteLoginDisconnectJSON(w, reason)
}
