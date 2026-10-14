package indexers

import (
	"container/list"
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// Fallback pacing for indexers declaring no rate
const (
	defaultPerSec = 5
	defaultBurst  = 5
)

// Retry tuning, vars so tests can shrink them
var (
	maxAttempts      = 4
	baseBackoff      = 500 * time.Millisecond
	maxBackoff       = 15 * time.Second
	minRateCooldown  = 2 * time.Second
	maxRetryAfter    = 2 * time.Minute
	maxResponseBytes = int64(64 << 20)
	maxCachedBody    = 2 << 20
	maxEtagBytes     = 16 << 20
	maxStates        = 8
)

// Pacing, dedupe, and validator state for one indexer credential
type sharedState struct {
	limiter *rate.Limiter
	flights singleflight.Group
	lruEl   *list.Element

	mu            sync.Mutex
	cooldownUntil time.Time

	etagMu    sync.Mutex
	etags     map[string]*list.Element
	etagLRU   *list.List
	etagBytes int
}

type etagEntry struct {
	url  string
	etag string
	body []byte
}

// Bytes an entry charges against the cache budget
func (e *etagEntry) size() int {
	return len(e.url) + len(e.etag) + len(e.body)
}

var (
	statesMu  sync.Mutex
	states    = map[string]*sharedState{}
	statesLRU = list.New()
)

// Returns shared state per credential, evicting stale credentials
func stateFor(indexer, credential string) *sharedState {
	statesMu.Lock()
	defer statesMu.Unlock()
	key := indexer + "\x00" + credential
	if s, ok := states[key]; ok {
		statesLRU.MoveToFront(s.lruEl)
		return s
	}
	perSec, burst := rate.Limit(defaultPerSec), defaultBurst
	if info, ok := LookupIndexer(indexer); ok && info.RequestsPerSec > 0 {
		perSec, burst = rate.Limit(info.RequestsPerSec), info.RequestBurst
	}
	s := &sharedState{
		limiter: rate.NewLimiter(perSec, burst),
		etags:   map[string]*list.Element{},
		etagLRU: list.New(),
	}
	s.lruEl = statesLRU.PushFront(key)
	states[key] = s
	for len(states) > maxStates {
		oldest := statesLRU.Back()
		delete(states, statesLRU.Remove(oldest).(string))
	}
	return s
}

// Blocks until any active rate limit cooldown passes
func (s *sharedState) waitCooldown(ctx context.Context) error {
	for {
		s.mu.Lock()
		wait := time.Until(s.cooldownUntil)
		s.mu.Unlock()
		if wait <= 0 {
			return nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Pauses every request on this credential after a 429
func (s *sharedState) startCooldown(d time.Duration) {
	if d <= 0 {
		return
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	until := time.Now().Add(d)
	s.mu.Lock()
	if until.After(s.cooldownUntil) {
		s.cooldownUntil = until
	}
	s.mu.Unlock()
}

// Returns remembered validator and body for url
func (s *sharedState) cachedETag(url string) (string, []byte, bool) {
	s.etagMu.Lock()
	defer s.etagMu.Unlock()
	el, ok := s.etags[url]
	if !ok {
		return "", nil, false
	}
	s.etagLRU.MoveToFront(el)
	e := el.Value.(*etagEntry)
	return e.etag, e.body, true
}

// Remembers validator and body for url within byte budget
func (s *sharedState) storeETag(url, etag string, body []byte) {
	if etag == "" || len(body) > maxCachedBody {
		return
	}
	s.etagMu.Lock()
	defer s.etagMu.Unlock()
	if el, ok := s.etags[url]; ok {
		e := el.Value.(*etagEntry)
		s.etagBytes -= e.size()
		e.etag, e.body = etag, body
		s.etagBytes += e.size()
		s.etagLRU.MoveToFront(el)
	} else {
		e := &etagEntry{url: url, etag: etag, body: body}
		s.etags[url] = s.etagLRU.PushFront(e)
		s.etagBytes += e.size()
	}
	for s.etagBytes > maxEtagBytes {
		oldest := s.etagLRU.Back()
		if oldest == nil {
			return
		}
		e := s.etagLRU.Remove(oldest).(*etagEntry)
		delete(s.etags, e.url)
		s.etagBytes -= e.size()
	}
}

// Parses Retry-After as seconds or an http date
func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// Jittered exponential delay for the given attempt
func backoffDelay(attempt int) time.Duration {
	d := baseBackoff << attempt
	if d > maxBackoff {
		d = maxBackoff
	}
	return d + rand.N(d/2+1)
}

// Sleeps for d unless ctx ends first
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
