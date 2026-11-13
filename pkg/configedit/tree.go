package configedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Brace syntaxes edited in place by value span
type dialect struct {
	name           string
	hashComments   bool // # to end of line, SNBT
	slashComments  bool // // and /* */, JSON5 style
	optionalCommas bool // Newlines separate members, SNBT
	snbt           bool
}

var (
	jsonDialect = &dialect{name: "json", slashComments: true}
	snbtDialect = &dialect{name: "snbt", hashComments: true, optionalCommas: true, snbt: true}
)

type nodeKind int

const (
	nodeScalar nodeKind = iota
	nodeObject
	nodeArray
)

type member struct {
	key      string
	keyStart int
	quoted   bool
	value    *node
}

type node struct {
	kind       nodeKind
	start, end int
	members    []member
	elems      []*node
}

type treeEditor struct{ d *dialect }

func (treeEditor) keyPath(key string) ([]string, error) { return ParseKeyPath(key) }

func (e treeEditor) check(data []byte) error {
	_, err := parseTree(e.d, string(data))
	return err
}

func (e treeEditor) set(data []byte, keyPath []string, value string) ([]byte, error) {
	s := string(data)
	root, err := parseTree(e.d, s)
	if err != nil {
		return nil, err
	}
	strictJSON := !e.d.snbt && json.Valid(data)

	cur := root
	for i, seg := range keyPath {
		switch cur.kind {
		case nodeObject:
			var next *node
			for j := range cur.members {
				if cur.members[j].key == seg {
					next = cur.members[j].value
				}
			}
			if next == nil {
				out, err := e.insert(s, cur, keyPath[i:], value)
				if err != nil {
					return nil, err
				}
				if strictJSON && !json.Valid([]byte(out)) {
					return nil, errors.New("value is not valid JSON")
				}
				return []byte(out), nil
			}
			cur = next
		case nodeArray:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(cur.elems) {
				return nil, fmt.Errorf("%s has no element %q", pathLabel(keyPath[:i]), seg)
			}
			cur = cur.elems[idx]
		default:
			return nil, fmt.Errorf("%s is a value, not an object", pathLabel(keyPath[:i]))
		}
	}

	lit := s[cur.start:cur.end]
	existing := kindRaw
	if cur.kind == nodeScalar {
		existing = e.scalarKind(lit)
	}
	t, err := coerce(value, existing)
	if err != nil && e.d.snbt && existing == kindInt && strings.HasSuffix(strings.ToLower(lit), "b") {
		// SNBT bytes double as booleans
		if b := infer(value); b.k == kindBool {
			t, err = typed{kindInt, map[string]string{"true": "1", "false": "0"}[b.text]}, nil
		}
	}
	if err != nil {
		return nil, err
	}
	out := s[:cur.start] + e.literal(t, lit) + s[cur.end:]
	if strictJSON && !json.Valid([]byte(out)) {
		return nil, errors.New("value is not valid JSON")
	}
	return []byte(out), nil
}

func pathLabel(path []string) string {
	if len(path) == 0 {
		return "the root"
	}
	return strings.Join(path, ".")
}

// Adds rest under obj, nesting new objects for missing parents
func (e treeEditor) insert(s string, obj *node, rest []string, value string) (string, error) {
	t, err := coerce(value, kindNone)
	if err != nil {
		return "", err
	}
	nl := newline(s)
	unit := indentUnit(s, "  ")
	if e.d.snbt {
		unit = indentUnit(s, "\t")
	}

	var memberIndent string
	var at int
	var prefix, suffix string
	closeIndent := lineIndent(s, obj.end-1)

	if n := len(obj.members); n > 0 {
		last := obj.members[n-1]
		memberIndent = lineIndent(s, last.keyStart)
		multiline := strings.TrimSpace(s[lineStartOf(s, last.keyStart):last.keyStart]) == ""
		at = last.value.end
		gap := s[last.value.end : obj.end-1]
		trailing := strings.HasPrefix(strings.TrimLeft(gap, " \t\r\n"), ",")
		comma := !e.d.optionalCommas || e.usesCommas(s, obj)
		if trailing {
			at = last.value.end + strings.IndexByte(gap, ',') + 1
			comma = false
			suffix = ","
		}
		sep := ""
		if comma {
			sep = ","
		}
		if multiline {
			prefix = sep + nl + memberIndent
		} else {
			prefix = sep + " "
			memberIndent = closeIndent
		}
	} else {
		memberIndent = closeIndent + unit
		at = obj.end - 1
		prefix = nl + memberIndent
		suffix = nl + closeIndent
		// Drops blank filler inside an empty object
		inner := s[obj.start+1 : obj.end-1]
		if strings.TrimSpace(inner) == "" {
			s = s[:obj.start+1] + s[obj.end-1:]
			at = obj.start + 1
		}
	}

	body := e.memberText(rest, t, memberIndent, unit, nl)
	return s[:at] + prefix + body + suffix + s[at:], nil
}

