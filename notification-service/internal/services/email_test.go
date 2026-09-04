package services

import (
	"errors"
	"strings"
	"testing"
	"time"

	"task-management-notification-service/internal/events"
)

// fakeSender captures what would have gone to the SMTP server, so the tests
// assert on a rendered message rather than on a mock's call log.
type fakeSender struct {
	sent [][]byte
	to   [][]string
	err  error
}

func (f *fakeSender) Send(to []string, msg []byte) error {
	if f.err != nil {
		return f.err
	}
	f.to = append(f.to, to)
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeSender) lastBody(t *testing.T) string {
	t.Helper()
	if len(f.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	return string(f.sent[len(f.sent)-1])
}

// configured builds a service that believes SMTP is set up, wired to a fake.
func configured(sender mailSender) *EmailService {
	return &EmailService{
		SMTPHost:     "smtp.example.com",
		SMTPPort:     "587",
		SMTPUsername: "bot@example.com",
		SMTPPassword: "hunter2",
		FromEmail:    "bot@example.com",
		sender:       sender,
	}
}

func samplePayload() events.TaskPayload {
	due := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	return events.TaskPayload{
		ID:          42,
		Title:       "Ship the notifications",
		Description: "port the email service",
		ProjectID:   3,
		Status:      "In Progress",
		Priority:    "High",
		Estimate:    "M",
		DueDate:     &due,
	}
}

// The headline behaviour of this stage. An update email must list exactly the
// fields that changed -- naming an unchanged field is the failure a user would
// actually notice and complain about, and it is pure string rendering, so there
// is no excuse for not testing it.
func TestUpdateEmailListsExactlyTheChangedFields(t *testing.T) {
	tests := []struct {
		name        string
		changes     []events.ChangeDetail
		wantPresent []string
		wantAbsent  []string
	}{
		{
			name: "single field",
			changes: []events.ChangeDetail{
				{Field: "Status", From: "Not Started", To: "In Progress"},
			},
			wantPresent: []string{"Status", "Not Started", "In Progress"},
			wantAbsent:  []string{"Priority</strong>", "Estimate</strong>", "Title</strong>"},
		},
		{
			name: "several fields",
			changes: []events.ChangeDetail{
				{Field: "Status", From: "Not Started", To: "Done"},
				{Field: "Priority", From: "Low", To: "Urgent"},
			},
			wantPresent: []string{"Status", "Not Started", "Done", "Priority", "Low", "Urgent"},
			wantAbsent:  []string{"Estimate</strong>"},
		},
		{
			name: "a field cleared to empty renders readably",
			changes: []events.ChangeDetail{
				{Field: "Estimate", From: "L", To: ""},
			},
			wantPresent: []string{"Estimate", "empty"},
			wantAbsent:  []string{"Status</strong>", "Priority</strong>"},
		},
		{
			name: "a field set from empty renders readably",
			changes: []events.ChangeDetail{
				{Field: "Description", From: "", To: "now has one"},
			},
			wantPresent: []string{"Description", "empty", "now has one"},
			wantAbsent:  []string{"Status</strong>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeSender{}
			svc := configured(f)

			if err := svc.SendTaskUpdatedNotification(samplePayload(), "user@example.com", tt.changes); err != nil {
				t.Fatalf("SendTaskUpdatedNotification: %v", err)
			}

			body := f.lastBody(t)
			// The change list is the region between the "What Changed" heading
			// and the "Current Details" heading. Asserting on the whole body
			// would be meaningless, since Current Details legitimately names
			// every field whether it changed or not.
			changeBlock := betweenMarkers(t, body, "What Changed", "Current Details")

			for _, want := range tt.wantPresent {
				if !strings.Contains(changeBlock, want) {
					t.Errorf("change list does not mention %q\n--- block ---\n%s", want, changeBlock)
				}
			}
			for _, notWant := range tt.wantAbsent {
				if strings.Contains(changeBlock, notWant) {
					t.Errorf("change list mentions %q, which did not change\n--- block ---\n%s",
						notWant, changeBlock)
				}
			}
		})
	}
}

func betweenMarkers(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("body has no %q marker", start)
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		t.Fatalf("body has no %q marker after %q", end, start)
	}
	return body[i : i+j]
}

// The monolith's graceful degradation, turned from an accident into a
// guarantee: with no credentials, nothing is sent and nothing errors.
func TestUnconfiguredSMTPSendsNothing(t *testing.T) {
	f := &fakeSender{}
	svc := &EmailService{
		SMTPHost: "smtp.example.com",
		SMTPPort: "587",
		sender:   f,
		// Username and password deliberately empty.
	}

	if err := svc.SendTaskCreatedNotification(samplePayload(), "user@example.com"); err != nil {
		t.Errorf("unconfigured send returned %v, want nil", err)
	}
	if len(f.sent) != 0 {
		t.Errorf("sent %d messages with no credentials, want 0", len(f.sent))
	}
	if svc.Configured() {
		t.Error("Configured() is true with no credentials")
	}
}

