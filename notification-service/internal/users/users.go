// Package users resolves an event's actor_id to the address to notify.
//
// This is the one thing the consumer cannot get from the event itself. The
// envelope deliberately carries the whole task so the consumer never calls back
// into task-service -- but it carries a user ID, not an address, because an
// address is mutable personal data that would otherwise be frozen into every
// event on the topic forever.
package users

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrNoRecipient means there is no address to send to, and no retry will
// produce one: the user does not exist, or has no email on record.
//
// Distinguishing this from a database outage is the whole point of the type. A
// missing user is permanent and the event should be set aside; an unreachable
// database is transient and the event should be retried. Conflating them either
// discards events that were fine or retries ones that never can be.
var ErrNoRecipient = errors.New("no recipient")

// Lookup is the seam the consumer depends on, so its tests need no database.
type Lookup interface {
	EmailFor(ctx context.Context, userID uint) (string, error)
}

// DBLookup reads addresses from the shared users table -- the same table the
// monolith's inline goroutine read, now keyed off the event's actor_id.
type DBLookup struct {
	db *gorm.DB
}

func NewDBLookup(db *gorm.DB) *DBLookup {
	return &DBLookup{db: db}
}

// EmailFor returns the address for userID.
//
// It selects the email column alone rather than loading the row. Reading
// SELECT * would pull the bcrypt password hash into this process to send an
// email, which it has no business holding; TestLookupSelectsOnlyTheEmailColumn
// enforces it.
func (l *DBLookup) EmailFor(ctx context.Context, userID uint) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("%w: actor_id is zero", ErrNoRecipient)
	}

	var email string
	err := l.db.WithContext(ctx).
		Table("users").
		Select("email").
		Where("id = ?", userID).
		Take(&email).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return "", fmt.Errorf("%w: no user %d", ErrNoRecipient, userID)
	case err != nil:
		// Deliberately NOT wrapped in ErrNoRecipient. This is transient, and
		// labelling it permanent would have the consumer discard a perfectly
		// good event because the database blinked.
		return "", fmt.Errorf("look up recipient for user %d: %w", userID, err)
	}

	if email == "" {
		return "", fmt.Errorf("%w: user %d has no email address", ErrNoRecipient, userID)
	}
	return email, nil
}
