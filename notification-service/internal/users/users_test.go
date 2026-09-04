package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// newMockDB mirrors the harness in task-service/internal/handlers: the same two
// settings are load-bearing, for the same reasons. DisableAutomaticPing stops
// gorm.Open pinging a connection that cannot answer, PreferSimpleProtocol stops
// pgx preparing statements the mock knows nothing about.
//
// Consequence to remember: PreferSimpleProtocol interpolates arguments into the
// SQL text rather than binding them, so sqlmock's WithArgs never matches. Match
// on the query regex and assert on the returned value instead.
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

func TestEmailForReturnsTheAddress(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("demo@example.com"))

	got, err := NewDBLookup(db).EmailFor(context.Background(), 7)
	if err != nil {
		t.Fatalf("EmailFor: %v", err)
	}
	if got != "demo@example.com" {
		t.Errorf("email = %q, want %q", got, "demo@example.com")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A user who does not exist is a permanent condition: no amount of retrying
// conjures an address. The consumer must be able to recognise it and set the
// message aside rather than retry forever.
func TestMissingUserIsPermanent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"email"}))

	_, err := NewDBLookup(db).EmailFor(context.Background(), 999)
	if err == nil {
		t.Fatal("EmailFor returned nil for a user that does not exist")
	}
	if !errors.Is(err, ErrNoRecipient) {
		t.Errorf("error %v does not wrap ErrNoRecipient", err)
	}
}

// A user row with a blank email is the same permanent problem wearing a
// different hat, and it would otherwise sail through as an empty To: header.
func TestBlankEmailIsPermanent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow(""))

	_, err := NewDBLookup(db).EmailFor(context.Background(), 7)
	if !errors.Is(err, ErrNoRecipient) {
		t.Errorf("error = %v, want it to wrap ErrNoRecipient", err)
	}
}

// A database that is down is the opposite case: transient, worth retrying, and
// it must NOT be reported as a missing recipient or the event gets discarded.
func TestDatabaseFailureIsNotAMissingRecipient(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM "users"`).
		WillReturnError(errors.New("connection refused"))

	_, err := NewDBLookup(db).EmailFor(context.Background(), 7)
	if err == nil {
		t.Fatal("EmailFor returned nil despite the database erroring")
	}
	if errors.Is(err, ErrNoRecipient) {
		t.Error("a database outage was reported as a missing recipient; " +
			"the event would be discarded instead of retried")
	}
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

// Reading the whole user row to get one address pulls the bcrypt hash into this
// process for no reason. Selecting just the column needed keeps the credential
// out of notification-service entirely.
func TestLookupSelectsOnlyTheEmailColumn(t *testing.T) {
	var queries []string
	db, mock := recordingMockDB(t, &queries)

	mock.ExpectQuery(`SELECT .* FROM "users"`).
		WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow("demo@example.com"))

	if _, err := NewDBLookup(db).EmailFor(context.Background(), 7); err != nil {
		t.Fatalf("EmailFor: %v", err)
	}

	if len(queries) == 0 {
		t.Fatal("no query reached the driver")
	}
	q := queries[len(queries)-1]
	if strings.Contains(strings.ToLower(q), "password") {
		t.Errorf("lookup reads the password column:\n  %s", q)
	}
	if strings.Contains(q, `SELECT *`) {
		t.Errorf("lookup selects every column; ask for the address only:\n  %s", q)
	}
	if !strings.Contains(strings.ToLower(q), "email") {
		t.Errorf("lookup does not select email:\n  %s", q)
	}
}
