package proxy

import (
	"os"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto/hub"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Parsed hub grid cached by file identity
type hubGridCache struct {
	mu      sync.Mutex
	path    string
	modTime time.Time
	size    int64
	grid    *hub.Grid
}

// Points the cache at one lobby data dir
func (c *hubGridCache) SetDataPath(dataPath string) {
	path := ""
	if dataPath != "" {
		path = runtimespec.HubGridPath(dataPath)
	}
	c.mu.Lock()
	if c.path != path {
		c.path = path
		c.grid = nil
		c.modTime = time.Time{}
		c.size = 0
	}
	c.mu.Unlock()
}

// Freshest parsed grid, nil without one
// Stale grids survive short read hiccups
func (c *hubGridCache) Grid(log *logger.Logger) *hub.Grid {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	info, err := os.Stat(c.path)
	if err != nil {
		c.grid = nil
		return nil
	}
	if c.grid != nil && info.ModTime().Equal(c.modTime) && info.Size() == c.size {
		return c.grid
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return c.grid
	}
	grid, err := hub.Parse(data)
	if err != nil {
		if log != nil {
			log.Error("Hub grid unreadable at %s: %v", c.path, err)
		}
		return c.grid
	}
	c.grid = grid
	c.modTime = info.ModTime()
	c.size = info.Size()
	return grid
}
