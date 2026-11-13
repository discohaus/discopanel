package services

import (
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestEscalatingChange(t *testing.T) {
	adminSet := &v1.DockerOverrides{
		Privileged:  true,
		CapAdd:      []string{"NET_ADMIN", "SYS_ADMIN"},
		Devices:     []string{"/dev/net/tun:/dev/net/tun"},
		SecurityOpt: []string{"apparmor=unconfined"},
		NetworkMode: "host",
	}
	cases := []struct {
		name       string
		next, prev *v1.DockerOverrides
		want       string
	}{
		{"plain overrides", &v1.DockerOverrides{CpuLimit: 2, Environment: map[string]string{"A": "b"}}, nil, ""},
		{"privileged on create", &v1.DockerOverrides{Privileged: true}, nil, "privileged mode"},
		{"cap add", &v1.DockerOverrides{CapAdd: []string{"SYS_ADMIN"}}, nil, "added capabilities"},
		{"cap drop is fine", &v1.DockerOverrides{CapDrop: []string{"ALL"}}, nil, ""},
		{"devices", &v1.DockerOverrides{Devices: []string{"/dev/sda:/dev/sda"}}, nil, "device mappings"},
		{"security opt", &v1.DockerOverrides{SecurityOpt: []string{"seccomp=unconfined"}}, nil, "security options"},
		{"host network", &v1.DockerOverrides{NetworkMode: "host"}, nil, "network mode host"},
		{"host network caps", &v1.DockerOverrides{NetworkMode: " HOST "}, nil, "network mode  HOST "},
		{"container network", &v1.DockerOverrides{NetworkMode: "container:abc"}, nil, "network mode container:abc"},
		{"bridge network", &v1.DockerOverrides{NetworkMode: "bridge"}, nil, ""},
		{"editor keeps admin settings", adminSet, adminSet, ""},
		{"editor reorders caps", &v1.DockerOverrides{
			Privileged: true, CapAdd: []string{"SYS_ADMIN", "NET_ADMIN"},
			Devices: adminSet.Devices, SecurityOpt: adminSet.SecurityOpt, NetworkMode: "host",
		}, adminSet, ""},
		{"editor removes privileges", &v1.DockerOverrides{}, adminSet, ""},
		{"editor drops one cap", &v1.DockerOverrides{CapAdd: []string{"NET_ADMIN"}}, adminSet, ""},
		{"editor adds a cap", &v1.DockerOverrides{
			Privileged: true, CapAdd: []string{"NET_ADMIN", "SYS_ADMIN", "SYS_PTRACE"},
		}, adminSet, "added capabilities"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escalatingChange(tc.next, tc.prev); got != tc.want {
				t.Fatalf("escalatingChange() = %q, want %q", got, tc.want)
			}
		})
	}
}
