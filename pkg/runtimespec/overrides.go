package runtimespec

import (
	"fmt"
	"os"
	"path/filepath"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// Per-server config file overrides, re-applied on every start
	ConfigOverridesFileName = "config-overrides.json"

	// Newest config overrides format this code writes and understands
	ConfigOverridesVersion = 1
)

func ConfigOverridesPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, ConfigOverridesFileName)
}

// Loads config overrides, empty when absent
func ReadConfigOverrides(dataDir string) (*v1.ConfigOverrides, error) {
	data, err := os.ReadFile(ConfigOverridesPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return &v1.ConfigOverrides{Version: ConfigOverridesVersion}, nil
		}
		return nil, err
	}
	var o v1.ConfigOverrides
	if err := protojson.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("invalid config overrides: %w", err)
	}
	return &o, nil
}

// Persists config overrides, rename keeps readers from a torn file
func WriteConfigOverrides(dataDir string, o *v1.ConfigOverrides) error {
	dir := filepath.Join(dataDir, StateDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	o.Version = ConfigOverridesVersion
	data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(o)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config-overrides-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), ConfigOverridesPath(dataDir)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
