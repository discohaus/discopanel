package lifecycle

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/events"
)

// Stop events carry a coarse reason, never the acting username
func TestStopEventDataHidesUser(t *testing.T) {
	base := context.Background()
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"panel user", metrics.WithSource(base, "alice"), events.StopReasonManual},
		{"untagged", base, events.StopReasonManual},
		{"idle", metrics.WithSource(base, metrics.SourceAutostop), events.StopReasonAutostop},
		{"scheduler", metrics.WithSource(base, metrics.SourceScheduler), events.StopReasonScheduled},
		{"shutdown", metrics.WithSource(base, metrics.SourceSystem), events.StopReasonSystem},
		{"restart by user", context.WithValue(metrics.WithSource(base, "alice"), restartKey{}, true), events.StopReasonRestart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := stopEventData(tc.ctx)
			if keys := slices.Collect(maps.Keys(data)); !slices.Equal(keys, []string{events.DataStopReason}) {
				t.Fatalf("stop data keys %v, want only %q", keys, events.DataStopReason)
			}
			if data[events.DataStopReason] != tc.want {
				t.Fatalf("reason %q, want %q", data[events.DataStopReason], tc.want)
			}
		})
	}
}
