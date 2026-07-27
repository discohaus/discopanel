// Lets a sidecar module ask the user for input
package moduleprompt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Widget kinds a prompt can request
const (
	KindText     = "text"
	KindPassword = "password"
	KindSelect   = "select"
)

// One choice for a select prompt
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// One piece of input the module waits on
type Prompt struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Message     string    `json:"message"`
	Kind        string    `json:"kind"`
	Options     []Option  `json:"options,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Returned when a newer prompt replaces the pending one
var ErrSuperseded = errors.New("prompt superseded")

// Holds one pending prompt served over HTTP
type Broker struct {
	mu      sync.Mutex
	pending *Prompt
	answer  chan string
}

// Builds an empty broker
func New() *Broker {
	return &Broker{}
}

// Wires the prompt endpoint onto a mux
func (b *Broker) Register(mux *http.ServeMux) {
	mux.HandleFunc("/prompt", b.handle)
}

// Publishes a prompt and blocks for the answer
func (b *Broker) Ask(ctx context.Context, p Prompt) (string, error) {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	answer := make(chan string, 1)

	b.mu.Lock()
	// A new prompt cancels whoever was waiting before
	if b.answer != nil {
		close(b.answer)
	}
	b.pending = &p
	b.answer = answer
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		b.clear(answer)
		return "", ctx.Err()
	case v, ok := <-answer:
		if !ok {
			return "", ErrSuperseded
		}
		return v, nil
	}
}

// Returns the current prompt or nil
func (b *Broker) Pending() *Prompt {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pending
}

// Drops the pending prompt when still current
func (b *Broker) clear(answer chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.answer == answer {
		b.pending = nil
		b.answer = nil
	}
}

func (b *Broker) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.servePending(w)
	case http.MethodPost:
		b.serveAnswer(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Returns the pending prompt or 204 when idle
func (b *Broker) servePending(w http.ResponseWriter) {
	b.mu.Lock()
	p := b.pending
	b.mu.Unlock()

	if p == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// Delivers a posted answer to the waiting caller
func (b *Broker) serveAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil || b.answer == nil {
		http.Error(w, "no prompt pending", http.StatusConflict)
		return
	}
	if body.ID != b.pending.ID {
		http.Error(w, "prompt id mismatch", http.StatusConflict)
		return
	}

	b.answer <- body.Value
	b.pending = nil
	b.answer = nil
	w.WriteHeader(http.StatusOK)
}
