package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"task-management-notification-service/internal/events"
	"task-management-notification-service/internal/users"
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

// --- Stage 6: the handler now looks up a recipient and sends an email ---

type fakeLookup struct {
	email string
	err   error
	calls []uint
}

func (f *fakeLookup) EmailFor(_ context.Context, userID uint) (string, error) {
	f.calls = append(f.calls, userID)
	if f.err != nil {
		return "", f.err
	}
	return f.email, nil
}

type fakeMailer struct {
	created []string // recipient per created-email call
	updated []string
	deleted []string
	changes [][]events.ChangeDetail
	err     error
}

func (f *fakeMailer) SendTaskCreatedNotification(_ events.TaskPayload, to string) error {
	f.created = append(f.created, to)
	return f.err
}

func (f *fakeMailer) SendTaskUpdatedNotification(_ events.TaskPayload, to string, ch []events.ChangeDetail) error {
	f.updated = append(f.updated, to)
	f.changes = append(f.changes, ch)
	return f.err
}

func (f *fakeMailer) SendTaskDeletedNotification(_ events.TaskPayload, to string) error {
	f.deleted = append(f.deleted, to)
	return f.err
}

func wired(lookup *fakeLookup, mailer *fakeMailer) *Handler {
	h := New()
	h.Recipients = lookup
	h.Mailer = mailer
	return h
}

// The event carries actor_id; the address comes from the database. That
// indirection is deliberate -- an address is mutable personal data and freezing
// it into every event on the topic would be wrong.
func TestCreatedEventSendsToTheActorsAddress(t *testing.T) {
	lookup := &fakeLookup{email: "demo@example.com"}
	mailer := &fakeMailer{}
	h := wired(lookup, mailer)

	res, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if res.Action != ActionNotified {
		t.Errorf("action = %v, want notified", res.Action)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != 7 {
		t.Errorf("recipient looked up for %v, want [7] (the actor_id)", lookup.calls)
	}
	if len(mailer.created) != 1 || mailer.created[0] != "demo@example.com" {
		t.Errorf("created email sent to %v, want [demo@example.com]", mailer.created)
	}
	if len(mailer.updated) != 0 {
		t.Errorf("an update email was sent for a created event")
	}
}

func TestUpdatedEventSendsTheChangeList(t *testing.T) {
	lookup := &fakeLookup{email: "demo@example.com"}
	mailer := &fakeMailer{}
	h := wired(lookup, mailer)

	env := sampleEnvelope(events.EventTaskUpdated)
	env.Changes = []events.ChangeDetail{{Field: "Status", From: "Not Started", To: "Done"}}

	if _, err := h.HandleMessage(context.Background(), mustJSON(t, env)); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(mailer.updated) != 1 {
		t.Fatalf("sent %d update emails, want 1", len(mailer.updated))
	}
	if len(mailer.changes[0]) != 1 || mailer.changes[0][0].Field != "Status" {
		t.Errorf("changes passed to the mailer = %v, want the Status diff", mailer.changes[0])
	}
	if len(mailer.created) != 0 {
		t.Error("a created email was sent for an update event")
	}
}

// An unknown event type must not send anything, and must not look anyone up.
func TestSkippedEventSendsNothing(t *testing.T) {
	lookup := &fakeLookup{email: "demo@example.com"}
	mailer := &fakeMailer{}
	h := wired(lookup, mailer)

	res, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope("task.archived")))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if res.Action != ActionSkipped {
		t.Errorf("action = %v, want skipped", res.Action)
	}
	if len(mailer.created)+len(mailer.updated) != 0 {
		t.Error("an unrecognised event type produced an email")
	}
	if len(lookup.calls) != 0 {
		t.Error("an unrecognised event type triggered a database lookup")
	}
}

// A user who does not exist can never be emailed, so the message is poison:
// discard it, commit the offset, keep the partition moving.
func TestMissingRecipientIsABadMessage(t *testing.T) {
	lookup := &fakeLookup{err: fmt.Errorf("%w: no user 7", users.ErrNoRecipient)}
	mailer := &fakeMailer{}
	h := wired(lookup, mailer)

	_, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrBadMessage) {
		t.Errorf("error %v is not an ErrBadMessage; the loop would retry forever", err)
	}
}

