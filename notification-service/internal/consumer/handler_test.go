package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"task-management-notification-service/internal/events"
)

func sampleEnvelope(eventType string) events.Envelope {
	due := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	return events.Envelope{
		EventID:    "3f1c2b7e-0000-4000-8000-000000000001",
		EventType:  eventType,
		OccurredAt: time.Date(2026, 9, 4, 14, 3, 11, 0, time.UTC),
		TaskID:     42,
		ActorID:    7,
		Task: events.TaskPayload{
			ID:          42,
			Title:       "Ship the consumer",
			Description: "read events off the topic",
			ProjectID:   3,
			Status:      "In Progress",
			Priority:    "High",
			Estimate:    "M",
			DueDate:     &due,
		},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// The action a message resulted in. Returning this rather than logging inside
// the handler is what makes the decision testable at all: the loop logs, the
// handler decides.
func TestHandleMessageActions(t *testing.T) {
	tests := []struct {
		name    string
		value   []byte
		want    Action
		wantErr bool
	}{
		{
			name:  "created event is handled",
			value: mustJSON(t, sampleEnvelope(events.EventTaskCreated)),
			want:  ActionNotified,
		},
		{
			name:  "updated event is handled",
			value: mustJSON(t, sampleEnvelope(events.EventTaskUpdated)),
			want:  ActionNotified,
		},
		{
			name:  "deleted event is handled",
			value: mustJSON(t, sampleEnvelope(events.EventTaskDeleted)),
			want:  ActionNotified,
		},
		{
			// Forward compatibility. A producer deployed ahead of this consumer
			// will publish types this build has never heard of, and the right
			// answer is to move on. Treating it as an error would stall the
			// partition behind a message that is not even malformed.
			name:  "unknown event type is skipped, not an error",
			value: mustJSON(t, sampleEnvelope("task.archived")),
			want:  ActionSkipped,
		},
		{
			name:    "malformed JSON is an error, not a panic",
			value:   []byte(`{"event_type": "task.created", ` + "\x00" + `broken`),
			want:    ActionFailed,
			wantErr: true,
		},
		{
			name:    "empty message is an error",
			value:   []byte(``),
			want:    ActionFailed,
			wantErr: true,
		},
		{
			// There is nobody to notify, so this can never succeed. It is a bad
			// message rather than a transient failure.
			name: "zero actor_id is rejected",
			value: mustJSON(t, func() events.Envelope {
				e := sampleEnvelope(events.EventTaskCreated)
				e.ActorID = 0
				return e
			}()),
			want:    ActionFailed,
			wantErr: true,
		},
		{
			name: "zero task_id is rejected",
			value: mustJSON(t, func() events.Envelope {
				e := sampleEnvelope(events.EventTaskCreated)
				e.TaskID = 0
				return e
			}()),
			want:    ActionFailed,
			wantErr: true,
		},
		{
			// event_id is the dedupe key Stage 8 depends on. An event without
			// one cannot be deduplicated, so it is rejected now rather than
			// discovered later.
			name: "missing event_id is rejected",
			value: mustJSON(t, func() events.Envelope {
				e := sampleEnvelope(events.EventTaskCreated)
				e.EventID = ""
				return e
			}()),
			want:    ActionFailed,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New()
			got, err := h.HandleMessage(context.Background(), tt.value)

			if tt.wantErr && err == nil {
				t.Errorf("HandleMessage returned nil error, want an error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("HandleMessage returned %v, want nil", err)
			}
			if got.Action != tt.want {
				t.Errorf("action = %v, want %v", got.Action, tt.want)
			}
		})
	}
}

// A message the handler cannot parse must not take the process down. The reader
// loop calls this for anything that lands on the topic, including whatever a
// stray producer or a console tool puts there.
func TestHandleMessageNeverPanics(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte(``),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`"a bare string"`),
		[]byte(`{`),
		[]byte(`{"event_type": 12345}`),
		[]byte(`{"task": "not an object"}`),
		[]byte("\xff\xfe\x00binary garbage"),
	}

	h := New()
	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("input %d (%q) panicked: %v", i, string(in), r)
				}
			}()
			if _, err := h.HandleMessage(context.Background(), in); err == nil {
				t.Errorf("input %d (%q) returned nil error", i, string(in))
			}
		}()
	}
}

// The handler describes what it did; the loop is what prints it. Stage 6
// replaces this description with an actual email, and the summary is what the
// log line is built from.
func TestSummaryDescribesTheEvent(t *testing.T) {
	h := New()
	env := sampleEnvelope(events.EventTaskUpdated)
	env.Changes = []events.ChangeDetail{
		{Field: "Status", From: "Not Started", To: "In Progress"},
	}

	summary, err := h.describe(env)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{"task.updated", "42", "Ship the consumer", "Status"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q does not mention %q", summary, want)
		}
	}
}

// Envelopes produced by task-service must decode here. This is the duplicated
// contract's safety net: a rename on the producer side shows up as a zero value
// on this side, and this test is what turns that into a failure.
func TestDecodesProducerWireFormat(t *testing.T) {
	// Captured verbatim from the console consumer in Stage 4.
	const onTheWire = `{"event_id":"5b361816-6b0f-4bdf-b259-929ba19e2f27",` +
		`"event_type":"task.created","occurred_at":"2026-09-04T17:56:30.560871595Z",` +
		`"task_id":3,"actor_id":1,"task":{"id":3,"title":"Published task 3",` +
		`"description":"","project_id":1,"status":"In Progress","priority":"High",` +
		`"estimate":"M","due_date":null}}`

	h := New()
	res, err := h.HandleMessage(context.Background(), []byte(onTheWire))
	if err != nil {
		t.Fatalf("real producer output rejected: %v", err)
	}
	if res.Action != ActionNotified {
		t.Errorf("action = %v, want %v", res.Action, ActionNotified)
	}

	var env events.Envelope
	if err := json.Unmarshal([]byte(onTheWire), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.EventID == "" || env.TaskID != 3 || env.ActorID != 1 {
		t.Errorf("envelope decoded as %+v; a field name has drifted", env)
	}
	if env.Task.Title != "Published task 3" {
		t.Errorf("task.title = %q, want %q", env.Task.Title, "Published task 3")
	}
	if env.Task.DueDate != nil {
		t.Errorf("due_date = %v, want nil", env.Task.DueDate)
	}
	if env.Changes != nil {
		t.Errorf("changes = %v on a created event, want nil", env.Changes)
	}
}

// A bad message must be distinguishable from a broker problem, because Stage 8
// routes them differently: a poison message goes to the dead-letter topic and
// its offset is committed, while a transient failure is retried.
func TestBadMessageErrorsAreIdentifiable(t *testing.T) {
	h := New()

	_, err := h.HandleMessage(context.Background(), []byte(`{"nope`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrBadMessage) {
		t.Errorf("error %v does not wrap ErrBadMessage; the loop cannot tell "+
			"a poison message from a broker failure", err)
	}
}
