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
			name:     "Standard Farbcodes",
			input:    "§aHallo §bWelt",
			expected: "Hallo Welt",
		},
		{
			name:     "Aufeinanderfolgende Farbcodes",
			input:    "§7§6/about: §fGets the version",
			expected: "/about: Gets the version",
		},
		{
			name:     "Formatierungscodes (Fett, Kursiv, Reset)",
			input:    "§lFett §oKursiv §rNormal",
			expected: "Fett Kursiv Normal",
		},
		{
			name:     "Help Header aus Paper/Spigot",
			input:    "§e--------- §fHelp: §rPaper (1/3) §e---------------------------",
			expected: "--------- Help: Paper (1/3) ---------------------------",
		},
		{
			name:     "Keine Farbcodes enthalten",
			input:    "Ein normaler Text ohne Codes",
			expected: "Ein normaler Text ohne Codes",
		},
		{
			name:     "Leerer String",
			input:    "",
			expected: "",
		},
		{
			name:     "Isoliertes § Symbol am Ende (Edge Case)",
			input:    "Text mit §",
			expected: "Text mit §",
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
