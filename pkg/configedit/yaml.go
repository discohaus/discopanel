package configedit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAML via the node API, comments survive a re-encode
type yamlEditor struct{}

func (yamlEditor) keyPath(key string) ([]string, error) { return ParseKeyPath(key) }

func (yamlEditor) check(data []byte) error {
	_, err := decodeYAML(data)
	return err
}

func decodeYAML(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, &doc)
	}
}

func (yamlEditor) set(data []byte, keyPath []string, value string) ([]byte, error) {
	docs, err := decodeYAML(data)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		docs = []*yaml.Node{{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}}
	}
	root := docs[0]
	if len(root.Content) == 0 {
		root.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}

	cur := root.Content[0]
	for i, seg := range keyPath {
		if cur.Kind == yaml.AliasNode {
			return nil, fmt.Errorf("%s is an alias, edit the anchor instead", pathLabel(keyPath[:i]))
		}
		switch cur.Kind {
		case yaml.MappingNode:
			var next *yaml.Node
			for j := 0; j+1 < len(cur.Content); j += 2 {
				if cur.Content[j].Value == seg {
					next = cur.Content[j+1]
				}
			}
			if next == nil {
				t, err := coerce(value, kindNone)
				if err != nil {
					return nil, err
				}
				leaf, err := yamlValue(t)
				if err != nil {
					return nil, err
				}
				for k := len(keyPath) - 1; k > i; k-- {
					leaf = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{yamlKey(keyPath[k]), leaf}}
				}
				cur.Content = append(cur.Content, yamlKey(seg), leaf)
				return encodeYAML(data, docs)
			}
			cur = next
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(cur.Content) {
				return nil, fmt.Errorf("%s has no element %q", pathLabel(keyPath[:i]), seg)
			}
			cur = cur.Content[idx]
		default:
			return nil, fmt.Errorf("%s is a value, not a mapping", pathLabel(keyPath[:i]))
		}
	}

	existing := kindRaw
	if cur.Kind == yaml.ScalarNode {
		existing = yamlKind(cur)
	}
	t, err := coerce(value, existing)
	if err != nil {
		return nil, err
	}
	repl, err := yamlValue(t)
	if err != nil {
		return nil, err
	}
	if repl.Kind == yaml.ScalarNode && cur.Kind == yaml.ScalarNode && repl.Value == cur.Value && repl.ShortTag() == cur.ShortTag() {
		// Already set, skip the re-encode so the layout stays as is
		return data, nil
	}
	if repl.Kind == yaml.ScalarNode && cur.Kind == yaml.ScalarNode && t.k == kindString && existing == kindString {
		// Keeps the quoting style the file chose
		repl.Style = cur.Style
	}
	repl.HeadComment, repl.LineComment, repl.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
	*cur = *repl
	return encodeYAML(data, docs)
}

func yamlKey(k string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
}

func yamlKind(n *yaml.Node) kind {
	switch n.ShortTag() {
	case "!!str":
		return kindString
	case "!!bool":
		return kindBool
	case "!!int":
		return kindInt
	case "!!float":
		return kindFloat
	case "!!null":
		return kindNone
	}
	return kindRaw
}

func yamlValue(t typed) (*yaml.Node, error) {
	switch t.k {
	case kindString:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t.text}, nil
	case kindBool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: t.text}, nil
	case kindInt:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: t.text}, nil
	case kindFloat:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: floatText(t.text)}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(t.text), &doc); err != nil || len(doc.Content) == 0 {
		return nil, fmt.Errorf("value %q is not valid YAML", t.text)
	}
	n := doc.Content[0]
	// Flow style keeps literal lists on one line like the user typed
	n.Style = yaml.FlowStyle
	return n, nil
}

func encodeYAML(orig []byte, docs []*yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent(string(orig)))
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if strings.Contains(string(orig), "\r\n") {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out, nil
}

// Indent width the file uses for nested mappings
func yamlIndent(s string) int {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		if n := len(line) - len(trimmed); n >= 2 && n <= 8 {
			return n
		}
	}
	return 2
}
