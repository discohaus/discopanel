package configedit

import (
	"errors"
	"fmt"
	"strings"
)

// Java properties, key paths are the literal key
type propertiesEditor struct{}

// One key line, offsets into the file text
type propLine struct {
	key        string
	valueStart int
	end        int // End of the logical line, before its newline
}

func (propertiesEditor) keyPath(key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("key is empty")
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("key must be a single line")
	}
	return []string{key}, nil
}

func (propertiesEditor) check(data []byte) error {
	_, err := scanProperties(string(data))
	return err
}

func (propertiesEditor) set(data []byte, keyPath []string, value string) ([]byte, error) {
	s := string(data)
	lines, err := scanProperties(s)
	if err != nil {
		return nil, err
	}
	key := keyPath[0]
	encoded := escapePropertyValue(strings.TrimSpace(value))
	if t, forced := unquoteForced(strings.TrimSpace(value)); forced {
		encoded = escapePropertyValue(t)
	}

	// Java keeps the last duplicate, rewrite them all
	found := false
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l.key != key {
			continue
		}
		found = true
		s = s[:l.valueStart] + encoded + s[l.end:]
	}
	if found {
		return []byte(s), nil
	}

	nl := newline(s)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += nl
	}
	return []byte(s + escapePropertyKey(key) + "=" + encoded + nl), nil
}

// Splits the text into key lines, joining continuations
func scanProperties(s string) ([]propLine, error) {
	var out []propLine
	pos := 0
	for pos < len(s) {
		end := logicalLineEnd(s, pos)
		next := end
		if next < len(s) && s[next] == '\r' {
			next++
		}
		if next < len(s) && s[next] == '\n' {
			next++
		}
		if l, ok := parsePropLine(s, pos, end); ok {
			out = append(out, l)
		}
		if next == pos {
			return nil, fmt.Errorf("unreadable line at offset %d", pos)
		}
		pos = next
	}
	return out, nil
}

// End of a logical line, odd trailing backslashes continue it
func logicalLineEnd(s string, pos int) int {
	for {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			return len(s)
		}
		end := pos + nl
		lineEnd := end
		if lineEnd > pos && s[lineEnd-1] == '\r' {
			lineEnd--
		}
		slashes := 0
		for i := lineEnd - 1; i >= pos && s[i] == '\\'; i-- {
			slashes++
		}
		if slashes%2 == 0 {
			return lineEnd
		}
		pos = end + 1
	}
}

func parsePropLine(s string, start, end int) (propLine, bool) {
	i := start
	for i < end && (s[i] == ' ' || s[i] == '\t' || s[i] == '\f') {
		i++
	}
	if i >= end || s[i] == '#' || s[i] == '!' {
		return propLine{}, false
	}
	var key strings.Builder
	for i < end {
		c := s[i]
		if c == '\\' && i+1 < end {
			i++
			key.WriteByte(unescapePropertyChar(s[i]))
			i++
			continue
		}
		if c == '=' || c == ':' || c == ' ' || c == '\t' || c == '\f' {
			break
		}
		key.WriteByte(c)
		i++
	}
	for i < end && (s[i] == ' ' || s[i] == '\t' || s[i] == '\f') {
		i++
	}
	if i < end && (s[i] == '=' || s[i] == ':') {
		i++
	}
	for i < end && (s[i] == ' ' || s[i] == '\t' || s[i] == '\f') {
		i++
	}
	return propLine{key: key.String(), valueStart: i, end: end}, true
}

func unescapePropertyChar(c byte) byte {
	switch c {
	case 't':
		return '\t'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 'f':
		return '\f'
	}
	return c
}

func escapePropertyValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	// Leading blanks would be eaten as separator whitespace
	if strings.HasPrefix(v, " ") {
		v = `\` + v
	}
	return v
}

func escapePropertyKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch r {
		case '\\', '=', ':', ' ', '#', '!':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
