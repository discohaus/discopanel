// Sets individual keys inside mod config files, keeping the rest intact
package configedit

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Config file dialect an edit parses and writes
type Format int

const (
	FormatUnknown Format = iota
	FormatProperties
	FormatTOML
	FormatJSON
	FormatYAML
	FormatCFG // Forge legacy categories with typed keys
	FormatSNBT
)

func (f Format) String() string {
	switch f {
	case FormatProperties:
		return "properties"
	case FormatTOML:
		return "toml"
	case FormatJSON:
		return "json"
	case FormatYAML:
		return "yaml"
	case FormatCFG:
		return "cfg"
	case FormatSNBT:
		return "snbt"
	}
	return "unknown"
}

// Picks the format from a file extension, unknown when unsupported
func DetectFormat(name string) Format {
	switch strings.ToLower(path.Ext(name)) {
	case ".properties":
		return FormatProperties
	case ".toml":
		return FormatTOML
	case ".json", ".json5", ".jsonc", ".mcmeta":
		return FormatJSON
	case ".yml", ".yaml":
		return FormatYAML
	case ".cfg":
		return FormatCFG
	case ".snbt":
		return FormatSNBT
	}
	return FormatUnknown
}

// One key to force to a value
type Entry struct {
	Key   string // Dot path or JSON pointer, literal key for properties
	Value string // Typed by the existing value, quotes force text
}

// Failure of one entry, the others still apply
type EntryError struct {
	Key string
	Err error
}

func (e *EntryError) Error() string { return fmt.Sprintf("%s: %v", e.Key, e.Err) }
func (e *EntryError) Unwrap() error { return e.Err }

// Outcome of applying entries to one file
type Result struct {
	Data    []byte
	Changed bool
	Applied int // Entries that now hold their value
	Errors  []*EntryError
}

// Reported when the file itself cannot be parsed
var ErrParse = errors.New("parse error")

// Applies entries to data, errors only when the file does not parse
func Apply(format Format, data []byte, entries []Entry) (*Result, error) {
	ed, err := editorFor(format)
	if err != nil {
		return nil, err
	}
	if err := CheckText(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParse, err)
	}
	// Editors see the text without a byte order mark, writes restore it
	bom, body := splitBOM(data)
	if err := ed.check(body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParse, err)
	}
	res := &Result{Data: data}
	for _, e := range entries {
		next, err := applyOne(ed, body, e)
		if err != nil {
			res.Errors = append(res.Errors, &EntryError{Key: e.Key, Err: err})
			continue
		}
		body = next
		res.Applied++
	}
	res.Data = append(append([]byte{}, bom...), body...)
	res.Changed = !bytes.Equal(res.Data, data)
	return res, nil
}

// Refuses binary and non UTF-8 content, config files are text
func CheckText(data []byte) error {
	if bytes.IndexByte(data, 0) >= 0 {
		return errors.New("file contains NUL bytes, not a text config")
	}
	if !utf8.Valid(data) {
		return errors.New("file is not valid UTF-8 text")
	}
	return nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func splitBOM(data []byte) ([]byte, []byte) {
	if bytes.HasPrefix(data, utf8BOM) {
		return utf8BOM, data[len(utf8BOM):]
	}
	return nil, data
}

func applyOne(ed editor, data []byte, e Entry) ([]byte, error) {
	if strings.ContainsAny(e.Value, "\r\n") {
		return nil, errors.New("values must be a single line")
	}
	keyPath, err := ed.keyPath(e.Key)
	if err != nil {
		return nil, err
	}
	next, err := ed.set(data, keyPath, e.Value)
	if err != nil {
		return nil, err
	}
	// Every edit must leave a file its loader still reads
	if err := ed.check(next); err != nil {
		return nil, fmt.Errorf("edit would break the file: %v", err)
	}
	return next, nil
}

// Validates a key for format without touching any file
func ValidateKey(format Format, key string) error {
	ed, err := editorFor(format)
	if err != nil {
		return err
	}
	_, err = ed.keyPath(key)
	return err
}

type editor interface {
	check(data []byte) error
	keyPath(key string) ([]string, error)
	set(data []byte, keyPath []string, value string) ([]byte, error)
}

func editorFor(format Format) (editor, error) {
	switch format {
	case FormatProperties:
		return propertiesEditor{}, nil
	case FormatTOML:
		return tomlEditor{}, nil
	case FormatJSON:
		return treeEditor{d: jsonDialect}, nil
	case FormatSNBT:
		return treeEditor{d: snbtDialect}, nil
	case FormatYAML:
		return yamlEditor{}, nil
	case FormatCFG:
		return cfgEditor{}, nil
	}
	return nil, fmt.Errorf("unsupported config format")
}

// Splits a dot path or a JSON pointer into segments
//
// Dot paths may quote a segment holding dots: a."b.c".d
func ParseKeyPath(key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("key is empty")
	}
	if strings.HasPrefix(key, "/") {
		return parsePointer(key)
	}
	var segs []string
	var cur strings.Builder
	quoted := false
	closed := false
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '"' && cur.Len() == 0 && !quoted && !closed:
			quoted = true
		case c == '"' && quoted:
			quoted = false
			closed = true
		case c == '\\' && quoted && i+1 < len(key):
			i++
			cur.WriteByte(key[i])
		case c == '.' && !quoted:
			if cur.Len() == 0 && !closed {
				return nil, fmt.Errorf("key %q has an empty segment", key)
			}
			segs = append(segs, cur.String())
			cur.Reset()
			closed = false
		case closed:
			return nil, fmt.Errorf("key %q has text after a closing quote", key)
		default:
			cur.WriteByte(c)
		}
	}
	if quoted {
		return nil, fmt.Errorf("key %q has an unclosed quote", key)
	}
	if cur.Len() == 0 && !closed {
		return nil, fmt.Errorf("key %q has an empty segment", key)
	}
	return append(segs, cur.String()), nil
}

