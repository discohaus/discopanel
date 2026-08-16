package proxy

import (
	"math/rand/v2"
	"time"

	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/family"
	"github.com/discohaus/discopanel/pkg/minecraft"
)

// Paint pools for every mood the proxy speaks in
var (
	inkGhost = []minecraft.RGB{minecraft.Hex("#8e8ea6"), minecraft.Hex("#d8d8e8")}
	inkDream = []minecraft.RGB{minecraft.Hex("#7fb8ff"), minecraft.Hex("#c77dff")}
	inkSpark = []minecraft.RGB{minecraft.Hex("#7cff9e"), minecraft.Hex("#55ffe0")}
	inkEmber = []minecraft.RGB{minecraft.Hex("#ffd25f"), minecraft.Hex("#ff9c41")}
	inkAlarm = []minecraft.RGB{minecraft.Hex("#ff6b6b"), minecraft.Hex("#ffad5c")}
	inkParty = []minecraft.RGB{
		minecraft.Hex("#ff5fd2"), minecraft.Hex("#ffe156"), minecraft.Hex("#7cff6b"),
		minecraft.Hex("#5fd2ff"), minecraft.Hex("#c77dff"),
	}
)

// Solo swatches shared across the screens
var (
	inkGray  = minecraft.Hex("#aaaaaa")
	inkMuted = minecraft.Hex("#8e8ea6")
	inkFaint = minecraft.Hex("#55555f")
	inkWhite = minecraft.Hex("#ffffff")
)

// Zero uuid carried by hover sample entries
const sampleUUID = "00000000-0000-0000-0000-000000000000"

// Splash lines for addresses nothing lives at
var emptySplashes = []string{
	"empty as a looted chest",
	"\"hmm\" - upset villager",
	"the beacon shines for no one",
	"echo... echo... echo...",
}

// Splash lines while a woken server loads
var wakeChores = []string{
	"herding the chickens",
	"untangling the redstone",
	"waxing the copper",
	"teaching zombies to knock",
	"warming up the furnaces",
	"convincing the endermen to chill",
}

// Picks one random line from a pool
func pick(pool []string) string {
	return pool[rand.IntN(len(pool))]
}

// Rotates brand colors a step every refresh
func discoSpin() []minecraft.RGB {
	step := int(time.Now().UnixMilli()/400) % len(inkParty)
	out := make([]minecraft.RGB, 0, len(inkParty)+1)
	out = append(out, inkParty[step:]...)
	out = append(out, inkParty[:step]...)
	return append(out, inkParty[step])
}

// Paints the wordmark, mono disco then green panel
func brandSpans() []minecraft.Span {
	return []minecraft.Span{
		minecraft.Fade("disco", minecraft.Hex("#f8f8ff"), minecraft.Hex("#d9d9e8")),
		minecraft.Fade("panel", minecraft.Hex("#69ff8c"), minecraft.Hex("#2ee86b")),
	}
}

// Builds a sparkle divider with rotating colors
func sparkleRow() []minecraft.Span {
	spin := discoSpin()
	return []minecraft.Span{
		minecraft.Solid("✧ ", spin[0]),
		minecraft.Solid("✦ ", spin[1]),
		minecraft.Solid("✧", spin[2]),
	}
}

// Paints a bold gradient headline for kicks
func headline(text string, stops []minecraft.RGB) minecraft.Span {
	return minecraft.Span{Text: text, Stops: stops, Bold: true}
}

// Frames kick lines between sparkle rows
func poster(spans ...minecraft.Span) minecraft.Text {
	t := minecraft.Text{}
	t = append(t, sparkleRow()...)
	t = append(t, minecraft.Plain("\n\n"))
	t = append(t, spans...)
	t = append(t, minecraft.Plain("\n\n"))
	return append(t, sparkleRow()...)
}

// Prebuilt synthetic status answer for one ping
type synthStatus struct {
	desc       any
	version    string
	maxPlayers int
	online     int
	favicon    string
	sample     []string
}

// Status card for addresses nothing lives at
func statusUnknownHost(hostname string, protocol int) synthStatus {
	line := minecraft.Text{
		minecraft.Fade("✧ ", discoSpin()...),
		minecraft.Fade("nothing is playing at ", inkGhost...),
		{Text: hostname, Stops: []minecraft.RGB{inkWhite}, Bold: true},
		minecraft.Plain("\n"),
		minecraft.Solid("♪ ", minecraft.Hex("#c77dff")),
		{Text: pick(emptySplashes), Stops: []minecraft.RGB{inkMuted}, Italic: true},
		minecraft.Solid(" ~ ", inkFaint),
	}
	line = append(line, brandSpans()...)
	return synthStatus{
		desc:       line.Render(protocol),
		version:    "discopanel",
		maxPlayers: 0,
		favicon:    minecraft.DefaultFavicon(),
		sample: []string{
			"§7double check the address",
			"§7or grab it from §fdisco§apanel",
		},
	}
}

// Status card for a paused sleeping server
func statusSleeping(motd string, maxPlayers int, favicon string) synthStatus {
	return synthStatus{
		desc:       motd,
		version:    "zZz",
		maxPlayers: maxPlayers,
		favicon:    favicon,
		sample: []string{
			"§bthis server is sleeping",
			"§7join the game to wake it up",
		},
	}
}