// The opposite case, and the one that matters most: a database outage or an
// SMTP failure is TRANSIENT. Marking it as a bad message would discard a
// perfectly good event and the user would never be told about their task.
func TestTransientFailuresAreNotBadMessages(t *testing.T) {
	t.Run("database down", func(t *testing.T) {
		lookup := &fakeLookup{err: errors.New("connection refused")}
		h := wired(lookup, &fakeMailer{})

		_, err := h.HandleMessage(context.Background(),
			mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrBadMessage) {
			t.Error("a database outage was classified as a bad message; the event would be discarded")
		}
	})

	t.Run("smtp down", func(t *testing.T) {
		lookup := &fakeLookup{email: "demo@example.com"}
		h := wired(lookup, &fakeMailer{err: errors.New("smtp: connection refused")})

		_, err := h.HandleMessage(context.Background(),
			mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
		if err == nil {
			t.Fatal("expected an error")
		}
		if errors.Is(err, ErrBadMessage) {
			t.Error("an SMTP outage was classified as a bad message; the event would be discarded")
		}
	})
}

// A handler with no mailer wired -- which is what New() returns -- must still
// work, so the Stage 5 log-only behaviour remains available and no test can
// nil-panic.
func TestHandlerWithoutMailerStillProcesses(t *testing.T) {
	h := New()
	res, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
	if err != nil {
		t.Fatalf("HandleMessage with no mailer: %v", err)
	}
	if res.Action != ActionNotified {
		t.Errorf("action = %v, want notified", res.Action)
	}
}

// --- retry policy: transient failures must not be committed away ---

// countingHandler fails a set number of times before succeeding, so the retry
// policy can be tested without a broker.
type countingHandler struct {
	failures int
	err      error
	calls    int
}

func (c *countingHandler) process(_ context.Context, _ []byte) (Result, error) {
	c.calls++
	if c.calls <= c.failures {
		return Result{Action: ActionFailed}, c.err
	}
	return Result{Action: ActionNotified, Summary: "ok"}, nil
}

// Stage 6 made this path live: before it, every failure the handler could
// produce was a bad message. Now the database or the mail server can be down,
// and committing the offset on the first stumble throws away a notification
// that would have succeeded a moment later.
func TestTransientFailureIsRetried(t *testing.T) {
	c := &countingHandler{failures: 2, err: errors.New("connection refused")}

	res, err := processWithRetry(context.Background(), c.process, nil, time.Millisecond)
	if err != nil {
		t.Fatalf("processWithRetry: %v", err)
	}
	if res.Action != ActionNotified {
		t.Errorf("action = %v, want notified after the retries succeeded", res.Action)
	}
	if c.calls != 3 {
		t.Errorf("handler called %d times, want 3 (two failures then success)", c.calls)
	}
}

// A poison message must not be retried at all: it cannot succeed, and retrying
// only holds the partition up.
func TestBadMessageIsNotRetried(t *testing.T) {
	c := &countingHandler{failures: 99, err: fmt.Errorf("%w: broken", ErrBadMessage)}

	if _, err := processWithRetry(context.Background(), c.process, nil, time.Millisecond); err == nil {
		t.Fatal("expected an error")
	}
	if c.calls != 1 {
		t.Errorf("handler called %d times for a bad message, want 1", c.calls)
	}
}

func TestRetriesAreBounded(t *testing.T) {
	c := &countingHandler{failures: 99, err: errors.New("still down")}

	if _, err := processWithRetry(context.Background(), c.process, nil, time.Millisecond); err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if c.calls != maxProcessAttempts {
		t.Errorf("handler called %d times, want maxProcessAttempts = %d", c.calls, maxProcessAttempts)
	}
}

// A shutdown mid-retry must stop promptly rather than sleeping out the backoff.
func TestRetryStopsOnContextCancel(t *testing.T) {
	c := &countingHandler{failures: 99, err: errors.New("still down")}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := processWithRetry(ctx, c.process, nil, time.Second); err == nil {
		t.Fatal("expected an error")
	}
	if c.calls > 1 {
		t.Errorf("handler called %d times with a cancelled context, want 1", c.calls)
	}
}

// Before Stage 7 the handler routed everything that was not an update to the
// created template, so this event would have emailed the user "New Task
// Created" about a task they had just deleted. Nothing published deletes, so it
// was unreachable -- publishing them is what makes this test necessary.
func TestDeletedEventSendsTheDeletionEmail(t *testing.T) {
	lookup := &fakeLookup{email: "demo@example.com"}
	mailer := &fakeMailer{}
	h := wired(lookup, mailer)

	res, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskDeleted)))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if res.Action != ActionNotified {
		t.Errorf("action = %v, want notified", res.Action)
	}
	if len(mailer.deleted) != 1 || mailer.deleted[0] != "demo@example.com" {
		t.Errorf("deletion email sent to %v, want [demo@example.com]", mailer.deleted)
	}
	if len(mailer.created) != 0 {
		t.Error("a deletion sent the CREATED email; the user would be told their " +
			"deleted task was just created")
	}
	if len(mailer.updated) != 0 {
		t.Error("a deletion sent the updated email")
	}
}

