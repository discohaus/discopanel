package hub

import (
	"encoding/json"
	"net/url"
	"strings"
)

type IndexError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Origin  string `json:"origin"`
}

// Parses an index error body
func ParseIndexError(reqURL *url.URL, body []byte) *IndexError {
	st := current.Load()
	if reqURL == nil || !strings.EqualFold(reqURL.Host, st.index.Host) {
		return nil
	}
	var e IndexError
	if json.Unmarshal(body, &e) != nil || e.Origin == "" {
		return nil
	}
	origin, err := url.Parse(e.Origin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" {
		return nil
	}
	if st.isHubHost(origin) {
		return nil
	}
	return &e
}
