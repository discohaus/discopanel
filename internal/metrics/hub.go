package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Feeds human-readable agent lines into a server's console stream
type ConsoleSink func(serverID string, message string)

// One live agent stream, hub owns the registry
type Session struct {
	ServerID string
	DataPath string
	sendCh   chan *agentv1.PanelMessage
	closed   chan struct{}
	cancel   context.CancelFunc
	once     sync.Once
}

// Returns panel-to-agent messages the RPC handler pumps into the stream
func (s *Session) Outbound() <-chan *agentv1.PanelMessage {
	return s.sendCh
}

// Reports session teardown to the RPC handler's send pump
func (s *Session) Closed() <-chan struct{} {
	return s.closed
}

// Cancels the owning stream so displacement never leaks handlers
func (s *Session) close() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.closed)
	})
}

// Tracks live agent sessions and routes telemetry
type Hub struct {
	collector *Collector
	bus       *events.Bus
	rec       *Recorder
	log       *logger.Logger

	mu       sync.Mutex
	sessions map[string]*Session
	sink     ConsoleSink
}

func NewHub(collector *Collector, bus *events.Bus, rec *Recorder, log *logger.Logger) *Hub {
	return &Hub{
		collector: collector,
		bus:       bus,
		rec:       rec,
		log:       log,
		sessions:  make(map[string]*Session),
	}
}

// Wires agent lifecycle lines into the server console stream
func (h *Hub) SetConsoleSink(sink ConsoleSink) {
	h.mu.Lock()
	h.sink = sink
	h.mu.Unlock()
}

func (h *Hub) console(serverID, format string, args ...any) {
	h.mu.Lock()
	sink := h.sink
	h.mu.Unlock()
	if sink != nil {
		sink(serverID, fmt.Sprintf(format, args...))
	}
}

// Registers a new live session, reconnect displaces the stale one
func (h *Hub) Attach(serverID, dataPath string, hello *agentv1.Hello, cancel context.CancelFunc) *Session {
	sess := &Session{
		ServerID: serverID,
		DataPath: dataPath,
		sendCh:   make(chan *agentv1.PanelMessage, 64),
		closed:   make(chan struct{}),
		cancel:   cancel,
	}
	h.mu.Lock()
	old := h.sessions[serverID]
	h.sessions[serverID] = sess
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	// Durable stamp keeps replayed exits stale across panel restarts
	if stamp := readExitAck(dataPath); !stamp.IsZero() {
		h.collector.SeedExitFloor(serverID, stamp)
	}
	h.collector.SetAgentConnected(serverID, true)
	h.collector.ApplyAgentHello(serverID, hello)
	h.log.Info("agent: session attached for server %s (runtime %s, %s MC %s)",
		serverID, hello.GetVersion(), hello.GetLoader(), hello.GetMcVersion())
	return sess
}

// Unregisters a session, no-op if a newer session displaced it
func (h *Hub) Detach(serverID string, sess *Session) {
	h.mu.Lock()
	current := h.sessions[serverID] == sess
	if current {
		delete(h.sessions, serverID)
	}
	h.mu.Unlock()
	sess.close()
	if current {
		h.collector.SetAgentConnected(serverID, false)
		h.log.Info("agent: session detached for server %s", serverID)
	}
}

// Reports whether a live agent session exists for the server
func (h *Hub) Connected(serverID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[serverID] != nil
}

func (h *Hub) sendToAgent(ctx context.Context, serverID string, msg *agentv1.PanelMessage) error {
	h.mu.Lock()
	sess := h.sessions[serverID]
	h.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("no agent session for server %s", serverID)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case sess.sendCh <- msg:
		return nil
	case <-sess.closed:
		return fmt.Errorf("agent session for server %s is closing", serverID)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("agent session for server %s is not draining", serverID)
	}
}

// DataPath of the live session, empty when detached
func (h *Hub) sessionDataPath(serverID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sess := h.sessions[serverID]; sess != nil {
		return sess.DataPath
	}
	return ""
}

// Writes one command line to java stdin via supervisor
func (h *Hub) SendConsole(ctx context.Context, serverID, command string) error {
	return h.sendToAgent(ctx, serverID, &agentv1.PanelMessage{Payload: &agentv1.PanelMessage_ConsoleCommand{
		ConsoleCommand: &agentv1.ConsoleCommand{Command: command},
	}})
}

