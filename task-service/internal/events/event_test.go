package events

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"task-management-task-service/internal/models"
)

// sampleTask is a task as the create handler has it immediately after
// database.DB.Create -- ID assigned, relations NOT preloaded. The zero-valued
// Project/Creator/Assignee are deliberate: they are what makes marshalling
// models.Task directly onto the wire unacceptable.
func sampleTask() models.Task {
	due := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	creator := uint(7)
	return models.Task{
		ID:          42,
		Title:       "Ship the event contract",
		Description: "with a test first",
		ProjectID:   3,
		CreatorID:   &creator,
		AssigneeID:  &creator,
		Status:      "In Progress",
		Priority:    "High",
		Estimate:    "M",
		DueDate:     &due,
		CreatedAt:   time.Date(2026, 9, 4, 14, 3, 11, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 9, 4, 14, 3, 11, 0, time.UTC),
	}
}

func keysOf(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// samePayload compares TaskPayload by value. A plain != would compare the
// DueDate *time.Time by address, which always differs after a JSON round trip
// even when the instant is identical -- the two structs then print the same
// while the comparison fails.
func samePayload(a, b TaskPayload) bool {
	if a.ID != b.ID || a.Title != b.Title || a.Description != b.Description ||
		a.ProjectID != b.ProjectID || a.Status != b.Status ||
		a.Priority != b.Priority || a.Estimate != b.Estimate {
		return false
	}
	switch {
	case a.DueDate == nil && b.DueDate == nil:
		return true
	case a.DueDate == nil || b.DueDate == nil:
		return false
	default:
		return a.DueDate.Equal(*b.DueDate)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The wire contract. Once two services depend on these key names, renaming one
// is a coordinated deploy -- so an accidental rename must fail here rather than
// silently deliver null to the consumer.
func TestEnvelopeJSONKeys(t *testing.T) {
	env := NewTaskCreated(sampleTask(), 7)

	want := []string{"actor_id", "event_id", "event_type", "occurred_at", "task", "task_id"}
	got := keysOf(t, env)
	if !equalStrings(got, want) {
		t.Errorf("envelope keys:\n got %v\nwant %v", got, want)
	}
}

func TestTaskPayloadJSONKeys(t *testing.T) {
	env := NewTaskCreated(sampleTask(), 7)

	want := []string{
		"description", "due_date", "estimate", "id",
		"priority", "project_id", "status", "title",
	}
	got := keysOf(t, env.Task)
	if !equalStrings(got, want) {
		t.Errorf("task payload keys:\n got %v\nwant %v", got, want)
	}
}

// Guards against someone "simplifying" TaskPayload into models.Task.
func TestTaskPayloadLeaksNoDatabaseFields(t *testing.T) {
	raw, err := json.Marshal(NewTaskCreated(sampleTask(), 7))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	task, ok := probe["task"].(map[string]any)
	if !ok {
		t.Fatalf("task is not an object: %T", probe["task"])
	}
	for _, forbidden := range []string{
		"deleted_at", "project", "assignee", "creator",
		"assignee_id", "creator_id", "created_at", "updated_at",
	} {
		if _, present := task[forbidden]; present {
			t.Errorf("task payload leaks database-only field %q", forbidden)
		}
	}
}

func TestNewTaskCreatedFields(t *testing.T) {
	task := sampleTask()
	env := NewTaskCreated(task, 7)

	if env.EventType != EventTaskCreated {
		t.Errorf("event_type = %q, want %q", env.EventType, EventTaskCreated)
	}
	if env.EventType != "task.created" {
		t.Errorf("event_type literal = %q, want %q", env.EventType, "task.created")
	}
	if env.TaskID != task.ID {
		t.Errorf("task_id = %d, want %d", env.TaskID, task.ID)
	}
	if env.ActorID != 7 {
		t.Errorf("actor_id = %d, want 7", env.ActorID)
	}
	if env.Task.ID != task.ID || env.Task.Title != task.Title || env.Task.ProjectID != task.ProjectID {
		t.Errorf("task payload not copied from the model: %+v", env.Task)
	}
	if env.OccurredAt.IsZero() {
		t.Error("occurred_at is zero; it should be set at construction")
	}
}

// event_id exists so the consumer can dedupe under at-least-once delivery.
// Two events must never share one.
func TestEventIDIsUniquePerEvent(t *testing.T) {
	a := NewTaskCreated(sampleTask(), 7)
	b := NewTaskCreated(sampleTask(), 7)

	if a.EventID == "" {
		t.Fatal("event_id is empty")
	}
	if a.EventID == b.EventID {
		t.Errorf("two events share event_id %q", a.EventID)
	}
}

func TestOccurredAtIsRFC3339(t *testing.T) {
	raw, err := json.Marshal(NewTaskCreated(sampleTask(), 7))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		OccurredAt string `json:"occurred_at"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, probe.OccurredAt); err != nil {
		t.Errorf("occurred_at %q is not RFC3339: %v", probe.OccurredAt, err)
	}
}

// A created event carries no diff, so `changes` must be absent -- not null,
// not []. An update event carries it.
func TestChangesOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(NewTaskCreated(sampleTask(), 7))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["changes"]; present {
		t.Errorf("changes present on a created event: %s", raw)
	}
}

func TestChangesPresentWhenSet(t *testing.T) {
	env := NewTaskCreated(sampleTask(), 7)
	env.EventType = EventTaskUpdated
	env.Changes = []ChangeDetail{{Field: "Status", From: "Not Started", To: "In Progress"}}

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Changes []map[string]string `json:"changes"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(probe.Changes) != 1 {
		t.Fatalf("changes length = %d, want 1: %s", len(probe.Changes), raw)
	}
	// Lowercase keys on the wire. The monolith's services.ChangeDetail has the
	// matching Go field names (Field/From/To) but no JSON tags, so the ported
	// copy in Stage 6 needs these tags added.
	got := probe.Changes[0]
	for k, want := range map[string]string{"field": "Status", "from": "Not Started", "to": "In Progress"} {
		if got[k] != want {
			t.Errorf("changes[0][%q] = %q, want %q (keys present: %v)", k, got[k], want, got)
		}
	}
}

// "No due date" is information the email template needs, and is distinct from
// an absent field -- so the key stays, with a null value.
func TestDueDateNullWhenUnset(t *testing.T) {
	task := sampleTask()
	task.DueDate = nil

	raw, err := json.Marshal(NewTaskCreated(task, 7))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Task map[string]json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, present := probe.Task["due_date"]
	if !present {
		t.Fatal("due_date key missing; it should be present and null")
	}
	if string(v) != "null" {
		t.Errorf("due_date = %s, want null", v)
	}
}

func TestDueDateRFC3339WhenSet(t *testing.T) {
	raw, err := json.Marshal(NewTaskCreated(sampleTask(), 7))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe struct {
		Task struct {
			DueDate string `json:"due_date"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, probe.Task.DueDate); err != nil {
		t.Errorf("due_date %q is not RFC3339: %v", probe.Task.DueDate, err)
	}
}

// The consumer unmarshals what the producer marshalled; prove the envelope
// survives the trip with its meaning intact.
func TestEnvelopeRoundTrips(t *testing.T) {
	orig := NewTaskCreated(sampleTask(), 7)
	orig.EventType = EventTaskUpdated
	orig.Changes = []ChangeDetail{{Field: "Priority", From: "High", To: "Low"}}

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.EventID != orig.EventID || back.EventType != orig.EventType {
		t.Errorf("identity lost: %+v vs %+v", back, orig)
	}
	if back.TaskID != orig.TaskID || back.ActorID != orig.ActorID {
		t.Errorf("ids lost: %+v vs %+v", back, orig)
	}
	if !back.OccurredAt.Equal(orig.OccurredAt) {
		t.Errorf("occurred_at %v != %v", back.OccurredAt, orig.OccurredAt)
	}
	if !samePayload(back.Task, orig.Task) {
		t.Errorf("task payload changed:\n got %+v\nwant %+v", back.Task, orig.Task)
	}
	if len(back.Changes) != 1 || back.Changes[0] != orig.Changes[0] {
		t.Errorf("changes changed: %+v", back.Changes)
	}
}

func TestEventTypeConstants(t *testing.T) {
	// A slice, not a map keyed by the constants: if a constant held the wrong
	// value a map would collide and silently drop an entry, letting this pass.
	for _, c := range []struct{ name, got, want string }{
		{"EventTaskCreated", EventTaskCreated, "task.created"},
		{"EventTaskUpdated", EventTaskUpdated, "task.updated"},
		{"EventTaskDeleted", EventTaskDeleted, "task.deleted"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}
