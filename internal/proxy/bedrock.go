package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// RakNet offline message marker
var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

// Built-in Geyser template, its udp ports are bedrock gateways
const GeyserTemplateID = "builtin-geyser"

const (
	raknetUnconnectedPing     = 0x01
	raknetUnconnectedPingOpen = 0x02
	raknetOpenRequest1        = 0x05
	raknetUnconnectedPong     = 0x1c

	// Clients pad Open Connection Request 1 to their MTU probe, which
	// starts at 1492 and never drops under RakNet's 576 floor minus
	// the IPv6 and UDP headers, short ones are not real joins
	raknetJoinMinLen = 400

	// Display only, clients list any protocol and judge it on join
	bedrockPongProtocol = 844
	bedrockPongVersion  = "1.21"
	// Whole pong status string, keeps replies near ping size
	bedrockPongMaxStatus = 100

	// One bedrock wake attempt per server per window
	bedrockWakeCooldown = 5 * time.Second
	// First hold after a failed or crashed start, doubles per failure
	bedrockWakeBackoff = 10 * time.Second
	// Longest hold a crash looping server earns
	bedrockWakeMaxBackoff = 5 * time.Minute
	// Bounds a paused resume started from a udp packet
	bedrockWakeTimeout = 30 * time.Second
)

// What a client datagram means to the wake path
type raknetKind int

const (
	raknetOther raknetKind = iota
	raknetPing
	raknetJoin
)

// Sorts a datagram by its RakNet offline header
func classifyRakNet(b []byte) raknetKind {
	if len(b) == 0 {
		return raknetOther
	}
	switch b[0] {
	case raknetUnconnectedPing, raknetUnconnectedPingOpen:
		// id, time, magic, client guid
		if len(b) >= 33 && bytes.Equal(b[9:25], raknetMagic) {
			return raknetPing
		}
	case raknetOpenRequest1:
		// id, magic, protocol, mtu padding
		if len(b) >= raknetJoinMinLen && bytes.Equal(b[1:17], raknetMagic) {
			return raknetJoin
		}
	}
	return raknetOther
}

// Strips the pong field separator from display text
func bedrockField(s string) string {
	return strings.NewReplacer(";", ",", "\n", " ", "\r", " ").Replace(s)
}

// Cuts s to at most n bytes without splitting a rune
func truncateUTF8(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Stable pong guid per route, survives relay restarts
func bedrockGUID(routeID string) int64 {
	h := fnv.New32a()
	h.Write([]byte(routeID))
	return int64(h.Sum32())
}

// Answers an unconnected ping for a relay that speaks for its server
func bedrockPong(ping []byte, guid int64, motd string, maxPlayers, port int) []byte {
	line1, line2, _ := strings.Cut(motd, "\n")
	line1, line2 = bedrockField(line1), bedrockField(line2)
	gid := strconv.FormatInt(guid, 10)
	ps := strconv.Itoa(port)
	build := func(l1, l2 string) string {
		return strings.Join([]string{
			"MCPE", l1, strconv.Itoa(bedrockPongProtocol), bedrockPongVersion,
			"0", strconv.Itoa(maxPlayers), gid, l2, "Survival", "1", ps, ps,
		}, ";") + ";"
	}
	// Name gives way first, the wake hint second
	budget := bedrockPongMaxStatus - len(build("", ""))
	line2 = truncateUTF8(line2, budget)
	line1 = truncateUTF8(line1, budget-len(line2))
	status := build(line1, line2)

	out := make([]byte, 0, 35+len(status))
	out = append(out, raknetUnconnectedPong)
	// Echo the client's ping time
	out = append(out, ping[1:9]...)
	out = binary.BigEndian.AppendUint64(out, uint64(guid))
	out = append(out, raknetMagic...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(status)))
	return append(out, status...)
}

// Status lines a sleeping server's bedrock relay answers with
func bedrockOfflineMOTD(server *v1.Server, cfg *v1.ServerProperties) string {
	head := server.GetName()
	if cfg != nil && cfg.OfflineMotd != nil && *cfg.OfflineMotd != "" {
		head, _, _ = strings.Cut(*cfg.OfflineMotd, "\n")
	}
	return head + "\n§7join to wake"
}

// What a bedrock join should do to its server
type bedrockAction int

const (
	bedrockNone bedrockAction = iota
	bedrockResume
	bedrockStart
)

// Same states the java path wakes from
func bedrockWakeAction(status v1.ServerStatus, wakeEnabled, sleeping bool) bedrockAction {
	switch {
	case !wakeEnabled:
		return bedrockNone
	case sleeping:
		return bedrockResume
	case serverOffline(status):
		return bedrockStart
	}
	return bedrockNone
}

