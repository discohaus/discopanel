package commandcompletion

import "github.com/discohaus/discopanel/internal/command-completion/engine"

type EngineCache struct {
	engines map[string]engine.CompletionEngine
}

func NewEngineCache() *EngineCache {
	return &EngineCache{
		engines: make(map[string]engine.CompletionEngine),
	}
}

func (c *EngineCache) GetEngine(serverID string) (engine.CompletionEngine, bool) {
	engine, ok := c.engines[serverID]
	return engine, ok
}

func (c *EngineCache) SetEngine(serverID string, engine engine.CompletionEngine) {
	c.engines[serverID] = engine
}

func (c *EngineCache) RemoveEngine(serverID string) {
	delete(c.engines, serverID)
}
