package runtimespec

import "testing"

func TestParseMemoryMB(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"4096M", 4096},
		{"12G", 12288},
		{"2048", 2048},
		{"  8g ", 8192},
		{"1048576K", 1024},
		{"", 0},
		{"lots", 0},
		{"12.5G", 0},
	}
	for _, c := range cases {
		if got := ParseMemoryMB(c.in); got != c.want {
			t.Errorf("ParseMemoryMB(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
