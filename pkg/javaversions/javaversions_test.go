package javaversions

import "testing"

func TestTagMajor(t *testing.T) {
	cases := map[string]int{"java21": 21, "java25-graal": 25, "java8": 8, "java99": 0, "": 0, "latest": 0}
	for tag, want := range cases {
		if got := TagMajor(tag); got != want {
			t.Errorf("TagMajor(%q) = %d, want %d", tag, got, want)
		}
	}
}
