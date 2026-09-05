// Package events is the consumer-side copy of the task event wire contract.
//
// DUPLICATED ON PURPOSE. The authoritative definition lives in
// task-service/internal/events/event.go. These are separate Go modules, so there
// is no import path between them, and reaching across with a replace directive
// would couple the two services' build graphs -- which is exactly what the event
// backbone exists to avoid. Duplicating a contract is the standard trade in a
// polyrepo-style layout: the price is that a change to the envelope must be
// applied in both places, and the tests in each service are what make a
// mismatch fail loudly instead of silently delivering null.
//
// This copy is deliberately NOT identical. It carries only what a consumer
// needs -- the types, so an envelope can be decoded. The producer-side
// constructors (NewTaskCreated, payloadFrom) and the models.Task import they
// require are omitted, which is what keeps gorm, and a database dependency,
// out of notification-service altogether.
//
// If you change a JSON tag here, change it there, and run both test suites.
package events

import "time"

// Event types carried on the single task-events topic.
//
// A consumer must treat this list as open, not closed: a future producer may
// add a type this build has never heard of, and the correct response is to skip
// the message, not to crash or stall the partition.
const (
	EventTaskCreated = "task.created"
	EventTaskUpdated = "task.updated"
	EventTaskDeleted = "task.deleted"
)

// ChangeDetail is one field-level diff, populated only on task.updated.
//
// The Go field names match services.ChangeDetail in the monolith, so that
// email.go can be ported in Stage 6 without touching its signatures. The
// monolith's copy carries no JSON tags at all -- meaning it would marshal as
// Field/From/To -- so the lowercase wire names are declared here and must be
// added to the ported struct.
type ChangeDetail struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// TaskPayload is the task as it travels on the wire. The event carries the whole
// task so the consumer never has to call back into task-service; keep it that
// way, or the decoupling this design buys is given straight back.
//
// DueDate has no omitempty: "this task has no due date" is information the email
// template needs, and is meaningfully different from an absent field.
type TaskPayload struct {
	ID          uint       `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	ProjectID   uint       `json:"project_id"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	Estimate    string     `json:"estimate"`
	DueDate     *time.Time `json:"due_date"`
}

// Envelope is the JSON value published for every task event.
//
// EventID is the dedupe key. Kafka delivers at least once, so a consumer that
// crashes after sending an email but before committing its offset will see the
// same event again on restart. Stage 8 uses this field to make that harmless;
// until then a redelivery means a duplicate email.
//
// Changes is omitempty, so its absence distinguishes "this event carries no
// diff" from "this task changed nothing".
type Envelope struct {
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	OccurredAt time.Time      `json:"occurred_at"`
	TaskID     uint           `json:"task_id"`
	ActorID    uint           `json:"actor_id"`
	Task       TaskPayload    `json:"task"`
	Changes    []ChangeDetail `json:"changes,omitempty"`
}
