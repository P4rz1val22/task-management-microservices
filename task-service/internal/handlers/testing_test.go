package handlers

import (
	"database/sql/driver"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"task-management-task-service/internal/database"
	"task-management-task-service/internal/models"
)

// testActorID is the user_id every test context carries. It matches what
// middleware.RequireAuth sets on a real request (internal/middleware/auth.go:34),
// so handlers cannot tell the difference.
const testActorID = uint(7)

// newMockDB swaps database.DB for a GORM handle over a mock connection, and puts
// the real one back when the test ends.
//
// Two settings are load-bearing and were established by spiking this before any
// handler test existed. DisableAutomaticPing stops gorm.Open from pinging a
// connection that cannot answer, and PreferSimpleProtocol stops the pgx driver
// from preparing statements the mock knows nothing about. With both set,
// gorm.Open issues no queries at all -- TestMockDBOpensCleanly asserts that.
//
// PreferSimpleProtocol has one consequence worth remembering: GORM interpolates
// arguments straight into the SQL text rather than binding them, so sqlmock's
// WithArgs will never match. Match on the query regex and assert on values
// through the resulting struct instead.
func newMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()

	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}

	gdb, err := gorm.Open(
		postgres.New(postgres.Config{Conn: raw, PreferSimpleProtocol: true}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("gorm.Open over sqlmock: %v", err)
	}

	previous := database.DB
	database.DB = gdb
	t.Cleanup(func() {
		database.DB = previous
		raw.Close()
	})

	return mock
}

// expectOwnedProject queues the ownership lookup CreateTask performs before it
// writes anything: SELECT ... FROM "projects" WHERE id = ? AND owner_id = ?.
func expectOwnedProject(mock sqlmock.Sqlmock, projectID uint) {
	mock.ExpectQuery(`SELECT .* FROM "projects"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}).
			AddRow(projectID, "Test project", testActorID))
}

// expectProjectNotFound queues an empty result, which GORM's First turns into
// ErrRecordNotFound and the handler turns into a 404.
func expectProjectNotFound(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT .* FROM "projects"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}))
}

// expectTaskInsert queues the create. GORM wraps writes in a transaction and
// gets the generated key back through RETURNING "id", so this is ExpectQuery
// between a Begin and a Commit -- not ExpectExec.
func expectTaskInsert(mock sqlmock.Sqlmock, assignedID uint) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(assignedID))
	mock.ExpectCommit()
}

// expectTaskInsertFailure queues a write that blows up, so the handler takes its
// 500 branch.
func expectTaskInsertFailure(mock sqlmock.Sqlmock, err error) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "tasks"`).WillReturnError(err)
	mock.ExpectRollback()
}

// newTestContext builds a *gin.Context around a JSON body, carrying the same
// user_id RequireAuth would have set. Tests invoke handlers directly rather than
// through the router, so no JWT and no middleware are involved.
func newTestContext(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/tasks", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", testActorID)

	return c, rec
}

// The harness's own smoke test: gorm.Open must not talk to the database.
func TestMockDBOpensCleanly(t *testing.T) {
	mock := newMockDB(t)
	if database.DB == nil {
		t.Fatal("database.DB not installed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("gorm.Open issued unexpected queries: %v", err)
	}
}

// expectOwnedTaskForUpdate queues the authorisation lookup UpdateTask and
// DeleteTask both perform: the task joined to its project, checked against the
// caller. Preload("Project") makes it two queries, not one.
func expectOwnedTask(mock sqlmock.Sqlmock, task models.Task) {
	mock.ExpectQuery(`SELECT .* FROM "tasks"`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "title", "description", "project_id", "assignee_id",
			"creator_id", "status", "priority", "estimate", "due_date",
		}).AddRow(task.ID, task.Title, task.Description, task.ProjectID,
			testActorID, testActorID, task.Status, task.Priority,
			task.Estimate, task.DueDate))

	mock.ExpectQuery(`SELECT .* FROM "projects"`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}).
			AddRow(task.ProjectID, "Test project", testActorID))
}

// expectTaskSave queues what GORM's Save actually emits for a task whose
// Project association was preloaded.
//
// The projects upsert is not a mistake in this harness. UpdateTask loads the
// task with Preload("Project"), and GORM auto-saves loaded associations, so
// every task update also issues an INSERT ... ON CONFLICT DO NOTHING against
// projects. It is harmless -- the conflict clause makes it a no-op -- but it is
// what the handler does, and a mock that pretended otherwise would not be
// testing the handler.
func expectTaskSave(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "projects"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	mock.ExpectExec(`UPDATE "tasks"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// expectTaskDelete queues the soft delete: gorm.DeletedAt on the model turns
// Delete into an UPDATE that sets deleted_at rather than a DELETE.
func expectTaskDelete(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "tasks" SET "deleted_at"`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

// existingTask is the task the mocks above hand back.
func existingTask() models.Task {
	due := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	return models.Task{
		ID:          42,
		Title:       "Original title",
		Description: "Original description",
		ProjectID:   3,
		Status:      "Not Started",
		Priority:    "Low",
		Estimate:    "S",
		DueDate:     &due,
	}
}

// ginParams builds the URL parameter list a handler reads via c.Param, since
// tests invoke handlers directly rather than through the router.
func ginParams(kv ...string) gin.Params {
	params := make(gin.Params, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		params = append(params, gin.Param{Key: kv[i], Value: kv[i+1]})
	}
	return params
}

// mockEmptyTaskRows is a task query that finds nothing, which GORM turns into
// ErrRecordNotFound and the handlers turn into a 404.
func mockEmptyTaskRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "title", "project_id", "assignee_id"})
}

// sqlmockRows is a one-row shorthand for the small fixtures above.
func sqlmockRows(column, value string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{column}).AddRow(value)
}

// expectOutboxInsert queues the event row that must be written in the same
// transaction as the change that caused it. Its position between the write and
// the COMMIT is the assertion: sqlmock matches expectations in order, so a
// handler that wrote outside the transaction, or not at all, fails here.
func expectOutboxInsert(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`INSERT INTO "outbox"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
}

// expectOutboxInsertFailure queues an event row that cannot be written, so the
// surrounding transaction must roll back and take the task change with it.
func expectOutboxInsertFailure(mock sqlmock.Sqlmock, err error) {
	mock.ExpectQuery(`INSERT INTO "outbox"`).WillReturnError(err)
}

// mockIDRow is the RETURNING "id" result GORM reads after an insert.
func mockIDRow(id uint) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id"}).AddRow(id)
}

func sqlmockResult() driver.Result {
	return sqlmock.NewResult(0, 1)
}

// expectTaskSaveWithOutbox is expectTaskSave plus the event row that must land
// inside the same transaction.
func expectTaskSaveWithOutbox(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "projects"`).WillReturnRows(mockIDRow(3))
	mock.ExpectExec(`UPDATE "tasks"`).WillReturnResult(sqlmockResult())
	expectOutboxInsert(mock)
	mock.ExpectCommit()
}

// expectTaskDeleteWithOutbox is expectTaskDelete plus the event row.
func expectTaskDeleteWithOutbox(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE "tasks" SET "deleted_at"`).WillReturnResult(sqlmockResult())
	expectOutboxInsert(mock)
	mock.ExpectCommit()
}
