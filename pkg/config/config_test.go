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