func lineStartOf(s string, off int) int {
	return strings.LastIndexByte(s[:off], '\n') + 1
}

// Whether this object already separates members with commas
func (e treeEditor) usesCommas(s string, obj *node) bool {
	for i := 1; i < len(obj.members); i++ {
		if strings.Contains(s[obj.members[i-1].value.end:obj.members[i].keyStart], ",") {
			return true
		}
	}
	return false
}

func (e treeEditor) memberText(path []string, t typed, indent, unit, nl string) string {
	key := e.keyLiteral(path[0])
	if len(path) == 1 {
		return key + ": " + e.literal(t, "")
	}
	inner := indent + unit
	return key + ": {" + nl + inner + e.memberText(path[1:], t, inner, unit, nl) + nl + indent + "}"
}

var snbtBareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_\-.+]+$`)

func (e treeEditor) keyLiteral(k string) string {
	if e.d.snbt {
		if snbtBareKeyRe.MatchString(k) {
			return k
		}
		return snbtString(k)
	}
	return jsonString(k)
}

// SNBT readers only unescape quotes and backslashes
func snbtString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

var (
	snbtIntRe   = regexp.MustCompile(`^[+-]?[0-9]+[bBsSlL]?$`)
	snbtFloatRe = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?[fFdD]?$`)
)

func (e treeEditor) scalarKind(lit string) kind {
	switch {
	case lit == "":
		return kindNone
	case lit[0] == '"' || lit[0] == '\'':
		return kindString
	case lit == "true" || lit == "false":
		return kindBool
	case lit == "null":
		return kindNone
	}
	if e.d.snbt {
		switch {
		case snbtIntRe.MatchString(lit):
			return kindInt
		case snbtFloatRe.MatchString(lit):
			return kindFloat
		}
		// Bare words are unquoted strings in SNBT
		return kindString
	}
	switch {
	case intRe.MatchString(lit):
		return kindInt
	case floatRe.MatchString(lit):
		return kindFloat
	}
	return kindRaw
}

// Encodes t, keeping the number suffix the old literal carried
func (e treeEditor) literal(t typed, old string) string {
	suffix := ""
	if e.d.snbt && old != "" && !strings.HasPrefix(old, `"`) {
		last := old[len(old)-1]
		if strings.IndexByte("bBsSlLfFdD", last) >= 0 {
			suffix = string(last)
		}
	}
	switch t.k {
	case kindString:
		if e.d.snbt {
			return snbtString(t.text)
		}
		return jsonString(t.text)
	case kindInt:
		return t.text + suffix
	case kindFloat:
		if e.d.snbt {
			if suffix == "" && old == "" {
				suffix = "d"
			}
			return floatText(t.text) + suffix
		}
		return t.text
	}
	return t.text
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// Recursive descent parser recording spans
type treeParser struct {
	d   *dialect
	s   string
	pos int
}

func parseTree(d *dialect, s string) (*node, error) {
	p := &treeParser{d: d, s: s}
	p.skip()
	if p.pos >= len(s) {
		return nil, errors.New("file is empty")
	}
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	if n.kind != nodeObject {
		return nil, errors.New("top level value is not an object")
	}
	p.skip()
	if p.pos < len(s) {
		return nil, fmt.Errorf("unexpected text at %s", p.where())
	}
	return n, nil
}

func (p *treeParser) where() string {
	line := strings.Count(p.s[:p.pos], "\n") + 1
	return fmt.Sprintf("line %d", line)
}

// Whitespace and comments
func (p *treeParser) skip() {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case p.d.hashComments && c == '#':
			p.toEOL()
		case p.d.slashComments && strings.HasPrefix(p.s[p.pos:], "//"):
			p.toEOL()
		case p.d.slashComments && strings.HasPrefix(p.s[p.pos:], "/*"):
			end := strings.Index(p.s[p.pos+2:], "*/")
			if end < 0 {
				p.pos = len(p.s)
				return
			}
			p.pos += end + 4
		default:
			return
		}
	}
}

