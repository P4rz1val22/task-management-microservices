package consumer

import (
	"testing"

	"github.com/segmentio/kafka-go"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "kafka:29092")
	t.Setenv("KAFKA_TOPIC", "task-events")
	t.Setenv("KAFKA_GROUP_ID", "notification-service")

	cfg := ConfigFromEnv()
	if len(cfg.Brokers) != 1 || cfg.Brokers[0] != "kafka:29092" {
		t.Errorf("brokers = %v, want [kafka:29092]", cfg.Brokers)
	}
	if cfg.Topic != "task-events" {
		t.Errorf("topic = %q, want %q", cfg.Topic, "task-events")
	}
	if cfg.GroupID != "notification-service" {
		t.Errorf("group = %q, want %q", cfg.GroupID, "notification-service")
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "")
	t.Setenv("KAFKA_TOPIC", "")
	t.Setenv("KAFKA_GROUP_ID", "")

	cfg := ConfigFromEnv()
	if len(cfg.Brokers) != 1 || cfg.Brokers[0] != DefaultBrokers {
		t.Errorf("brokers = %v, want [%s]", cfg.Brokers, DefaultBrokers)
	}
	if cfg.Topic != DefaultTopic {
		t.Errorf("topic = %q, want %q", cfg.Topic, DefaultTopic)
	}
	if cfg.GroupID != DefaultGroupID {
		t.Errorf("group = %q, want %q", cfg.GroupID, DefaultGroupID)
	}
}

// Without a GroupID, kafka-go gives you a bare partition reader: no committed
// offsets, so a restart either replays the whole topic or skips whatever arrived
// while the service was down. Every resilience claim this service makes rests on
// this field being set, and nothing else in the code would fail without it.
func TestReaderUsesAConsumerGroup(t *testing.T) {
	rc := readerConfig(Config{
		Brokers: []string{"localhost:9092"},
		Topic:   "task-events",
		GroupID: DefaultGroupID,
	})

	if got := rc.GroupID; got != DefaultGroupID {
		t.Errorf("GroupID = %q, want %q -- without it offsets are never committed", got, DefaultGroupID)
	}
}

// The group ID is effectively persistent state: the broker keys committed
// offsets on this exact string. Renaming it in a later refactor would silently
// reset the service to the beginning of the topic and re-notify every task ever
// created.
func TestGroupIDIsStable(t *testing.T) {
	if DefaultGroupID != "notification-service" {
		t.Errorf("DefaultGroupID = %q; changing it resets committed offsets and "+
			"replays the entire topic", DefaultGroupID)
	}
}

// A brand-new deployment should pick up what is already on the topic rather than
// ignoring everything that happened before it existed. This only applies the
// first time the group reads a partition; afterwards the committed offset wins.
func TestReaderStartsFromFirstOffset(t *testing.T) {
	rc := readerConfig(Config{
		Brokers: []string{"localhost:9092"},
		Topic:   "task-events",
		GroupID: DefaultGroupID,
	})

	if got := rc.StartOffset; got != kafka.FirstOffset {
		t.Errorf("StartOffset = %d, want kafka.FirstOffset (%d)", got, kafka.FirstOffset)
	}
}

func TestSplitBrokersHandlesLists(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"kafka:29092", []string{"kafka:29092"}},
		{"a:9092,b:9092", []string{"a:9092", "b:9092"}},
		{" a:9092 , b:9092 ", []string{"a:9092", "b:9092"}},
		{",,", []string{DefaultBrokers}},
		{"", []string{DefaultBrokers}},
	}

	for _, tt := range tests {
		got := splitBrokers(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitBrokers(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitBrokers(%q) = %v, want %v", tt.in, got, tt.want)
				break
			}
		}
	}
}
