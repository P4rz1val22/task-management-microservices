package consumer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

// Retry policy for a dead-letter write.
//
// Identical in purpose to the one in task-service's publisher, and added for
// the identical reason: with auto-creation on, the first write to a topic that
// has never existed sends a metadata request that triggers the creation and is
// then answered with UnknownTopicOrPartition, because the topic is not ready
// when the broker builds that response. kafka-go raises it upstream of its own
// produce retries, so it reaches the caller unretried.
//
// It bit here in a more interesting way than it did there. The failed write
// correctly stopped the offset being committed, so nothing was lost -- but the
// poison messages could not reach the one place built to hold them, and the
// partition stalled until the service was restarted. The safety property held
// and liveness did not.
const (
	maxDeadLetterAttempts = 3
	deadLetterRetryDelay  = 100 * time.Millisecond
)

// DeadLetterSuffix is appended to the source topic to name its dead-letter
// topic. Keeping it derived rather than separately configurable means the two
// can never be pointed at the same topic by a typo -- which would loop failures
// back into the consumer forever.
const DeadLetterSuffix = ".dlq"

// DeadLetterTopic returns the dead-letter topic for a source topic.
func DeadLetterTopic(sourceTopic string) string {
	return sourceTopic + DeadLetterSuffix
}

// dlqWriter is the seam the tests use; *kafka.Writer satisfies it.
type dlqWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// DeadLetter publishes messages this consumer could not process.
//
// It is what turns "commit the offset and lose the event" into "set the event
// aside and keep going". Before it existed the loop faced a genuine dilemma:
// committing a failed message loses it, and not committing blocks every event
// behind it on that partition. A dead-letter topic is the option that does
// neither -- the message is durably kept somewhere else, so the offset can be
// committed honestly.
//
// Note this is the notification-service's first *producer*. Until now it only
// ever read.
type DeadLetter struct {
	writer dlqWriter
	topic  string
}

// NewDeadLetter builds a writer for the dead-letter topic derived from cfg.
func NewDeadLetter(cfg Config) *DeadLetter {
	topic := DeadLetterTopic(cfg.Topic)
	return &DeadLetter{
		topic: topic,
		writer: &kafka.Writer{
			Addr:  kafka.TCP(cfg.Brokers...),
			Topic: topic,

			// Same reasoning as the producer in task-service: the zero-value
			// balancer is round-robin and ignores the key, and a dead-lettered
			// message should keep landing with its siblings.
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			Async:        false,

			// The dead-letter topic is written to rarely and read from by hand,
			// so it is never pre-created. Auto-creation on first failure is the
			// difference between a recorded failure and a lost one.
			AllowAutoTopicCreation: true,

			// kafka-go defaults this to a full second and this writer sends one
			// message at a time. See the same setting in task-service.
			BatchTimeout: 10 * time.Millisecond,
		},
	}
}

// newTestDeadLetter wires a DeadLetter onto a fake writer.
func newTestDeadLetter(w dlqWriter, topic string) *DeadLetter {
	return &DeadLetter{writer: w, topic: topic}
}

// Send publishes msg to the dead-letter topic, annotated with why it failed and
// where it came from.
//
// A nil receiver is a no-op, so the dead-letter path stays optional and a
// service running without one does not panic.
func (d *DeadLetter) Send(ctx context.Context, msg kafka.Message, cause error) error {
	if d == nil || d.writer == nil {
		return nil
	}

	reason := "unknown"
	if cause != nil {
		reason = cause.Error()
	}

	// The original key and value travel untouched -- a dead-lettered message
	// must be replayable byte for byte once whatever broke is fixed. Everything
	// diagnostic goes in headers instead, so nothing is added to the payload
	// that a replay would have to strip back out.
	dead := kafka.Message{
		Key:   msg.Key,
		Value: msg.Value,
		Headers: []kafka.Header{
			{Key: "dlq-error", Value: []byte(reason)},
			{Key: "dlq-topic", Value: []byte(msg.Topic)},
			{Key: "dlq-partition", Value: []byte(strconv.Itoa(msg.Partition))},
			{Key: "dlq-offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
			{Key: "dlq-at", Value: []byte(time.Now().UTC().Format(time.RFC3339))},
		},
	}

	var err error
	for attempt := 0; attempt < maxDeadLetterAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(deadLetterRetryDelay):
			}
		}

		if err = d.writer.WriteMessages(ctx, dead); err == nil {
			return nil
		}
		if !isRetryable(err) {
			break
		}
	}

	// Deliberately surfaced. If this fails there is nowhere safe to put the
	// message, and the caller must NOT then commit the offset as though it had
	// been handled -- that would be the silent loss this whole type exists to
	// prevent.
	return fmt.Errorf("dead-letter %s partition %d offset %d: %w",
		msg.Topic, msg.Partition, msg.Offset, err)
}

// isRetryable reports whether another attempt could plausibly succeed. kafka-go
// classifies its own protocol errors, and a permanent one only wastes time.
func isRetryable(err error) bool {
	var kerr kafka.Error
	if errors.As(err, &kerr) {
		return kerr.Temporary()
	}
	return false
}

// Close releases the underlying writer.
func (d *DeadLetter) Close() error {
	if d == nil {
		return nil
	}
	if c, ok := d.writer.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
