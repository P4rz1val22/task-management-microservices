package handlers

import (
	"errors"
	"net/http"
	"testing"

	"task-management-task-service/internal/events"
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

// The rule this whole stage is built around. Hitting save without editing
// anything is something real users do constantly, and it must not mail them.
func TestUpdateWithNoChangesPublishesNothing(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSave(mock)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, unchangedBody())
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 0 {
		t.Errorf("a no-op update published %d events (%+v), want 0",
			len(pub.calls), pub.calls)
	}
}

func TestUpdatePublishesOnlyTheChangedFields(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSave(mock)
	pub := usePublisher(t, &fakePublisher{})

	// Status and priority change; everything else is resent as-is.
	c, rec := newTestContext(t, updateBody("Original title", "Original description",
		"Done", "Urgent", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}

	call := pub.calls[0]
	if call.eventType != events.EventTaskUpdated {
		t.Errorf("event type = %q, want %q", call.eventType, events.EventTaskUpdated)
	}
	if len(call.changes) != 2 {
		t.Fatalf("published %d changes %+v, want 2", len(call.changes), call.changes)
	}
	if call.changes[0].Field != "Status" || call.changes[0].To != "Done" {
		t.Errorf("change 0 = %+v, want Status -> Done", call.changes[0])
	}
	if call.changes[1].Field != "Priority" || call.changes[1].To != "Urgent" {
		t.Errorf("change 1 = %+v, want Priority -> Urgent", call.changes[1])
	}
	if call.actorID != testActorID {
		t.Errorf("actor = %d, want %d", call.actorID, testActorID)
	}
}

// The diff has to be taken against the values as they were, so the capture must
// happen before the request is applied. Reading them afterwards would compare
// the task to itself and every update would look like a no-op.
func TestUpdateDiffsAgainstTheOriginalValues(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSave(mock)
	pub := usePublisher(t, &fakePublisher{})

	c, _ := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}
	ch := pub.calls[0].changes
	if len(ch) != 1 {
		t.Fatalf("changes = %+v, want exactly one", ch)
	}
	if ch[0].From != "Original title" {
		t.Errorf("from = %q, want the pre-update value %q", ch[0].From, "Original title")
	}
	if ch[0].To != "Renamed" {
		t.Errorf("to = %q, want %q", ch[0].To, "Renamed")
	}
}

// Same guarantee as the create path: notification failures must never turn a
// successful write into an error response.
func TestUpdateReturns200WhenPublishFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskSave(mock)
	pub := usePublisher(t, &fakePublisher{err: errors.New("broker unreachable")})

	c, rec := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with a failing publisher, want 200", rec.Code)
	}
	if len(pub.calls) != 1 {
		t.Errorf("published %d events, want 1 (the attempt must still happen)", len(pub.calls))
	}
}

func TestUpdateDoesNotPublishWhenSaveFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "projects"`).
		WillReturnRows(sqlmockRows("id", "3"))
	mock.ExpectExec(`UPDATE "tasks"`).WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, updateBody("Renamed", "Original description",
		"Not Started", "Low", "S", "2026-09-30"))
	c.Params = ginParams("id", "42")
	UpdateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(pub.calls) != 0 {
		t.Errorf("published %d events after a failed save, want 0", len(pub.calls))
	}
}

func TestUpdateDoesNotPublishWhenTaskNotFound(t *testing.T) {
	mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "tasks"`).
		WillReturnRows(mockEmptyTaskRows())
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, unchangedBody())
	c.Params = ginParams("id", "999")
	UpdateTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(pub.calls) != 0 {
		t.Errorf("published %d events for a task that does not exist, want 0", len(pub.calls))
	}
}

// --- delete ---

func TestDeletePublishesDeletedEvent(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskDelete(mock)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}

	call := pub.calls[0]
	if call.eventType != events.EventTaskDeleted {
		t.Errorf("event type = %q, want %q", call.eventType, events.EventTaskDeleted)
	}
	// The row is gone as far as every query is concerned, so the event has to
	// carry the task or nothing downstream can say what was deleted.
	if call.task.Title != "Original title" {
		t.Errorf("delete event does not carry the task: %+v", call.task)
	}
	if call.actorID != testActorID {
		t.Errorf("actor = %d, want %d", call.actorID, testActorID)
	}
	if len(call.changes) != 0 {
		t.Errorf("delete carried changes %+v, want none", call.changes)
	}
}

func TestDeleteReturns200WhenPublishFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	expectTaskDelete(mock)
	usePublisher(t, &fakePublisher{err: errors.New("broker unreachable")})

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with a failing publisher, want 200", rec.Code)
	}
}

func TestDeleteDoesNotPublishWhenDeleteFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedTask(mock, existingTask())
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "tasks" SET "deleted_at"`).
		WillReturnError(errors.New("disk on fire"))
	mock.ExpectRollback()
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "42")
	DeleteTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(pub.calls) != 0 {
		t.Errorf("published %d events after a failed delete, want 0", len(pub.calls))
	}
}

func TestDeleteDoesNotPublishWhenTaskNotFound(t *testing.T) {
	mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "tasks"`).WillReturnRows(mockEmptyTaskRows())
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, "")
	c.Params = ginParams("id", "999")
	DeleteTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(pub.calls) != 0 {
		t.Errorf("published %d events for a task that does not exist, want 0", len(pub.calls))
	}
}
