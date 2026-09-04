package outbox

import (
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// Defaults for the background poller.
//
// The interval is the notification latency this design trades for durability:
// an event waits up to this long before it reaches the topic. A second is
// invisible for email and keeps the query rate trivial.
const (
	DefaultInterval  = 1 * time.Second
	DefaultBatchSize = 100

	// DefaultRetention is how long a published row is kept before the reaper
	// removes it. Rows are not deleted on success so that "what did we publish,
	// and when" stays answerable for a while after the fact.
	DefaultRetention = 24 * time.Hour

	// reapEvery keeps the delete off the hot path; there is no reason to run it
	// on every tick.
	reapEvery = 5 * time.Minute
)

// rawPublisher is the seam the poller is tested through. *events.Publisher
// satisfies it via PublishRaw.
type rawPublisher interface {
	PublishRaw(ctx context.Context, key string, value []byte) error
}

// Poller publishes outbox rows and marks them sent.
//
// It is the half of the outbox pattern that talks to Kafka, and the only half
// that can fail without consequence: a failed publish leaves the row untouched,
// so the next tick tries again. Nothing is lost by a broker outage, a restart,
// or the process being killed mid-batch.
type Poller struct {
	db        *gorm.DB
	publisher rawPublisher

	Interval  time.Duration
	BatchSize int
	Retention time.Duration
}

func NewPoller(db *gorm.DB, publisher rawPublisher) *Poller {
	return &Poller{
		db:        db,
		publisher: publisher,
		Interval:  DefaultInterval,
		BatchSize: DefaultBatchSize,
		Retention: DefaultRetention,
	}
}

func newTestPoller(db *gorm.DB, publisher rawPublisher) *Poller {
	p := NewPoller(db, publisher)
	p.Interval = time.Millisecond
	return p
}

// Run drains the outbox on a ticker until ctx is cancelled.
//
// Failures are logged and retried on the next tick rather than returned: this
// runs for the life of the process, and a broker that is briefly away is the
// normal case it exists to absorb, not a reason to stop.
func (p *Poller) Run(ctx context.Context) {
	log.Printf("[TASK-SERVICE] outbox poller started; interval=%s batch=%d retention=%s",
		p.Interval, p.BatchSize, p.Retention)

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	reaper := time.NewTicker(reapEvery)
	defer reaper.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[TASK-SERVICE] outbox poller stopped")
			return

		case <-ticker.C:
			n, err := p.drain(ctx)
			if err != nil {
				// Expected whenever Kafka is unreachable. The rows stay put and
				// the next tick retries them, which is the entire point.
				log.Printf("[TASK-SERVICE] outbox drain stopped early after %d rows: %v", n, err)
				continue
			}
			if n > 0 {
				log.Printf("[TASK-SERVICE] outbox published %d events", n)
			}

		case <-reaper.C:
			if n, err := p.reap(ctx); err != nil {
				log.Printf("[TASK-SERVICE] outbox reap: %v", err)
			} else if n > 0 {
				log.Printf("[TASK-SERVICE] outbox reaped %d published rows", n)
			}
		}
	}
}

// drain publishes one batch of unsent rows, oldest first, and returns how many
// were published.
//
// Two properties matter more than anything else here.
//
// **Order.** Rows go out in id order, which is insertion order, which is the
// order the events happened. Combined with the partition key that is what keeps
// a task.updated from overtaking its own task.created.
//
// **Stop at the first failure.** If publishing row 2 fails, row 3 is not
// attempted. Skipping ahead would publish a later event for a task before an
// earlier one -- the exact reordering the single-topic design exists to
// prevent, and it would happen precisely when the broker is flaky, which is
// when nobody is watching closely.
//
// Note there is no transaction around the batch. Holding one open across a
// network round trip per row would keep locks for the length of a Kafka
// outage. A second poller could therefore publish the same row twice, which the
// consumer already deduplicates -- the same duplicate this design accepts by
// construction. Ordering, however, assumes a single poller, which is what runs.
func (p *Poller) drain(ctx context.Context) (int, error) {
	var batch []Record
	err := p.db.WithContext(ctx).
		Where("sent_at IS NULL").
		Order("id ASC").
		Limit(p.BatchSize).
		Find(&batch).Error
	if err != nil {
		// Not reported as "nothing to do": that would look like healthy idling
		// while the outbox quietly filled up.
		return 0, fmt.Errorf("read outbox: %w", err)
	}

	published := 0
	for _, record := range batch {
		if err := p.publisher.PublishRaw(ctx, record.Key, record.Payload); err != nil {
			return published, fmt.Errorf("publish outbox row %d (event %s): %w",
				record.ID, record.EventID, err)
		}

		if err := p.markSent(ctx, record.ID); err != nil {
			// The event is on the topic but the row still says otherwise, so
			// the next tick republishes it. That duplicate is safe -- the
			// consumer dedupes on event_id -- and is strictly better than
			// risking a lost event.
			return published, fmt.Errorf("mark outbox row %d sent: %w", record.ID, err)
		}
		published++
	}

	return published, nil
}

// markSent stamps a row as published. The sent_at IS NULL guard makes the write
// harmless if another poller got there first.
func (p *Poller) markSent(ctx context.Context, id uint) error {
	now := time.Now().UTC()
	return p.db.WithContext(ctx).
		Model(&Record{}).
		Where("id = ? AND sent_at IS NULL", id).
		Updates(map[string]any{
			"sent_at":  now,
			"attempts": gorm.Expr("attempts + 1"),
		}).Error
}

// reap deletes rows published longer ago than the retention window.
//
// The sent_at IS NOT NULL guard is load-bearing: deleting an unsent row would
// discard an event that never reached the topic, silently reintroducing the
// loss this whole package removes.
func (p *Poller) reap(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().Add(-p.Retention)

	result := p.db.WithContext(ctx).
		Where("sent_at IS NOT NULL AND sent_at < ?", cutoff).
		Delete(&Record{})
	if result.Error != nil {
		return 0, fmt.Errorf("reap outbox: %w", result.Error)
	}
	return result.RowsAffected, nil
}