// RFC 6901 pointer segments
func parsePointer(key string) ([]string, error) {
	if key == "/" {
		return nil, errors.New("pointer must name a key")
	}
	parts := strings.Split(key[1:], "/")
	for i, p := range parts {
		if p == "" {
			return nil, fmt.Errorf("pointer %q has an empty segment", key)
		}
		if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(p, "~0", ""), "~1", ""), "~") {
			return nil, fmt.Errorf("pointer %q has a bad ~ escape", key)
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// Value type an override resolves to
type kind int

const (
	kindNone kind = iota // Key absent, type inferred from the value
	kindString
	kindBool
	kindInt
	kindFloat
	kindRaw // Array, table or other literal written verbatim
)

var (
	intRe   = regexp.MustCompile(`^[+-]?[0-9]+$`)
	floatRe = regexp.MustCompile(`^[+-]?([0-9]+\.[0-9]*|\.[0-9]+|[0-9]+)([eE][+-]?[0-9]+)?$`)
)

// Typed value ready for a format encoder
type typed struct {
	k    kind
	text string // Canonical text, unquoted for strings
}

// Reads what the user typed: quotes force text, literals keep their type
func infer(raw string) typed {
	v := strings.TrimSpace(raw)
	if s, ok := unquoteForced(v); ok {
		return typed{kindString, s}
	}
	lower := strings.ToLower(v)
	switch {
	case lower == "true" || lower == "false":
		return typed{kindBool, lower}
	case intRe.MatchString(v):
		return typed{kindInt, v}
	case floatRe.MatchString(v):
		return typed{kindFloat, v}
	case strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{"):
		return typed{kindRaw, v}
	}
	return typed{kindString, v}
}

func unquoteForced(v string) (string, bool) {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return "", false
	}
	if s, err := strconv.Unquote(v); err == nil {
		return s, true
	}
	return v[1 : len(v)-1], true
}

// Fits the user's value to the type the key already has
func coerce(raw string, existing kind) (typed, error) {
	t := infer(raw)
	if _, forced := unquoteForced(strings.TrimSpace(raw)); forced {
		if existing == kindString || existing == kindNone || existing == kindRaw {
			return t, nil
		}
		return t, fmt.Errorf("expects %s, got quoted text", kindName(existing))
	}
	switch existing {
	case kindNone:
		return t, nil
	case kindString:
		return typed{kindString, strings.TrimSpace(raw)}, nil
	case kindBool:
		if t.k != kindBool {
			return t, fmt.Errorf("expects true or false, got %q", raw)
		}
	case kindInt:
		if t.k != kindInt {
			return t, fmt.Errorf("expects a whole number, got %q", raw)
		}
	case kindFloat:
		if t.k != kindInt && t.k != kindFloat {
			return t, fmt.Errorf("expects a number, got %q", raw)
		}
		t.k = kindFloat
	case kindRaw:
		// Literal replaces literal, the reparse check validates it
		return typed{kindRaw, strings.TrimSpace(raw)}, nil
	}
	return t, nil
}

func kindName(k kind) string {
	switch k {
	case kindString:
		return "text"
	case kindBool:
		return "true or false"
	case kindInt:
		return "a whole number"
	case kindFloat:
		return "a number"
	}
	return "a literal"
}

// Float text always carrying a decimal point
func floatText(s string) string {
	if strings.ContainsAny(s, ".eE") {
		return s
	}
	return s + ".0"
}

// Leading whitespace of the line holding offset
func lineIndent(s string, off int) string {
	start := strings.LastIndexByte(s[:off], '\n') + 1
	end := start
	for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
		end++
	}
	return s[start:end]
}

// Most common indent step in the file, fallback when none
func indentUnit(s, fallback string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		ws := line[:len(line)-len(trimmed)]
		if ws[0] == '\t' {
			return "\t"
		}
		if len(ws) <= 8 {
			return ws
		}
	}
	return fallback
}

// Newline style the file already uses
func newline(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}
