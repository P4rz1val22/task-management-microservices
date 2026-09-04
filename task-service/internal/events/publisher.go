package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
			// KAFKA_NUM_PARTITIONS: 3, so a first publish against a fresh
			// stack succeeds instead of failing on an unknown topic.
			AllowAutoTopicCreation: true,
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

	if err := p.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("publish %s event for task %d: %w", env.EventType, env.TaskID, err)
	}
	return nil
}

// Close releases the underlying writer. It is a no-op for a writer that does
// not need closing, which keeps test fakes free of ceremony.
func (p *Publisher) Close() error {
	if c, ok := p.writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
