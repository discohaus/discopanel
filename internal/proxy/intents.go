package proxy

import (
	"strings"
	"sync"
	"time"
)

// How long a reroute claim outlives its kick
const defaultIntentTTL = 2 * time.Minute

// One pending reroute for a kicked player
type transferIntent struct {
	serverID string
	expires  time.Time
}

// Pending reroutes keyed by lowercase player name
type IntentTable struct {
	mu sync.Mutex
	m  map[string]transferIntent
}

func NewIntentTable() *IntentTable {
	return &IntentTable{m: make(map[string]transferIntent)}
}

// Remembers where a player goes on reconnect
func (t *IntentTable) Put(player, serverID string, ttl time.Duration) {
	if player == "" || serverID == "" {
		return
	}
	if ttl <= 0 {
		ttl = defaultIntentTTL
	}
	t.mu.Lock()
	t.sweepLocked()
	t.m[strings.ToLower(player)] = transferIntent{
		serverID: serverID,
		expires:  time.Now().Add(ttl),
	}
	t.mu.Unlock()
}

// Burns and returns a pending reroute once
func (t *IntentTable) Claim(player string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()
	key := strings.ToLower(player)
	intent, ok := t.m[key]
	if !ok {
		return "", false
	}
	delete(t.m, key)
	return intent.serverID, true
}

// Drops expired entries, callers hold the lock
func (t *IntentTable) sweepLocked() {
	now := time.Now()
	for key, intent := range t.m {
		if now.After(intent.expires) {
			delete(t.m, key)
		}
	}
}
