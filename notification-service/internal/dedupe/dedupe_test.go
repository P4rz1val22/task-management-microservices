package dedupe

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
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

func TestSeenReportsAKnownEvent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "processed_events"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	seen, err := NewStore(db).Seen(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if !seen {
		t.Error("Seen = false for an event already in the table")
	}
}

func TestSeenReportsAnUnknownEvent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "processed_events"`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	seen, err := NewStore(db).Seen(context.Background(), "evt-1")
	if err != nil {
		t.Fatalf("Seen: %v", err)
	}
	if seen {
		t.Error("Seen = true for an event that has never been handled")
	}
}

// A database failure must not read as "never seen". Treating an outage as
// "unseen" would send duplicates during exactly the moment things are already
// going wrong.
func TestSeenPropagatesDatabaseErrors(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "processed_events"`).
		WillReturnError(errors.New("connection refused"))

	if _, err := NewStore(db).Seen(context.Background(), "evt-1"); err == nil {
		t.Fatal("Seen returned nil error despite the database failing")
	}
}

// Recording must tolerate the row already existing. Two consumers racing on the
// same event, or a retry after a partial failure, must not turn into an error
// that fails a message which was in fact handled successfully.
func TestMarkProcessedIsItselfIdempotent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "processed_events".*ON CONFLICT DO NOTHING`).
		WillReturnResult(sqlmock.NewResult(0, 0)) // zero rows: it already existed
	mock.ExpectCommit()

	if err := NewStore(db).MarkProcessed(context.Background(), "evt-1"); err != nil {
		t.Errorf("MarkProcessed on an existing row returned %v, want nil", err)
	}
}

func TestMarkProcessedWritesTheEventID(t *testing.T) {
	var queries []string
	db, mock := recordingMockDB(t, &queries)

	mock.ExpectBegin()
	// The event id travels as a bind parameter, not inlined into the SQL, so
	// it is asserted here rather than by searching the query text.
	mock.ExpectExec(`INSERT INTO "processed_events"`).
		WithArgs("evt-abc", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := NewStore(db).MarkProcessed(context.Background(), "evt-abc"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}

	joined := ""
	for _, q := range queries {
		joined += q + "\n"
	}
	// ON CONFLICT is what makes the insert safe to repeat. Without it a
	// redelivery that raced past Seen would fail on the primary key, be
	// reported as a processing failure, and be retried forever.
	if !contains(joined, "ON CONFLICT") {
		t.Errorf("insert has no ON CONFLICT clause:\n%s", joined)
	}
	if !contains(joined, `"processed_events"`) {
		t.Errorf("insert does not target processed_events:\n%s", joined)
	}
}

func TestMarkProcessedPropagatesDatabaseErrors(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "processed_events"`).
		WillReturnError(errors.New("connection refused"))
	mock.ExpectRollback()

	if err := NewStore(db).MarkProcessed(context.Background(), "evt-1"); err == nil {
		t.Fatal("MarkProcessed returned nil despite the database failing")
	}
}

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

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
