package configedit

import (
	"errors"
	"strings"
	"testing"
)

type editCase struct {
	name    string
	in      string
	entries []Entry
	want    string // Exact output when set
	has     []string
	hasNot  []string
	errKeys []string // Entries expected to fail
}

func runCases(t *testing.T, format Format, cases []editCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Apply(format, []byte(tc.in), tc.entries)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			got := string(res.Data)
			if tc.want != "" && got != tc.want {
				t.Fatalf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
			for _, h := range tc.has {
				if !strings.Contains(got, h) {
					t.Errorf("output lacks %q\n%s", h, got)
				}
			}
			for _, h := range tc.hasNot {
				if strings.Contains(got, h) {
					t.Errorf("output still has %q\n%s", h, got)
				}
			}
			var failed []string
			for _, e := range res.Errors {
				failed = append(failed, e.Key)
			}
			if strings.Join(failed, ",") != strings.Join(tc.errKeys, ",") {
				t.Fatalf("failed entries = %v, want %v (%v)", failed, tc.errKeys, res.Errors)
			}
			if res.Applied != len(tc.entries)-len(tc.errKeys) {
				t.Fatalf("applied = %d", res.Applied)
			}
			// Applying again must be a no-op
			again, err := Apply(format, res.Data, tc.entries)
			if err != nil {
				t.Fatalf("reapply: %v", err)
			}
			if again.Changed {
				t.Fatalf("reapply changed the file\n%s\n---\n%s", got, again.Data)
			}
		})
	}
}

func TestProperties(t *testing.T) {
	in := "# header comment\nmax-claims=5\nname = old\nrcon.port: 25575\n"
	runCases(t, FormatProperties, []editCase{
		{
			name:    "replace keeps separators and comments",
			in:      in,
			entries: []Entry{{"max-claims", "10"}, {"name", "new value"}, {"rcon.port", "25580"}},
			want:    "# header comment\nmax-claims=10\nname = new value\nrcon.port: 25580\n",
		},
		{
			name:    "missing key appended",
			in:      "a=1",
			entries: []Entry{{"b", "two"}},
			want:    "a=1\nb=two\n",
		},
		{
			name:    "quotes strip and backslashes escape",
			in:      "path=x\n",
			entries: []Entry{{"path", `"C:\dir"`}},
			want:    "path=C:\\\\dir\n",
		},
		{
			name:    "continuation lines replaced whole",
			in:      "list=a,\\\n  b\nnext=1\n",
			entries: []Entry{{"list", "c"}},
			want:    "list=c\nnext=1\n",
		},
	})
}

func TestTOML(t *testing.T) {
	in := `# Create server config
[general]
	# Max claims
	maxClaims = 5 # inline
	enabled = true
	ratio = 0.5
	name = "old"
	list = [
		"a", # first
		"b",
	]

[general.nested]
	depth = 1

[[entries]]
	id = 1
`
	runCases(t, FormatTOML, []editCase{
		{
			name: "set nested keys with types",
			in:   in,
			entries: []Entry{
				{"general.maxClaims", "10"},
				{"general.enabled", "false"},
				{"general.ratio", "2"},
				{"general.name", "new"},
				{"general.nested.depth", "3"},
			},
			has: []string{
				"\t# Max claims\n\tmaxClaims = 10 # inline\n",
				"\tenabled = false\n", "\tratio = 2.0\n", "\tname = \"new\"\n",
				"\tdepth = 3\n", "# Create server config\n", "\"a\", # first",
			},
		},
		{
			name:    "replace multi-line array",
			in:      in,
			entries: []Entry{{"general.list", `["x", "y"]`}},
			has:     []string{"\tlist = [\"x\", \"y\"]\n\n[general.nested]"},
			hasNot:  []string{"# first"},
		},
		{
			name:    "missing key goes into its table",
			in:      in,
			entries: []Entry{{"general.newKey", "on"}, {"general.nested.other", "1.5"}},
			has:     []string{"\tname = \"old\"", "\tlist = [", "\tdepth = 1\n\tother = 1.5\n", "\tnewKey = \"on\"\n\n[general.nested]"},
		},
		{
			name:    "missing table appended",
			in:      in,
			entries: []Entry{{"client.render.fancy", "true"}},
			has:     []string{"\n[client.render]\nfancy = true\n"},
		},
		{
			name:    "root key before first table",
			in:      "[a]\nx = 1\n",
			entries: []Entry{{"top", "1"}},
			want:    "top = 1\n\n[a]\nx = 1\n",
		},
		{
			name:    "type mismatches and array tables rejected",
			in:      in,
			entries: []Entry{{"general.maxClaims", "lots"}, {"general.enabled", "maybe"}, {"entries.id", "2"}, {"general.maxClaims.x", "1"}, {"general.ratio", "3"}},
			errKeys: []string{"general.maxClaims", "general.enabled", "entries.id", "general.maxClaims.x"},
			has:     []string{"ratio = 3.0"},
		},
		{
			name:    "quoted text forced onto a string key",
			in:      "[a]\nv = \"1\"\n",
			entries: []Entry{{"a.v", "2"}, {"a.w", `"3"`}},
			want:    "[a]\nv = \"2\"\nw = \"3\"\n",
		},
	})
}

