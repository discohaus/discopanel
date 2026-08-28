package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/discohaus/discopanel/internal/alias"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Builds and delivers webhooks sync, schedulers own concurrency

// A single delivery attempt outcome
type Result struct {
	Success      bool
	ResponseCode int
	ResponseBody string
	ErrorMessage string
	DurationMs   int64
	Attempts     int
}

// Canonical event data fed into payload templates
type Payload struct {
	Msg  *v1.WebhookPayload
	vars map[string]any
}

// Canonical event name, manual when no event fired
func eventName(t v1.TriggeredEventType) string {
	if t == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED {
		return "manual"
	}
	return protometa.Name(t)
}

// Readable event title for templates
func eventTitle(t v1.TriggeredEventType) string {
	if t == v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED {
		return "Manual"
	}
	return protometa.Label(t)
}

// Renders, signs, POSTs, and retries one delivery
func Deliver(ctx context.Context, cfg *v1.WebhookTaskConfig, payload *Payload) Result {
	start := time.Now()
	maxAttempts := int(cfg.MaxRetries)
	if maxAttempts < 1 {
		maxAttempts = 1
	} else {
		maxAttempts++ // initial attempt + retries
	}
	retryBaseMs := int(cfg.RetryDelayMs)
	if retryBaseMs <= 0 {
		retryBaseMs = 1000
	}

	var last Result
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		last = deliverOnce(ctx, cfg, payload)
		last.Attempts = attempt
		if last.Success {
			break
		}
		if attempt == maxAttempts {
			break
		}
		// Exponential backoff doubles the base per attempt
		delay := time.Duration(retryBaseMs) * time.Millisecond * time.Duration(1<<(attempt-1))
		select {
		case <-ctx.Done():
			last.ErrorMessage = ctx.Err().Error()
			last.DurationMs = time.Since(start).Milliseconds()
			return last
		case <-time.After(delay):
		}
	}
	last.DurationMs = time.Since(start).Milliseconds()
	return last
}

func deliverOnce(ctx context.Context, cfg *v1.WebhookTaskConfig, payload *Payload) Result {
	start := time.Now()
	result := Result{}

	body, err := renderBody(cfg, payload)
	if err != nil {
		result.ErrorMessage = err.Error()
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", cfg.Url, bytes.NewReader(body))
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("request error: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscoPanel-Webhook/1.0")
	req.Header.Set("X-DiscoPanel-Event", eventName(payload.Msg.Event))
	req.Header.Set("X-DiscoPanel-Delivery", uuid.New().String())

	if cfg.Secret != "" {
		req.Header.Set("X-DiscoPanel-Signature", "sha256="+sign(body, cfg.Secret))
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.ErrorMessage = err.Error()
		return result
	}
	defer resp.Body.Close()

	result.ResponseCode = resp.StatusCode
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result.ResponseBody = string(bodyBytes)
	result.Success = resp.StatusCode >= 200 && resp.StatusCode < 300
	if !result.Success {
		result.ErrorMessage = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, result.ResponseBody)
	}
	return result
}

func renderBody(cfg *v1.WebhookTaskConfig, payload *Payload) ([]byte, error) {
	if cfg.PayloadTemplate != "" {
		return renderTemplate(cfg.PayloadTemplate, payload)
	}
	return protojson.Marshal(payload.Msg)
}

func sign(body []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Flattens alias paths into template vars, one shared vocabulary
func templateVars(event v1.TriggeredEventType, timestamp time.Time, server *v1.Server, data map[string]string) map[string]any {
	name := eventName(event)
	vars := map[string]any{
		"event":      name,
		"is_" + name: true,
		"timestamp":  timestamp.Format(time.RFC3339),
		"title":      eventTitle(event),
		"player":     "",
	}
	rctx := alias.NewContext()
	rctx.Server = server
	for k, v := range alias.GetResolvedAliases(rctx) {
		path := strings.TrimSuffix(strings.TrimPrefix(k, "{{"), "}}")
		if strings.HasPrefix(path, "server.config.") {
			continue
		}
		if strings.HasPrefix(path, "server.") || strings.HasPrefix(path, "host.") {
			vars[strings.ReplaceAll(path, ".", "_")] = v
		}
	}
	for k, v := range data {
		vars[k] = v
	}
	return vars
}

func renderTemplate(tmplStr string, p *Payload) ([]byte, error) {
	tmpl, err := template.New("payload").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p.vars); err != nil {
		return nil, fmt.Errorf("template execution failed: %w", err)
	}
	out := buf.Bytes()
	if !json.Valid(out) {
		return nil, fmt.Errorf("template produced invalid JSON")
	}
	return out, nil
}

// Verifies a template renders valid JSON from samples
func ValidateTemplate(tmplStr string) error {
	if strings.TrimSpace(tmplStr) == "" {
		return nil
	}
	sample := BuildPayload(v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START, &v1.Server{
		Id: "test-id", Name: "Test Server", Status: v1.ServerStatus_SERVER_STATUS_RUNNING,
		McVersion: "1.21", ModLoader: v1.ModLoader_MOD_LOADER_VANILLA,
		MaxPlayers: 20, Port: 25565,
	}, nil)
	_, err := renderTemplate(tmplStr, sample)
	return err
}

// Assembles a Payload for the given event and server
func BuildPayload(event v1.TriggeredEventType, server *v1.Server, data map[string]string) *Payload {
	redacted := server.Redact()
	now := time.Now().UTC()
	p := &Payload{
		Msg: &v1.WebhookPayload{
			Event:     event,
			Timestamp: timestamppb.New(now),
			Server:    redacted,
			Data:      data,
		},
	}
	p.vars = templateVars(event, now, redacted, data)
	return p
}
