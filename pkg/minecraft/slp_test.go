package minecraft

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDescription(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{"empty", ``, ""},
		{"plain string", `"A Minecraft Server"`, "A Minecraft Server"},
		{"object text", `{"text":"Hello"}`, "Hello"},
		{"object with extra objects", `{"text":"A ","extra":[{"text":"Minecraft"},{"text":" Server"}]}`, "A Minecraft Server"},
		{"bukkit string extra", `{"text":"","extra":["A Minecraft Server"]}`, "A Minecraft Server"},
		{"mixed extra", `{"text":"","extra":[{"text":"A ","color":"gold"},"Minecraft ",{"text":"Server","bold":true}]}`, "A Minecraft Server"},
		{"nested extra", `{"text":"","extra":[{"text":"A ","extra":[{"text":"Minecraft ","extra":["Server"]}]}]}`, "A Minecraft Server"},
		{"top level array", `["A ",{"text":"Minecraft"}," Server"]`, "A Minecraft Server"},
		{"no text field", `{"extra":[{"text":"Hi"}]}`, "Hi"},
		{"not a component", `12`, "12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDescription(json.RawMessage(tt.desc)); got != tt.want {
				t.Fatalf("parseDescription(%s) = %q, want %q", tt.desc, got, tt.want)
			}
		})
	}
}

func TestParseDescriptionDeepNesting(t *testing.T) {
	desc := strings.Repeat(`{"extra":[`, 100) + `"deep"` + strings.Repeat(`]}`, 100)
	if got := parseDescription(json.RawMessage(desc)); strings.Contains(got, "deep") {
		t.Fatalf("text below the depth limit should be dropped, got %d bytes", len(got))
	}
}
