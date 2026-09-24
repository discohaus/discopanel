package utils

import (
	"regexp"
)

var minecraftColorRegex = regexp.MustCompile(`(?i)[§&][0-9a-fk-or]`)

// Removes all Minecraft color codes and formatting from a string
func StripMinecraftColors(input string) string {
	return minecraftColorRegex.ReplaceAllString(input, "")
}
