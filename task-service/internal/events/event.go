// Package events defines the wire contract for task events published to Kafka.
//
// This package holds no Kafka client and performs no I/O: it is pure data
// transformation, so its tests need no broker, no database, and run in
// milliseconds. Keep it that way -- the publisher lives in publisher.go and is
// the only thing here that should ever know Kafka exists.
//
// It does import internal/models (which transitively pulls in gorm) so it can
// map a persisted task onto the wire payload. That is fine. What is not fine is
// marshalling models.Task directly: that type carries gorm.DeletedAt plus
// Project, Assignee, and Creator relation structs which are not preloaded on
// the create path, so it would put zero-valued junk like
// {"project":{"id":0,"name":""}} on the wire. TaskPayload exists to prevent
// that, and TestTaskPayloadLeaksNoDatabaseFields enforces it.
package events

import (
	"time"

	"github.com/google/uuid"

	"task-management-task-service/internal/models"
)

// Event types carried on the single task-events topic. One topic rather than
// three, partitioned by task ID, so events for the same task stay ordered --
// three topics would let a task.updated overtake its own task.created.
const (
	EventTaskCreated = "task.created"
	EventTaskUpdated = "task.updated"
	EventTaskDeleted = "task.deleted"
)

// ChangeDetail is one field-level diff, populated only on task.updated.
//
// The Go field names match services.ChangeDetail in the monolith
// (monolith/internal/services/email.go) so that email.go can be ported to
// notification-service without touching its signatures. The monolith's copy
// carries no JSON tags, so the lowercase wire names are declared here; the
// ported copy needs the same tags added.
type ChangeDetail struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// TaskPayload is the task as it travels on the wire. The event carries the
// whole task so the consumer never has to call back into task-service; keep it
// self-contained.
//
// DueDate has no omitempty on purpose. "This task has no due date" is
// information the email template needs, and is meaningfully different from an
// absent field, so an unset due date serialises as an explicit null.
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
// EventID exists so the consumer can dedupe: Kafka is at-least-once, so a
// consumer that crashes after sending an email but before committing its
// offset will see the same event again on restart. Without dedupe that is a
// duplicate email.
//
// Changes IS omitempty: its absence is how a consumer distinguishes "this
// event carries no diff" from "this task changed nothing", and a created event
// should not ship an empty diff.
type Envelope struct {
	EventID    string         `json:"event_id"`
	EventType  string         `json:"event_type"`
	OccurredAt time.Time      `json:"occurred_at"`
	TaskID     uint           `json:"task_id"`
	ActorID    uint           `json:"actor_id"`
	Task       TaskPayload    `json:"task"`
	Changes    []ChangeDetail `json:"changes,omitempty"`
}

// payloadFrom projects a persisted task onto the wire payload, dropping every
// database-only field.
func payloadFrom(task models.Task) TaskPayload {
	p := TaskPayload{
		ID:          task.ID,
		Title:       task.Title,
		Description: task.Description,
		ProjectID:   task.ProjectID,
		Status:      task.Status,
		Priority:    task.Priority,
		Estimate:    task.Estimate,
	}
	// Copy the value rather than aliasing the model's pointer, so a later
	// mutation of the task cannot retroactively change an event already built.
	if task.DueDate != nil {
		due := *task.DueDate
		p.DueDate = &due
	}
	return p
}

func newEnvelope(eventType string, task models.Task, actorID uint) Envelope {
	return Envelope{
		EventID:    uuid.NewString(),
		EventType:  eventType,
		OccurredAt: time.Now().UTC(),
		TaskID:     task.ID,
		ActorID:    actorID,
		Task:       payloadFrom(task),
	}
}

// NewTaskCreated builds the event for a task that has just been committed.
// actorID is the user_id the handlers read via c.GetUint("user_id"); the
// consumer needs it to look up the recipient's address.
func NewTaskCreated(task models.Task, actorID uint) Envelope {
	return newEnvelope(EventTaskCreated, task, actorID)
}
