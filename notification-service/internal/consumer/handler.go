// Package consumer holds the per-message decision, kept separate from the
// reader loop that feeds it.
//
// The split is deliberate and it is what makes this testable. HandleMessage is
// a pure function of the message bytes: no broker, no network, no clock. The
// loop in reader.go owns everything that is awkward to test -- connections,
// offsets, signals -- and owns none of the decisions.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"task-management-notification-service/internal/events"
	"task-management-notification-service/internal/users"
)

// ErrBadMessage marks a message that will never succeed no matter how often it
// is retried: malformed JSON, or an envelope missing a field the consumer
// cannot work without.
//
// The distinction matters more than it looks. A transient failure (the mail
// server is down) should be retried and its offset left uncommitted; a poison
// message must have its offset committed and be set aside, or it blocks its
// partition forever and every task after it goes unnotified. Stage 8 builds the
// dead-letter path on top of this error; the loop already needs it to decide
// whether to keep going.
var ErrBadMessage = errors.New("bad message")

// errAlreadyProcessed is internal control flow, not a failure: it marks an
// event this consumer has already acted on, which the caller turns into
// ActionDuplicate and a committed offset.
var errAlreadyProcessed = errors.New("already processed")

// Action is what a message resulted in. The handler returns it and the loop
// logs it, rather than the handler logging directly -- otherwise the only way
// to test the decision would be to capture stdout.
type Action int

const (
	// ActionFailed means the message was not processed. Paired with an error.
	ActionFailed Action = iota
	// ActionNotified means the event was recognised and acted on. In Stage 5
	// "acted on" is a log line; Stage 6 makes it an email.
	ActionNotified
	// ActionSkipped means the event was well-formed but carried a type this
	// build does not handle. Deliberately not an error.
	ActionSkipped
	// ActionDuplicate means the event had already been handled and was
	// deliberately not acted on a second time.
	ActionDuplicate
)

func (a Action) String() string {
	switch a {
	case ActionNotified:
		return "notified"
	case ActionSkipped:
		return "skipped"
	case ActionDuplicate:
		return "duplicate"
	default:
		return "failed"
	}
}

// Result is what one message produced: what happened, and a one-line
// description the caller can log. The summary is returned rather than logged
// here so that the handler stays a pure function of the message bytes -- and so
// the loop does not have to decode the same message a second time to say
// something useful about it.
type Result struct {
	Action  Action
	Summary string
}

// processedStore is what this package needs from internal/dedupe. Kafka
// delivers at least once, so the same event really does arrive twice; without a
// memory of what has been done, twice means two emails.
type processedStore interface {
	Seen(ctx context.Context, eventID string) (bool, error)
	MarkProcessed(ctx context.Context, eventID string) error
}

// mailer is what this package needs from internal/services. Declared here, by
// the consumer, so the handler tests need no SMTP and no HTML.
type mailer interface {
	SendTaskCreatedNotification(task events.TaskPayload, to string) error
	SendTaskUpdatedNotification(task events.TaskPayload, to string, changes []events.ChangeDetail) error
	SendTaskDeletedNotification(task events.TaskPayload, to string) error
}

// Handler processes one event at a time.
//
// Both dependencies are optional. With neither set -- which is what New()
// returns -- the handler behaves exactly as it did in Stage 5, recognising
// events and describing them without sending anything. That is what keeps the
// log-only mode available and stops a half-wired binary from panicking.
type Handler struct {
	Recipients users.Lookup
	Mailer     mailer
	Processed  processedStore
}

func New() *Handler {
	return &Handler{}
}

// HandleMessage decodes one Kafka message value and acts on it.
//
// It never panics, whatever arrives on the topic. That is not defensive
// paranoia: the topic is shared, auto-created, and reachable by any console
// tool on the network, so "something unparseable turns up" is a matter of when.
func (h *Handler) HandleMessage(ctx context.Context, value []byte) (Result, error) {
	env, err := decode(value)
	if err != nil {
		return Result{Action: ActionFailed}, err
	}

	summary, err := h.describe(env)
	if err != nil {
		return Result{Action: ActionFailed}, err
	}

	switch env.EventType {
	case events.EventTaskCreated, events.EventTaskUpdated, events.EventTaskDeleted:
		if err := h.notify(ctx, env); err != nil {
			if errors.Is(err, errAlreadyProcessed) {
				return Result{Action: ActionDuplicate, Summary: summary}, nil
			}
			return Result{Action: ActionFailed, Summary: summary}, err
		}
		return Result{Action: ActionNotified, Summary: summary}, nil
	default:
		// Forward compatibility: a producer running ahead of this build will
		// publish types it has never heard of. Skipping keeps the partition
		// moving; erroring would wedge it behind a message that is not even
		// malformed.
		return Result{Action: ActionSkipped, Summary: summary}, nil
	}
}

