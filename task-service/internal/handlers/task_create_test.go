package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const validCreateBody = `{
	"title": "Ship the event system",
	"description": "publish on create",
	"project_id": 3,
	"status": "In Progress",
	"priority": "High",
	"estimate": "M"
}`

// The assigned ID the mock hands back through RETURNING "id".
const createdTaskID = uint(42)

// The task and its event are one atomic fact. sqlmock matches expectations in
// order, so this is a genuine assertion about the shape of the transaction:
// BEGIN, the task, the event, COMMIT. A handler that published outside the
// transaction, or wrote the event first, or skipped it, fails here.
func TestCreateTaskWritesTaskAndEventInOneTransaction(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnRows(mockIDRow(createdTaskID))
	expectOutboxInsert(mock)
	mock.ExpectCommit()

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the task and its event were not written together: %v", err)
	}
}

// The headline assertion of Stage 8b, and the direct successor to Stage 4's
// "a publish failure still returns 201".
//
// The guarantee moved rather than disappeared. Before, the event was published
// after the commit, so a broker outage lost it silently while the caller was
// told everything was fine. Now the event is a row in the same transaction: if
// it cannot be written the task is not created either, and the caller is told.
// Sharing a fate is the only way a crash cannot separate them.
func TestCreateTaskRollsBackWhenTheEventCannotBeRecorded(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnRows(mockIDRow(createdTaskID))
	expectOutboxInsertFailure(mock, errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 -- a task must not be created without its event",
			rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the transaction did not roll back: %v", err)
	}
}

// The event is recorded after the insert, so it carries the database-assigned
// ID as its partition key. Recording first would key it on 0 and every consumer
// would see events for a task that does not exist.
func TestCreateTaskRecordsTheEventAfterTheInsert(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnRows(mockIDRow(createdTaskID))
	mock.ExpectQuery(`INSERT INTO "outbox"`).
		WithArgs(sqlmock.AnyArg(), "42", "task-events", sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(mockIDRow(1))
	mock.ExpectCommit()

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the event is not keyed on the assigned task id: %v", err)
	}
}

func TestCreateTaskRecordsNothingWhenValidationFails(t *testing.T) {
	mock := newMockDB(t)

	// No expectations queued at all: any query, in or out of a transaction,
	// fails the check below.
	c, rec := newTestContext(t, `{"description":"no title, no project"}`)
	CreateTask(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a rejected request touched the database: %v", err)
	}
}

func TestCreateTaskRecordsNothingWhenStatusInvalid(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)

	c, rec := newTestContext(t, `{"title":"t","project_id":3,"status":"Nonsense"}`)
	CreateTask(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("an invalid status still wrote an event: %v", err)
	}
}

func TestCreateTaskRecordsNothingWhenProjectNotFound(t *testing.T) {
	mock := newMockDB(t)
	expectProjectNotFound(mock)

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("an inaccessible project still wrote an event: %v", err)
	}
}

// No event for a task that does not exist. The rollback guarantees it now --
// there is no separate publish left to suppress.
func TestCreateTaskRecordsNothingWhenInsertFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

// The response body is part of the API contract, and moving to an outbox must
// not disturb it.
func TestCreateTaskResponseShapeUnchanged(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnRows(mockIDRow(createdTaskID))
	expectOutboxInsert(mock)
	mock.ExpectCommit()

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	var body struct {
		Message string                 `json:"message"`
		Task    map[string]interface{} `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body.Message != "Task created successfully" {
		t.Errorf("message = %q, want %q", body.Message, "Task created successfully")
	}
	for _, key := range []string{"id", "title", "description", "project_id",
		"status", "estimate", "priority", "due_date", "created_at"} {
		if _, ok := body.Task[key]; !ok {
			t.Errorf("response task is missing %q", key)
		}
	}
	if len(body.Task) != 9 {
		keys := make([]string, 0, len(body.Task))
		for k := range body.Task {
			keys = append(keys, k)
		}
		t.Errorf("response task has %d fields (%s), want the original 9",
			len(body.Task), strings.Join(keys, ", "))
	}
}
