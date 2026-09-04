package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"

	"task-management-task-service/internal/models"
)

// Defaults used when KAFKA_BROKERS or KAFKA_TOPIC is unset or empty.
//
// DefaultBrokers is the host listener, not the compose-network one, because the
// only context in which the environment is missing is a developer running
// task-service directly on their machine. Inside compose, KAFKA_BROKERS is set
// to kafka:29092.
const (
	DefaultBrokers = "localhost:9092"
	DefaultTopic   = "task-events"
)

// Retry policy for a single publish.
//
// This exists for one specific, reproducible failure. With topic auto-creation
// on, the very first write against a fresh broker sends a metadata request that
// triggers the creation and is then answered with UnknownTopicOrPartition,
// because the topic does not exist yet at the moment the broker builds the
// response. kafka-go raises that from Writer.partitions, upstream of its own
// produce-path retries, so it reaches the caller unretried. The topic does get
// created, which is why a second attempt a moment later succeeds -- and why
// without this loop exactly one event is lost per fresh environment, quietly.
const (
	maxPublishAttempts = 3
	publishRetryDelay  = 100 * time.Millisecond
)

// messageWriter is the seam this package is tested through. *kafka.Writer
// already satisfies it, so production passes the real writer and tests pass a
// fake that records messages in a slice -- no broker, no network, no docker in
// the unit test path.
type messageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Publisher turns task events into Kafka messages. It is safe for concurrent
// use: *kafka.Writer is, and Publisher adds no mutable state of its own.
type Publisher struct {
	writer messageWriter
}

// NewPublisher builds a Publisher over a real Kafka writer, reading its
// configuration from the environment once, here. Publish must not consult the
// environment again -- config that can move at runtime turns a typo into a
// mystery that only surfaces on the first event.
func NewPublisher() *Publisher {
	brokers := splitBrokers(os.Getenv("KAFKA_BROKERS"))
	topic := os.Getenv("KAFKA_TOPIC")
	if topic == "" {
		topic = DefaultTopic
	}

	return &Publisher{
		writer: &kafka.Writer{
			Addr:  kafka.TCP(brokers...),
			Topic: topic,

			// Hash, explicitly. kafka-go's zero-value balancer is
			// round-robin, which ignores the key entirely and would scatter
			// one task's events across all three partitions -- destroying the
			// per-task ordering the whole single-topic design rests on.
			Balancer: &kafka.Hash{},

			// Every in-sync replica must acknowledge before WriteMessages
			// returns. There is one broker locally, so this costs nothing
			// today, but it is the setting that makes "the event is durably
			// logged" true rather than hopeful.
			RequiredAcks: kafka.RequireAll,

			// Synchronous on purpose. An async writer returns nil immediately
			// and reports failures to a callback, which would make the error
			// this package returns meaningless -- and Stage 4's handler needs
			// a truthful error to decide (deliberately) to ignore it.
			Async: false,

			// The topic is auto-created by the broker with
			// KAFKA_NUM_PARTITIONS: 3. Note that this alone is not enough for
			// the first write to land -- see the retry constants above.
			AllowAutoTopicCreation: true,

			// kafka-go holds a synchronous write open for BatchTimeout waiting
			// for more messages to batch with, and defaults that to one full
			// second. This service publishes one message per request, so that
			// default is paid in full on every create. 10ms keeps the batching
			// machinery intact while making the wait invisible.
			BatchTimeout: 10 * time.Millisecond,
		},
	}
}

// splitBrokers parses the comma-separated KAFKA_BROKERS list, tolerating stray
// whitespace and empty entries.
func splitBrokers(raw string) []string {
	out := make([]string, 0, 1)
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return []string{DefaultBrokers}
	}
	return out
}

// PublishTaskCreated publishes a task.created event for a task that has just
// been committed.
//
// The error is returned, never logged and swallowed. Whether a failed publish
// should fail the request is the caller's decision, not this layer's -- and the
// answer for the create handler is no, but that belongs in the handler where a
// reader can see it.
func (p *Publisher) PublishTaskCreated(ctx context.Context, task models.Task, actorID uint) error {
	return p.publish(ctx, NewTaskCreated(task, actorID))
}

// PublishTaskUpdated publishes a task.updated event carrying the diff.
//
// Callers must not call this with an empty changes slice: an update that
// changed nothing should produce no event at all. That decision belongs to the
// handler, where a reader can see it, not to this layer.
func (p *Publisher) PublishTaskUpdated(ctx context.Context, task models.Task, actorID uint, changes []ChangeDetail) error {
	return p.publish(ctx, NewTaskUpdated(task, actorID, changes))
}

// PublishTaskDeleted publishes a task.deleted event.
//
// Published after the row is deleted, so the envelope's copy of the task is the
// only description of it a consumer can still get.
func (p *Publisher) PublishTaskDeleted(ctx context.Context, task models.Task, actorID uint) error {
	return p.publish(ctx, NewTaskDeleted(task, actorID))
}

func (p *Publisher) publish(ctx context.Context, env Envelope) error {
	value, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal %s event for task %d: %w", env.EventType, env.TaskID, err)
	}

	msg := kafka.Message{
		// The partition key, and the reason events for one task stay ordered.
		// It is the task ID and nothing else: anything per-event mixed in here
		// (an event ID, a timestamp) would spread a single task across
		// partitions and let its update overtake its own creation.
		Key:   []byte(strconv.FormatUint(uint64(env.TaskID), 10)),
		Value: value,
	}

	for attempt := 0; attempt < maxPublishAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("publish %s event for task %d: %w",
					env.EventType, env.TaskID, ctx.Err())
			case <-time.After(publishRetryDelay):
			}
		}

		if err = p.writer.WriteMessages(ctx, msg); err == nil {
			return nil
		}
		if !isRetryable(err) {
			break
		}
	}

	return fmt.Errorf("publish %s event for task %d: %w", env.EventType, env.TaskID, err)
}

// isRetryable reports whether another attempt could plausibly succeed. kafka-go
// classifies its protocol errors itself, and a permanent one (a malformed
// message, say) only wastes the caller's deadline on retries that cannot work.
func isRetryable(err error) bool {
	var kerr kafka.Error
	if errors.As(err, &kerr) {
		return kerr.Temporary()
	}
	return false
}

// Close releases the underlying writer. It is a no-op for a writer that does
// not need closing, which keeps test fakes free of ceremony.
func (p *Publisher) Close() error {
	if c, ok := p.writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
