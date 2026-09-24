package minecraft

import (
	"fmt"
	"strings"
)

// First protocol where hex chat colors render
const HexChatProtocol = 735

// Holds one color used to paint text
type RGB struct{ R, G, B uint8 }

// Parses a #rrggbb string, white when malformed
func Hex(s string) RGB {
	var c RGB
	if _, err := fmt.Sscanf(s, "#%02x%02x%02x", &c.R, &c.G, &c.B); err != nil {
		return RGB{R: 255, G: 255, B: 255}
	}
	return c
}

// Formats the color as a json hex string
func (c RGB) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// Blends two colors at fraction t
func lerp(a, b RGB, t float64) RGB {
	mix := func(x, y uint8) uint8 {
		return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	return RGB{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B)}
}

// Samples a multi stop gradient at fraction t
func gradientAt(stops []RGB, t float64) RGB {
	switch {
	case len(stops) == 0:
		return RGB{R: 255, G: 255, B: 255}
	case len(stops) == 1 || t <= 0:
		return stops[0]
	case t >= 1:
		return stops[len(stops)-1]
	}
	seg := t * float64(len(stops)-1)
	i := int(seg)
	return lerp(stops[i], stops[i+1], seg-float64(i))
}

// One run of characters sharing paint and style
type Span struct {
	Text       string
	Stops      []RGB
	Bold       bool
	Italic     bool
	Obfuscated bool
}

// Styled chat line renderable modern or legacy
type Text []Span

// Paints whole text with one solid color
func Solid(text string, color RGB) Span {
	return Span{Text: text, Stops: []RGB{color}}
}

// Paints text with a smooth gradient
func Fade(text string, stops ...RGB) Span {
	return Span{Text: text, Stops: stops}
}

// Leaves text unpainted for client default color
func Plain(text string) Span {
	return Span{Text: text}
}

// Picks hex or classic rendering by client protocol
func (t Text) Render(protocol int) map[string]any {
	if protocol >= HexChatProtocol {
		return t.Component()
	}
	return map[string]any{"text": t.Legacy()}
}

// Builds a chat component with per rune hex colors
func (t Text) Component() map[string]any {
	extras := make([]map[string]any, 0, len(t))
	for _, span := range t {
		extras = append(extras, span.parts()...)
	}
	return map[string]any{"text": "", "extra": extras}
}

// Renders one span into merged colored parts
func (sp Span) parts() []map[string]any {
	runes := []rune(sp.Text)
	if len(runes) == 0 {
		return nil
	}
	var out []map[string]any
	var buf strings.Builder
	current := ""
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		part := map[string]any{"text": buf.String()}
		if current != "" {
			part["color"] = current
		}
		sp.mark(part)
		out = append(out, part)
		buf.Reset()
	}
	for i, r := range runes {
		color := ""
		if len(sp.Stops) > 0 {
			at := 0.0
			if len(runes) > 1 {
				at = float64(i) / float64(len(runes)-1)
			}
			color = gradientAt(sp.Stops, at).hex()
		}
		if color != current {
			flush()
			current = color
		}
		buf.WriteRune(r)
	}
	flush()
	return out
}

// Copies style flags onto a component part
func (sp Span) mark(part map[string]any) {
	if sp.Bold {
		part["bold"] = true
	}
	if sp.Italic {
		part["italic"] = true
	}
	if sp.Obfuscated {
		part["obfuscated"] = true
	}
}

// Classic sixteen color palette for old clients
var legacyPalette = []struct {
	code rune
	c    RGB
}{
	{'0', RGB{R: 0, G: 0, B: 0}},
	{'1', RGB{R: 0, G: 0, B: 170}},
	{'2', RGB{R: 0, G: 170, B: 0}},
	{'3', RGB{R: 0, G: 170, B: 170}},
	{'4', RGB{R: 170, G: 0, B: 0}},
	{'5', RGB{R: 170, G: 0, B: 170}},
	{'6', RGB{R: 255, G: 170, B: 0}},
	{'7', RGB{R: 170, G: 170, B: 170}},
	{'8', RGB{R: 85, G: 85, B: 85}},
	{'9', RGB{R: 85, G: 85, B: 255}},
	{'a', RGB{R: 85, G: 255, B: 85}},
	{'b', RGB{R: 85, G: 255, B: 255}},
	{'c', RGB{R: 255, G: 85, B: 85}},
	{'d', RGB{R: 255, G: 85, B: 255}},
	{'e', RGB{R: 255, G: 255, B: 85}},
	{'f', RGB{R: 255, G: 255, B: 255}},
}

// Finds the closest classic code for a color
func nearestLegacy(c RGB) rune {
	best := 'f'
	bestDist := 1 << 30
	for _, entry := range legacyPalette {
		dr := int(entry.c.R) - int(c.R)
		dg := int(entry.c.G) - int(c.G)
		db := int(entry.c.B) - int(c.B)
		d := dr*dr + dg*dg + db*db
		if d < bestDist {
			best, bestDist = entry.code, d
		}
	}
	return best
}

// Renders classic section codes for old clients
func (t Text) Legacy() string {
	var b strings.Builder
	for _, sp := range t {
		prefix := sp.legacyPrefix()
		for i, line := range strings.Split(sp.Text, "\n") {
			if i > 0 {
				b.WriteString("\n")
			}
			if line == "" {
				continue
			}
			b.WriteString(prefix)
			b.WriteString(line)
		}
	}
	return b.String()
}

// Builds the section code prefix for one span
func (sp Span) legacyPrefix() string {
	var b strings.Builder
	if len(sp.Stops) == 0 {
		b.WriteString("§r")
	} else {
		b.WriteString("§")
		b.WriteRune(nearestLegacy(gradientAt(sp.Stops, 0.5)))
	}
	if sp.Bold {
		b.WriteString("§l")
	}
	if sp.Italic {
		b.WriteString("§o")
	}
	if sp.Obfuscated {
		b.WriteString("§k")
	}
	return b.String()
}
