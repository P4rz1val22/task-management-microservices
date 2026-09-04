package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/segmentio/kafka-go"
)

// fakeWriter stands in for *kafka.Writer. The seam is an interface with the one
// method the publisher calls, so the test needs no broker, no network, and no
// docker -- and *kafka.Writer satisfies it without an adapter.
type fakeWriter struct {
	calls  int
	sent   []kafka.Message
	err    error
	closed bool
}

func (f *fakeWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msgs...)
	return nil
}

func (f *fakeWriter) Close() error {
	f.closed = true
	return nil
}

// newTestPublisher wires a Publisher onto the fake. Production builds the same
// struct over a real *kafka.Writer via NewPublisher.
func newTestPublisher(w messageWriter) *Publisher {
	return &Publisher{writer: w}
}

func TestPublishTaskCreatedWritesExactlyOneMessage(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)

	if err := p.PublishTaskCreated(context.Background(), sampleTask(), 7); err != nil {
		t.Fatalf("PublishTaskCreated: %v", err)
	}

	if f.calls != 1 {
		t.Errorf("WriteMessages called %d times, want exactly 1", f.calls)
	}
	if len(f.sent) != 1 {
		t.Fatalf("wrote %d messages, want exactly 1", len(f.sent))
	}
}

// The single most important assertion in this package. The partition key is
// what guarantees that every event for one task lands on one partition and is
// therefore consumed in the order it happened. Get it wrong and the events
// scatter across three partitions, which no amount of manual clicking would
// reveal -- it only shows up as a task.updated email arriving before its own
// task.created.
func TestMessageKeyIsTaskID(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)

	task := sampleTask() // ID 42
	if err := p.PublishTaskCreated(context.Background(), task, 7); err != nil {
		t.Fatalf("PublishTaskCreated: %v", err)
	}

	got := string(f.sent[0].Key)
	if got != "42" {
		t.Errorf("message key = %q, want %q (the task ID as a string)", got, "42")
	}
}

// Two events for the same task must carry byte-identical keys, since the hash
// balancer partitions on the key bytes. An event ID or a timestamp leaking into
// the key would silently break ordering while still passing the assertion
// above.
func TestSameTaskProducesSameKey(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)
	ctx := context.Background()

	task := sampleTask()
	if err := p.PublishTaskCreated(ctx, task, 7); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := p.PublishTaskCreated(ctx, task, 9); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	if a, b := string(f.sent[0].Key), string(f.sent[1].Key); a != b {
		t.Errorf("keys differ across events for the same task: %q vs %q", a, b)
	}
}

func TestDifferentTasksProduceDifferentKeys(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)
	ctx := context.Background()

	first := sampleTask()
	second := sampleTask()
	second.ID = 43

	if err := p.PublishTaskCreated(ctx, first, 7); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if err := p.PublishTaskCreated(ctx, second, 7); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	if a, b := string(f.sent[0].Key), string(f.sent[1].Key); a == b {
		t.Errorf("tasks 42 and 43 share key %q; they must partition independently", a)
	}
}

// The value on the wire is the envelope from event.go and nothing else -- no
// second layer of wrapping, no re-encoded string.
func TestMessageValueIsTheEnvelope(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)

	task := sampleTask()
	if err := p.PublishTaskCreated(context.Background(), task, 7); err != nil {
		t.Fatalf("PublishTaskCreated: %v", err)
	}

	var got Envelope
	if err := json.Unmarshal(f.sent[0].Value, &got); err != nil {
		t.Fatalf("value is not an envelope: %v", err)
	}

	if got.EventType != EventTaskCreated {
		t.Errorf("event_type = %q, want %q", got.EventType, EventTaskCreated)
	}
	if got.TaskID != task.ID {
		t.Errorf("task_id = %d, want %d", got.TaskID, task.ID)
	}
	if got.ActorID != 7 {
		t.Errorf("actor_id = %d, want 7", got.ActorID)
	}
	if got.EventID == "" {
		t.Error("event_id is empty; the consumer dedupes on it")
	}
	if !samePayload(got.Task, payloadFrom(task)) {
		t.Errorf("task payload = %+v, want %+v", got.Task, payloadFrom(task))
	}
	if got.Changes != nil {
		t.Errorf("changes = %v, want absent on a created event", got.Changes)
	}
}

// The publisher does not decide what a failed publish means. Stage 4's handler
// decides to log it and still return 201; this layer's only job is to report it
// truthfully, so an error here must come back rather than be logged and
// swallowed.
func TestWriterErrorIsReturned(t *testing.T) {
	boom := errors.New("broker unreachable")
	f := &fakeWriter{err: boom}
	p := newTestPublisher(f)

	err := p.PublishTaskCreated(context.Background(), sampleTask(), 7)
	if err == nil {
		t.Fatal("PublishTaskCreated returned nil; the writer error was swallowed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the writer error %v", err, boom)
	}
}

func TestCloseClosesTheWriter(t *testing.T) {
	f := &fakeWriter{}
	p := newTestPublisher(f)

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !f.closed {
		t.Error("Close did not close the underlying writer")
	}
}

// Configuration is read once, at construction. Reading env on every publish
// would make the destination mutable at runtime and hide a typo until the first
// event.
func TestNewPublisherReadsEnvAtConstruction(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:29092")
	t.Setenv("KAFKA_TOPIC", "task-events")

	p := NewPublisher()
	defer p.Close()

	w, ok := p.writer.(*kafka.Writer)
	if !ok {
		t.Fatalf("NewPublisher built a %T, want *kafka.Writer", p.writer)
	}
	if w.Topic != "task-events" {
		t.Errorf("topic = %q, want %q", w.Topic, "task-events")
	}
	if addr := w.Addr.String(); !strings.Contains(addr, "kafka:29092") {
		t.Errorf("addr = %q, want it to contain %q", addr, "kafka:29092")
	}

	// Changing the environment after construction must not move the target.
	t.Setenv("KAFKA_TOPIC", "somewhere-else")
	if w.Topic != "task-events" {
		t.Errorf("topic changed to %q after the env changed; config must be frozen at construction", w.Topic)
	}
}

func TestNewPublisherDefaultsWhenEnvUnset(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "")
	t.Setenv("KAFKA_TOPIC", "")

	p := NewPublisher()
	defer p.Close()

	w := p.writer.(*kafka.Writer)
	if w.Topic != DefaultTopic {
		t.Errorf("topic = %q, want the default %q", w.Topic, DefaultTopic)
	}
	if addr := w.Addr.String(); !strings.Contains(addr, DefaultBrokers) {
		t.Errorf("addr = %q, want the default %q", addr, DefaultBrokers)
	}
}

// kafka-go's zero-value balancer is round-robin, which ignores the key and
// scatters a task's events across all three partitions. The key assertion above
// passes regardless -- only this one catches it.
func TestWriterUsesHashBalancer(t *testing.T) {
	p := NewPublisher()
	defer p.Close()

	w := p.writer.(*kafka.Writer)
	if _, ok := w.Balancer.(*kafka.Hash); !ok {
		t.Errorf("balancer is %T, want *kafka.Hash so the partition follows the key", w.Balancer)
	}
}

// Async writes report success immediately and deliver errors to a callback, so
// an async writer would make TestWriterErrorIsReturned a lie in production even
// while it passes against the fake.
func TestWriterIsSynchronous(t *testing.T) {
	p := NewPublisher()
	defer p.Close()

	w := p.writer.(*kafka.Writer)
	if w.Async {
		t.Error("writer is async; publish errors would never reach the caller")
	}
}