// Each of the three event types must reach its own template, and only its own.
func TestEachEventTypeRoutesToItsOwnEmail(t *testing.T) {
	tests := []struct {
		eventType string
		created   int
		updated   int
		deleted   int
	}{
		{events.EventTaskCreated, 1, 0, 0},
		{events.EventTaskUpdated, 0, 1, 0},
		{events.EventTaskDeleted, 0, 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			mailer := &fakeMailer{}
			h := wired(&fakeLookup{email: "demo@example.com"}, mailer)

			if _, err := h.HandleMessage(context.Background(),
				mustJSON(t, sampleEnvelope(tt.eventType))); err != nil {
				t.Fatalf("HandleMessage: %v", err)
			}

			if len(mailer.created) != tt.created {
				t.Errorf("created emails = %d, want %d", len(mailer.created), tt.created)
			}
			if len(mailer.updated) != tt.updated {
				t.Errorf("updated emails = %d, want %d", len(mailer.updated), tt.updated)
			}
			if len(mailer.deleted) != tt.deleted {
				t.Errorf("deleted emails = %d, want %d", len(mailer.deleted), tt.deleted)
			}
		})
	}
}

// --- Stage 8: idempotent consumption ---

type fakeDedupe struct {
	seen     map[string]bool
	marked   []string
	seenErr  error
	markErr  error
	seenCall int
}

func newFakeDedupe() *fakeDedupe {
	return &fakeDedupe{seen: map[string]bool{}}
}

func (f *fakeDedupe) Seen(_ context.Context, eventID string) (bool, error) {
	f.seenCall++
	if f.seenErr != nil {
		return false, f.seenErr
	}
	return f.seen[eventID], nil
}

func (f *fakeDedupe) MarkProcessed(_ context.Context, eventID string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.seen[eventID] = true
	f.marked = append(f.marked, eventID)
	return nil
}

func wiredWithDedupe(lookup *fakeLookup, mailer *fakeMailer, d *fakeDedupe) *Handler {
	h := wired(lookup, mailer)
	h.Processed = d
	return h
}

// The entire feature in one assertion. Kafka delivers at least once, so the
// same event genuinely does arrive twice -- after a failed offset commit, a
// rebalance, a restart mid-batch. Without this the user gets two emails, and
// there is no way to demonstrate the fix by clicking around.
func TestSameEventTwiceSendsOneEmail(t *testing.T) {
	mailer := &fakeMailer{}
	h := wiredWithDedupe(&fakeLookup{email: "demo@example.com"}, mailer, newFakeDedupe())
	msg := mustJSON(t, sampleEnvelope(events.EventTaskCreated))

	first, err := h.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	second, err := h.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}

	if len(mailer.created) != 1 {
		t.Errorf("sent %d emails for the same event, want exactly 1", len(mailer.created))
	}
	if first.Action != ActionNotified {
		t.Errorf("first action = %v, want notified", first.Action)
	}
	if second.Action != ActionDuplicate {
		t.Errorf("second action = %v, want duplicate", second.Action)
	}
}

