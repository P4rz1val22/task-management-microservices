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
)

func (a Action) String() string {
	switch a {
	case ActionNotified:
		return "notified"
	case ActionSkipped:
		return "skipped"
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

// Handler processes one event at a time.
//
// It holds no state today. It is a struct rather than a bare function because
// Stage 6 gives it an email service and Stage 8 gives it a dedupe store, and
// having the seam already there means neither stage has to reshape the loop.
type Handler struct{}

func New() *Handler {
	return &Handler{}
}

// HandleMessage decodes one Kafka message value and acts on it.
//
// It never panics, whatever arrives on the topic. That is not defensive
// paranoia: the topic is shared, auto-created, and reachable by any console
// tool on the network, so "something unparseable turns up" is a matter of when.
func (h *Handler) HandleMessage(_ context.Context, value []byte) (Result, error) {
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
		// Stage 6 replaces this with a real send.
		return Result{Action: ActionNotified, Summary: summary}, nil
	default:
		// Forward compatibility: a producer running ahead of this build will
		// publish types it has never heard of. Skipping keeps the partition
		// moving; erroring would wedge it behind a message that is not even
		// malformed.
		return Result{Action: ActionSkipped, Summary: summary}, nil
	}
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
