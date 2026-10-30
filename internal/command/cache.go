package command

import (
	"sync"

	"github.com/discohaus/discopanel/pkg/mcconsole"
)

// Holds one completion engine per server
type EngineCache struct {
	mu      sync.Mutex
	engines map[string]mcconsole.CompletionEngine
	locks   map[string]*sync.Mutex
}

func NewEngineCache() *EngineCache {
	return &EngineCache{
		engines: make(map[string]mcconsole.CompletionEngine),
		locks:   make(map[string]*sync.Mutex),
	}
}

// Serializes engine use per server, engines are not goroutine safe
func (c *EngineCache) Lock(serverID string) func() {
	c.mu.Lock()
	lock, ok := c.locks[serverID]
	if !ok {
		lock = &sync.Mutex{}
		c.locks[serverID] = lock
	}
	c.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (c *EngineCache) GetEngine(serverID string) (mcconsole.CompletionEngine, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
