package mcproto

import (
	"reflect"
	"testing"
)

// Known releases resolve to their protocol numbers
func TestProtocolForVersion(t *testing.T) {
	cases := []struct {
		version string
		proto   int32
	}{
		{"1.7.10", 5},
		{"1.8", 47},
		{"1.8.9", 47},
		{"1.12.2", 340},
		{"1.16.5", 754},
		{"1.18.2", 758},
		{"1.20.1", 763},
		{"1.20.5", 766},
		{"1.21.8", 772},
	}
	for _, tc := range cases {
		got, ok := ProtocolForVersion(tc.version)
		if !ok || got != tc.proto {
			t.Fatalf("%s = %d/%v, want %d", tc.version, got, ok, tc.proto)
		}
	}
	if _, ok := ProtocolForVersion("1.99"); ok {
		t.Fatal("unknown version must miss")
	}
	if got, ok := ProtocolForVersion(" 1.12.2 "); !ok || got != 340 {
		t.Fatal("padded version must still resolve")
	}
}

// Shared protocols list every release in order
func TestVersionNamesForProtocol(t *testing.T) {
	got := VersionNamesForProtocol(754)
	want := []string{"1.16.4", "1.16.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("754 names = %v, want %v", got, want)
	}
	if newest, ok := NewestVersionForProtocol(763); !ok || newest != "1.20.1" {
		t.Fatalf("newest 763 = %q/%v, want 1.20.1", newest, ok)
	}
	if oldest, ok := OldestVersionForProtocol(754); !ok || oldest != "1.16.4" {
		t.Fatalf("oldest 754 = %q/%v, want 1.16.4", oldest, ok)
	}
	if _, ok := NewestVersionForProtocol(9999); ok {
		t.Fatal("unknown protocol must miss")
	}
}

// Forge markers split off and plain hosts pass through
func TestSplitHostMarkers(t *testing.T) {
	host, markers := SplitHostMarkers("mc.example.com\x00FML\x00")
	if host != "mc.example.com" || markers != "\x00FML\x00" {
		t.Fatalf("split = %q + %q", host, markers)
	}
	host, markers = SplitHostMarkers("mc.example.com")
	if host != "mc.example.com" || markers != "" {
		t.Fatalf("plain split = %q + %q", host, markers)
	}
}
