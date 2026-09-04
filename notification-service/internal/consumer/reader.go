package consumer

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// Defaults for a developer running this on the host rather than in Compose.
// Inside the network the broker is kafka:29092, supplied by docker-compose.yml.
const (
	DefaultBrokers = "localhost:9092"
	DefaultTopic   = "task-events"

	// DefaultGroupID is the consumer group name, and it is the single most
	// important setting in this file.
	//
	// A group is how Kafka remembers how far a logical consumer has read. The
	// broker stores a committed offset per group per partition, so restarting
	// this service resumes from where it stopped rather than replaying the
	// topic or silently skipping whatever arrived while it was down. Reading
	// without a group -- a bare partition reader -- would have neither
	// property, and the "stop the consumer, create tasks, start it again, watch
	// them arrive" behaviour that justifies this whole architecture depends
	// entirely on this string staying stable across deploys.
	DefaultGroupID = "notification-service"
)

// Config is resolved from the environment once, at startup.
type Config struct {
	Brokers []string
	Topic   string
	GroupID string
}

// ConfigFromEnv reads KAFKA_BROKERS, KAFKA_TOPIC and KAFKA_GROUP_ID, falling
// back to the defaults above.
func ConfigFromEnv() Config {
	return Config{
		Brokers: splitBrokers(os.Getenv("KAFKA_BROKERS")),
		Topic:   valueOr(os.Getenv("KAFKA_TOPIC"), DefaultTopic),
		GroupID: valueOr(os.Getenv("KAFKA_GROUP_ID"), DefaultGroupID),
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

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

// NewReader builds the group reader. Offsets are committed explicitly by Run,
// not automatically after each read, so that a message is only marked as done
// once the handler has actually finished with it.
//
// Constructing the reader is a side-effecting act: kafka-go immediately starts
// a background goroutine to join the consumer group, and Close blocks until it
// has finished. That is why the settings live in readerConfig, which is pure --
// the tests assert on that and never open a socket, so the default suite still
// needs no broker.
func NewReader(cfg Config) *kafka.Reader {
	return kafka.NewReader(readerConfig(cfg))
}

func readerConfig(cfg Config) kafka.ReaderConfig {
	return kafka.ReaderConfig{
		Brokers: cfg.Brokers,
		Topic:   cfg.Topic,
		GroupID: cfg.GroupID,

		// Only consulted the first time this group ever reads a partition,
		// i.e. when there is no committed offset yet. FirstOffset means a
		// brand-new deployment picks up the backlog already on the topic
		// instead of ignoring everything that happened before it existed.
		StartOffset: kafka.FirstOffset,

		MinBytes: 1,
		MaxBytes: 10e6,

		// Bounded so a quiet topic still lets the loop notice a cancelled
		// context and shut down promptly.
		MaxWait: 2 * time.Second,
	}
}

// Run consumes until ctx is cancelled, returning nil on a clean shutdown.
//
// The loop deliberately owns no decisions: it fetches, hands the bytes to the
// handler, logs the outcome and commits. Everything worth testing lives in
// HandleMessage.
func Run(ctx context.Context, reader *kafka.Reader, h *Handler) error {
	log.Printf("[NOTIFICATION-SERVICE] consuming topic=%q group=%q",
		reader.Config().Topic, reader.Config().GroupID)

	for {
		// FetchMessage rather than ReadMessage: ReadMessage commits the offset
		// as soon as it hands the message over, which would mark an event as
		// done before the handler has looked at it. Fetching and committing
		// separately is what makes delivery at-least-once instead of
		// at-most-once -- a crash between the two replays the message rather
		// than losing it.
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				log.Println("[NOTIFICATION-SERVICE] shutting down, offsets committed")
				return nil
			}
			return err
		}

		res, handleErr := h.HandleMessage(ctx, msg.Value)
		switch {
		case handleErr != nil && errors.Is(handleErr, ErrBadMessage):
			// Poison message. Commit it and move on: leaving it uncommitted
			// would replay it forever and every event behind it on this
			// partition would go unprocessed. Stage 8 routes these to a
			// dead-letter topic instead of only logging them.
			log.Printf("[NOTIFICATION-SERVICE] partition=%d offset=%d DISCARDED: %v",
				msg.Partition, msg.Offset, handleErr)
		case handleErr != nil:
			// Nothing reaches this branch yet -- every failure the handler can
			// produce today is a bad message. It becomes live in Stage 6, when
			// a real SMTP send can fail transiently, and committing here would
			// then be wrong: that is where retry-then-dead-letter goes.
			log.Printf("[NOTIFICATION-SERVICE] partition=%d offset=%d FAILED: %v",
				msg.Partition, msg.Offset, handleErr)
		default:
			log.Printf("[NOTIFICATION-SERVICE] partition=%d offset=%d key=%s %s: %s",
				msg.Partition, msg.Offset, string(msg.Key), res.Action, res.Summary)
		}

		if err := reader.CommitMessages(ctx, msg); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}