// Two different events must both go out. A dedupe that suppresses everything
// would pass the test above and be catastrophic.
func TestDistinctEventsBothSend(t *testing.T) {
	mailer := &fakeMailer{}
	h := wiredWithDedupe(&fakeLookup{email: "demo@example.com"}, mailer, newFakeDedupe())

	first := sampleEnvelope(events.EventTaskCreated)
	second := sampleEnvelope(events.EventTaskCreated)
	second.EventID = "3f1c2b7e-0000-4000-8000-000000000002"

	if _, err := h.HandleMessage(context.Background(), mustJSON(t, first)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := h.HandleMessage(context.Background(), mustJSON(t, second)); err != nil {
		t.Fatalf("second: %v", err)
	}

	if len(mailer.created) != 2 {
		t.Errorf("sent %d emails for two distinct events, want 2", len(mailer.created))
	}
}

// The event is recorded only after the send succeeds. Recording first would
// mean a failed send is never retried -- the redelivery would be waved through
// as a duplicate and the notification lost silently.
func TestFailedSendIsNotRecordedAsProcessed(t *testing.T) {
	d := newFakeDedupe()
	mailer := &fakeMailer{err: errors.New("smtp: connection refused")}
	h := wiredWithDedupe(&fakeLookup{email: "demo@example.com"}, mailer, d)

	if _, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated))); err == nil {
		t.Fatal("expected an error")
	}
	if len(d.marked) != 0 {
		t.Errorf("a failed send was recorded as processed %v; the retry would be "+
			"skipped as a duplicate and the email never sent", d.marked)
	}
}

// A redelivery must not even look up a recipient. Skipping early keeps a
// duplicate cheap and avoids a pointless database hit per replayed message.
func TestDuplicateSkipsBeforeLookingUpTheRecipient(t *testing.T) {
	d := newFakeDedupe()
	d.seen["3f1c2b7e-0000-4000-8000-000000000001"] = true
	lookup := &fakeLookup{email: "demo@example.com"}
	mailer := &fakeMailer{}
	h := wiredWithDedupe(lookup, mailer, d)

	res, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if res.Action != ActionDuplicate {
		t.Errorf("action = %v, want duplicate", res.Action)
	}
	if len(lookup.calls) != 0 {
		t.Errorf("a duplicate triggered %d recipient lookups, want 0", len(lookup.calls))
	}
	if len(mailer.created) != 0 {
		t.Error("a duplicate sent an email")
	}
}

// A database outage must never be read as "not seen" -- that would send
// duplicates during exactly the moment things are already going wrong. It is
// transient, so it must not be an ErrBadMessage either.
func TestDedupeFailureIsTransientNotADuplicate(t *testing.T) {
	d := newFakeDedupe()
	d.seenErr = errors.New("connection refused")
	mailer := &fakeMailer{}
	h := wiredWithDedupe(&fakeLookup{email: "demo@example.com"}, mailer, d)

	_, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated)))
	if err == nil {
		t.Fatal("expected an error when the dedupe store is unreachable")
	}
	if errors.Is(err, ErrBadMessage) {
		t.Error("a dedupe outage was classified as a bad message; the event would be discarded")
	}
	if len(mailer.created) != 0 {
		t.Error("an email was sent despite being unable to check for a duplicate")
	}
}

// With no dedupe store wired -- the Stage 5 and 6 shape -- everything still
// works, just without the guarantee.
func TestHandlerWithoutDedupeStillSends(t *testing.T) {
	mailer := &fakeMailer{}
	h := wired(&fakeLookup{email: "demo@example.com"}, mailer)

	if _, err := h.HandleMessage(context.Background(),
		mustJSON(t, sampleEnvelope(events.EventTaskCreated))); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(mailer.created) != 1 {
		t.Errorf("sent %d emails, want 1", len(mailer.created))
	}
}