// Broadcasts a chat message in game via the supervisor's tellraw
func (h *Hub) SendChat(ctx context.Context, serverID, sender, message string) error {
	return h.sendToAgent(ctx, serverID, &agentv1.PanelMessage{Payload: &agentv1.PanelMessage_ChatMessage{
		ChatMessage: &agentv1.ChatMessage{Sender: sender, Message: message},
	}})
}

// Ack lets the runtime stop replaying a delivered exit report
func (h *Hub) ackExit(ctx context.Context, serverID string, exitedAtUnixMs int64) {
	err := h.sendToAgent(ctx, serverID, &agentv1.PanelMessage{Payload: &agentv1.PanelMessage_ExitAck{
		ExitAck: &agentv1.ExitAck{ExitedAtUnixMs: exitedAtUnixMs},
	}})
	if err != nil {
		h.log.Debug("agent: exit ack for server %s not sent: %v", serverID, err)
	}
}

// Routes one agent telemetry message to the collector and bus
func (h *Hub) HandleMessage(ctx context.Context, serverID string, msg *agentv1.AgentMessage) {
	switch p := msg.GetPayload().(type) {
	case *agentv1.AgentMessage_Hello:
		// Second hello on an open session means javaagent started
		if p.Hello.GetSource() == agentv1.HelloSource_HELLO_SOURCE_JVM {
			h.collector.SetAgentJvmActive(serverID, true)
			h.console(serverID, "JVM telemetry active (disco-agent %s)", p.Hello.GetVersion())
		}

	case *agentv1.AgentMessage_Ready:
		h.collector.ApplyAgentReady(ctx, serverID, p.Ready.GetStartupSeconds())
		if secs := p.Ready.GetStartupSeconds(); secs > 0 {
			h.console(serverID, "server ready in %.1fs", secs)
		}

	case *agentv1.AgentMessage_Stopping:
		h.console(serverID, "server is shutting down")

	case *agentv1.AgentMessage_Exited:
		// Boot replay repeats the live report, skip stale copies
		fresh := h.collector.ApplyAgentExit(serverID, p.Exited)
		if fresh && p.Exited.GetCrashed() {
			rctx := WithTrace(WithSource(ctx, "runtime"))
			attrs := Attrs{"exit_code": strconv.Itoa(int(p.Exited.GetExitCode()))}
			if path := p.Exited.GetCrashReportPath(); path != "" {
				attrs["crash_report"] = path
			}
			mods := fatalModSummary(p.Exited.GetFatalError())
			if mods != "" {
				attrs["failed_mods"] = mods
			}
			switch {
			case p.Exited.GetOomKilled():
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_OOM, attrs, "server was killed after running out of memory (exit code %d), raise the container memory or lower the heap", p.Exited.GetExitCode())
			case p.Exited.GetBootFailed() && mods != "":
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_BOOT_FAILED, attrs, "server failed to start, mods failed to load: %s", mods)
			case p.Exited.GetBootFailed() && p.Exited.GetCrashReportPath() != "":
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_BOOT_FAILED, attrs, "server failed to start (crash report: %s)", p.Exited.GetCrashReportPath())
			case p.Exited.GetBootFailed():
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_BOOT_FAILED, attrs, "server failed to start (exit code %d)", p.Exited.GetExitCode())
			case mods != "":
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_CRASH, attrs, "server crashed, mods failed to load: %s", mods)
			case p.Exited.GetCrashReportPath() != "":
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_CRASH, attrs, "server crashed (exit code %d, crash report: %s)", p.Exited.GetExitCode(), p.Exited.GetCrashReportPath())
			default:
				h.rec.Announce(rctx, serverID, v1.ServerActionKind_SERVER_ACTION_KIND_SERVER_CRASH, attrs, "server exited abnormally (exit code %d)", p.Exited.GetExitCode())
			}
		}
		if fresh {
			// Stamp first so a lost ack never doubles the story
			if ms := p.Exited.GetExitedAtUnixMs(); ms > 0 {
				if err := writeExitAck(h.sessionDataPath(serverID), time.UnixMilli(ms)); err != nil {
					h.log.Debug("agent: exit ack stamp for server %s not written: %v", serverID, err)
				}
			}
		}
		// Acks off the recv loop so telemetry never stalls
		go h.ackExit(ctx, serverID, p.Exited.GetExitedAtUnixMs())

	case *agentv1.AgentMessage_ProcSample:
		h.collector.ApplyAgentProc(serverID, p.ProcSample)

	case *agentv1.AgentMessage_TickSample:
		h.collector.ApplyAgentTick(serverID, p.TickSample)

	case *agentv1.AgentMessage_JvmSample:
		h.collector.ApplyAgentJvm(serverID, p.JvmSample)

	case *agentv1.AgentMessage_PlayerEvent:
		h.handlePlayerEvent(ctx, serverID, p.PlayerEvent)

	case *agentv1.AgentMessage_Roster:
		h.collector.ApplyAgentRoster(serverID, p.Roster.GetOnlinePlayers())

	}
}