func TestJSON(t *testing.T) {
	in := `{
  // comment kept
  "general": {
    "maxClaims": 5,
    "enabled": true,
    "name": "old",
    "list": [1, 2, 3]
  },
  "empty": {}
}
`
	runCases(t, FormatJSON, []editCase{
		{
			name:    "set nested and pointer keys",
			in:      in,
			entries: []Entry{{"general.maxClaims", "10"}, {"/general/enabled", "false"}, {"general.name", "a \"q\""}, {"general.list.1", "9"}},
			has:     []string{"// comment kept", `"maxClaims": 10,`, `"enabled": false,`, `"name": "a \"q\"",`, `[1, 9, 3]`},
		},
		{
			name:    "add missing keys with nesting",
			in:      in,
			entries: []Entry{{"general.ratio", "1.5"}, {"empty.x", "1"}, {"new.deep.key", "yes"}},
			has: []string{
				"\"list\": [1, 2, 3],\n    \"ratio\": 1.5\n  },",
				"\"empty\": {\n    \"x\": 1\n  },",
				"\"new\": {\n    \"deep\": {\n      \"key\": \"yes\"\n    }\n  }\n}",
			},
		},
		{
			name:    "strict json stays strict",
			in:      "{\"a\": 1}",
			entries: []Entry{{"b", "true"}, {"a", "nope"}, {"a.b", "1"}},
			want:    "{\"a\": 1, \"b\": true}",
			errKeys: []string{"a", "a.b"},
		},
		{
			name:    "json5 trailing commas and bare keys",
			in:      "{\n  a: 1,\n  b: 'x',\n}\n",
			entries: []Entry{{"b", "y"}, {"c", "false"}},
			want:    "{\n  a: 1,\n  b: \"y\",\n  \"c\": false,\n}\n",
		},
	})
}

