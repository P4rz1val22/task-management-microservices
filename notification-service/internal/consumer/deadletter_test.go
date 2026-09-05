package consumer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/segmentio/kafka-go"
)

type fakeDLQ struct {
	sent   []kafka.Message
	err    error
	errSeq []error
	calls  int
}

func (f *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	attempt := f.calls
	f.calls++

	if attempt < len(f.errSeq) {
		if err := f.errSeq[attempt]; err != nil {
			return err
		}
	} else if f.err != nil {
		return f.err
	}

	f.sent = append(f.sent, msgs...)
	return nil
}

func original() kafka.Message {
	return kafka.Message{
		Topic:     "task-events",
		Partition: 2,
		Offset:    17,
		Key:       []byte("42"),
		Value:     []byte(`{"event_id":"evt-1","event_type":"task.created"}`),
	}
}

// A poison message must leave the partition. Publishing it to the dead-letter
// topic is what makes committing its offset safe: without somewhere to put it,
// committing loses it and not committing blocks every event behind it.
func TestDeadLetterCarriesTheOriginalMessage(t *testing.T) {
	f := &fakeDLQ{}
	dlq := newTestDeadLetter(f, "task-events.dlq")

	err := dlq.Send(context.Background(), original(), errors.New("malformed envelope"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(f.sent))
	}

	got := f.sent[0]
	if string(got.Value) != string(original().Value) {
		t.Errorf("value = %q, want the original message unchanged", got.Value)
	}
	if string(got.Key) != "42" {
		t.Errorf("key = %q, want the original key %q", got.Key, "42")
	}
}

// Whoever reads the dead-letter topic later needs to know why the message is
// there and where it came from. Without that it is an undiagnosable blob.
func TestDeadLetterRecordsWhyAndWhereFrom(t *testing.T) {
	f := &fakeDLQ{}
	dlq := newTestDeadLetter(f, "task-events.dlq")

	if err := dlq.Send(context.Background(), original(), errors.New("smtp refused")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	headers := map[string]string{}
	for _, h := range f.sent[0].Headers {
		headers[h.Key] = string(h.Value)
	}

	if reason := headers["dlq-error"]; !strings.Contains(reason, "smtp refused") {
		t.Errorf("dlq-error header = %q, want it to name the failure", reason)
	}
	if headers["dlq-topic"] != "task-events" {
		t.Errorf("dlq-topic = %q, want %q", headers["dlq-topic"], "task-events")
	}
	if headers["dlq-partition"] != "2" {
		t.Errorf("dlq-partition = %q, want %q", headers["dlq-partition"], "2")
	}
	if headers["dlq-offset"] != "17" {
		t.Errorf("dlq-offset = %q, want %q", headers["dlq-offset"], "17")
	}
	if headers["dlq-at"] == "" {
		t.Error("dlq-at header is empty; there is no way to tell when this failed")
	}
}

// If the dead-letter write itself fails there is nowhere safe to put the
// message, so the error must surface rather than be swallowed -- the caller
// must not then commit the offset as though the message were handled.
func TestDeadLetterErrorsPropagate(t *testing.T) {
	boom := errors.New("broker unreachable")
	dlq := newTestDeadLetter(&fakeDLQ{err: boom}, "task-events.dlq")

	err := dlq.Send(context.Background(), original(), errors.New("malformed"))
	if err == nil {
		t.Fatal("Send returned nil despite the writer failing")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap %v", err, boom)
	}
}

// The dead-letter topic must never be the topic being consumed. Writing
// failures back onto task-events would loop them forever.
func TestDeadLetterTopicIsNotTheSourceTopic(t *testing.T) {
	cfg := Config{Brokers: []string{"localhost:9092"}, Topic: "task-events", GroupID: "g"}
	if got := DeadLetterTopic(cfg.Topic); got == cfg.Topic {
		t.Fatalf("dead-letter topic = %q, same as the source; failures would loop forever", got)
	}
	if got := DeadLetterTopic("task-events"); got != "task-events.dlq" {
		t.Errorf("DeadLetterTopic(\"task-events\") = %q, want %q", got, "task-events.dlq")
	}
}

// A nil dead-letter sender must not panic -- it is optional, and the service
// still runs without one.
func TestNilDeadLetterIsSafe(t *testing.T) {
	var dlq *DeadLetter
	if err := dlq.Send(context.Background(), original(), errors.New("boom")); err != nil {
		t.Errorf("Send on a nil DeadLetter returned %v, want nil", err)
	}
}

// --- the loop's use of the dead-letter path ---

// Both failure kinds end up in the same place, for different reasons. A poison
// message can never succeed; a transient one has run out of attempts. Either
// way the message must be preserved and the partition must keep moving.
func TestOutcomeForFailedMessage(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantDead    bool
		wantCommit  bool
		description string
	}{
		{
			name:       "poison message is dead-lettered and committed",
			err:        errors.New("bad message: malformed envelope"),
			wantDead:   true,
			wantCommit: true,
		},
		{
			name:       "exhausted transient failure is dead-lettered and committed",
			err:        errors.New("smtp: connection refused"),
			wantDead:   true,
			wantCommit: true,
		},
		{
			name:       "success is committed and not dead-lettered",
			err:        nil,
			wantDead:   false,
			wantCommit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeDLQ{}
			dlq := newTestDeadLetter(f, "task-events.dlq")

			commit, err := resolveMessage(context.Background(), dlq, original(), tt.err)
			if err != nil {
				t.Fatalf("resolveMessage: %v", err)
			}
			if commit != tt.wantCommit {
				t.Errorf("commit = %v, want %v", commit, tt.wantCommit)
			}
			if got := len(f.sent) > 0; got != tt.wantDead {
				t.Errorf("dead-lettered = %v, want %v", got, tt.wantDead)
			}
		})
	}
}

