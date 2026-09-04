// Package outbox makes a task change and its event a single atomic fact.
//
// Before this, task-service did two things that were not one thing: it committed
// the task, then published the event. A crash between them -- or an unreachable
// broker -- left the task existing and the event never happening, with nothing
// anywhere that could replay it. Retrying could not fix that, because the failure
// can be the process disappearing, and no code runs after a crash.
//
// The fix is to write the event into a table in the same database transaction as
// the task itself. The two rows commit together or neither exists. Publishing
// then becomes a separate concern with its own retry loop -- one that a later
// run of a different process can pick up, which is the property that actually
// matters.
//
// The trade this accepts, deliberately: the poller publishes a row and then marks
// it sent, so a crash between those two republishes the event. That is why the
// consumer's deduplication had to land first. An outbox produces duplicates by
// design; it is only safe on top of an idempotent consumer.
package outbox

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"task-management-task-service/internal/events"
)

// Record is one event waiting to be published, or already published.
//
// Payload holds the exact bytes that go on the wire. The poller republishes them
// verbatim and makes no decisions about their content, which keeps envelope
// construction and diffing on the handler side where they are already tested.
type Record struct {
	ID      uint   `gorm:"primaryKey"`
	EventID string `gorm:"column:event_id;not null;index"`

	// Key is the Kafka partition key -- the task ID as a string. Carried
	// explicitly rather than re-derived from the payload so the poller never
	// has to parse what it is forwarding.
	Key   string `gorm:"column:key;not null"`
	Topic string `gorm:"column:topic;not null"`

	Payload []byte `gorm:"column:payload;type:bytea;not null"`

	CreatedAt time.Time  `gorm:"column:created_at;autoCreateTime"`
	SentAt    *time.Time `gorm:"column:sent_at;index"`

	// Attempts exists for visibility when something is stuck. Nothing branches
	// on it: a row is retried until it succeeds, because giving up would
	// reintroduce the silent loss this package removes.
	Attempts int `gorm:"column:attempts;not null;default:0"`
}

func (Record) TableName() string { return "outbox" }

// Migrate creates the table if it is absent.
//
// task-service owns this table outright, so creating it here is not the
// four-way AutoMigrate race documented in CLAUDE.md -- that race is four
// services fighting over shared tables, this is one service creating its own.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&Record{}); err != nil {
		return fmt.Errorf("migrate outbox: %w", err)
	}
	return nil
}

// newRecord turns an envelope into a row, marshalling the payload once.
func newRecord(env events.Envelope, topic string) (Record, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return Record{}, fmt.Errorf("marshal %s event for task %d: %w",
			env.EventType, env.TaskID, err)
	}

	return Record{
		EventID: env.EventID,
		Key:     strconv.FormatUint(uint64(env.TaskID), 10),
		Topic:   topic,
		Payload: payload,
		// SentAt stays nil and Attempts stays zero: a new row is unpublished by
		// definition, and anything else would have the poller skip it forever.
	}, nil
}

// Write appends an event to the outbox inside the caller's transaction.
//
// It takes a *gorm.DB rather than reaching for the package-level handle, and
// that is the whole design in one parameter: if this opened its own connection
// the two writes could not share a fate, and the gap this package closes would
// still be open. Callers must pass the tx from database.DB.Transaction.
//
// A failure here must propagate so the surrounding transaction rolls back.
// Swallowing it would commit a task with no event -- precisely the bug.
func Write(tx *gorm.DB, env events.Envelope, topic string) error {
	record, err := newRecord(env, topic)
	if err != nil {
		return err
	}
	if err := tx.Create(&record).Error; err != nil {
		return fmt.Errorf("write outbox row for event %s: %w", env.EventID, err)
	}
	return nil
}
