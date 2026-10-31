package docker

import (
	"testing"

	"github.com/docker/docker/api/types"
)

func TestLogDriverFor(t *testing.T) {
	docker := types.Version{Components: []types.ComponentVersion{{Name: "Engine", Version: "28.5.2"}}}
	docker.Platform.Name = "Docker Engine - Community"

	podmanComponent := types.Version{Components: []types.ComponentVersion{{Name: "Podman Engine", Version: "5.2.2"}}}

	podmanPlatform := types.Version{}
	podmanPlatform.Platform.Name = "linux/amd64/fedora-41 Podman"

	tests := []struct {
		name string
		v    types.Version
		want string
	}{
		{"docker", docker, "local"},
		{"empty answer", types.Version{}, "local"},
		{"podman component", podmanComponent, "json-file"},
		{"podman platform", podmanPlatform, "json-file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logDriverFor(tt.v); got != tt.want {
				t.Fatalf("logDriverFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultLogDriverConfigured(t *testing.T) {
	c := &Client{config: ClientConfig{LogDriver: "journald"}}
	if got := c.DefaultLogDriver(); got != "journald" {
		t.Fatalf("configured driver ignored: got %q", got)
	}
	unconfigured := &Client{}
	if got := unconfigured.DefaultLogDriver(); got != "local" {
		t.Fatalf("no engine should fall back to local: got %q", got)
	}
}
