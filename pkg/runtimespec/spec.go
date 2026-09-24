// Reads and writes panel to runtime spec files
package runtimespec

import (
	"fmt"
	"os"
	"path/filepath"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	// Directory inside server data dir holding provisioner state
	StateDir = ".discopanel"

	// Launch spec file consumed by runtime entrypoint
	LaunchFileName = "launch.json"

	// Records what the provisioner installed, for idempotency
	ManifestFileName = "manifest.json"

	// Agent connection spec telling runtime how to reach panel
	AgentFileName = "agent.json"

	// Newest launch spec format this code writes and understands
	LaunchSpecVersion = 1

	// Newest agent spec format this code writes and understands
	AgentSpecVersion = 1
)

func LaunchPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, LaunchFileName)
}

func AgentPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, AgentFileName)
}

func ManifestPath(dataDir string) string {
	return filepath.Join(dataDir, StateDir, ManifestFileName)
}

// Loads agent spec, nil when absent
func ReadAgentSpec(dataDir string) (*v1.AgentSpec, error) {
	data, err := os.ReadFile(AgentPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var spec v1.AgentSpec
	if err := protojson.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("invalid agent spec: %w", err)
	}
	return &spec, nil
}

// Persists agent spec, file kept unreadable by group or world
func WriteAgentSpec(dataDir string, spec *v1.AgentSpec) error {
	if err := os.MkdirAll(filepath.Join(dataDir, StateDir), 0755); err != nil {
		return err
	}
	if spec.Version == 0 {
		spec.Version = AgentSpecVersion
	}
	data, err := protojson.Marshal(spec)
	if err != nil {
		return err
	}
	// Rename replaces the root-owned file container protection leaves
	tmp, err := os.CreateTemp(filepath.Join(dataDir, StateDir), "agent-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), AgentPath(dataDir)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Loads the launch spec from a server data directory
func ReadLaunchSpec(dataDir string) (*v1.LaunchSpec, error) {
	data, err := os.ReadFile(LaunchPath(dataDir))
	if err != nil {
		return nil, err
	}
	var spec v1.LaunchSpec
	if err := protojson.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("invalid launch spec: %w", err)
	}
	return &spec, nil
}

// Persists the launch spec into a server data directory
func WriteLaunchSpec(dataDir string, spec *v1.LaunchSpec) error {
	if err := os.MkdirAll(filepath.Join(dataDir, StateDir), 0755); err != nil {
		return err
	}
	if spec.Version == 0 {
		spec.Version = LaunchSpecVersion
	}
	data, err := protojson.Marshal(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(LaunchPath(dataDir), data, 0644)
}

// Loads provision manifest, nil when absent
func ReadManifest(dataDir string) (*v1.Manifest, error) {
	data, err := os.ReadFile(ManifestPath(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m v1.Manifest
	if err := protojson.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid provision manifest: %w", err)
	}
	return &m, nil
}

// Persists the provision manifest into a server data directory
func WriteManifest(dataDir string, m *v1.Manifest) error {
	if err := os.MkdirAll(filepath.Join(dataDir, StateDir), 0755); err != nil {
		return err
	}
	data, err := protojson.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(ManifestPath(dataDir), data, 0644)
}