func (p *treeParser) toEOL() {
	for p.pos < len(p.s) && p.s[p.pos] != '\n' {
		p.pos++
	}
}

func (p *treeParser) value() (*node, error) {
	if p.pos >= len(p.s) {
		return nil, errors.New("unexpected end of file")
	}
	switch c := p.s[p.pos]; c {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"', '\'':
		start := p.pos
		if _, err := p.str(); err != nil {
			return nil, err
		}
		return &node{kind: nodeScalar, start: start, end: p.pos}, nil
	}
	start := p.pos
	for p.pos < len(p.s) && !p.delim(p.s[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("unexpected %q at %s", p.s[p.pos], p.where())
	}
	// JSON5 has no bare words, only numbers and keywords
	if word := p.s[start:p.pos]; !p.d.snbt && !json5BareRe.MatchString(word) {
		p.pos = start
		return nil, fmt.Errorf("bare word %q at %s is not valid JSON, quote it", word, p.where())
	}
	return &node{kind: nodeScalar, start: start, end: p.pos}, nil
}

var json5BareRe = regexp.MustCompile(`^(true|false|null|[+-]?(Infinity|NaN|0[xX][0-9A-Fa-f]+|([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?))$`)

func (p *treeParser) delim(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', ',', '}', ']', ':':
		return true
	case '#':
		return p.d.hashComments
	case '/':
		return p.d.slashComments && (strings.HasPrefix(p.s[p.pos:], "//") || strings.HasPrefix(p.s[p.pos:], "/*"))
	}
	return false
}

// Quoted string, returns the decoded text
func (p *treeParser) str() (string, error) {
	q := p.s[p.pos]
	start := p.pos
	var b strings.Builder
	p.pos++
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.s):
			if q == '"' && !p.d.snbt {
				// Exact JSON decoding for the common case
				end := p.pos + 2
				if p.s[p.pos+1] == 'u' && end+4 <= len(p.s) {
					end += 4
				}
				if r, err := strconv.Unquote(`"` + p.s[p.pos:end] + `"`); err == nil {
					b.WriteString(r)
					p.pos = end
					continue
				}
			}
			b.WriteByte(p.s[p.pos+1])
			p.pos += 2
		case c == q:
			p.pos++
			return b.String(), nil
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	p.pos = start
	return "", fmt.Errorf("unclosed string at %s", p.where())
}

func (p *treeParser) object() (*node, error) {
	n := &node{kind: nodeObject, start: p.pos}
	p.pos++
	for {
		p.skip()
		if p.pos >= len(p.s) {
			return nil, errors.New("unclosed object at end of file")
		}
		if p.s[p.pos] == '}' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		m := member{keyStart: p.pos}
		if c := p.s[p.pos]; c == '"' || c == '\'' {
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			m.key, m.quoted = k, true
		} else {
			start := p.pos
			for p.pos < len(p.s) && !p.delim(p.s[p.pos]) {
				p.pos++
			}
			if p.pos == start {
				return nil, fmt.Errorf("expected a key at %s", p.where())
			}
			m.key = p.s[start:p.pos]
		}
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ':' {
			return nil, fmt.Errorf("expected : after key %q at %s", m.key, p.where())
		}
		p.pos++
		p.skip()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		m.value = v
		n.members = append(n.members, m)
		if err := p.separator('}'); err != nil {
			return nil, err
		}
	}
}

func (p *treeParser) array() (*node, error) {
	n := &node{kind: nodeArray, start: p.pos}
	p.pos++
	// Typed SNBT arrays open with a B, I or L marker
	if p.d.snbt && p.pos+1 < len(p.s) && strings.IndexByte("BIL", p.s[p.pos]) >= 0 && p.s[p.pos+1] == ';' {
		p.pos += 2
	}
	for {
		p.skip()
		if p.pos >= len(p.s) {
			return nil, errors.New("unclosed array at end of file")
		}
		if p.s[p.pos] == ']' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.elems = append(n.elems, v)
		if err := p.separator(']'); err != nil {
			return nil, err
		}
	}
}

// Comma, closer, or a newline where commas are optional
func (p *treeParser) separator(closer byte) error {
	before := p.pos
	p.skip()
	if p.pos >= len(p.s) {
		return nil
	}
	switch c := p.s[p.pos]; {
	case c == ',':
		p.pos++
	case c == closer:
	case p.d.optionalCommas && strings.ContainsAny(p.s[before:p.pos], "\n"):
	default:
		return fmt.Errorf("expected , or %c at %s", closer, p.where())
	}
	return nil
}
