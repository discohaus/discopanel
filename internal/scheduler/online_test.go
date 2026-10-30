package scheduler

import (
	"testing"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func TestNeedsOnlineServer(t *testing.T) {
	stop := v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP
	none := v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED
	cases := []struct {
		name  string
		typ   v1.TaskType
		req   bool
		event v1.TriggeredEventType
		want  bool
	}{
		{"backup on stop event (issue #121)", v1.TaskType_TASK_TYPE_BACKUP, true, stop, false},
		{"backup run now while stopped", v1.TaskType_TASK_TYPE_BACKUP, true, none, false},
		{"command on schedule needs server", v1.TaskType_TASK_TYPE_COMMAND, true, none, true},
		{"command on stop event cannot need it", v1.TaskType_TASK_TYPE_COMMAND, true, stop, false},
		{"webhook never needs it", v1.TaskType_TASK_TYPE_WEBHOOK, true, none, false},
		{"opted out", v1.TaskType_TASK_TYPE_COMMAND, false, none, false},
	}
	for _, tc := range cases {
		task := &v1.ScheduledTask{TaskType: tc.typ, RequireOnline: tc.req}
		if got := needsOnlineServer(task, tc.event); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