func TestSNBT(t *testing.T) {
	in := `# FTB Chunks world config
{
	# Max claimed chunks
	max_claimed_chunks: 500
	force_load: false
	ratio: 1.5d
	big: 10L
	flag: 1b
	mode: "normal"
	dims: [
		"minecraft:overworld"
	]
	nested: {
		inner: 1
	}
}
`
	runCases(t, FormatSNBT, []editCase{
		{
			name: "set keeps suffixes and comments",
			in:   in,
			entries: []Entry{
				{"max_claimed_chunks", "1000"}, {"force_load", "true"}, {"ratio", "2"},
				{"big", "20"}, {"flag", "false"}, {"mode", "hard"}, {"nested.inner", "7"},
			},
			has: []string{
				"# FTB Chunks world config", "\t# Max claimed chunks\n\tmax_claimed_chunks: 1000\n",
				"force_load: true", "ratio: 2.0d", "big: 20L", "flag: 0b", `mode: "hard"`, "\t\tinner: 7\n",
			},
		},
		{
			name:    "add missing keys newline separated",
			in:      in,
			entries: []Entry{{"new_key", "3"}, {"nested.other", "x"}, {"deep.a", "1.5"}},
			has:     []string{"\t\tinner: 1\n\t\tother: \"x\"\n\t}", "\tnew_key: 3\n", "\tdeep: {\n\t\ta: 1.5d\n\t}\n}"},
			hasNot:  []string{",\n"},
		},
		{
			name:    "type mismatch rejected",
			in:      in,
			entries: []Entry{{"max_claimed_chunks", "many"}, {"dims.3", "x"}},
			errKeys: []string{"max_claimed_chunks", "dims.3"},
		},
	})
}

func TestYAML(t *testing.T) {
	in := `# Plugin config
general:
  # max claims per player
  maxClaims: 5
  enabled: true
  name: 'old'
list:
  - a
  - b
`
	runCases(t, FormatYAML, []editCase{
		{
			name:    "set nested keeps comments",
			in:      in,
			entries: []Entry{{"general.maxClaims", "10"}, {"general.enabled", "false"}, {"general.name", "new"}, {"list.1", "c"}},
			has:     []string{"# Plugin config", "# max claims per player", "maxClaims: 10", "enabled: false", "name: 'new'", "- c"},
		},
		{
			name:    "add missing nested",
			in:      in,
			entries: []Entry{{"general.ratio", "0.5"}, {"other.deep", "true"}, {"general.str", `"123"`}},
			has:     []string{"  ratio: 0.5", "other:\n  deep: true", `str: "123"`},
		},
		{
			name:    "mismatch rejected",
			in:      in,
			entries: []Entry{{"general.maxClaims", "x"}, {"list.9", "x"}},
			errKeys: []string{"general.maxClaims", "list.9"},
		},
	})
}

func TestCFG(t *testing.T) {
	in := `# Configuration file

general {
    # Max claims
    I:maxClaims=5
    B:enabled=true
    D:ratio=0.5
    S:name=old
    S:some.dotted=x

    S:list <
        a
     >

    sub {
        I:depth=1
    }

}
`
	runCases(t, FormatCFG, []editCase{
		{
			name:    "set typed keys",
			in:      in,
			entries: []Entry{{"general.maxClaims", "10"}, {"general.enabled", "false"}, {"general.ratio", "2"}, {"general.name", "new name"}, {"general.some.dotted", "y"}, {"general.sub.depth", "4"}},
			has:     []string{"    # Max claims\n    I:maxClaims=10\n", "B:enabled=false", "D:ratio=2.0", "S:name=new name", "S:some.dotted=y", "        I:depth=4"},
		},
		{
			name:    "add missing key and category",
			in:      in,
			entries: []Entry{{"general.extra", "true"}, {"general.sub.more", "x"}, {"client.render.fancy", "1"}},
			has:     []string{"    }\n    B:extra=true\n\n}", "        I:depth=1\n        S:more=x\n    }", "client {\n    render {\n        I:fancy=1\n    }\n}\n"},
		},
		{
			name:    "mismatch and lists rejected",
			in:      in,
			entries: []Entry{{"general.maxClaims", "1.5"}, {"general.list", "b"}},
			errKeys: []string{"general.maxClaims", "general.list"},
		},
	})
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		format Format
		in     string
	}{
		{FormatTOML, "a = [1, 2\n"},
		{FormatJSON, `{"a": }`},
		{FormatSNBT, "{ a: 1"},
		{FormatYAML, "a: [1, 2\n"},
		{FormatCFG, "general {\n I:x=1\n"},
	}
	for _, tc := range cases {
		_, err := Apply(tc.format, []byte(tc.in), []Entry{{"a", "1"}})
		if !errors.Is(err, ErrParse) {
			t.Errorf("%s: err = %v, want parse error", tc.format, err)
		}
	}
}

func TestParseKeyPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"general.maxClaims", "general|maxClaims", false},
		{`a."b.c".d`, "a|b.c|d", false},
		{"/a/b~1c/d~0e", "a|b/c|d~e", false},
		{"a..b", "", true},
		{"", "", true},
		{`a."b`, "", true},
		{"/a//b", "", true},
	}
	for _, tc := range cases {
		got, err := ParseKeyPath(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v", tc.in, err)
			continue
		}
		if !tc.wantErr && strings.Join(got, "|") != tc.want {
			t.Errorf("%q = %v, want %s", tc.in, got, tc.want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]Format{
		"config/create-server.toml":   FormatTOML,
		"config/ftbchunks-world.snbt": FormatSNBT,
		"config/a.JSON5":              FormatJSON,
		"plugins/x/config.yml":        FormatYAML,
		"config/old.cfg":              FormatCFG,
		"x.properties":                FormatProperties,
		"x.conf":                      FormatUnknown,
	}
	for name, want := range cases {
		if got := DetectFormat(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestReviewFixes(t *testing.T) {
	t.Run("toml with byte order mark", func(t *testing.T) {
		res, err := Apply(FormatTOML, []byte("\xEF\xBB\xBF[a]\nb = 1\n"), []Entry{{"a.b", "2"}})
		if err != nil || string(res.Data) != "\xEF\xBB\xBF[a]\nb = 2\n" {
			t.Fatalf("got %q, %v", res.Data, err)
		}
	})
	t.Run("toml dotted root key stays dotted", func(t *testing.T) {
		res, err := Apply(FormatTOML, []byte("a.b = 1\n\n[c]\nd = 1\n"), []Entry{{"a.x", "2"}})
		if err != nil || len(res.Errors) > 0 || string(res.Data) != "a.b = 1\na.x = 2\n\n[c]\nd = 1\n" {
			t.Fatalf("got %q, %v %v", res.Data, err, res.Errors)
		}
	})
	t.Run("snbt strings escape only quotes and backslashes", func(t *testing.T) {
		res, err := Apply(FormatSNBT, []byte("{\n\tname: \"x\"\n}\n"), []Entry{{"name", "tab\there é \"q\" \\ <&>"}})
		if err != nil || !strings.Contains(string(res.Data), "name: \"tab\there é \\\"q\\\" \\\\ <&>\"") {
			t.Fatalf("got %q, %v %v", res.Data, err, res.Errors)
		}
	})
	t.Run("json bare words refused", func(t *testing.T) {
		in := "{\n  // c\n  \"a\": 0x1F,\n  \"n\": Infinity\n}\n"
		res, err := Apply(FormatJSON, []byte(in), []Entry{{"a", "hello"}, {"n", "[hello]"}, {"a", "0x20"}, {"n", "NaN"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Errors) != 2 || !strings.Contains(string(res.Data), `"a": 0x20`) || !strings.Contains(string(res.Data), `"n": NaN`) {
			t.Fatalf("got %q, %v", res.Data, res.Errors)
		}
		if _, err := Apply(FormatJSON, []byte(`{"a": hello}`), nil); !errors.Is(err, ErrParse) {
			t.Fatalf("bare word file parsed: %v", err)
		}
	})
	t.Run("binary and non utf8 refused", func(t *testing.T) {
		for _, in := range []string{"a=1\x00", "a=\xff"} {
			if _, err := Apply(FormatProperties, []byte(in), []Entry{{"b", "1"}}); !errors.Is(err, ErrParse) {
				t.Fatalf("%q: err = %v", in, err)
			}
		}
	})
	t.Run("json family extensions", func(t *testing.T) {
		for _, name := range []string{"a.jsonc", "pack.mcmeta"} {
			if DetectFormat(name) != FormatJSON {
				t.Fatalf("%s not json", name)
			}
		}
	})
}
