package telemetry

import (
	"time"

	"github.com/discohaus/discopanel/pkg/hub"
)

// Heartbeat cadence
const (
	// Wait after startup before the first heartbeat
	FirstDelay = time.Minute
	// Interval when the hub names none
	DefaultInterval = time.Hour
	// Bounds on the interval the hub names
	MinInterval = time.Minute
	MaxInterval = 24 * time.Hour
	// Retry schedule after a failure
	BackoffStart = time.Minute
	BackoffCap   = time.Hour
	// Wait after the hub reports telemetry paused
	PausedDelay = time.Hour
	// One heartbeat's budget, gathering facts included
	beatTimeout = 30 * time.Second
	// Failure warnings are logged at most this often
	warnEvery = 10 * time.Minute
)

// Delay until the next heartbeat after a success, from the hub's answer
func NextDelay(resp *hub.HeartbeatResponse) time.Duration {
	if resp == nil || resp.NextInSeconds <= 0 {
		return DefaultInterval
	}
	d := time.Duration(resp.NextInSeconds) * time.Second
	return min(max(d, MinInterval), MaxInterval)
}

// Exponential delay for the nth consecutive failure, capped
func Backoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := BackoffStart
	for i := 1; i < failures && d < BackoffCap; i++ {
		d *= 2
	}
	return min(d, BackoffCap)
}

// Delay until the next attempt after the nth consecutive failure
// A paused hub waits an hour, a rate limit honours Retry-After
func RetryDelay(failures int, err error) time.Duration {
	if he := hub.AsError(err); he != nil {
		if he.Paused() {
			return PausedDelay
		}
		if he.RateLimited() && he.RetryAfter > 0 {
			return min(max(he.RetryAfter, Backoff(failures)), BackoffCap)
		}
	}
	return Backoff(failures)
}
