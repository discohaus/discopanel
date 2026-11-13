package configedit

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Forge legacy cfg: nested "name {" categories and T:key=value lines
type cfgEditor struct{}

type cfgEntry struct {
	path      []string // Categories plus the key name
	typ       byte     // B, I, D, S or 0 when untyped
	list      bool
	line      int
	valueFrom int // Byte offset of the value within the line
}

type cfgCategory struct {
	path       []string
	indent     string
	closeLine  int // Line holding the closing brace
	lastInside int // Last non-blank line before the close
}

type cfgDoc struct {
	lines      []string
	entries    []cfgEntry
	categories []cfgCategory
}

var (
	cfgEntryRe    = regexp.MustCompile(`^([A-Za-z]):("[^"]*"|[^=<]+?)\s*=`)
	cfgUntypedRe  = regexp.MustCompile(`^("[^"]*"|[^=<{}#]+?)\s*=`)
	cfgListRe     = regexp.MustCompile(`^([A-Za-z]):("[^"]*"|[^=<]+?)\s*<\s*$`)
	cfgCategoryRe = regexp.MustCompile(`^("[^"]*"|[^{}=]+?)\s*\{\s*$`)
)

func (cfgEditor) keyPath(key string) ([]string, error) { return ParseKeyPath(key) }

func (cfgEditor) check(data []byte) error {
	_, err := scanCFG(string(data))
	return err
}

func scanCFG(s string) (*cfgDoc, error) {
	doc := &cfgDoc{lines: strings.Split(s, "\n")}
	var stack []int // Open category indexes
	inList := false
	for i, raw := range doc.lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if inList {
			if trimmed == ">" {
				inList = false
				for _, c := range stack {
					doc.categories[c].lastInside = i
				}
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "~") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if trimmed == "}" {
			if len(stack) == 0 {
				return nil, fmt.Errorf("line %d: unmatched }", i+1)
			}
			doc.categories[stack[len(stack)-1]].closeLine = i
			stack = stack[:len(stack)-1]
			for _, c := range stack {
				doc.categories[c].lastInside = i
			}
			continue
		}
		var cats []string
		for _, c := range stack {
			cats = doc.categories[c].path
		}
		for _, c := range stack {
			doc.categories[c].lastInside = i
		}
		if m := cfgCategoryRe.FindStringSubmatch(trimmed); m != nil {
			path := append(append([]string{}, cats...), strings.Trim(m[1], `"`))
			doc.categories = append(doc.categories, cfgCategory{path: path, indent: indent, closeLine: -1, lastInside: i})
			stack = append(stack, len(doc.categories)-1)
			continue
		}
		if m := cfgListRe.FindStringSubmatch(trimmed); m != nil {
			doc.entries = append(doc.entries, cfgEntry{path: append(append([]string{}, cats...), strings.Trim(m[2], `"`)), typ: m[1][0], list: true, line: i})
			inList = true
			continue
		}
		if m := cfgEntryRe.FindStringSubmatchIndex(trimmed); m != nil {
			doc.entries = append(doc.entries, cfgEntry{
				path:      append(append([]string{}, cats...), strings.Trim(trimmed[m[4]:m[5]], `"`)),
				typ:       strings.ToUpper(trimmed[m[2]:m[3]])[0],
				line:      i,
				valueFrom: len(indent) + m[1],
			})
			continue
		}
		if m := cfgUntypedRe.FindStringSubmatchIndex(trimmed); m != nil {
			doc.entries = append(doc.entries, cfgEntry{
				path:      append(append([]string{}, cats...), strings.TrimSpace(strings.Trim(trimmed[m[2]:m[3]], `"`))),
				line:      i,
				valueFrom: len(indent) + m[1],
			})
			continue
		}
		return nil, fmt.Errorf("line %d: unreadable line %q", i+1, trimmed)
	}
	if inList {
		return nil, errors.New("unclosed < list at end of file")
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("category %q is never closed", strings.Join(doc.categories[stack[len(stack)-1]].path, "."))
	}
	return doc, nil
}

// Matches by joined text so key names may hold dots
func cfgMatch(path, keyPath []string) bool {
	return strings.Join(path, ".") == strings.Join(keyPath, ".")
}

func (cfgEditor) set(data []byte, keyPath []string, value string) ([]byte, error) {
	s := string(data)
	doc, err := scanCFG(s)
	if err != nil {
		return nil, err
	}
	for _, e := range doc.entries {
		if !cfgMatch(e.path, keyPath) {
			continue
		}
		if e.list {
			return nil, errors.New("list values cannot be overridden")
		}
		t, err := coerce(value, cfgKind(e.typ))
		if err != nil {
			return nil, err
		}
		line := doc.lines[e.line]
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
		}
		doc.lines[e.line] = line[:e.valueFrom] + cfgLiteral(t) + cr
		return []byte(strings.Join(doc.lines, "\n")), nil
	}

	t, err := coerce(value, kindNone)
	if err != nil {
		return nil, err
	}
	unit := "    "
	if u := indentUnit(s, ""); u != "" {
		unit = u
	}
	cr := ""
	if strings.Contains(s, "\r\n") {
		cr = "\r"
	}

	// Deepest existing category that prefixes the key
	best := -1
	for i, c := range doc.categories {
		if len(c.path) < len(keyPath) && cfgMatch(c.path, keyPath[:len(c.path)]) {
			if best < 0 || len(c.path) > len(doc.categories[best].path) {
				best = i
			}
		}
	}
	depth, indent, at := 0, "", len(doc.lines)
	if best >= 0 {
		c := doc.categories[best]
		depth, indent, at = len(c.path), c.indent+unit, c.lastInside+1
	} else if at > 0 && doc.lines[at-1] == "" {
		at--
	}

	var block []string
	rest := keyPath[depth:]
	for i, cat := range rest[:len(rest)-1] {
		block = append(block, strings.Repeat(unit, i)+indent+cfgName(cat)+" {"+cr)
	}
	inner := indent + strings.Repeat(unit, len(rest)-1)
	block = append(block, inner+cfgTypeFor(t)+":"+cfgName(rest[len(rest)-1])+"="+cfgLiteral(t)+cr)
	for i := len(rest) - 2; i >= 0; i-- {
		block = append(block, strings.Repeat(unit, i)+indent+"}"+cr)
	}
	if best < 0 && at > 0 && strings.TrimSpace(doc.lines[at-1]) != "" {
		block = append([]string{cr}, block...)
	}
	lines := append(append(append([]string{}, doc.lines[:at]...), block...), doc.lines[at:]...)
	out := strings.Join(lines, "\n")
	if best < 0 && !strings.HasSuffix(out, "\n") {
		out += cr + "\n"
	}
	return []byte(out), nil
}

func cfgName(n string) string {
	if strings.ContainsAny(n, " ={}<>#:\"") {
		return `"` + strings.ReplaceAll(n, `"`, "") + `"`
	}
	return n
}

func cfgKind(typ byte) kind {
	switch typ {
	case 'B':
		return kindBool
	case 'I':
		return kindInt
	case 'D':
		return kindFloat
	}
	return kindString
}

func cfgTypeFor(t typed) string {
	switch t.k {
	case kindBool:
		return "B"
	case kindInt:
		return "I"
	case kindFloat:
		return "D"
	}
	return "S"
}

func cfgLiteral(t typed) string {
	if t.k == kindFloat {
		return floatText(t.text)
	}
	return t.text
}
