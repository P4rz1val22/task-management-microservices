package events

import (
	"testing"
	"time"

	"task-management-task-service/internal/models"
)

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

// baseTask is the "before" side of every diff case below.
func baseTask() models.Task {
	return models.Task{
		ID:          42,
		Title:       "Original title",
		Description: "Original description",
		ProjectID:   3,
		Status:      "Not Started",
		Priority:    "Low",
		Estimate:    "S",
		DueDate:     date(2026, 9, 30),
	}
}

func TestDiffTask(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.Task)
		want   []ChangeDetail
	}{
		{
			// The rule that stops a save-with-no-edits from mailing anybody.
			// Everything else in this stage depends on it.
			name:   "nothing changed",
			mutate: func(*models.Task) {},
			want:   nil,
		},
		{
			name:   "title",
			mutate: func(t *models.Task) { t.Title = "New title" },
			want:   []ChangeDetail{{Field: "Title", From: "Original title", To: "New title"}},
		},
		{
			name:   "status",
			mutate: func(t *models.Task) { t.Status = "In Progress" },
			want:   []ChangeDetail{{Field: "Status", From: "Not Started", To: "In Progress"}},
		},
		{
			name:   "priority",
			mutate: func(t *models.Task) { t.Priority = "Urgent" },
			want:   []ChangeDetail{{Field: "Priority", From: "Low", To: "Urgent"}},
		},
		{
			name:   "estimate",
			mutate: func(t *models.Task) { t.Estimate = "XL" },
			want:   []ChangeDetail{{Field: "Estimate", From: "S", To: "XL"}},
		},
		{
			// The monolith ignored description entirely, so rewriting one sent
			// no mail at all. Included here deliberately.
			name:   "description",
			mutate: func(t *models.Task) { t.Description = "Rewritten" },
			want: []ChangeDetail{
				{Field: "Description", From: "Original description", To: "Rewritten"},
			},
		},
		{
			// Also ignored by the monolith. Moving a task between projects is a
			// meaningful change to the person assigned to it.
			name:   "project",
			mutate: func(t *models.Task) { t.ProjectID = 9 },
			want:   []ChangeDetail{{Field: "Project", From: "3", To: "9"}},
		},
		{
			name:   "due date moved",
			mutate: func(t *models.Task) { t.DueDate = date(2026, 10, 15) },
			want:   []ChangeDetail{{Field: "Due Date", From: "2026-09-30", To: "2026-10-15"}},
		},
		{
			// DueDate is a *time.Time, so an unset one is a nil pointer. Render
			// it carelessly and the user gets a memory address in their inbox.
			name:   "due date cleared",
			mutate: func(t *models.Task) { t.DueDate = nil },
			want:   []ChangeDetail{{Field: "Due Date", From: "2026-09-30", To: "none"}},
		},
		{
			name: "due date set from nothing",
			mutate: func(t *models.Task) {
				t.DueDate = date(2026, 12, 1)
			},
			want: []ChangeDetail{{Field: "Due Date", From: "2026-09-30", To: "2026-12-01"}},
		},
		{
			// Same instant, different pointer. Comparing the pointers rather
			// than the times would report a change that did not happen -- and
			// this is exactly what a JSON round trip produces.
			name:   "due date replaced with an equal value",
			mutate: func(t *models.Task) { t.DueDate = date(2026, 9, 30) },
			want:   nil,
		},
		{
			name: "several fields at once",
			mutate: func(t *models.Task) {
				t.Status = "Done"
				t.Priority = "High"
				t.Title = "Renamed"
			},
			want: []ChangeDetail{
				{Field: "Title", From: "Original title", To: "Renamed"},
				{Field: "Status", From: "Not Started", To: "Done"},
				{Field: "Priority", From: "Low", To: "High"},
			},
		},
		{
			name:   "a field cleared to empty",
			mutate: func(t *models.Task) { t.Estimate = "" },
			want:   []ChangeDetail{{Field: "Estimate", From: "S", To: ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := baseTask()
			after := baseTask()
			tt.mutate(&after)

			got := DiffTask(before, after)

			if len(got) != len(tt.want) {
				t.Fatalf("got %d changes %+v, want %d %+v", len(got), got, len(tt.want), tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("change %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Field order must be identical on every run. Building the change list by
// ranging over a map would shuffle it, since Go randomises map iteration --
// the email's fields would reorder between sends and any order assertion would
// fail roughly one run in six. Running the same diff many times is what makes
// that surface deterministically.
func TestDiffOrderIsStable(t *testing.T) {
	before := baseTask()
	after := baseTask()
	after.Title = "Renamed"
	after.Description = "Rewritten"
	after.ProjectID = 9
	after.Status = "Done"
	after.Priority = "High"
	after.Estimate = "XL"
	after.DueDate = date(2026, 12, 25)

	first := DiffTask(before, after)
	if len(first) != 7 {
		t.Fatalf("changed every field but got %d changes: %+v", len(first), first)
	}

	for i := 0; i < 200; i++ {
		got := DiffTask(before, after)
		if len(got) != len(first) {
			t.Fatalf("run %d produced %d changes, want %d", i, len(got), len(first))
		}
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("run %d differs at position %d: %+v, want %+v -- "+
					"the diff is not deterministic", i, j, got[j], first[j])
			}
		}
	}
}

// Nothing changed must be an empty result, not a slice of one entry saying so.
// The handler's decision to publish or not is a plain length check.
func TestNoChangesIsEmpty(t *testing.T) {
	got := DiffTask(baseTask(), baseTask())
	if len(got) != 0 {
		t.Errorf("DiffTask on identical tasks = %+v, want empty", got)
	}
}

// Fields the user never sees must not count as changes. UpdatedAt in particular
// moves on every single save, so including it would make every no-op update
// look like a real one and defeat the whole rule.
func TestBookkeepingFieldsAreNotChanges(t *testing.T) {
	before := baseTask()
	before.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	after := baseTask()
	after.CreatedAt = before.CreatedAt
	after.UpdatedAt = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

	if got := DiffTask(before, after); len(got) != 0 {
		t.Errorf("UpdatedAt moving produced changes %+v; a no-op save would email", got)
	}
}

// A due date is a pointer, and a formatted change must never contain one.
func TestDueDateNeverRendersAPointer(t *testing.T) {
	before := baseTask()
	before.DueDate = nil
	after := baseTask()

	got := DiffTask(before, after)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1", len(got))
	}
	for _, v := range []string{got[0].From, got[0].To} {
		if len(v) > 1 && v[0] == '0' && v[1] == 'x' {
			t.Errorf("rendered a pointer address %q instead of a date", v)
		}
	}
	if got[0].From != "none" {
		t.Errorf("from = %q for an unset due date, want %q", got[0].From, "none")
	}
}

// NewTaskUpdated carries the diff; NewTaskDeleted does not.
func TestNewTaskUpdatedCarriesChanges(t *testing.T) {
	changes := []ChangeDetail{{Field: "Status", From: "Not Started", To: "Done"}}
	env := NewTaskUpdated(baseTask(), 7, changes)

	if env.EventType != EventTaskUpdated {
		t.Errorf("event_type = %q, want %q", env.EventType, EventTaskUpdated)
	}
	if len(env.Changes) != 1 || env.Changes[0].Field != "Status" {
		t.Errorf("changes = %+v, want the Status diff", env.Changes)
	}
	if env.TaskID != 42 || env.ActorID != 7 {
		t.Errorf("task_id/actor_id = %d/%d, want 42/7", env.TaskID, env.ActorID)
	}
}

func TestNewTaskDeleted(t *testing.T) {
	env := NewTaskDeleted(baseTask(), 7)

	if env.EventType != EventTaskDeleted {
		t.Errorf("event_type = %q, want %q", env.EventType, EventTaskDeleted)
	}
	if env.Changes != nil {
		t.Errorf("changes = %+v on a delete, want absent", env.Changes)
	}
	// The consumer cannot look the task up afterwards -- as far as every query
	// is concerned it is gone -- so the event has to carry it.
	if env.Task.Title != "Original title" {
		t.Errorf("deleted event does not carry the task: %+v", env.Task)
	}
}
