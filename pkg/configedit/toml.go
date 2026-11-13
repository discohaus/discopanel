package configedit

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// TOML edited line by line so comments and layout survive
type tomlEditor struct{}

// One key = value statement, offsets into the file text
type tomlKV struct {
	path       []string // Table path plus dotted key
	keyIndent  string
	valueStart int
	valueEnd   int
	lineEnd    int // Offset past the statement's newline
	table      int // Index into tables, -1 for the root
}

// One [table] header and where its body ends
type tomlTable struct {
	path      []string
	array     bool // [[array]] tables are never edited
	indent    string
	headerEnd int // Offset past the header line
	lastKV    int // Offset past the last statement, headerEnd when none
}

type tomlDoc struct {
	kvs        []tomlKV
	tables     []tomlTable
	rootLastKV int // Offset past the last root statement, -1 when none
	firstTable int // Offset of the first header, len when none
}

func (tomlEditor) keyPath(key string) ([]string, error) { return ParseKeyPath(key) }

func (tomlEditor) check(data []byte) error {
	var v map[string]any
	if err := toml.Unmarshal(data, &v); err != nil {
		return err
	}
	_, err := scanTOML(string(data))
	return err
}

func (tomlEditor) set(data []byte, keyPath []string, value string) ([]byte, error) {
	s := string(data)
	doc, err := scanTOML(s)
	if err != nil {
		return nil, err
	}

	for _, kv := range doc.kvs {
		if !slices.Equal(kv.path, keyPath) {
			continue
		}
		if kv.table >= 0 && doc.tables[kv.table].array {
			return nil, errors.New("keys inside [[array]] tables cannot be overridden")
		}
		t, err := coerce(value, tomlKind(s[kv.valueStart:kv.valueEnd]))
		if err != nil {
			return nil, err
		}
		return []byte(s[:kv.valueStart] + tomlLiteral(t) + s[kv.valueEnd:]), nil
	}
	for _, kv := range doc.kvs {
		if len(kv.path) < len(keyPath) && slices.Equal(kv.path, keyPath[:len(kv.path)]) {
			return nil, fmt.Errorf("%s is a value, not a table", strings.Join(kv.path, "."))
		}
	}

	t, err := coerce(value, kindNone)
	if err != nil {
		return nil, err
	}
	nl := newline(s)

	// Deepest table already declared that holds the key
	best := -1
	for i, tb := range doc.tables {
		if tb.array || len(tb.path) >= len(keyPath) || !slices.Equal(tb.path, keyPath[:len(tb.path)]) {
			continue
		}
		if best < 0 || len(tb.path) > len(doc.tables[best].path) {
			best = i
		}
	}

	if best >= 0 {
		tb := doc.tables[best]
		indent := tb.indent
		if last := lastKVIn(doc, best); last != nil {
			indent = last.keyIndent
		}
		line := indent + tomlKey(keyPath[len(tb.path):]) + " = " + tomlLiteral(t) + nl
		return []byte(insertAt(s, tb.lastKV, line, nl)), nil
	}

	// A dotted root key already started this table, keep it dotted
	dottedRoot := false
	for _, kv := range doc.kvs {
		if kv.table < 0 && len(kv.path) > 1 && kv.path[0] == keyPath[0] {
			dottedRoot = true
		}
	}

	if len(keyPath) == 1 || len(doc.tables) == 0 || dottedRoot {
		line := tomlKey(keyPath) + " = " + tomlLiteral(t) + nl
		at := doc.rootLastKV
		if at < 0 {
			at = 0
			if len(doc.tables) > 0 {
				// Root keys must precede the first header
				line += nl
			}
		}
		return []byte(insertAt(s, at, line, nl)), nil
	}

	// New table at the end keeps related keys together
	out := s
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += nl
	}
	if out != "" {
		out += nl
	}
	out += "[" + tomlKey(keyPath[:len(keyPath)-1]) + "]" + nl
	out += tomlKey(keyPath[len(keyPath)-1:]) + " = " + tomlLiteral(t) + nl
	return []byte(out), nil
}

func lastKVIn(doc *tomlDoc, table int) *tomlKV {
	var last *tomlKV
	for i := range doc.kvs {
		if doc.kvs[i].table == table {
			last = &doc.kvs[i]
		}
	}
	return last
}

// Inserts a full line at a line boundary
func insertAt(s string, at int, line, nl string) string {
	if at > 0 && s[at-1] != '\n' {
		line = nl + line
	}
	return s[:at] + line + s[at:]
}

var tomlIntRe = regexp.MustCompile(`^[+-]?([0-9][0-9_]*|0x[0-9A-Fa-f_]+|0o[0-7_]+|0b[01_]+)$`)
var tomlFloatRe = regexp.MustCompile(`^[+-]?(([0-9][0-9_]*)(\.[0-9_]+)?([eE][+-]?[0-9_]+)?|inf|nan)$`)

