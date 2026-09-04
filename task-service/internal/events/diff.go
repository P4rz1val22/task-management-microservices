package events

import (
	"strconv"
	"time"

	"task-management-task-service/internal/models"
)

// dueDateLayout is how a due date appears in a change summary. Dates on tasks
// have no time component -- the API accepts YYYY-MM-DD -- so rendering the
// clock would be noise.
const dueDateLayout = "2006-01-02"

// noDueDate is what an unset due date reads as in an email. It has to be a
// word: DueDate is a *time.Time, and formatting a pointer without checking for
// nil first puts an address like 0xc000123456 in front of a user.
const noDueDate = "none"

// DiffTask reports which user-visible fields differ between two versions of a
// task, in a fixed order.
//
// It is a free function over two structs -- no database, no gin, no clock --
// which is what makes the fiddly rules below cheap to test exhaustively.
//
// Two properties are load-bearing and each has its own test:
//
//   - **Empty means empty.** Identical tasks produce no entries, which is what
//     makes "saving without changing anything notifies nobody" true. The
//     handler's decision to publish is a plain length check on this result.
//   - **The order never varies.** The comparisons run in a fixed sequence
//     rather than over a map, because Go randomises map iteration: a
//     map-driven version would reorder the fields of an email between sends
//     and fail an order assertion around one run in six.
//
// Scope note. The monolith only ever compared Title, Status, Priority and
// Estimate, so editing a description or a due date notified nobody. Description,
// Project and Due Date are included here deliberately -- that omission looked
// more like an oversight than a decision.
//
// Bookkeeping columns are excluded on purpose. UpdatedAt in particular changes
// on every single save, so counting it would make every no-op update look real
// and defeat the rule above.
func DiffTask(before, after models.Task) []ChangeDetail {
	var changes []ChangeDetail

	add := func(field, from, to string) {
		if from != to {
			changes = append(changes, ChangeDetail{Field: field, From: from, To: to})
		}
	}

	// Fixed order, and it is the order the change list appears in an email.
	add("Title", before.Title, after.Title)
	add("Description", before.Description, after.Description)
	add("Status", before.Status, after.Status)
	add("Priority", before.Priority, after.Priority)
	add("Estimate", before.Estimate, after.Estimate)
	add("Project", strconv.FormatUint(uint64(before.ProjectID), 10),
		strconv.FormatUint(uint64(after.ProjectID), 10))
	add("Due Date", formatDueDate(before.DueDate), formatDueDate(after.DueDate))

	return changes
}

// formatDueDate renders a due date for a human, turning an unset one into a
// word rather than a nil pointer.
//
// Comparing the formatted strings rather than the pointers is deliberate: two
// pointers to the same instant are different values, and a JSON round trip
// produces exactly that. Comparing pointers would report a change that never
// happened, on every update, forever.
func formatDueDate(t *time.Time) string {
	if t == nil {
		return noDueDate
	}
	return t.Format(dueDateLayout)
}

// NewTaskUpdated builds the event for a task that has just been saved, carrying
// the diff the consumer renders into a change summary.
//
// Callers must not publish this with an empty changes slice: an update that
// changed nothing should produce no event at all, which is the handler's job to
// enforce and TestUpdateWithNoChangesPublishesNothing that proves it.
func NewTaskUpdated(task models.Task, actorID uint, changes []ChangeDetail) Envelope {
	env := newEnvelope(EventTaskUpdated, task, actorID)
	env.Changes = changes
	return env
}

// NewTaskDeleted builds the event for a task that has just been deleted.
//
// The envelope carries the whole task, which matters more here than anywhere
// else: by the time a consumer sees this, the task is gone from every query, so
// the event is the only remaining description of what was deleted.
func NewTaskDeleted(task models.Task, actorID uint) Envelope {
	return newEnvelope(EventTaskDeleted, task, actorID)
}
