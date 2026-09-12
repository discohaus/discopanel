package command

import (
	"sync"

	"github.com/discohaus/discopanel/pkg/mcconsole"
)

// Holds one completion engine per server
type EngineCache struct {
	mu      sync.RWMutex
	engines map[string]mcconsole.CompletionEngine
}

func NewEngineCache() *EngineCache {
	return &EngineCache{
		engines: make(map[string]mcconsole.CompletionEngine),
	}
}

func (c *EngineCache) GetEngine(serverID string) (mcconsole.CompletionEngine, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	engine, ok := c.engines[serverID]
	return engine, ok
}

func (c *EngineCache) SetEngine(serverID string, engine mcconsole.CompletionEngine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.engines[serverID] = engine
}

func (c *EngineCache) RemoveEngine(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.engines, serverID)
}
