package docker

import (
	"testing"
)

func TestParseSelfContainerIDs(t *testing.T) {
	const id = "9ac6b5947cb5e8412f02d63448e1aad24a43f861ab86c161f9cb3069fee484cd"
	hostNet := "715 706 259:8 /var/lib/docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw,noatime - ext4 /dev/nvme1n1p2 rw\n" +
		"716 706 259:8 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,noatime - ext4 /dev/nvme1n1p2 rw\n" +
		"43 1 259:8 / / rw,noatime shared:1 - ext4 /dev/nvme1n1p2 rw\n"
	ids := parseSelfContainerIDs([]byte(hostNet), []byte("0::/\n"), "disco")
	if len(ids) != 2 || ids[0] != id || ids[1] != "disco" {
		t.Fatalf("host network mode must yield the mounted id before the hostname, got %v", ids)
	}

	podman := "500 400 0:40 /containers/storage/overlay-containers/" + id + "/userdata/resolv.conf /etc/resolv.conf rw - tmpfs tmpfs rw\n"
	if ids := parseSelfContainerIDs([]byte(podman), nil, ""); len(ids) != 1 || ids[0] != id {
		t.Fatalf("podman layout not recognised, got %v", ids)
	}

	cgroupV1 := "12:memory:/docker/" + id + "\n"
	if ids := parseSelfContainerIDs(nil, []byte(cgroupV1), "e2391ca4470d"); len(ids) != 2 || ids[0] != id {
		t.Fatalf("cgroup v1 id must come before hostname, got %v", ids)
	}

	if ids := parseSelfContainerIDs([]byte("43 1 259:8 / / rw - ext4 /dev/sda1 rw\n"), []byte("0::/init.scope\n"), ""); len(ids) != 0 {
		t.Fatalf("bare metal must yield no candidates, got %v", ids)
	}
}
