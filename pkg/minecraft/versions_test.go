package minecraft

import "testing"

func TestIsReleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"1.20.1": true, "1.20": true, "26.1": true, "1.7.10": true, " 1.21 ": true,
		"24w14a": false, "1.21-pre1": false, "1.20.1-rc1": false, "": false,
		"1": false, "1.2.3.4": false, "1..2": false, "forge": false,
	}
	for v, want := range cases {
		if got := IsReleaseVersion(v); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestFindMostRecentMinecraftVersion(t *testing.T) {
	if got := FindMostRecentMinecraftVersion([]string{"1.20.1", "1.19.2", "1.20.4", "24w14a", "1.21-pre1"}); got != "1.20.4" {
		t.Fatalf("highest release must win, got %q", got)
	}
	if got := FindMostRecentMinecraftVersion([]string{"1.10", "1.9"}); got != "1.10" {
		t.Fatalf("numeric compare must beat lexical order, got %q", got)
	}
	if got := FindMostRecentMinecraftVersion([]string{"24w14a", "1.21-pre1"}); got != "1.21-pre1" {
		t.Fatalf("no release must fall back to the last entry, got %q", got)
	}
	if got := FindMostRecentMinecraftVersion(nil); got != "" {
		t.Fatalf("empty list must yield empty, got %q", got)
	}
}
