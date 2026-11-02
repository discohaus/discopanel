package webhook

import (
	"strings"
	"testing"

	"github.com/discohaus/discopanel/pkg/events"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Stop payloads expose the coarse reason and nothing user shaped
func TestStopPayloadCarriesReasonOnly(t *testing.T) {
	stop := v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP
	server := &v1.Server{Id: "srv", Name: "Lazy"}
	p := BuildPayload(stop, server, map[string]string{events.DataStopReason: events.StopReasonManual})

	if got := p.Msg.Data; len(got) != 1 || got[events.DataStopReason] != events.StopReasonManual {
		t.Fatalf("payload data %v, want only the reason", got)
	}
	for _, key := range []string{"source", "user", "username", "actor"} {
		if _, ok := p.vars[key]; ok {
			t.Fatalf("template vars expose %q", key)
		}
	}
	if p.vars[events.DataStopReason] != events.StopReasonManual {
		t.Fatalf("template var reason = %v", p.vars[events.DataStopReason])
	}

	body, err := renderBody(&v1.WebhookTaskConfig{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"reason":"manual"`) {
		t.Fatalf("default body lacks the reason: %s", body)
	}
	tmpl, err := renderBody(&v1.WebhookTaskConfig{PayloadTemplate: `{"why":"{{.reason}}"}`}, p)
	if err != nil || string(tmpl) != `{"why":"manual"}` {
		t.Fatalf("template body %s, err %v", tmpl, err)
	}
}
