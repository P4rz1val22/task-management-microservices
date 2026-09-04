//go:build integration

// Integration coverage for the publisher against a real broker.
//
// Behind a build tag on purpose: the default `go test ./...` must stay fast and
// need no Docker, so this never runs unless asked for explicitly:
//
//	docker compose up -d kafka
//	cd task-service && go test -tags=integration ./internal/events/
//
// Everything here talks to localhost:9092, the EXTERNAL listener. Containers use
// kafka:29092; a host process that tries that name gets a DNS failure.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

func integrationBrokers(t *testing.T) []string {
	t.Helper()
	if b := os.Getenv("KAFKA_BROKERS"); b != "" {
		return splitBrokers(b)
	}
	return []string{DefaultBrokers}
}

// requireBroker fails fast with a usable message rather than hanging, if the
// stack is not up.
func requireBroker(t *testing.T, brokers []string) {
	t.Helper()
	conn, err := kafka.DialContext(context.Background(), "tcp", brokers[0])
	if err != nil {
		t.Fatalf("no broker at %s (is `docker compose up -d kafka` running?): %v", brokers[0], err)
	}
	conn.Close()
}

// The end-to-end claim of Stages 2 through 4: what the publisher writes is a
// valid envelope, keyed by task ID, and it comes back off the wire intact.
func TestPublishAndReadBack(t *testing.T) {
	brokers := integrationBrokers(t)
	requireBroker(t, brokers)

	topic := fmt.Sprintf("task-events-itest-%d", time.Now().UnixNano())
	t.Setenv("KAFKA_BROKERS", brokers[0])
	t.Setenv("KAFKA_TOPIC", topic)

	p := NewPublisher()
	defer p.Close()

	task := sampleTask()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.PublishTaskCreated(ctx, task, 7); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The message could be on any of the three partitions, so read across all of
	// them rather than guessing which one the hash chose.
	msg, err := readAnyPartition(ctx, brokers, topic)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if got, want := string(msg.Key), strconv.FormatUint(uint64(task.ID), 10); got != want {
		t.Errorf("key on the wire = %q, want %q", got, want)
	}

	var env Envelope
	if err := json.Unmarshal(msg.Value, &env); err != nil {
		t.Fatalf("value is not an envelope: %v", err)
	}
	if env.EventType != EventTaskCreated {
		t.Errorf("event_type = %q, want %q", env.EventType, EventTaskCreated)
	}
	if env.TaskID != task.ID {
		t.Errorf("task_id = %d, want %d", env.TaskID, task.ID)
	}
	if env.ActorID != 7 {
		t.Errorf("actor_id = %d, want 7", env.ActorID)
	}
	if env.EventID == "" {
		t.Error("event_id empty on the wire")
	}
	if env.Changes != nil {
		t.Errorf("changes = %v on a created event, want absent", env.Changes)
	}
	if !samePayload(env.Task, payloadFrom(task)) {
		t.Errorf("task payload round-tripped as %+v, want %+v", env.Task, payloadFrom(task))
	}
}

// The ordering guarantee, proved against a real broker rather than asserted
// against a fake balancer: every event for one task must land on one partition.
// This is the test that would have caught a round-robin balancer.
func TestSameTaskAlwaysSamePartition(t *testing.T) {
	brokers := integrationBrokers(t)
	requireBroker(t, brokers)

	topic := fmt.Sprintf("task-events-itest-%d", time.Now().UnixNano())
	t.Setenv("KAFKA_BROKERS", brokers[0])
	t.Setenv("KAFKA_TOPIC", topic)

	p := NewPublisher()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	task := sampleTask()
	const events = 5
	for i := 0; i < events; i++ {
		if err := p.PublishTaskCreated(ctx, task, uint(i)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	counts, err := countByPartition(ctx, brokers, topic, events)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	occupied := 0
	for _, n := range counts {
		if n > 0 {
			occupied++
		}
	}
	if occupied != 1 {
		t.Errorf("%d events for one task spread over %d partitions (%v); "+
			"they must all share one or ordering is not guaranteed", events, occupied, counts)
	}
}

// A first publish against a topic that has never existed must succeed. Without
// the retry in publish(), the auto-creation metadata request is answered with
// UnknownTopicOrPartition and this fails -- which is exactly how the bug showed
// up on a fresh stack, losing one event per environment.
func TestFirstPublishToNewTopicSucceeds(t *testing.T) {
	brokers := integrationBrokers(t)
	requireBroker(t, brokers)

	topic := fmt.Sprintf("task-events-coldstart-%d", time.Now().UnixNano())
	t.Setenv("KAFKA_BROKERS", brokers[0])
	t.Setenv("KAFKA_TOPIC", topic)

	p := NewPublisher()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.PublishTaskCreated(ctx, sampleTask(), 7); err != nil {
		t.Fatalf("first publish to a brand-new topic failed: %v", err)
	}
}

// readAnyPartition returns the first message found on any partition of topic.
func readAnyPartition(ctx context.Context, brokers []string, topic string) (kafka.Message, error) {
	type result struct {
		msg kafka.Message
		err error
	}
	results := make(chan result, 3)

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for partition := 0; partition < 3; partition++ {
		go func(partition int) {
			r := kafka.NewReader(kafka.ReaderConfig{
				Brokers:   brokers,
				Topic:     topic,
				Partition: partition,
				MinBytes:  1,
				MaxBytes:  10e6,
			})
			defer r.Close()
			msg, err := r.ReadMessage(readCtx)
			results <- result{msg: msg, err: err}
		}(partition)
	}

	for i := 0; i < 3; i++ {
		res := <-results
		if res.err == nil {
			return res.msg, nil
		}
	}
	return kafka.Message{}, fmt.Errorf("no message on any partition of %s", topic)
}

// countByPartition reads up to want messages and reports how many landed on each
// of the three partitions.
func countByPartition(ctx context.Context, brokers []string, topic string, want int) ([3]int, error) {
	var counts [3]int

	readCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	done := make(chan [2]int, 3)
	for partition := 0; partition < 3; partition++ {
		go func(partition int) {
			r := kafka.NewReader(kafka.ReaderConfig{
				Brokers:   brokers,
				Topic:     topic,
				Partition: partition,
				MinBytes:  1,
				MaxBytes:  10e6,
			})
			defer r.Close()

			n := 0
			for {
				if _, err := r.ReadMessage(readCtx); err != nil {
					break
				}
				n++
				if n >= want {
					break
				}
			}
			done <- [2]int{partition, n}
		}(partition)
	}

	total := 0
	for i := 0; i < 3; i++ {
		res := <-done
		counts[res[0]] = res[1]
		total += res[1]
	}
	if total < want {
		return counts, fmt.Errorf("read %d of %d messages", total, want)
	}
	return counts, nil
}
