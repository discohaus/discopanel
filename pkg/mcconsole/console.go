// Parses server console lines into player events
package mcconsole

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"

	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
)

// Bracketed prefix groups every modern server prints
var logPrefixPattern = regexp.MustCompile(`^(?:\[[^\]]*\] ?)+: `)

// Dated prefix beta era servers print
var legacyPrefixPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[[A-Z]+\] `)

var (
	uuidPattern = regexp.MustCompile(`^UUID of player (.{1,48}?) is ([0-9a-fA-F-]{32,36})$`)

	loginPattern = regexp.MustCompile(`^(.{1,48}?) ?\[/[^\]]+\] logged in with entity id \d+`)

	disconnectPattern = regexp.MustCompile(`^(.{1,48}?) lost connection: `)

	chatPattern        = regexp.MustCompile(`^(?:\[Not Secure\] )?<([^>]{1,48})> (.*)$`)
	advancementPattern = regexp.MustCompile(`^(.{1,48}?) has (?:made the advancement|reached the goal|completed the challenge) \[(.+)\]$`)
)

// Death message stems after the victim name
var deathPhrases = []string{
	"was slain by", "was shot by", "was fireballed by", "was pummeled by",
	"was pricked to death", "was stung to death", "was squashed by",
	"was skewered by", "was impaled", "was blown up by", "was killed",
	"was struck by lightning", "was frozen to death by", "was smashed by",
	"was poked to death by", "was squished too much", "was burned to a crisp",
	"was doomed to fall", "was obliterated by",
	"walked into a cactus", "walked into fire", "walked into the danger zone",
	"drowned", "died", "blew up", "burned to death", "starved to death",
	"froze to death", "suffocated in a wall", "withered away",
	"experienced kinetic energy", "hit the ground too hard",
	"fell from a high place", "fell off", "fell while climbing",
	"fell out of the world", "went up in flames", "went off with a bang",
	"tried to swim in lava", "discovered the floor was lava",
	"left the confines of this world",
	"didn't want to live in the same world as",
}

// Strips the log prefix, false when the line has none
func StripLogPrefix(line string) (string, bool) {
	if prefix := logPrefixPattern.FindString(line); prefix != "" {
		return line[len(prefix):], true
	}
	if prefix := legacyPrefixPattern.FindString(line); prefix != "" {
		return line[len(prefix):], true
	}
	return "", false
}

// One player event parsed out of console output
type ConsoleEvent struct {
	Type   agentv1.PlayerEventType
	Player string
	UUID   string
	Detail string
	// Online count after a join or leave, negative otherwise
	Online int
}

// One online player from an authoritative roster source
type RosterPlayer struct {
	Name string
	UUID string
}

// Tracks the online roster from console lines
type ConsoleTracker struct {
	mu     sync.Mutex
	online map[string]bool
	uuids  map[string]string
}

func NewConsoleTracker() *ConsoleTracker {
	return &ConsoleTracker{
		online: make(map[string]bool),
		uuids:  make(map[string]string),
	}
}

// Parses one raw console line, nil when nothing happened
func (t *ConsoleTracker) Handle(raw string) *ConsoleEvent {
	msg, ok := StripLogPrefix(strings.TrimRight(raw, "\r"))
	if !ok {
		return nil
	}
	return t.HandleMessage(msg)
}

// Parses one line with its prefix already stripped
func (t *ConsoleTracker) HandleMessage(msg string) *ConsoleEvent {
	if m := uuidPattern.FindStringSubmatch(msg); m != nil {
		t.SetUUID(m[1], m[2])
		return nil
	}
	if m := loginPattern.FindStringSubmatch(msg); m != nil {
		return t.SetOnline(m[1], true)
	}
	if m := disconnectPattern.FindStringSubmatch(msg); m != nil {
		return t.SetOnline(m[1], false)
	}
	if m := chatPattern.FindStringSubmatch(msg); m != nil {
		if t.Online(m[1]) {
			return t.event(agentv1.PlayerEventType_PLAYER_EVENT_TYPE_CHAT, m[1], m[2], -1)
		}
		return nil
	}
	if m := advancementPattern.FindStringSubmatch(msg); m != nil {
		if t.Online(m[1]) {
			return t.event(agentv1.PlayerEventType_PLAYER_EVENT_TYPE_ADVANCEMENT, m[1], m[2], -1)
		}
		return nil
	}
	if player, found := t.matchDeath(msg); found {
		return t.event(agentv1.PlayerEventType_PLAYER_EVENT_TYPE_DEATH, player, msg, -1)
	}
	return nil
}

// Finds an online player the death message names first
func (t *ConsoleTracker) matchDeath(msg string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name := range t.online {
		if len(msg) <= len(name)+1 || !strings.HasPrefix(msg, name) || msg[len(name)] != ' ' {
			continue
		}
		rest := msg[len(name)+1:]
		for _, phrase := range deathPhrases {
			if strings.HasPrefix(rest, phrase) {
				return name, true
			}
		}
	}
	return "", false
}

// Records a join or leave, nil when nothing changed
func (t *ConsoleTracker) SetOnline(player string, online bool) *ConsoleEvent {
	t.mu.Lock()
	if t.online[player] == online {
		t.mu.Unlock()
		return nil
	}
	if online {
		t.online[player] = true
	} else {
		delete(t.online, player)
	}
	count := len(t.online)
	t.mu.Unlock()

	kind := agentv1.PlayerEventType_PLAYER_EVENT_TYPE_LEAVE
	if online {
		kind = agentv1.PlayerEventType_PLAYER_EVENT_TYPE_JOIN
	}
	return t.event(kind, player, "", count)
}

// Applies an authoritative roster, returns the resulting changes
func (t *ConsoleTracker) Sync(players []RosterPlayer) []*ConsoleEvent {
	current := make(map[string]bool, len(players))
	for _, p := range players {
		if p.Name == "" {
			continue
		}
		current[p.Name] = true
		t.SetUUID(p.Name, p.UUID)
	}

	var joins, leaves []string
	t.mu.Lock()
	for name := range current {
		if !t.online[name] {
			joins = append(joins, name)
		}
	}
	for name := range t.online {
		if !current[name] {
			leaves = append(leaves, name)
		}
	}
	t.mu.Unlock()
	sort.Strings(joins)
	sort.Strings(leaves)

	var events []*ConsoleEvent
	for _, name := range joins {
		if ev := t.SetOnline(name, true); ev != nil {
			events = append(events, ev)
		}
	}
	for _, name := range leaves {
		if ev := t.SetOnline(name, false); ev != nil {
			events = append(events, ev)
		}
	}
	return events
}

// Remembers a player's uuid, blanks are ignored
func (t *ConsoleTracker) SetUUID(player, uuid string) {
	if player == "" || uuid == "" {
		return
	}
	t.mu.Lock()
	t.uuids[player] = uuid
	t.mu.Unlock()
}

func (t *ConsoleTracker) UUID(player string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.uuids[player]
}

func (t *ConsoleTracker) Online(player string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.online[player]
}

// Lists online players in name order
func (t *ConsoleTracker) Roster() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	players := make([]string, 0, len(t.online))
	for name := range t.online {
		players = append(players, name)
	}
	sort.Strings(players)
	return players
}

func (t *ConsoleTracker) event(kind agentv1.PlayerEventType, player, detail string, online int) *ConsoleEvent {
	return &ConsoleEvent{Type: kind, Player: player, UUID: t.UUID(player), Detail: detail, Online: online}
}

// Builds the tellraw command that shows a chat line
func TellrawCommand(sender, message string) string {
	var component strings.Builder
	enc := json.NewEncoder(&component)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"text": "<" + sender + "> " + message})
	return "tellraw @a " + strings.TrimSpace(component.String())
}