// Type of an existing TOML value from its literal
func tomlKind(lit string) kind {
	lit = strings.TrimSpace(lit)
	switch {
	case lit == "":
		return kindNone
	case lit[0] == '"' || lit[0] == '\'':
		return kindString
	case lit == "true" || lit == "false":
		return kindBool
	case tomlIntRe.MatchString(lit):
		return kindInt
	case tomlFloatRe.MatchString(lit):
		return kindFloat
	}
	return kindRaw
}

func tomlLiteral(t typed) string {
	switch t.k {
	case kindString:
		return tomlQuote(t.text)
	case kindFloat:
		return floatText(t.text)
	}
	return t.text
}

func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var tomlBareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		if tomlBareKeyRe.MatchString(p) {
			parts[i] = p
		} else {
			parts[i] = tomlQuote(p)
		}
	}
	return strings.Join(parts, ".")
}

// Statement scanner tracking table context and value spans
type tomlScanner struct {
	s   string
	pos int
}

func scanTOML(s string) (*tomlDoc, error) {
	sc := &tomlScanner{s: s}
	doc := &tomlDoc{rootLastKV: -1, firstTable: len(s)}
	table := -1
	for sc.pos < len(s) {
		lineStart := sc.pos
		sc.skipBlanks()
		if sc.pos >= len(s) {
			break
		}
		switch c := s[sc.pos]; {
		case c == '\n' || c == '\r':
			sc.skipEOL()
			continue
		case c == '#':
			sc.skipComment()
			sc.skipEOL()
			continue
		case c == '[':
			indent := s[lineStart:sc.pos]
			array := strings.HasPrefix(s[sc.pos:], "[[")
			if array {
				sc.pos += 2
			} else {
				sc.pos++
			}
			path, err := sc.keyPath()
			if err != nil {
				return nil, err
			}
			closer := "]"
			if array {
				closer = "]]"
			}
			sc.skipBlanks()
			if !strings.HasPrefix(s[sc.pos:], closer) {
				return nil, fmt.Errorf("unclosed table header at offset %d", sc.pos)
			}
			sc.pos += len(closer)
			if err := sc.finishLine(); err != nil {
				return nil, err
			}
			if len(doc.tables) == 0 {
				doc.firstTable = lineStart
			}
			doc.tables = append(doc.tables, tomlTable{path: path, array: array, indent: indent, headerEnd: sc.pos, lastKV: sc.pos})
			table = len(doc.tables) - 1
		default:
			indent := s[lineStart:sc.pos]
			key, err := sc.keyPath()
			if err != nil {
				return nil, err
			}
			sc.skipBlanks()
			if sc.pos >= len(s) || s[sc.pos] != '=' {
				return nil, fmt.Errorf("expected = after key at offset %d", sc.pos)
			}
			sc.pos++
			sc.skipBlanks()
			vStart := sc.pos
			if err := sc.value(true); err != nil {
				return nil, err
			}
			vEnd := sc.pos
			if err := sc.finishLine(); err != nil {
				return nil, err
			}
			full := key
			if table >= 0 {
				full = append(slices.Clone(doc.tables[table].path), key...)
				doc.tables[table].lastKV = sc.pos
			} else {
				doc.rootLastKV = sc.pos
			}
			doc.kvs = append(doc.kvs, tomlKV{path: full, keyIndent: indent, valueStart: vStart, valueEnd: vEnd, lineEnd: sc.pos, table: table})
		}
	}
	return doc, nil
}

func (sc *tomlScanner) skipBlanks() {
	for sc.pos < len(sc.s) && (sc.s[sc.pos] == ' ' || sc.s[sc.pos] == '\t') {
		sc.pos++
	}
}

func (sc *tomlScanner) skipComment() {
	for sc.pos < len(sc.s) && sc.s[sc.pos] != '\n' {
		sc.pos++
	}
}

func (sc *tomlScanner) skipEOL() {
	if sc.pos < len(sc.s) && sc.s[sc.pos] == '\r' {
		sc.pos++
	}
	if sc.pos < len(sc.s) && sc.s[sc.pos] == '\n' {
		sc.pos++
	}
}

// Trailing blanks and comment, then the newline
func (sc *tomlScanner) finishLine() error {
	sc.skipBlanks()
	if sc.pos < len(sc.s) && sc.s[sc.pos] == '#' {
		sc.skipComment()
	}
	if sc.pos < len(sc.s) && sc.s[sc.pos] != '\n' && sc.s[sc.pos] != '\r' {
		return fmt.Errorf("unexpected text at offset %d", sc.pos)
	}
	sc.skipEOL()
	return nil
}