// Status card for a stopped server
func statusOffline(route Route) synthStatus {
	sample := []string{
		"§7this server is offline",
		"§7ask the owner to start it",
	}
	if route.Wakeable {
		sample = []string{
			"§7this server is off",
			"§7join to start it up",
		}
	}
	return synthStatus{
		desc:       route.Motd,
		version:    "offline",
		maxPlayers: route.MaxPlayers,
		favicon:    route.Favicon,
		sample:     sample,
	}
}

// Status card for a booting server
func statusStarting(route Route) synthStatus {
	return synthStatus{
		desc:       route.Motd,
		version:    "starting",
		maxPlayers: route.MaxPlayers,
		favicon:    route.Favicon,
		sample: []string{
			"§7the server is starting",
			"§7join again in a moment",
		},
	}
}

// Kick screen for addresses nothing lives at
func kickUnknownHost(hostname string) minecraft.Text {
	spans := []minecraft.Span{
		headline("you found an empty server", inkGhost),
		minecraft.Plain("\n"),
		minecraft.Solid("nothing is running at ", inkGray),
		minecraft.Solid(hostname, inkWhite),
		minecraft.Plain("\n\n"),
		minecraft.Solid("grab the right address from ", inkGray),
	}
	spans = append(spans, brandSpans()...)
	spans = append(spans,
		minecraft.Plain("\n"),
		minecraft.Solid("then come back and jump in", inkFaint),
	)
	return poster(spans...)
}

// Kick screen when a sleeping server stays asleep
func kickWakeFailed() minecraft.Text {
	return poster(
		headline("the server is waking up", inkDream),
		minecraft.Plain("\n"),
		minecraft.Solid("try again in a few seconds", inkGray),
	)
}

// Kick screen for a server left switched off
func kickOffline() minecraft.Text {
	spans := []minecraft.Span{
		headline("this server is offline", inkGhost),
		minecraft.Plain("\n"),
		minecraft.Solid("ask whoever runs it to start it in ", inkGray),
	}
	spans = append(spans, brandSpans()...)
	return poster(spans...)
}

// Kick screen after a login boots the server
func kickStarted() minecraft.Text {
	return poster(
		headline("the server is starting now", inkSpark),
		minecraft.Plain("\n"),
		minecraft.Span{Text: "( " + pick(wakeChores) + " )", Stops: []minecraft.RGB{inkMuted}, Italic: true},
		minecraft.Plain("\n\n"),
		minecraft.Solid("join again in about a minute", inkWhite),
	)
}

// Kick screen when the cold start fails
func kickStartFailed() minecraft.Text {
	spans := []minecraft.Span{
		headline("the server couldn't start", inkAlarm),
		minecraft.Plain("\n"),
		minecraft.Solid("check ", inkGray),
	}
	spans = append(spans, brandSpans()...)
	spans = append(spans, minecraft.Solid(" to see what went wrong", inkGray))
	return poster(spans...)
}

// Kick screen while the container still boots
func kickStillStarting() minecraft.Text {
	return poster(
		headline("the server is still starting", inkEmber),
		minecraft.Plain("\n"),
		minecraft.Solid("join again in a moment", inkGray),
	)
}

// Kick screen for a route missing its backend
func kickUnreachable() minecraft.Text {
	return poster(
		headline("this server isn't reachable right now", inkAlarm),
		minecraft.Plain("\n"),
		minecraft.Solid("try again in a moment", inkGray),
	)
}

// Kick screen when identity checks fall through
func kickAuthFailed() minecraft.Text {
	return poster(
		headline("we couldn't verify your account", inkAlarm),
		minecraft.Plain("\n"),
		minecraft.Solid("restart your game and join again", inkGray),
	)
}

// Kick screen naming the versions the lobby speaks
func kickHubVersion() minecraft.Text {
	floor := "1.16.4"
	if names := mcproto.VersionNamesForProtocol(family.ModernFloor); len(names) > 0 {
		floor = names[0]
	}
	return poster(
		headline("the lobby can't host your version yet", inkEmber),
		minecraft.Plain("\n"),
		minecraft.Solid("join with minecraft "+floor+" or newer", inkGray),
	)
}

// Kick screen when the lobby is at capacity
func kickHubFull() minecraft.Text {
	return poster(
		headline("the lobby is full right now", inkEmber),
		minecraft.Plain("\n"),
		minecraft.Solid("try again in a moment", inkGray),
	)
}

// Kick screen when the world speaks another version
func kickVersionMismatch(version string) minecraft.Text {
	return poster(
		headline("this world runs minecraft "+version, inkEmber),
		minecraft.Plain("\n"),
		minecraft.Solid("switch your game to "+version+" and join again", inkGray),
	)
}

// Kick screen while the backend ignores our dial
func kickNotAccepting() minecraft.Text {
	return poster(
		headline("the server isn't accepting connections yet", inkEmber),
		minecraft.Plain("\n"),
		minecraft.Solid("try again in a moment", inkGray),
	)
}