// Single flight per server, a cooldown between attempts, and a growing
// hold after starts that fail or leave the server crashed
type wakeLimiter struct {
	mu       sync.Mutex
	cooldown time.Duration
	now      func() time.Time
	inflight map[string]bool
	last     map[string]time.Time
	failures map[string]int
	holdTill map[string]time.Time
}

func newWakeLimiter(cooldown time.Duration) *wakeLimiter {
	return &wakeLimiter{
		cooldown: cooldown,
		now:      time.Now,
		inflight: make(map[string]bool),
		last:     make(map[string]time.Time),
		failures: make(map[string]int),
		holdTill: make(map[string]time.Time),
	}
}

// Claims an attempt, false while one runs or a window holds
func (l *wakeLimiter) begin(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.inflight[id] {
		return false
	}
	if last, ok := l.last[id]; ok && now.Sub(last) < l.cooldown {
		return false
	}
	if now.Before(l.holdTill[id]) {
		return false
	}
	l.inflight[id] = true
	l.last[id] = now
	return true
}

func (l *wakeLimiter) end(id string) {
	l.mu.Lock()
	delete(l.inflight, id)
	l.mu.Unlock()
}

// Doubles the hold after a failed or crashed start
func (l *wakeLimiter) fail(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[id]++
	hold := bedrockWakeMaxBackoff
	if n := l.failures[id]; n <= 6 {
		hold = min(bedrockWakeBackoff<<(n-1), bedrockWakeMaxBackoff)
	}
	l.holdTill[id] = l.now().Add(hold)
}

// Clears the hold once a server wakes from a clean state
func (l *wakeLimiter) succeed(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, id)
	delete(l.holdTill, id)
}

// Udp relay hook, RakNet clients resend joins so attempts are limited
func (m *Manager) bedrockWake(serverID string, stats *RouteStats) {
	if !m.bedrockWakes.begin(serverID) {
		return
	}
	go func() {
		defer m.bedrockWakes.end(serverID)
		if m.wakeForBedrock(serverID) && stats != nil {
			stats.Wakes.Add(1)
		}
	}()
}

// Wakes or starts the server with fresh rows, routes can lag status.
// Reports whether a resume or start was asked for.
func (m *Manager) wakeForBedrock(serverID string) bool {
	m.mu.Lock()
	gate := m.gate
	m.mu.Unlock()
	if gate == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), bedrockWakeTimeout)
	defer cancel()

	server, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return false
	}
	cfg, err := m.store.GetServerProperties(ctx, serverID)
	if err != nil {
		cfg = nil
	}
	wake := propEnabled(cfg, func(c *v1.ServerProperties) *bool { return c.EnableWakeOnConnect })
	_, sleeping := gate.SleepingInfo(serverID)

	switch bedrockWakeAction(server.Status, wake, sleeping) {
	case bedrockResume:
		m.logger.Info("Waking paused server %s for Bedrock join", server.Name)
		if err := gate.WakeServer(ctx, serverID); err != nil {
			m.logger.Error("Failed to wake server %s for Bedrock join: %v", server.Name, err)
			m.bedrockWakes.fail(serverID)
		} else {
			m.bedrockWakes.succeed(serverID)
		}
		return true
	case bedrockStart:
		m.logger.Info("Starting stopped server %s for Bedrock join", server.Name)
		err := gate.StartServer(serverID)
		switch {
		case errors.Is(err, ErrServerBusy):
			m.logger.Info("Not starting server %s for Bedrock join: %v", server.Name, err)
			return false
		case err != nil:
			m.logger.Error("Failed to start server %s for Bedrock join: %v", server.Name, err)
			m.bedrockWakes.fail(serverID)
		case server.Status == v1.ServerStatus_SERVER_STATUS_ERROR:
			// Crashed servers retry on a growing hold, forged joins can't loop them
			m.bedrockWakes.fail(serverID)
		default:
			m.bedrockWakes.succeed(serverID)
		}
		return true
	}
	return false
}

// True for a proxied udp port that carries bedrock players
func isBedrockRelay(templateID string, port *v1.NetworkPort) bool {
	if port == nil || !port.ProxyEnabled || port.HostPort <= 0 || port.Protocol != v1.ModuleProtocol_MODULE_PROTOCOL_UDP {
		return false
	}
	// Older geyser modules predate the port flag
	return port.Bedrock || templateID == GeyserTemplateID
}

// True when any port relays bedrock through the proxy. Template id is
// empty for server ports, which count only when marked Bedrock.
func HasBedrockRelay(templateID string, ports []*v1.NetworkPort) bool {
	for _, port := range ports {
		if isBedrockRelay(templateID, port) {
			return true
		}
	}
	return false
}

// Bedrock subset of a port list
func bedrockRelayPorts(templateID string, ports []*v1.NetworkPort) []*v1.NetworkPort {
	var out []*v1.NetworkPort
	for _, port := range ports {
		if isBedrockRelay(templateID, port) {
			out = append(out, port)
		}
	}
	return out
}
