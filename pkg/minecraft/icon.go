package minecraft

import (
	_ "embed"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"time"
)

//go:embed assets/avatar-64.png
var avatarPNG []byte

// Avatar art rendered once into a data uri
var defaultFavicon = sync.OnceValue(func() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(avatarPNG)
})

// Discopanel avatar icon for servers without one
func DefaultFavicon() string {
	return defaultFavicon()
}

// Caches encoded server icons keyed by file identity
type FaviconCache struct {
	mu      sync.Mutex
	entries map[string]faviconEntry
}

type faviconEntry struct {
	modTime time.Time
	size    int64
	dataURI string
}

// Encodes server-icon.png as a data uri, cached by identity
func (c *FaviconCache) Get(key, dataPath string) string {
	if dataPath == "" {
		return ""
	}
	iconPath := filepath.Join(dataPath, "server-icon.png")
	info, err := os.Stat(iconPath)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok && e.modTime.Equal(info.ModTime()) && e.size == info.Size() {
		return e.dataURI
	}
	data, err := os.ReadFile(iconPath)
	if err != nil {
		return ""
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if c.entries == nil {
		c.entries = make(map[string]faviconEntry)
	}
	c.entries[key] = faviconEntry{modTime: info.ModTime(), size: info.Size(), dataURI: uri}
	return uri
}
