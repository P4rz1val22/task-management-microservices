// Package dedupe remembers which events have already been acted on.
//
// It exists because Kafka delivers at least once, and that is a deliberate
// choice rather than a shortcoming: the consumer commits its offset only after
// handling a message, so a crash in between replays the message instead of
// losing it. The cost of never losing an event is occasionally seeing one
// twice, and without a memory of what has already been done, "twice" means a
// second email landing in someone's inbox.
//
// The redelivery paths this actually closes are ordinary, not exotic: a failed
// offset commit, a consumer group rebalance, a container restart mid-batch, and
// -- once the transactional outbox lands -- a poller that published a row and
// died before marking it sent. That last one is why this is built first: the
// outbox produces duplicates by design, and is only safe on top of this.
package dedupe

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ProcessedEvent is one handled event. The table is keyed on the event ID that
// every envelope has carried since Stage 2, put there for exactly this.
//
// A primary key rather than a plain column: the uniqueness has to be enforced
// by the database, not by the check in Seen. Two consumers racing on the same
// event would both see "not seen" and both proceed, and only a constraint can
// stop that becoming two rows.
type ProcessedEvent struct {
	EventID     string    `gorm:"column:event_id;primaryKey"`
	ProcessedAt time.Time `gorm:"column:processed_at;autoCreateTime"`
}

func (ProcessedEvent) TableName() string { return "processed_events" }

// Store records and queries handled events.
type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Migrate creates the table if it is absent.
//
// This service owns this table outright -- nothing else reads or writes it --
// which is why migrating it here does not repeat the four-way AutoMigrate race
// documented in CLAUDE.md. That race is four services fighting over shared
// tables; this is one service creating its own.
func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(&ProcessedEvent{}); err != nil {
		return fmt.Errorf("migrate processed_events: %w", err)
	}
	return nil
}

// Seen reports whether eventID has already been handled.
//
// An error here is never reported as "not seen". Treating a database outage as
// "never handled" would send duplicates during precisely the moment things are
// already going wrong, which is the opposite of what this package is for.
func (s *Store) Seen(ctx context.Context, eventID string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).
		Model(&ProcessedEvent{}).
		Where("event_id = ?", eventID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check processed event %s: %w", eventID, err)
	}
	return count > 0, nil
}

// MarkProcessed records that eventID has been handled.
//
// ON CONFLICT DO NOTHING, so recording the same event twice is not an error.
// Without it a redelivery that raced past Seen would fail on the primary key
// and be reported as a processing failure, which would then be retried, which
// would fail again -- turning a harmless duplicate into a stuck message.
func (s *Store) MarkProcessed(ctx context.Context, eventID string) error {
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&ProcessedEvent{EventID: eventID}).Error
	if err != nil {
		return fmt.Errorf("record processed event %s: %w", eventID, err)
	}
	return nil
}