// A send failure must reach the caller. The consumer decides whether to retry
// or dead-letter, and it cannot do that if the error is swallowed here -- which
// is exactly what the monolith's version did.
func TestSendErrorsPropagate(t *testing.T) {
	boom := errors.New("smtp: connection refused")
	svc := configured(&fakeSender{err: boom})

	err := svc.SendTaskCreatedNotification(samplePayload(), "user@example.com")
	if err == nil {
		t.Fatal("SendTaskCreatedNotification returned nil; the send error was swallowed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap %v", err, boom)
	}

	err = svc.SendTaskUpdatedNotification(samplePayload(), "user@example.com", nil)
	if err == nil {
		t.Fatal("SendTaskUpdatedNotification returned nil; the send error was swallowed")
	}
}

func TestCreatedEmailContainsTheTaskDetails(t *testing.T) {
	f := &fakeSender{}
	svc := configured(f)

	task := samplePayload()
	if err := svc.SendTaskCreatedNotification(task, "user@example.com"); err != nil {
		t.Fatalf("send: %v", err)
	}

	body := f.lastBody(t)
	for _, want := range []string{task.Title, task.Description, task.Status, task.Priority, task.Estimate} {
		if !strings.Contains(body, want) {
			t.Errorf("created email does not contain %q", want)
		}
	}
	if len(f.to) != 1 || f.to[0][0] != "user@example.com" {
		t.Errorf("recipients = %v, want [[user@example.com]]", f.to)
	}
}

// A description-less task must not render an empty gap where the text should be.
func TestCreatedEmailFallsBackForEmptyFields(t *testing.T) {
	f := &fakeSender{}
	svc := configured(f)

	task := samplePayload()
	task.Description = ""
	task.Priority = ""
	task.Estimate = ""

	if err := svc.SendTaskCreatedNotification(task, "user@example.com"); err != nil {
		t.Fatalf("send: %v", err)
	}

	body := f.lastBody(t)
	if !strings.Contains(body, "No description") {
		t.Error("empty description does not fall back to 'No description'")
	}
	// Priority and Estimate render as nothing at all rather than an empty row.
	if strings.Contains(body, `<span class="label">Priority:</span>`) {
		t.Error("an unset priority still rendered its row")
	}
	if strings.Contains(body, `<span class="label">Estimate:</span>`) {
		t.Error("an unset estimate still rendered its row")
	}
}

// The message must be a well-formed MIME email: headers, blank line, HTML body.
// The monolith built this by hand with string joins and never tested it.
func TestMessageHasProperHeaders(t *testing.T) {
	f := &fakeSender{}
	svc := configured(f)

	if err := svc.SendTaskCreatedNotification(samplePayload(), "user@example.com"); err != nil {
		t.Fatalf("send: %v", err)
	}

	msg := f.lastBody(t)
	for _, want := range []string{
		"To: user@example.com",
		"From: bot@example.com",
		"Subject: ",
		"MIME-version: 1.0;",
		`Content-Type: text/html; charset="UTF-8";`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing header %q", want)
		}
	}
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Error("no blank line between headers and body; the body would be read as a header")
	}
	if !strings.Contains(msg, "<!DOCTYPE html>") {
		t.Error("body is not the HTML template")
	}
}

func TestSubjectsNameTheTask(t *testing.T) {
	f := &fakeSender{}
	svc := configured(f)
	task := samplePayload()

	if err := svc.SendTaskCreatedNotification(task, "u@e.com"); err != nil {
		t.Fatalf("send: %v", err)
	}
	created := f.lastBody(t)
	if !strings.Contains(created, "Subject: ") || !strings.Contains(created, task.Title) {
		t.Error("created subject does not name the task")
	}

	if err := svc.SendTaskUpdatedNotification(task, "u@e.com", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	updated := f.lastBody(t)
	if !strings.Contains(updated, task.Title) {
		t.Error("updated subject does not name the task")
	}
	if created == updated {
		t.Error("created and updated emails are identical")
	}
}

// An update carrying no diff still has to render something rather than an empty
// box. Stage 7 stops publishing these, but the consumer must not depend on that.
func TestUpdateWithNoChangesStillRenders(t *testing.T) {
	f := &fakeSender{}
	svc := configured(f)

	if err := svc.SendTaskUpdatedNotification(samplePayload(), "u@e.com", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	body := f.lastBody(t)
	if !strings.Contains(body, "Task details updated") {
		t.Error("an empty change set does not render its fallback line")
	}
}

// The monolith logged SMTP_USERNAME in the clear at startup. The password was
// masked; the address was not. Ported code should not carry that forward.
func TestMaskHidesCredentials(t *testing.T) {
	tests := []struct{ in, wantNot string }{
		{"hunter2placeholder", "hunter2placeholder"},
		{"bot@example.com", "bot@example.com"},
		{"ab", "ab"},
	}
	for _, tt := range tests {
		if got := mask(tt.in); strings.Contains(got, tt.wantNot) {
			t.Errorf("mask(%q) = %q, which still reveals the value", tt.in, got)
		}
	}
	if got := mask(""); got != "(not set)" {
		t.Errorf("mask(\"\") = %q, want %q", got, "(not set)")
	}
}