// The assertion the plan singles out. If the dead-letter write fails there is
// nowhere safe to put the message, so the offset must NOT be committed --
// committing anyway would be the silent loss the whole feature exists to stop.
func TestOffsetIsNotCommittedIfDeadLetteringFails(t *testing.T) {
	dlq := newTestDeadLetter(&fakeDLQ{err: errors.New("broker unreachable")}, "task-events.dlq")

	commit, err := resolveMessage(context.Background(), dlq, original(),
		errors.New("smtp refused"))
	if err == nil {
		t.Fatal("expected an error when dead-lettering fails")
	}
	if commit {
		t.Error("offset would be committed even though the message was not preserved anywhere")
	}
}

// Without a dead-letter writer the loop keeps its old behaviour rather than
// wedging: commit and log loudly. Worse than dead-lettering, better than
// blocking the partition forever.
func TestWithoutDeadLetterFailedMessagesStillCommit(t *testing.T) {
	commit, err := resolveMessage(context.Background(), nil, original(),
		errors.New("smtp refused"))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if !commit {
		t.Error("commit = false with no dead-letter writer; the partition would wedge")
	}
}

// The first write to a topic that has never existed fails, because the metadata
// request that triggers auto-creation is answered with UnknownTopicOrPartition
// before the topic is ready. task-service hit this in Stage 4 and got a retry;
// this writer did not, and the omission showed up live: the dead-letter write
// failed, the offset was correctly NOT committed, and the partition stalled
// with the poison messages undeliverable to the one place built to hold them.
//
// The safety property held -- nothing was lost -- but the consumer could not
// make progress again without a restart.
func TestDeadLetterRetriesUnknownTopic(t *testing.T) {
	f := &fakeDLQ{errSeq: []error{kafka.UnknownTopicOrPartition}}
	dlq := newTestDeadLetter(f, "task-events.dlq")

	if err := dlq.Send(context.Background(), original(), errors.New("malformed")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if f.calls != 2 {
		t.Errorf("writer called %d times, want 2 (one failure, one retry)", f.calls)
	}
	if len(f.sent) != 1 {
		t.Errorf("wrote %d messages, want 1 -- a retry must not duplicate", len(f.sent))
	}
}

// A permanent error is not worth retrying, and retrying holds the loop open.
func TestDeadLetterDoesNotRetryPermanentErrors(t *testing.T) {
	f := &fakeDLQ{err: errors.New("message too large")}
	dlq := newTestDeadLetter(f, "task-events.dlq")

	if err := dlq.Send(context.Background(), original(), errors.New("malformed")); err == nil {
		t.Fatal("expected an error")
	}
	if f.calls != 1 {
		t.Errorf("writer called %d times for a permanent error, want 1", f.calls)
	}
}
