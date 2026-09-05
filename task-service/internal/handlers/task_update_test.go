package handlers

import (
	"errors"
	"net/http"
	"testing"
)

// updateBody returns a full update request. The API replaces the whole task, so
// every field has to be present -- an omitted one would read as "clear it".
func updateBody(title, description, status, priority, estimate, due string) string {
	return `{
		"title": "` + title + `",
		"description": "` + description + `",
		"project_id": 3,
		"status": "` + status + `",
		"priority": "` + priority + `",
		"estimate": "` + estimate + `",
		"due_date": "` + due + `"
	}`
}

// unchangedBody re-sends exactly what existingTask() already holds.
func unchangedBody() string {
	return updateBody("Original title", "Original description",
		"Not Started", "Low", "S", "2026-09-30")
}

// Stage 7's rule, re-proved at the new seam. Hitting save without editing
// anything is something real users do constantly, and it must not write an
// event -- the save still happens, the outbox row does not.
func TestUpdateWithNoChangesRecordsNoEvent(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSave(mock) // BEGIN, projects upsert, UPDATE tasks, COMMIT -- no outbox

	c, rec := newTestContext(t, unchangedBody())
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a no-op update wrote an event: %v", err)
	}
}

// A real edit writes its event inside the same transaction as the save.
func TestUpdateWritesTheEventInTheSameTransaction(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSaveWithOutbox(mock)

	// Status and priority change; everything else is resent as-is.
	c, rec := newTestContext(t, updateBody("Original title", "Original description",
		"Done", "Urgent", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the save and its event were not written together: %v", err)
	}
}

// The diff has to be taken against the values as they were, so the capture must
// happen before the request is applied. Reading them afterwards would compare
// the task to itself and every update would look like a no-op.
// The diff is taken against the pre-update snapshot. If it were taken after the
// assignments, every update would compare the task to itself, look like a no-op,
// and write no event at all -- so the presence of the outbox row here is what
// proves the snapshot is real. The from/to values themselves are covered
// exhaustively by TestDiffTask in internal/events.
func TestUpdateDiffsAgainstTheOriginalValues(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSaveWithOutbox(mock)

	c, _ := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("a title change produced no event; the diff compared the task "+
			"to itself: %v", err)
	}
}

// Same guarantee as the create path: an unwritable event rolls the save back,
// so a task can never be updated without its change summary being recorded.
func TestUpdateRollsBackWhenTheEventCannotBeRecorded(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "projects"`).WillReturnRows(mockIDRow(3))
	mock.ExpectExec(`UPDATE "tasks"`).WillReturnResult(sqlmockResult())
	expectOutboxInsertFailure(mock, errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the transaction did not roll back: %v", err)
	}
}

func TestUpdateRecordsNothingWhenSaveFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "projects"`).
		WillReturnRows(sqlmockRows("id", "3"))
	mock.ExpectExec(`UPDATE "tasks"`).WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

func TestUpdateRecordsNothingWhenTaskNotFound(t *testing.T) {
	mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "tasks"`).
		WillReturnRows(mockEmptyTaskRows())

	c, rec := newTestContext(t, unchangedBody())
	c.Params = ginParams("id", "999")
	UpdateTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

// --- delete ---

func TestDeleteWritesTheEventInTheSameTransaction(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskDeleteWithOutbox(mock)

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	// The row is gone as far as every query is concerned from here on, so the
	// event is the only remaining description of what was deleted -- which is
	// why it has to be written before the transaction closes, not after.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the delete and its event were not written together: %v", err)
	}
}

func TestDeleteRollsBackWhenTheEventCannotBeRecorded(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "tasks" SET "deleted_at"`).WillReturnResult(sqlmockResult())
	expectOutboxInsertFailure(mock, errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 -- a task must not vanish without its event",
			rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("the transaction did not roll back: %v", err)
	}
}

func TestDeleteRecordsNothingWhenDeleteFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "tasks" SET "deleted_at"`).
		WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

func TestDeleteRecordsNothingWhenTaskNotFound(t *testing.T) {
	mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "tasks"`).WillReturnRows(mockEmptyTaskRows())

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "999")
	DeleteTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}
