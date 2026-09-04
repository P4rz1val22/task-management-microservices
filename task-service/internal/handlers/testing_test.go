package handlers

import (
	"context"
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

// publishCall is one recorded invocation of the publisher.
//
// It records the state of the context as well as the arguments, because two of
// this stage's guarantees are about the context rather than the payload: that a
// client hanging up does not cancel a legitimate event, and that a wedged broker
// cannot pin the handler open forever.
type publishCall struct {
	task    models.Task
	actorID uint

	// Captured at call time, not read from a retained context afterwards. The
	// handler wraps its publish in a defer cancel(), so by the time a test
	// inspects a stored context it is always cancelled and always has an
	// expired deadline -- inspecting it later measures nothing.
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
}

// fakePublisher stands in for *events.Publisher. Because the handler declares
// its own eventPublisher interface, this fake keeps kafka-go out of the handler
// tests entirely.
type fakePublisher struct {
	calls []publishCall
	err   error
}

func (f *fakePublisher) PublishTaskCreated(ctx context.Context, task models.Task, actorID uint) error {
	deadline, hasDeadline := ctx.Deadline()
	f.calls = append(f.calls, publishCall{
		task:        task,
		actorID:     actorID,
		ctxErr:      ctx.Err(),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	})
	return f.err
}

// usePublisher installs a fake for the duration of one test and restores the
// package default afterwards.
func usePublisher(t *testing.T, f *fakePublisher) *fakePublisher {
	t.Helper()
	previous := Publisher
	Publisher = f
	t.Cleanup(func() { Publisher = previous })
	return f
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
