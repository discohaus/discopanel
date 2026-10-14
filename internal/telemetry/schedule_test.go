package telemetry

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
)

func TestNextDelay(t *testing.T) {
	cases := []struct {
		resp *hub.HeartbeatResponse
		want time.Duration
	}{
		{nil, time.Hour},
		{&hub.HeartbeatResponse{}, time.Hour},
		{&hub.HeartbeatResponse{NextInSeconds: 3600}, time.Hour},
		{&hub.HeartbeatResponse{NextInSeconds: 1800}, 30 * time.Minute},
		{&hub.HeartbeatResponse{NextInSeconds: 5}, MinInterval},
		{&hub.HeartbeatResponse{NextInSeconds: 1_000_000}, MaxInterval},
		{&hub.HeartbeatResponse{NextInSeconds: -1}, time.Hour},
	}
	for _, c := range cases {
		if got := NextDelay(c.resp); got != c.want {
			t.Errorf("NextDelay(%+v) = %s, want %s", c.resp, got, c.want)
		}
	}
}

func TestBackoffDoublesToTheCap(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	if got := Backoff(0); got != time.Minute {
		t.Errorf("Backoff(0) = %s, want 1m", got)
	}
	if got := Backoff(60); got != time.Hour {
		t.Errorf("Backoff(60) = %s, want the cap", got)
	}
}

func TestRetryDelay(t *testing.T) {
	network := errors.New("dial tcp: connection refused")
	if got := RetryDelay(3, network); got != 4*time.Minute {
		t.Errorf("network failure 3 = %s, want 4m", got)
	}
	paused := &hub.Error{Status: http.StatusServiceUnavailable, Code: "unavailable"}
	if got := RetryDelay(1, paused); got != time.Hour {
		t.Errorf("paused = %s, want 1h", got)
	}
	limited := &hub.Error{Status: http.StatusTooManyRequests, Code: "resource_exhausted", RetryAfter: 10 * time.Minute}
	if got := RetryDelay(1, limited); got != 10*time.Minute {
		t.Errorf("rate limited with retry-after = %s, want 10m", got)
	}
	if got := RetryDelay(6, limited); got != 32*time.Minute {
		t.Errorf("rate limited under a longer backoff = %s, want 32m", got)
	}
	limitedLong := &hub.Error{Status: http.StatusTooManyRequests, RetryAfter: 5 * time.Hour}
	if got := RetryDelay(1, limitedLong); got != time.Hour {
		t.Errorf("rate limited past the cap = %s, want 1h", got)
	}
	limitedBare := &hub.Error{Status: http.StatusTooManyRequests}
	if got := RetryDelay(2, limitedBare); got != 2*time.Minute {
		t.Errorf("rate limited without retry-after = %s, want 2m", got)
	}
	bad := &hub.Error{Status: http.StatusBadRequest, Code: "invalid_argument"}
	if got := RetryDelay(1, bad); got != time.Minute {
		t.Errorf("rejected payload = %s, want 1m", got)
	}
}
