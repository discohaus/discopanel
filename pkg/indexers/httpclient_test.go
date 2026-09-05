package indexers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// Shrinks retry pacing so tests run fast
func fastRetries(t *testing.T) {
	t.Helper()
	origBase, origMin, origMax := baseBackoff, minRateCooldown, maxBackoff
	baseBackoff = time.Millisecond
	minRateCooldown = 5 * time.Millisecond
	maxBackoff = 20 * time.Millisecond
	t.Cleanup(func() {
		baseBackoff, minRateCooldown, maxBackoff = origBase, origMin, origMax
	})
}

// Builds a client with a unique shared state per test
func testClient(t *testing.T, headers map[string]string) *HTTPClient {
	t.Helper()
	c := NewHTTPClient("test-"+t.Name(), "discopanel-test", headers)
	c.state.limiter = rate.NewLimiter(rate.Inf, 1)
	return c
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	var dest struct {
		OK bool `json:"ok"`
	}
	if err := c.DoJSON(t.Context(), srv.URL, &dest); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if !dest.OK || calls.Load() != 3 {
		t.Fatalf("want 3 calls and ok body, got calls=%d ok=%v", calls.Load(), dest.OK)
	}
}

func TestRateLimitExhaustsAttempts(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	var dest any
	err := c.DoJSON(t.Context(), srv.URL, &dest)
	var ie *IndexerError
	if !errors.As(err, &ie) || ie.Kind != ErrRateLimit {
		t.Fatalf("want rate limit error, got %v", err)
	}
	if calls.Load() != int32(maxAttempts) {
		t.Fatalf("want %d attempts, got %d", maxAttempts, calls.Load())
	}
}

func TestRetriesServerErrors(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	var dest struct {
		OK bool `json:"ok"`
	}
	if err := c.DoJSON(t.Context(), srv.URL, &dest); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("want 2 calls, got %d", calls.Load())
	}
}

func TestNoRetryOnClientErrors(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	var dest any
	err := c.DoJSON(t.Context(), srv.URL, &dest)
	var ie *IndexerError
	if !errors.As(err, &ie) || ie.Kind != ErrNotFound {
		t.Fatalf("want not found error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("want 1 call, got %d", calls.Load())
	}
}

func TestETagRevalidation(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `{"n":42}`)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	for range 2 {
		var dest struct {
			N int `json:"n"`
		}
		if err := c.DoJSON(t.Context(), srv.URL, &dest); err != nil {
			t.Fatalf("DoJSON: %v", err)
		}
		if dest.N != 42 {
			t.Fatalf("want cached body 42, got %d", dest.N)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("want 2 calls, got %d", calls.Load())
	}
}

func TestSingleflightCollapsesConcurrentGets(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var dest any
			errs[i] = c.DoJSON(t.Context(), srv.URL, &dest)
		}()
	}
	// Let all goroutines pile onto the flight
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("want 1 upstream call, got %d", calls.Load())
	}
}

func TestSingleflightFollowerCancels(t *testing.T) {
	fastRetries(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := testClient(t, nil)
	winnerErr := make(chan error, 1)
	go func() {
		var dest any
		winnerErr <- c.DoJSON(context.Background(), srv.URL, &dest)
	}()
	// Let the winner claim the flight
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var dest any
	start := time.Now()
	err := c.DoJSON(ctx, srv.URL, &dest)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("follower stayed blocked past its deadline")
	}

	close(release)
	if err := <-winnerErr; err != nil {
		t.Fatalf("winner: %v", err)
	}
}

func TestETagCacheEvictsByBytes(t *testing.T) {
	orig := maxEtagBytes
	maxEtagBytes = 60
	t.Cleanup(func() { maxEtagBytes = orig })

	s := stateFor("test-"+t.Name(), "")
	s.storeETag("u1", `"a"`, []byte("0123456789"))
	s.storeETag("u2", `"b"`, []byte("0123456789"))
	s.storeETag("u3", `"c"`, []byte("0123456789"))
	if _, _, ok := s.cachedETag("u1"); !ok {
		t.Fatal("u1 should be cached")
	}
	s.storeETag("u4", `"d"`, []byte("0123456789abcdefghij"))
	if _, _, ok := s.cachedETag("u2"); ok {
		t.Fatal("least recently used u2 should be evicted")
	}
	if _, _, ok := s.cachedETag("u1"); !ok {
		t.Fatal("recently used u1 should survive eviction")
	}
	if s.etagBytes > maxEtagBytes {
		t.Fatalf("cache over budget: %d > %d", s.etagBytes, maxEtagBytes)
	}
}

func TestStateEvictsRotatedCredentials(t *testing.T) {
	indexer := "test-" + t.Name()
	first := stateFor(indexer, "cred-0")
	for i := 1; i <= maxStates; i++ {
		stateFor(indexer, fmt.Sprintf("cred-%d", i))
	}
	if stateFor(indexer, "cred-0") == first {
		t.Fatal("rotated credential state should have been evicted")
	}
}

func TestCooldownSharedAcrossClients(t *testing.T) {
	fastRetries(t)
	c1 := testClient(t, nil)
	c2 := NewHTTPClient("test-"+t.Name(), "discopanel-test", nil)
	if c1.state != c2.state {
		t.Fatal("same indexer and credential must share state")
	}
	c3 := NewHTTPClient("test-"+t.Name(), "discopanel-test", map[string]string{"x-api-key": "other"})
	if c1.state == c3.state {
		t.Fatal("different credentials must not share state")
	}
}

func TestRetryAfterParsing(t *testing.T) {
	h := http.Header{}
	if d := retryAfter(h); d != 0 {
		t.Fatalf("empty header want 0, got %v", d)
	}
	h.Set("Retry-After", "7")
	if d := retryAfter(h); d != 7*time.Second {
		t.Fatalf("seconds form want 7s, got %v", d)
	}
	h.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if d := retryAfter(h); d < 25*time.Second || d > 31*time.Second {
		t.Fatalf("date form want about 30s, got %v", d)
	}
}
