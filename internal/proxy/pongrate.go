package proxy

import (
	"net"
	"sync"
	"time"
)

const (
	// Pongs one relay sends per second across all clients
	pongGlobalRate  = 50
	pongGlobalBurst = 50
	// Pongs per client network, a /24 or /64
	pongNetRate  = 5
	pongNetBurst = 10
	// Client networks tracked before old ones are evicted
	pongNetMax = 4096
)

// Refilling allowance for one key
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// Spends one token, refilling at rate up to burst
func (b *tokenBucket) take(now time.Time, rate, burst float64) bool {
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// True once the bucket would have refilled completely
func (b *tokenBucket) full(now time.Time, rate, burst float64) bool {
	return b.tokens+now.Sub(b.last).Seconds()*rate >= burst
}

// Caps synthetic pongs so a sleeping relay can't be used as a reflector
type pongLimiter struct {
	mu     sync.Mutex
	now    func() time.Time
	global tokenBucket
	nets   map[string]*tokenBucket
}

func newPongLimiter() *pongLimiter {
	now := time.Now
	return &pongLimiter{
		now:    now,
		global: tokenBucket{tokens: pongGlobalBurst, last: now()},
		nets:   make(map[string]*tokenBucket),
	}
}

// Groups addresses the way spoofers rotate them
func pongNetKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// Reports whether a pong may go to ip right now
func (l *pongLimiter) allow(ip net.IP) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := pongNetKey(ip)
	b := l.nets[key]
	if b == nil {
		if len(l.nets) >= pongNetMax {
			l.evictLocked(now)
		}
		b = &tokenBucket{tokens: pongNetBurst, last: now}
		l.nets[key] = b
	}
	// Per network first so one flood can't drain everyone's allowance
	if !b.take(now, pongNetRate, pongNetBurst) {
		return false
	}
	return l.global.take(now, pongGlobalRate, pongGlobalBurst)
}

// Drops refilled networks, then arbitrary ones until there is room
func (l *pongLimiter) evictLocked(now time.Time) {
	for key, b := range l.nets {
		if b.full(now, pongNetRate, pongNetBurst) {
			delete(l.nets, key)
		}
	}
	for key := range l.nets {
		if len(l.nets) < pongNetMax {
			return
		}
		delete(l.nets, key)
	}
}
