package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"task-management-task-service/internal/models"
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

func TestCreateTaskPublishesCreatedEvent(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 1 {
		t.Fatalf("publisher called %d times, want exactly 1", len(pub.calls))
	}

	got := pub.calls[0]
	if got.task.Title != "Ship the event system" {
		t.Errorf("published title = %q, want %q", got.task.Title, "Ship the event system")
	}
	if got.task.ProjectID != 3 {
		t.Errorf("published project_id = %d, want 3", got.task.ProjectID)
	}
	if got.actorID != testActorID {
		t.Errorf("published actor = %d, want %d", got.actorID, testActorID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

// The headline guarantee of this stage: mail delivery must never be able to fail
// task creation. A broker that is down, restarting, or misconfigured changes the
// latency of a create and nothing else.
func TestCreateTaskReturns201WhenPublishFails(t *testing.T) {
	// First, capture what a successful create looks like.
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)
	wantBody := rec.Body.String()

	// Now the same request with the publisher failing outright.
	mock2 := newMockDB(t)
	expectOwnedProject(mock2, 3)
	expectTaskInsert(mock2, createdTaskID)
	pub := usePublisher(t, &fakePublisher{err: errors.New("broker unreachable")})

	c2, rec2 := newTestContext(t, validCreateBody)
	CreateTask(c2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("status = %d with a failing publisher, want 201. body: %s",
			rec2.Code, rec2.Body.String())
	}
	if got, want := normaliseBody(t, rec2.Body.Bytes()), normaliseBody(t, []byte(wantBody)); got != want {
		t.Errorf("response body changed when publishing failed:\n got: %s\nwant: %s", got, want)
	}
	if len(pub.calls) != 1 {
		t.Errorf("publisher called %d times, want 1 (the attempt must still happen)", len(pub.calls))
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

// Publishing happens after the commit, so the event carries the real primary
// key. If this were published before the insert, task_id would be 0 and every
// consumer would key off a task that does not exist.
func TestCreateTaskPublishesTheCommittedTask(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	pub := usePublisher(t, &fakePublisher{})

	c, _ := newTestContext(t, validCreateBody)
	CreateTask(c)

	if len(pub.calls) != 1 {
		t.Fatalf("publisher called %d times, want 1", len(pub.calls))
	}
	if id := pub.calls[0].task.ID; id != createdTaskID {
		t.Errorf("published task.ID = %d, want %d assigned by the database", id, createdTaskID)
	}
}

func TestCreateTaskDoesNotPublishWhenValidationFails(t *testing.T) {
	newMockDB(t)
	pub := usePublisher(t, &fakePublisher{})

	// binding:"required" rejects this before any database work happens, so no
	// expectations are queued -- a stray query would fail the test.
	c, rec := newTestContext(t, `{"description":"no title, no project"}`)
	CreateTask(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 0 {
		t.Errorf("publisher called %d times on a rejected request, want 0", len(pub.calls))
	}
}

func TestCreateTaskDoesNotPublishWhenStatusInvalid(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t,
		`{"title":"t","project_id":3,"status":"Nonsense"}`)
	CreateTask(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 0 {
		t.Errorf("publisher called %d times on an invalid status, want 0", len(pub.calls))
	}
}

func TestCreateTaskDoesNotPublishWhenProjectNotFound(t *testing.T) {
	mock := newMockDB(t)
	expectProjectNotFound(mock)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 0 {
		t.Errorf("publisher called %d times for an inaccessible project, want 0", len(pub.calls))
	}
}

// No event for a task that does not exist. An event published despite a failed
// insert would have the consumer emailing about a task nobody can open.
func TestCreateTaskDoesNotPublishWhenInsertFails(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsertFailure(mock, errors.New("disk on fire"))
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, validCreateBody)
	CreateTask(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500. body: %s", rec.Code, rec.Body.String())
	}
	if len(pub.calls) != 0 {
		t.Errorf("publisher called %d times after a failed insert, want 0", len(pub.calls))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet database expectations: %v", err)
	}
}

// The task was created; the caller walking away does not un-create it. Deriving
// the publish context from c.Request.Context() would drop the event whenever a
// client hung up, which is both wrong and maddening to reproduce.
func TestCreateTaskPublishSurvivesClientDisconnect(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	pub := usePublisher(t, &fakePublisher{})

	c, rec := newTestContext(t, validCreateBody)

	// Simulate the client going away mid-request.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = c.Request.WithContext(cancelled)

	CreateTask(c)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if len(pub.calls) != 1 {
		t.Fatalf("publisher called %d times after a disconnect, want 1", len(pub.calls))
	}
	if err := pub.calls[0].ctxErr; err != nil {
		t.Errorf("publish context was already cancelled (%v) when the publisher ran; "+
			"it must not derive from the request", err)
	}
}

// A broker that accepts the connection and then goes quiet must not hold the
// handler open indefinitely.
func TestCreateTaskPublishContextHasDeadline(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	pub := usePublisher(t, &fakePublisher{})

	c, _ := newTestContext(t, validCreateBody)
	CreateTask(c)

	if len(pub.calls) != 1 {
		t.Fatalf("publisher called %d times, want 1", len(pub.calls))
	}
	call := pub.calls[0]
	if !call.hasDeadline {
		t.Fatal("publish context has no deadline; a wedged broker would pin the handler open")
	}
	if remaining := time.Until(call.deadline); remaining <= 0 || remaining > publishTimeout {
		t.Errorf("deadline was %v away at call time, want (0, %v]", remaining, publishTimeout)
	}
}

// An unwired binary -- or any future test that forgets to install a fake -- must
// log rather than panic on a nil interface.
func TestPublisherDefaultsToNoop(t *testing.T) {
	if Publisher == nil {
		t.Fatal("package default Publisher is nil; CreateTask would panic")
	}
	if err := Publisher.PublishTaskCreated(context.Background(), models.Task{ID: 1}, 7); err != nil {
		t.Errorf("default publisher returned %v, want nil", err)
	}
}

// The response body is part of the API contract and this stage must not disturb
// it. Guards against an event field accidentally leaking into the JSON.
func TestCreateTaskResponseShapeUnchanged(t *testing.T) {
	mock := newMockDB(t)
	expectOwnedProject(mock, 3)
	expectTaskInsert(mock, createdTaskID)
	usePublisher(t, &fakePublisher{})

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

// normaliseBody drops created_at before comparing two responses. GORM stamps it
// at insert time, so two otherwise identical creates always differ there; the
// point of the comparison is the shape and the other values.
func normaliseBody(t *testing.T, raw []byte) string {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if task, ok := body["task"].(map[string]interface{}); ok {
		delete(task, "created_at")
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(out)
}