// Plain words naming the mods a fatal error blamed
func fatalModSummary(fatal *agentv1.FatalError) string {
	if fatal == nil {
		return ""
	}
	var parts []string
	for _, fm := range fatal.GetFailedMods() {
		name := fm.GetModId()
		if name == "" {
			name = fm.GetFileName()
		}
		if name == "" {
			continue
		}
		if reason := failedModReason(fm); reason != "" {
			name = fmt.Sprintf("%s (%s)", name, reason)
		}
		parts = append(parts, name)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, ", ")
	if more := len(fatal.GetFailedMods()) - len(parts); more > 0 {
		out = fmt.Sprintf("%s and %d more", out, more)
	}
	return out
}

// Short human words for a loader failure key
func failedModReason(fm *agentv1.FailedMod) string {
	if strings.Contains(fm.GetReason(), "missingdependency") {
		if args := fm.GetReasonArgs(); len(args) > 0 {
			return "needs " + args[0]
		}
		return "missing dependency"
	}
	if msg := fm.GetErrorMessage(); msg != "" {
		if len(msg) > 60 {
			msg = msg[:60] + "..."
		}
		return msg
	}
	return ""
}

// Updates roster, emits bus event, replaces agent SLP diffing
func (h *Hub) handlePlayerEvent(ctx context.Context, serverID string, ev *agentv1.PlayerEvent) {
	player := ev.GetPlayer()
	data := map[string]string{"player": player}
	if d := ev.GetDetail(); d != "" {
		data["detail"] = d
	}
	if id := ev.GetUuid(); id != "" {
		data["uuid"] = id
	}

	var eventType v1.TriggeredEventType
	switch ev.GetType() {
	case agentv1.PlayerEventType_PLAYER_EVENT_TYPE_JOIN:
		h.collector.ApplyAgentPlayerChange(serverID, player, true, int(ev.GetPlayersOnline()))
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_JOIN
	case agentv1.PlayerEventType_PLAYER_EVENT_TYPE_LEAVE:
		h.collector.ApplyAgentPlayerChange(serverID, player, false, int(ev.GetPlayersOnline()))
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_LEAVE
	case agentv1.PlayerEventType_PLAYER_EVENT_TYPE_DEATH:
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_DEATH
	case agentv1.PlayerEventType_PLAYER_EVENT_TYPE_ADVANCEMENT:
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_ADVANCEMENT
	case agentv1.PlayerEventType_PLAYER_EVENT_TYPE_CHAT:
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_CHAT
	default:
		return
	}

	if h.bus != nil {
		h.bus.Emit(ctx, events.Event{Type: eventType, ServerId: serverID, Data: data})
	}
}

// Stamp file holds the acked exit time as unix millis
func exitAckPath(dataPath string) string {
	return filepath.Join(dataPath, runtimespec.StateDir, "exit-ack.json")
}

// Reads the stamp, zero time when absent or unreadable
func readExitAck(dataPath string) time.Time {
	if dataPath == "" {
		return time.Time{}
	}
	data, err := os.ReadFile(exitAckPath(dataPath))
	if err != nil {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// Persists the stamp so replays stay stale across panel restarts
func writeExitAck(dataPath string, exitedAt time.Time) error {
	if dataPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(dataPath, runtimespec.StateDir), 0755); err != nil {
		return err
	}
	return os.WriteFile(exitAckPath(dataPath), []byte(strconv.FormatInt(exitedAt.UnixMilli(), 10)), 0644)
}
