// Json transport for connect procedures
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Http caller holding the session token
type Client struct {
	Base  string
	HTTP  *http.Client
	Token string
}

// Client with a bounded per request timeout
func NewClient(base string) *Client {
	return &Client{
		Base: strings.TrimRight(base, "/"),
		HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

// Outcome of one procedure call
type Result struct {
	Status int
	Body   any
	Raw    []byte
	Err    error
}

// Whether the call was accepted
func (r Result) OK() bool {
	return r.Err == nil && r.Status >= 200 && r.Status < 300
}

// Short error text for reports
func (r Result) Error() string {
	if r.Err != nil {
		return r.Err.Error()
	}
	if r.OK() {
		return ""
	}
	msg := strings.TrimSpace(string(r.Raw))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("%d %s", r.Status, msg)
}

// Posts one json body and decodes whatever comes back
func (c *Client) Call(ctx context.Context, op *Operation, body any) Result {
	if body == nil {
		body = map[string]any{}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Result{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+op.Path, bytes.NewReader(data))
	if err != nil {
		return Result{Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	res := Result{Status: resp.StatusCode, Raw: raw}
	if len(bytes.TrimSpace(raw)) > 0 {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			res.Body = decoded
		}
	}
	return res
}