// Dotted key with bare, basic and literal segments
func (sc *tomlScanner) keyPath() ([]string, error) {
	var path []string
	for {
		sc.skipBlanks()
		if sc.pos >= len(sc.s) {
			return nil, errors.New("unexpected end of file in key")
		}
		switch sc.s[sc.pos] {
		case '"':
			start := sc.pos
			if err := sc.basicString(); err != nil {
				return nil, err
			}
			seg, err := decodeTOMLString(sc.s[start:sc.pos])
			if err != nil {
				return nil, err
			}
			path = append(path, seg)
		case '\'':
			start := sc.pos
			end := strings.IndexByte(sc.s[start+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unclosed key at offset %d", start)
			}
			path = append(path, sc.s[start+1:start+1+end])
			sc.pos = start + end + 2
		default:
			start := sc.pos
			for sc.pos < len(sc.s) && isTOMLBare(sc.s[sc.pos]) {
				sc.pos++
			}
			if sc.pos == start {
				return nil, fmt.Errorf("bad key at offset %d", start)
			}
			path = append(path, sc.s[start:sc.pos])
		}
		sc.skipBlanks()
		if sc.pos < len(sc.s) && sc.s[sc.pos] == '.' {
			sc.pos++
			continue
		}
		return path, nil
	}
}

func isTOMLBare(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// Skips one value, top level bare values run to the comment or line end
func (sc *tomlScanner) value(top bool) error {
	s := sc.s
	if sc.pos >= len(s) {
		return errors.New("missing value")
	}
	switch {
	case strings.HasPrefix(s[sc.pos:], `"""`):
		end := strings.Index(s[sc.pos+3:], `"""`)
		for end >= 0 && escapedAt(s, sc.pos+3+end) {
			next := strings.Index(s[sc.pos+3+end+1:], `"""`)
			if next < 0 {
				end = -1
				break
			}
			end += next + 1
		}
		if end < 0 {
			return fmt.Errorf("unclosed multi-line string at offset %d", sc.pos)
		}
		sc.pos += 3 + end + 3
		for sc.pos < len(s) && s[sc.pos] == '"' {
			sc.pos++
		}
	case strings.HasPrefix(s[sc.pos:], `'''`):
		end := strings.Index(s[sc.pos+3:], `'''`)
		if end < 0 {
			return fmt.Errorf("unclosed multi-line string at offset %d", sc.pos)
		}
		sc.pos += 3 + end + 3
		for sc.pos < len(s) && s[sc.pos] == '\'' {
			sc.pos++
		}
	case s[sc.pos] == '"':
		return sc.basicString()
	case s[sc.pos] == '\'':
		end := strings.IndexAny(s[sc.pos+1:], "'\n")
		if end < 0 || s[sc.pos+1+end] != '\'' {
			return fmt.Errorf("unclosed string at offset %d", sc.pos)
		}
		sc.pos += end + 2
	case s[sc.pos] == '[' || s[sc.pos] == '{':
		return sc.container()
	default:
		start := sc.pos
		for sc.pos < len(s) {
			c := s[sc.pos]
			if c == '\n' || c == '\r' || c == '#' || (!top && (c == ',' || c == ']' || c == '}')) {
				break
			}
			sc.pos++
		}
		for sc.pos > start && (s[sc.pos-1] == ' ' || s[sc.pos-1] == '\t') {
			sc.pos--
		}
		if sc.pos == start {
			return fmt.Errorf("missing value at offset %d", start)
		}
	}
	return nil
}

// Arrays and inline tables, which may span lines and hold comments
func (sc *tomlScanner) container() error {
	s := sc.s
	open := s[sc.pos]
	closer := byte(']')
	if open == '{' {
		closer = '}'
	}
	sc.pos++
	for sc.pos < len(s) {
		c := s[sc.pos]
		switch {
		case c == closer:
			sc.pos++
			return nil
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '=' || c == '.':
			sc.pos++
		case c == '#':
			sc.skipComment()
		case open == '{' && (isTOMLBare(c) || c == '"' || c == '\'') && sc.inlineKeyAhead():
			if _, err := sc.keyPath(); err != nil {
				return err
			}
		default:
			if err := sc.value(false); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("unclosed %c at end of file", open)
}

// Inline tables alternate keys and values, keys precede an =
func (sc *tomlScanner) inlineKeyAhead() bool {
	i := sc.pos
	for i > 0 {
		i--
		c := sc.s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c == '{' || c == ','
	}
	return false
}

func (sc *tomlScanner) basicString() error {
	s := sc.s
	i := sc.pos + 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"':
			sc.pos = i + 1
			return nil
		case '\n':
			return fmt.Errorf("unclosed string at offset %d", sc.pos)
		}
		i++
	}
	return fmt.Errorf("unclosed string at offset %d", sc.pos)
}

func escapedAt(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

func decodeTOMLString(lit string) (string, error) {
	var v struct{ K string }
	if err := toml.Unmarshal([]byte("K = "+lit), &v); err != nil {
		return "", err
	}
	return v.K, nil
}
