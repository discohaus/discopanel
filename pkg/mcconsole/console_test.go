package mcconsole

import (
	"slices"
	"testing"

	agentv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1"
)

func TestStripLogPrefix(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{"[12:34:56] [Server thread/INFO]: Nick joined the game", "Nick joined the game"},
		{"[12Jul2026 21:57:22.786] [Server thread/INFO] [minecraft/MinecraftServer]: Nick left the game", "Nick left the game"},
		{"[21:57:22 INFO]: <Nick> hello", "<Nick> hello"},
		{"2011-07-31 10:11:12 [INFO] steve [/127.0.0.1:52941] logged in with entity id 229 at (...)", "steve [/127.0.0.1:52941] logged in with entity id 229 at (...)"},
		{"2011-07-31 10:12:30 [INFO] steve lost connection: disconnect.quitting", "steve lost connection: disconnect.quitting"},
	}
	for _, c := range cases {
		msg, ok := StripLogPrefix(c.line)
		if !ok || msg != c.want {
			t.Errorf("StripLogPrefix(%q) = %q, %v, want %q", c.line, msg, ok, c.want)
		}
	}
	if _, ok := StripLogPrefix("Nick joined the game"); ok {
		t.Error("StripLogPrefix matched a prefixless line")
	}
}

func TestLoginAndDisconnectPatterns(t *testing.T) {
	logins := []struct {
		msg  string
		name string
	}{
		{"Nick[/172.18.0.5:53412] logged in with entity id 261 at (8.5, 65.0, 8.5)", "Nick"},
		{"steve [/127.0.0.1:52941] logged in with entity id 229 at (135.5, 63.0, 240.3)", "steve"},
		{".BedrockKid[/172.18.0.9:41234] logged in with entity id 512 at (0.5, 70.0, 0.5)", ".BedrockKid"},
		{"Player With Space[/10.0.0.2:60000] logged in with entity id 7 at (1.0, 2.0, 3.0)", "Player With Space"},
		{"Nick[local:E:1a2b3c4d] logged in with entity id 12 at (0.0, 0.0, 0.0)", ""},
	}
	for _, c := range logins {
		m := loginPattern.FindStringSubmatch(c.msg)
		if c.name == "" {
			if m != nil {
				t.Errorf("loginPattern false positive on %q: %v", c.msg, m)
			}
			continue
		}
		if m == nil || m[1] != c.name {
			t.Errorf("loginPattern(%q) captured %v, want %q", c.msg, m, c.name)
		}
	}

	disconnects := []struct {
		msg  string
		name string
	}{
		{"Nick lost connection: Disconnected", "Nick"},
		{".BedrockKid lost connection: Timed out", ".BedrockKid"},
		{"steve lost connection: disconnect.quitting", "steve"},
	}
	for _, c := range disconnects {
		m := disconnectPattern.FindStringSubmatch(c.msg)
		if m == nil || m[1] != c.name {
			t.Errorf("disconnectPattern(%q) captured %v, want %q", c.msg, m, c.name)
		}
	}
}

func TestConsoleTrackerRoster(t *testing.T) {
	tr := NewConsoleTracker()

	if ev := tr.Handle("[12:34:56] [User Authenticator #1/INFO]: UUID of player Nick is 3b9f1c2d-0000-0000-0000-000000000001"); ev != nil {
		t.Fatalf("uuid line produced an event: %+v", ev)
	}
	ev := tr.Handle("[12:34:56] [Server thread/INFO]: Nick[/172.18.0.5:53412] logged in with entity id 261 at (8.5, 65.0, 8.5)")
	if ev == nil || ev.Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_JOIN || ev.Player != "Nick" || ev.Online != 1 {
		t.Fatalf("login line event = %+v", ev)
	}
	if ev.UUID != "3b9f1c2d-0000-0000-0000-000000000001" {
		t.Errorf("uuid not carried on join, got %q", ev.UUID)
	}

	// The broadcast join line after is a duplicate
	if ev := tr.Handle("[12:34:56] [Server thread/INFO]: Nick joined the game"); ev != nil {
		t.Fatalf("broadcast join produced an event: %+v", ev)
	}

	ev = tr.Handle("[12:35:00] [Server thread/INFO]: <Nick> hello world")
	if ev == nil || ev.Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_CHAT || ev.Detail != "hello world" {
		t.Fatalf("chat event = %+v", ev)
	}
	ev = tr.Handle("[12:36:00] [Server thread/INFO]: Nick has made the advancement [Stone Age]")
	if ev == nil || ev.Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_ADVANCEMENT || ev.Detail != "Stone Age" {
		t.Fatalf("advancement event = %+v", ev)
	}
	ev = tr.Handle("[12:37:00] [Server thread/INFO]: Nick was slain by Zombie")
	if ev == nil || ev.Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_DEATH || ev.Player != "Nick" {
		t.Fatalf("death event = %+v", ev)
	}

	// Chat from names not online is noise, never an event
	if ev := tr.Handle("[12:38:00] [Server thread/INFO]: <Ghost> boo"); ev != nil {
		t.Fatalf("offline chat produced an event: %+v", ev)
	}

	ev = tr.Handle("[12:40:00] [Server thread/INFO]: Nick lost connection: Disconnected")
	if ev == nil || ev.Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_LEAVE || ev.Online != 0 {
		t.Fatalf("leave event = %+v", ev)
	}
	if ev := tr.Handle("[12:40:00] [Server thread/INFO]: Nick left the game"); ev != nil {
		t.Fatalf("duplicate leave produced an event: %+v", ev)
	}

	// Pre-login noise never enters the roster
	tr.Handle("[12:41:00] [Server thread/INFO]: /172.18.0.9:41234 lost connection: Took too long to log in")
	if len(tr.Roster()) != 0 {
		t.Fatalf("pre-login disconnect polluted the roster: %v", tr.Roster())
	}

	// Plugin and legacy servers roster through the same auth lines
	tr.Handle("[12:42:00] [Server thread/INFO]: .BedrockKid[/172.18.0.9:41234] logged in with entity id 512 at (0.5, 70.0, 0.5)")
	tr.Handle("2011-07-31 10:11:12 [INFO] steve [/127.0.0.1:52941] logged in with entity id 229 at (135.5, 63.0, 240.3)")
	tr.Handle("2011-07-31 10:12:30 [INFO] steve lost connection: disconnect.quitting")
	if got := tr.Roster(); !slices.Equal(got, []string{".BedrockKid"}) {
		t.Fatalf("final roster = %v, want [.BedrockKid]", got)
	}
}

