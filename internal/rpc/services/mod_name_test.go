package services

import "testing"

func TestIsPlainFileName(t *testing.T) {
	good := []string{"sodium-fabric-0.6.0.jar", "Create 1.21.jar", "a.jar"}
	bad := []string{"", ".", "..", "../x.jar", "../../srv/plugins/x.jar", "mods/x.jar", `..\x.jar`, `C:\x.jar`, "/etc/x.jar", ".hidden.jar", "x\x00.jar", "x\n.jar"}
	for _, n := range good {
		if !isPlainFileName(n) {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range bad {
		if isPlainFileName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}