// notify resolves the recipient and sends the mail.
//
// The error classification here is the whole point of the function, and it is
// the difference between a lost notification and a wedged partition:
//
//   - A missing or address-less user is PERMANENT. No retry can invent an
//     address, so it becomes an ErrBadMessage and the loop discards it.
//   - A database outage or an SMTP failure is TRANSIENT and is returned
//     unwrapped, so the loop can retry it (Stage 8) rather than throw the
//     event away.
//
// Getting these backwards is silent either way: one drops good events, the
// other retries hopeless ones forever.
func (h *Handler) notify(ctx context.Context, env events.Envelope) error {
	if h.Recipients == nil || h.Mailer == nil {
		// Stage 5 behaviour: recognise and describe, send nothing.
		return nil
	}

	// Checked before the recipient lookup, so a redelivery costs one query
	// rather than two plus an SMTP round trip.
	//
	// An error here is never treated as "not seen". Reading a database outage
	// as "never handled" would send duplicates during precisely the moment
	// things are already going wrong, and it is transient, so it is returned
	// unwrapped for the loop to retry rather than discard.
	if h.Processed != nil {
		seen, err := h.Processed.Seen(ctx, env.EventID)
		if err != nil {
			return err
		}
		if seen {
			return errAlreadyProcessed
		}
	}

	to, err := h.Recipients.EmailFor(ctx, env.ActorID)
	if err != nil {
		if errors.Is(err, users.ErrNoRecipient) {
			return fmt.Errorf("%w: %v", ErrBadMessage, err)
		}
		return err
	}

	// Explicitly exhaustive, with no default that sends mail. An earlier
	// version routed everything that was not an update to the created
	// template, which meant a task.deleted would have told the user their
	// deleted task had just been created. Only unreachable because nothing
	// published deletes; publishing them is what made it real.
	var sendErr error
	switch env.EventType {
	case events.EventTaskCreated:
		sendErr = h.Mailer.SendTaskCreatedNotification(env.Task, to)
	case events.EventTaskUpdated:
		sendErr = h.Mailer.SendTaskUpdatedNotification(env.Task, to, env.Changes)
	case events.EventTaskDeleted:
		sendErr = h.Mailer.SendTaskDeletedNotification(env.Task, to)
	default:
		// Unreachable: HandleMessage has already skipped unknown types. Send
		// nothing rather than guess, so a future event type added upstream can
		// never mail the wrong template.
		return nil
	}
	if sendErr != nil {
		return sendErr
	}

	// Recorded only after the send succeeded. Recording first would mean a
	// failed send is never retried: the redelivery would be waved through as a
	// duplicate and the notification lost silently.
	//
	// This leaves a window -- a crash between the send and this write replays
	// the email -- which is the irreducible one. Closing it would need the
	// email and the database write to be a single atomic act, and SMTP does not
	// participate in database transactions.
	if h.Processed != nil {
		if err := h.Processed.MarkProcessed(ctx, env.EventID); err != nil {
			return err
		}
	}
	return nil
}

// decode parses and validates an envelope, rejecting anything that cannot be
// acted on.
func decode(value []byte) (events.Envelope, error) {
	var env events.Envelope

	if len(value) == 0 {
		return env, fmt.Errorf("%w: empty message value", ErrBadMessage)
	}
	if err := json.Unmarshal(value, &env); err != nil {
		return env, fmt.Errorf("%w: %v", ErrBadMessage, err)
	}

	// A well-formed JSON document that is not an object decodes into the zero
	// envelope without error, so the field checks below are what catch `null`,
	// `[]` and `"a string"` -- not the unmarshal.
	if env.EventID == "" {
		// Stage 8 deduplicates on this. An event without one could not be
		// recognised as a redelivery, so it would eventually mean a duplicate
		// email; better to reject it while the cause is still obvious.
		return env, fmt.Errorf("%w: missing event_id", ErrBadMessage)
	}
	if env.EventType == "" {
		return env, fmt.Errorf("%w: missing event_type", ErrBadMessage)
	}
	if env.TaskID == 0 {
		return env, fmt.Errorf("%w: missing task_id", ErrBadMessage)
	}
	if env.ActorID == 0 {
		// There is no one to notify, so no retry could ever succeed.
		return env, fmt.Errorf("%w: missing actor_id, nobody to notify", ErrBadMessage)
	}

	return env, nil
}

// describe renders the one-line summary the loop logs. In Stage 6 the same
// envelope feeds the email templates; keeping the rendering here means the
// change lands in one place.
func (h *Handler) describe(env events.Envelope) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "%s task=%d %q actor=%d",
		env.EventType, env.TaskID, env.Task.Title, env.ActorID)

	if len(env.Changes) > 0 {
		fields := make([]string, 0, len(env.Changes))
		for _, c := range env.Changes {
			fields = append(fields, fmt.Sprintf("%s(%s->%s)", c.Field, c.From, c.To))
		}
		fmt.Fprintf(&b, " changed=[%s]", strings.Join(fields, " "))
	}

	return b.String(), nil
}
