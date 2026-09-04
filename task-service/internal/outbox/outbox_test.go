package outbox

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"task-management-task-service/internal/events"
	"task-management-task-service/internal/models"
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

func sampleTask() models.Task {
	due := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	creator := uint(7)
	return models.Task{
		ID:          42,
		Title:       "Ship the outbox",
		Description: "atomically",
		ProjectID:   3,
		CreatorID:   &creator,
		AssigneeID:  &creator,
		Status:      "In Progress",
		Priority:    "High",
		Estimate:    "M",
		DueDate:     &due,
	}
}

// Write must participate in the caller's transaction, not open its own. That is
// the entire point: the task row and the event row have to share a fate, and
// they cannot if this reaches past the transaction to a package-level handle.
func TestWriteUsesTheCallersTransaction(t *testing.T) {
	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "outbox"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	err := db.Transaction(func(tx *gorm.DB) error {
		return Write(tx, events.NewTaskCreated(sampleTask(), 7), "task-events")
	})
	if err != nil {
		t.Fatalf("Write inside a transaction: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// The stored payload is the exact bytes that will go on the wire. The poller
// republishes them verbatim and makes no decisions, so anything wrong here is
// wrong forever.
//
// Asserted against newRecord rather than through the driver: sqlmock's matcher
// sees the SQL text but not the bind arguments, so an assertion at that level
// could only check the statement shape and would quietly pass whatever bytes
// the code chose to send.
func TestRecordCarriesTheEnvelopeBytes(t *testing.T) {
	env := events.NewTaskCreated(sampleTask(), 7)

	r, err := newRecord(env, "task-events")
	if err != nil {
		t.Fatalf("newRecord: %v", err)
	}

	if r.EventID != env.EventID {
		t.Errorf("EventID = %q, want %q -- a row must be traceable to its event", r.EventID, env.EventID)
	}
	if r.Key != "42" {
		t.Errorf("Key = %q, want the task ID; the poller uses it as the partition key", r.Key)
	}
	if r.Topic != "task-events" {
		t.Errorf("Topic = %q, want %q", r.Topic, "task-events")
	}

	var round events.Envelope
	if err := json.Unmarshal(r.Payload, &round); err != nil {
		t.Fatalf("stored payload is not a valid envelope: %v", err)
	}
	if round.EventID != env.EventID || round.TaskID != env.TaskID ||
		round.EventType != env.EventType || round.ActorID != env.ActorID {
		t.Errorf("stored payload = %+v, want it to match %+v", round, env)
	}
	if round.Task.Title != env.Task.Title {
		t.Errorf("stored task title = %q, want %q", round.Task.Title, env.Task.Title)
	}
	if round.Changes != nil {
		t.Errorf("changes = %+v on a created event, want absent", round.Changes)
	}
}

// The values really do reach the database, checked as bind arguments since
// PreferSimpleProtocol does not inline them for this statement.
func TestWriteSendsTheValuesToTheDatabase(t *testing.T) {
	db, mock := newMockDB(t)
	env := events.NewTaskCreated(sampleTask(), 7)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "outbox"`).
		WithArgs(env.EventID, "42", "task-events", sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectCommit()

	if err := db.Transaction(func(tx *gorm.DB) error {
		return Write(tx, env, "task-events")
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A new row is unsent. Anything else and the poller would skip it forever.
func TestWrittenRowsStartUnsent(t *testing.T) {
	r, err := newRecord(events.NewTaskCreated(sampleTask(), 7), "task-events")
	if err != nil {
		t.Fatalf("newRecord: %v", err)
	}
	if r.SentAt != nil {
		t.Errorf("SentAt = %v on a new row, want nil", r.SentAt)
	}
	if r.Attempts != 0 {
		t.Errorf("Attempts = %d on a new row, want 0", r.Attempts)
	}
	if r.Key != "42" {
		t.Errorf("Key = %q, want the task ID", r.Key)
	}
}

// An update envelope carries its diff, and the diff has to survive the round
// trip through the database or the change summary email arrives empty.
func TestWritePreservesChanges(t *testing.T) {
	changes := []events.ChangeDetail{{Field: "Status", From: "Not Started", To: "Done"}}
	env := events.NewTaskUpdated(sampleTask(), 7, changes)

	r, err := newRecord(env, "task-events")
	if err != nil {
		t.Fatalf("newRecord: %v", err)
	}

	var round events.Envelope
	if err := json.Unmarshal(r.Payload, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(round.Changes) != 1 || round.Changes[0].Field != "Status" {
		t.Errorf("changes = %+v, want the Status diff", round.Changes)
	}
}

// A failed insert must surface so the surrounding transaction rolls back. If
// this were swallowed the task would commit without its event -- exactly the
// hole this package exists to close.
func TestWriteFailurePropagates(t *testing.T) {
	db, mock := newMockDB(t)

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "outbox"`).WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()

	err := db.Transaction(func(tx *gorm.DB) error {
		return Write(tx, events.NewTaskCreated(sampleTask(), 7), "task-events")
	})
	if err == nil {
		t.Fatal("Write returned nil despite the insert failing")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