func TestConsoleTrackerSync(t *testing.T) {
	tr := NewConsoleTracker()
	tr.SetOnline("Nick", true)
	tr.SetOnline("Gone", true)

	events := tr.Sync([]RosterPlayer{{Name: "Nick"}, {Name: "New", UUID: "abc"}})
	if len(events) != 2 {
		t.Fatalf("sync produced %d events, want 2: %+v", len(events), events)
	}
	if events[0].Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_JOIN || events[0].Player != "New" || events[0].UUID != "abc" {
		t.Errorf("sync join = %+v", events[0])
	}
	if events[1].Type != agentv1.PlayerEventType_PLAYER_EVENT_TYPE_LEAVE || events[1].Player != "Gone" {
		t.Errorf("sync leave = %+v", events[1])
	}
	if got := tr.Roster(); !slices.Equal(got, []string{"New", "Nick"}) {
		t.Fatalf("roster after sync = %v", got)
	}
}

func TestMatchDeath(t *testing.T) {
	tr := NewConsoleTracker()
	for _, name := range []string{"Nick", ".BedrockKid", "Player With Space"} {
		tr.SetOnline(name, true)
	}

	deaths := []struct {
		msg    string
		victim string
	}{
		{"Nick was slain by Zombie", "Nick"},
		{"Nick was shot by Skeleton using Bow of Doom", "Nick"},
		{"Nick drowned", "Nick"},
		{"Nick fell from a high place", "Nick"},
		{"Nick fell off a ladder", "Nick"},
		{"Nick tried to swim in lava to escape Blaze", "Nick"},
		{"Nick was killed by [Intentional Game Design]", "Nick"},
		{"Nick hit the ground too hard whilst trying to escape Spider", "Nick"},
		{"Nick withered away", "Nick"},
		{"Nick experienced kinetic energy", "Nick"},
		{"Nick didn't want to live in the same world as Warden", "Nick"},
		{".BedrockKid was slain by Zombie", ".BedrockKid"},
		{"Player With Space blew up", "Player With Space"},
	}
	for _, c := range deaths {
		if player, ok := tr.matchDeath(c.msg); !ok || player != c.victim {
			t.Errorf("matchDeath(%q) = %q, %v, want %q", c.msg, player, ok, c.victim)
		}
	}
	notDeaths := []string{
		"Nick lost connection: Disconnected",
		"Nick issued server command: /help",
		"Nick moved too quickly!",
		"Nick joined the game",
		"Preparing spawn area: 95%",
		"Ghost was slain by Zombie",
	}
	for _, msg := range notDeaths {
		if player, ok := tr.matchDeath(msg); ok {
			t.Errorf("matchDeath(%q) false positive for %q", msg, player)
		}
	}
}

func TestChatAndAdvancementPatterns(t *testing.T) {
	if m := chatPattern.FindStringSubmatch("<Nick> hello world"); m == nil || m[1] != "Nick" || m[2] != "hello world" {
		t.Errorf("chatPattern missed plain chat: %v", m)
	}
	if m := chatPattern.FindStringSubmatch("[Not Secure] <Nick> hi"); m == nil || m[1] != "Nick" {
		t.Errorf("chatPattern missed unsigned chat: %v", m)
	}
	if m := chatPattern.FindStringSubmatch("<Player With Space> bedrock says hi"); m == nil || m[1] != "Player With Space" {
		t.Errorf("chatPattern missed spaced name: %v", m)
	}
	if m := advancementPattern.FindStringSubmatch("Nick has made the advancement [Stone Age]"); m == nil || m[2] != "Stone Age" {
		t.Errorf("advancementPattern missed: %v", m)
	}
	if m := advancementPattern.FindStringSubmatch("Nick has completed the challenge [Uneasy Alliance]"); m == nil || m[2] != "Uneasy Alliance" {
		t.Errorf("advancementPattern missed challenge: %v", m)
	}
	if m := advancementPattern.FindStringSubmatch(".BedrockKid has reached the goal [Acquire Hardware]"); m == nil || m[1] != ".BedrockKid" {
		t.Errorf("advancementPattern missed prefixed name: %v", m)
	}
}

func TestTellrawCommand(t *testing.T) {
	got := TellrawCommand("Nick", `say "hi" <all>`)
	want := `tellraw @a {"text":"<Nick> say \"hi\" <all>"}`
	if got != want {
		t.Errorf("TellrawCommand = %s, want %s", got, want)
	}
}
