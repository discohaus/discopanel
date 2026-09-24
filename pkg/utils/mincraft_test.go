package utils

import (
	"testing"
)

func TestStripMinecraftColors(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Standard color codes",
			input:    "§aHello §bWorld",
			expected: "Hello World",
		},
		{
			name:     "Consecutive color codes",
			input:    "§7§6/about: §fGets the version",
			expected: "/about: Gets the version",
		},
		{
			name:     "Formatting codes (Bold, Italic, Reset)",
			input:    "§lBold §oItalic §rNormal",
			expected: "Bold Italic Normal",
		},
		{
			name:     "Help header from Paper/Spigot",
			input:    "§e--------- §fHelp: §rPaper (1/3) §e---------------------------",
			expected: "--------- Help: Paper (1/3) ---------------------------",
		},
		{
			name:     "No color codes present",
			input:    "A plain text without codes",
			expected: "A plain text without codes",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "Isolated § symbol at the end (Edge Case)",
			input:    "Text with §",
			expected: "Text with §",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMinecraftColors(tt.input)
			if got != tt.expected {
				t.Errorf("StripMinecraftColors(%q) = %q; expected %q", tt.input, got, tt.expected)
			}
		})
	}
}
