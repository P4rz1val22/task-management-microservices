package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type published struct {
	key   string
	value []byte
}

// fakePublisher stands in for *events.Publisher so the poller can be tested
// with no broker.
type fakePublisher struct {
	sent []published

	// failOn makes the nth call (1-based) fail, modelling a broker that goes
	// away partway through a batch.
	failOn int
	calls  int
	err    error
}

func (f *fakePublisher) PublishRaw(_ context.Context, key string, value []byte) error {
	f.calls++
	if f.failOn > 0 && f.calls == f.failOn {
		if f.err != nil {
			return f.err
		}
		return errors.New("broker unreachable")
	}
	f.sent = append(f.sent, published{key: key, value: value})
	return nil
}

func outboxRows(ids ...uint) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{
		"id", "event_id", "key", "topic", "payload", "created_at", "sent_at", "attempts",
	})
	for _, id := range ids {
		rows.AddRow(id, "evt-"+itoa(id), itoa(id), "task-events",
			[]byte(`{"event_id":"evt-`+itoa(id)+`"}`),
			time.Now(), nil, 0)
	}
	return rows
}

func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func expectSelect(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`SELECT \* FROM "outbox"`).WillReturnRows(rows)
}

// expectMarkSent queues the stamp that marks a row published. GORM wraps
// writes in a transaction by default, so this is BEGIN/UPDATE/COMMIT rather
// than a bare exec.
func expectMarkSent(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "outbox" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// Ordering is the whole reason the outbox has an autoincrementing id. Events
// for one task must reach the topic in the order they were recorded, or a
// task.updated can overtake its own task.created.
func TestDrainPublishesInIDOrder(t *testing.T) {
	db, mock := newMockDB(t)
	expectSelect(mock, outboxRows(1, 2, 3))
	expectMarkSent(mock)
	expectMarkSent(mock)
	expectMarkSent(mock)

	pub := &fakePublisher{}
	n, err := newTestPoller(db, pub).drain(context.Background())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 3 {
		t.Errorf("drained %d rows, want 3", n)
	}
	if len(pub.sent) != 3 {
		t.Fatalf("published %d messages, want 3", len(pub.sent))
	}
	for i, want := range []string{"1", "2", "3"} {
		if pub.sent[i].key != want {
			t.Errorf("message %d key = %q, want %q -- rows must publish in id order",
				i, pub.sent[i].key, want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The stored bytes go out untouched. The poller forwards; it does not decide.
func TestDrainPublishesTheStoredBytes(t *testing.T) {
	db, mock := newMockDB(t)
	expectSelect(mock, outboxRows(7))
	expectMarkSent(mock)

	pub := &fakePublisher{}
	if _, err := newTestPoller(db, pub).drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := string(pub.sent[0].value); got != `{"event_id":"evt-7"}` {
		t.Errorf("published %q, want the stored payload verbatim", got)
	}
}

// The assertion that makes the whole design honest. If the broker goes away
// partway through a batch, everything after the failure must stay unpublished
// -- publishing row 3 after row 2 failed would let a later event for a task
// overtake an earlier one, which is exactly what the single-topic design exists
// to prevent.
func TestDrainStopsAtTheFirstFailure(t *testing.T) {
	db, mock := newMockDB(t)
	expectSelect(mock, outboxRows(1, 2, 3))
	expectMarkSent(mock) // row 1 only

	pub := &fakePublisher{failOn: 2}
	_, err := newTestPoller(db, pub).drain(context.Background())
	if err == nil {
		t.Fatal("drain returned nil despite a publish failing")
	}

	if len(pub.sent) != 1 {
		t.Fatalf("published %d messages, want 1 -- the batch must stop at the failure", len(pub.sent))
	}
	if pub.sent[0].key != "1" {
		t.Errorf("published key %q, want %q", pub.sent[0].key, "1")
	}
	if pub.calls != 2 {
		t.Errorf("publisher called %d times, want 2 (row 1, then row 2 which failed); "+
			"row 3 must not be attempted", pub.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A failed row stays unsent so the next tick retries it. Marking it sent anyway
// would lose the event, which is the bug this package was built to remove.
func TestFailedRowIsNotMarkedSent(t *testing.T) {
	db, mock := newMockDB(t)
	expectSelect(mock, outboxRows(1))
	// No ExpectExec: any UPDATE at all fails the expectation check below.

	pub := &fakePublisher{failOn: 1}
	if _, err := newTestPoller(db, pub).drain(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a row that failed to publish was still marked sent: %v", err)
	}
}

// An empty outbox is the normal case and must not error or publish.
func TestDrainWithNothingToDo(t *testing.T) {
	db, mock := newMockDB(t)
	expectSelect(mock, outboxRows())

	pub := &fakePublisher{}
	n, err := newTestPoller(db, pub).drain(context.Background())
	if err != nil {
		t.Fatalf("drain on an empty outbox: %v", err)
	}
	if n != 0 {
		t.Errorf("drained %d rows from an empty outbox, want 0", n)
	}
	if len(pub.sent) != 0 {
		t.Errorf("published %d messages from an empty outbox", len(pub.sent))
	}
}

// A database failure must surface rather than be mistaken for an empty outbox,
// which would look like healthy idling while nothing was being published.
func TestDrainPropagatesSelectErrors(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT \* FROM "outbox"`).
		WillReturnError(errors.New("connection refused"))

	if _, err := newTestPoller(db, &fakePublisher{}).drain(context.Background()); err == nil {
		t.Fatal("drain returned nil despite the select failing")
	}
}

// The reaper must only ever delete rows that were published. Deleting an unsent
// row would discard an event that had not gone anywhere.
func TestReapDeletesOnlySentRows(t *testing.T) {
	var queries []string
	db, mock := recordingMockDB(t, &queries)

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM "outbox"`).WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectCommit()

	n, err := newTestPoller(db, &fakePublisher{}).reap(context.Background())
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 5 {
		t.Errorf("reaped %d rows, want 5", n)
	}

	joined := ""
	for _, q := range queries {
		joined += q + "\n"
	}
	if !containsAll(joined, "sent_at IS NOT NULL") {
		t.Errorf("the reaper does not restrict itself to sent rows:\n%s", joined)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for i := 0; i+len(n) <= len(haystack); i++ {
			if haystack[i:i+len(n)] == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// recordingMockDB captures the SQL that actually reaches the driver, which is
// the only way to assert on the shape of a query GORM builds.
func recordingMockDB(t *testing.T, queries *[]string) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		*queries = append(*queries, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	gdb, err := gorm.Open(
		postgres.New(postgres.Config{Conn: raw, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open over sqlmock: %v", err)
	}
	return gdb, mock
}
