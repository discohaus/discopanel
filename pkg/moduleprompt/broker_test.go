package moduleprompt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAskReturnsPostedAnswer(t *testing.T) {
	b := New()
	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	result := make(chan string, 1)
	go func() {
		v, err := b.Ask(context.Background(), Prompt{ID: "code", Title: "Code", Kind: KindText})
		if err != nil {
			t.Errorf("ask failed: %v", err)
		}
		result <- v
	}()

	waitPending(t, srv.URL)

	post(t, srv.URL, `{"id":"code","value":"FDYVD"}`, http.StatusOK)
	select {
	case v := <-result:
		if v != "FDYVD" {
			t.Fatalf("got %q", v)
		}
	case <-time.After(time.Second):
		t.Fatal("ask never returned")
	}
}

func TestPendingClearsAfterAnswer(t *testing.T) {
	b := New()
	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	go b.Ask(context.Background(), Prompt{ID: "code", Kind: KindText})
	waitPending(t, srv.URL)
	post(t, srv.URL, `{"id":"code","value":"x"}`, http.StatusOK)

	resp, err := http.Get(srv.URL + "/prompt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected no content, got %d", resp.StatusCode)
	}
}

func TestAnswerWrongIDRejected(t *testing.T) {
	b := New()
	mux := http.NewServeMux()
	b.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	go b.Ask(context.Background(), Prompt{ID: "code", Kind: KindText})
	waitPending(t, srv.URL)
	post(t, srv.URL, `{"id":"other","value":"x"}`, http.StatusConflict)
}

func TestSupersededPromptUnblocks(t *testing.T) {
	b := New()
	done := make(chan error, 1)
	go func() {
		_, err := b.Ask(context.Background(), Prompt{ID: "first", Kind: KindText})
		done <- err
	}()
	// Give the first Ask time to register
	time.Sleep(50 * time.Millisecond)
	go b.Ask(context.Background(), Prompt{ID: "second", Kind: KindText})

	select {
	case err := <-done:
		if err != ErrSuperseded {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first ask never unblocked")
	}
}

func TestAskCancelClearsPending(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		b.Ask(ctx, Prompt{ID: "code", Kind: KindText})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if b.Pending() != nil {
		t.Fatal("pending should be cleared after cancel")
	}
}

func waitPending(t *testing.T, base string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/prompt")
		if err == nil {
			body := struct{ ID string }{}
			json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && body.ID != "" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt never became pending")
}

func post(t *testing.T, base, body string, want int) {
	t.Helper()
	resp, err := http.Post(base+"/prompt", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("post status got %d want %d", resp.StatusCode, want)
	}
}
