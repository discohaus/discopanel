package indexers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Wraps http.Client with common indexer request logic
type HTTPClient struct {
	client       *http.Client
	userAgent    string
	indexer      string
	extraHeaders map[string]string
	state        *sharedState
}

// Creates a shared HTTP client for an indexer
func NewHTTPClient(indexer string, userAgent string, extraHeaders map[string]string) *HTTPClient {
	var cred strings.Builder
	for _, k := range slices.Sorted(maps.Keys(extraHeaders)) {
		cred.WriteString(k)
		cred.WriteByte('=')
		cred.WriteString(extraHeaders[k])
		cred.WriteByte(';')
	}
	return &HTTPClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		userAgent:    userAgent,
		indexer:      indexer,
		extraHeaders: extraHeaders,
		state:        stateFor(indexer, cred.String()),
	}
}

// Does GET request and returns the raw body
func (h *HTTPClient) DoBytes(ctx context.Context, url string) ([]byte, error) {
	return h.sharedGet(ctx, url)
}

// Does GET request and JSON-decodes into dest
func (h *HTTPClient) DoJSON(ctx context.Context, url string, dest any) error {
	data, err := h.sharedGet(ctx, url)
	if err != nil {
		return err
	}
	return h.decode(url, data, dest)
}

// Posts JSON body and decodes response into dest
func (h *HTTPClient) PostJSON(ctx context.Context, url string, body any, dest any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &IndexerError{Kind: ErrNetwork, Indexer: h.indexer, URL: url, Err: err}
	}
	data, err := h.fetch(ctx, http.MethodPost, url, payload)
	if err != nil {
		return err
	}
	return h.decode(url, data, dest)
}

// Collapses identical concurrent GETs into one upstream request
func (h *HTTPClient) sharedGet(ctx context.Context, url string) ([]byte, error) {
	ch := h.state.flights.DoChan(url, func() (any, error) {
		return h.fetch(ctx, http.MethodGet, url, nil)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			// Retry alone when another callers cancellation poisoned the flight
			if ctx.Err() == nil && (errors.Is(res.Err, context.Canceled) || errors.Is(res.Err, context.DeadlineExceeded)) {
				return h.fetch(ctx, http.MethodGet, url, nil)
			}
			return nil, res.Err
		}
		return res.Val.([]byte), nil
	}
}

// Runs paced request, retrying on 429, 5xx, network errors
func (h *HTTPClient) fetch(ctx context.Context, method, url string, payload []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := h.state.waitCooldown(ctx); err != nil {
			return nil, err
		}
		if err := h.state.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		data, retry, err := h.once(ctx, method, url, payload, attempt)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			return nil, lastErr
		}
		// 429 waits via the shared cooldown instead
		var ie *IndexerError
		if !(errors.As(err, &ie) && ie.Kind == ErrRateLimit) {
			if err := sleepCtx(ctx, backoffDelay(attempt)); err != nil {
				return nil, lastErr
			}
		}
	}
	return nil, lastErr
}

// Performs a single attempt, reporting whether a retry makes sense
func (h *HTTPClient) once(ctx context.Context, method, url string, payload []byte, attempt int) ([]byte, bool, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, false, &IndexerError{Kind: ErrNetwork, Indexer: h.indexer, URL: url, Err: err}
	}

	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.userAgent != "" {
		req.Header.Set("User-Agent", h.userAgent)
	}
	for k, v := range h.extraHeaders {
		req.Header.Set(k, v)
	}

	var cachedBody []byte
	var haveCached bool
	if method == http.MethodGet {
		var etag string
		if etag, cachedBody, haveCached = h.state.cachedETag(url); haveCached {
			req.Header.Set("If-None-Match", etag)
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, true, NewNetworkError(h.indexer, url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified && haveCached {
		return cachedBody, false, nil
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			return nil, true, NewNetworkError(h.indexer, url, err)
		}
		if method == http.MethodGet {
			h.state.storeETag(url, resp.Header.Get("ETag"), data)
		}
		return data, false, nil
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	apiErr := NewAPIError(h.indexer, resp.StatusCode, url, string(bodyBytes))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		d := retryAfter(resp.Header)
		if d <= 0 {
			d = max(backoffDelay(attempt), minRateCooldown)
		}
		h.state.startCooldown(d)
		return nil, true, apiErr
	case resp.StatusCode >= 500:
		return nil, true, apiErr
	}
	return nil, false, apiErr
}

// Unmarshals a response body into dest with error classification
func (h *HTTPClient) decode(url string, data []byte, dest any) error {
	if err := json.Unmarshal(data, dest); err != nil {
		return NewDecodeError(h.indexer, url, err)
	}
	return nil
}
