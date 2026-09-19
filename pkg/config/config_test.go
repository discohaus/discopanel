package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// Secret shaped names must refuse alias resolution
var secretFieldPattern = regexp.MustCompile(`(?i)(secret|token|password|api_?key)`)

func TestSecretConfigFieldsCarrySecretTag(t *testing.T) {
	assertSecretTags(t, reflect.TypeOf(Config{}), "Config")
}

func assertSecretTags(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldPath := path + "." + field.Name
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			assertSecretTags(t, ft, fieldPath)
			continue
		}
		if secretFieldPattern.MatchString(field.Name) && field.Tag.Get("alias") != "secret" {
			t.Errorf("field %s matches secret pattern without alias secret tag", fieldPath)
		}
	}
}

// Explicit file paths must load that exact file
func TestLoadExplicitConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: \"9999\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load explicit file: %v", err)
	}
	if cfg.Server.Port != "9999" {
		t.Errorf("port = %q, want 9999", cfg.Server.Port)
	}
}

// Missing explicit files must fail loudly
func TestLoadMissingExplicitFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing explicit config file")
	}
}

// Directory paths must stay searchable
func TestLoadConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("server:\n  port: \"7777\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load config dir: %v", err)
	}
	if cfg.Server.Port != "7777" {
		t.Errorf("port = %q, want 7777", cfg.Server.Port)
	}
}

// Hub env wins over the file and routes upstreams through the index
func TestHubEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	file := "support:\n  base_url: \"https://file-support.example\"\nindex:\n  base_url: \"https://file-index.example\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SupportBaseEnv, "http://127.0.0.1:8911/")
	t.Setenv(IndexBaseEnv, "http://127.0.0.1:8912//")
	t.Setenv("DISCOPANEL_INDEX_ENABLED", "false")
	t.Setenv("DISCOPANEL_TELEMETRY_ENABLED", "false")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Support.BaseURL != "http://127.0.0.1:8911" {
		t.Errorf("support.base_url = %q, want the env origin without its slash", cfg.Support.BaseURL)
	}
	if cfg.Index.BaseURL != "http://127.0.0.1:8912" {
		t.Errorf("index.base_url = %q, want the env origin without its slashes", cfg.Index.BaseURL)
	}
	if !cfg.Index.Enabled {
		t.Error("a hub supplied index must turn the index on")
	}
	if cfg.Telemetry.Enabled {
		t.Error("DISCOPANEL_TELEMETRY_ENABLED=false should turn telemetry off")
	}
}

// Prefixed env alone must turn the index on
func TestIndexEnabledEnv(t *testing.T) {
	t.Setenv("DISCOPANEL_INDEX_ENABLED", "true")
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Index.Enabled {
		t.Error("DISCOPANEL_INDEX_ENABLED=true should turn the index on")
	}
}

// Prefixed env alone must reach the hub keys too
func TestHubPrefixedEnv(t *testing.T) {
	t.Setenv("DISCOPANEL_SUPPORT_BASE_URL", "https://support.example")
	t.Setenv("DISCOPANEL_INDEX_BASE_URL", "https://index.example")
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Support.BaseURL != "https://support.example" {
		t.Errorf("support.base_url = %q", cfg.Support.BaseURL)
	}
	if cfg.Index.BaseURL != "https://index.example" {
		t.Errorf("index.base_url = %q", cfg.Index.BaseURL)
	}
}

// Empty and bare host origins are repaired, not refused
func TestHubLenientOrigins(t *testing.T) {
	cases := map[string]string{
		"":                 DefaultSupportBaseURL,
		"   ":              DefaultSupportBaseURL,
		"support.example":  "https://support.example",
		"support.example/": "https://support.example",
		"localhost:8911":   "https://localhost:8911",
	}
	for raw, want := range cases {
		dir := t.TempDir()
		file := "support:\n  base_url: \"" + raw + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Errorf("support.base_url %q: %v", raw, err)
			continue
		}
		if cfg.Support.BaseURL != want {
			t.Errorf("support.base_url %q = %q, want %q", raw, cfg.Support.BaseURL, want)
		}
	}
}

// Origins that are not http must fail loudly
func TestHubBadOriginFails(t *testing.T) {
	for _, bad := range []string{"ftp://support.example", "https://support.example/?x=1", "https://user:pw@support.example"} {
		dir := t.TempDir()
		file := "support:\n  base_url: \"" + bad + "\"\n"
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("support.base_url %q should be rejected", bad)
		}
	}
}
