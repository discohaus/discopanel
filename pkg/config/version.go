package config

import (
	"debug/buildinfo"
	"os"
	"path/filepath"
	"strings"
)

// Version string shown when nothing stamped the build
const UnknownVersion = "unknown"

// Panel version from env, stamp, home file, then vcs revision
func ResolvedVersion() string {
	if v := AppVersion(); v != "" {
		return v
	}

	// Version file stored in home wins over build info
	if home, err := os.UserHomeDir(); err == nil {
		versionFile := filepath.Join(home, ".discopanel")
		if data, err := os.ReadFile(versionFile); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				return v
			}
		}
	}

	info, err := buildinfo.ReadFile(os.Args[0])
	if err != nil {
		return UnknownVersion
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return UnknownVersion
}
