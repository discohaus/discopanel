package utils

import (
	"regexp"
)

var minecraftColorRegex = regexp.MustCompile(`(?i)[§&][0-9a-fk-or]`)

// StripMinecraftColors entfernt alle Minecraft-Farbcodes und Formatierungen aus einem String.
func StripMinecraftColors(input string) string {
	return minecraftColorRegex.ReplaceAllString(input, "")
}
