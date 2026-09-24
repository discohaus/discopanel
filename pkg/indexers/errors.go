package indexers

import (
	"errors"
	"fmt"
	"net"
)

// Indexer error kinds
type ErrorKind int

const (
	ErrAuth      ErrorKind = iota // 401/403 or missing API key
	ErrRateLimit                  // 429
	ErrNotFound                   // 404
	ErrNetwork                    // DNS failure, timeout, connection refused
	ErrAPI                        // Other non-2xx status codes
	ErrDecode                     // JSON decode failures
)

func (k ErrorKind) String() string {
	switch k {
	case ErrAuth:
		return "authentication error"
	case ErrRateLimit:
		return "rate limited"
	case ErrNotFound:
		return "not found"
	case ErrNetwork:
		return "network error"
	case ErrAPI:
		return "API error"
	case ErrDecode:
		return "decode error"
	default:
		return "unknown error"
	}
}

type IndexerError struct {
	Kind       ErrorKind
	Indexer    string
	StatusCode int
	URL        string
	Body       string
	Err        error
}

func (e *IndexerError) Error() string {
	base := fmt.Sprintf("%s: %s", e.Indexer, e.Kind)

	if e.StatusCode != 0 {
		base = fmt.Sprintf("%s (status %d)", base, e.StatusCode)
	}
	if e.URL != "" {
		base = fmt.Sprintf("%s url=%s", base, e.URL)
	}
	if e.Err != nil {
		base = fmt.Sprintf("%s: %v", base, e.Err)
	}
	if e.Body != "" {
		base = fmt.Sprintf("%s body=%s", base, e.Body)
	}
	return base
}

func (e *IndexerError) Unwrap() error {
	return e.Err
}

// Classifies status code into an error kind
func NewAPIError(indexer string, statusCode int, url string, body string) *IndexerError {
	kind := ErrAPI
	switch {
	case statusCode == 401 || statusCode == 403:
		kind = ErrAuth
	case statusCode == 404:
		kind = ErrNotFound
	case statusCode == 429:
		kind = ErrRateLimit
	}
	return &IndexerError{
		Kind:       kind,
		Indexer:    indexer,
		StatusCode: statusCode,
		URL:        url,
		Body:       body,
	}
}

// IndexerError for network-level failures like dns
func NewNetworkError(indexer string, url string, err error) *IndexerError {
	wrapped := err
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		wrapped = fmt.Errorf("DNS lookup failed for %s: %w", dnsErr.Name, err)
	}
	return &IndexerError{
		Kind:    ErrNetwork,
		Indexer: indexer,
		URL:     url,
		Err:     wrapped,
	}
}

// JSON decode failures
func NewDecodeError(indexer string, url string, err error) *IndexerError {
	return &IndexerError{
		Kind:    ErrDecode,
		Indexer: indexer,
		URL:     url,
		Err:     err,
	}
}

// Missing API key configuration
func NewAuthConfigError(indexer string, msg string) *IndexerError {
	return &IndexerError{
		Kind:    ErrAuth,
		Indexer: indexer,
		Err:     errors.New(msg),
	}
}
