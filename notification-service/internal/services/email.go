// Package services holds the notification rendering and delivery.
//
// email.go is PORTED from monolith/internal/services/email.go. Copied, not
// imported: the monolith is a separate Go module and the old side of the
// Strangler Fig, so reaching into it would recouple exactly what this migration
// is separating. The HTML template and the priority/estimate/change renderers
// are carried across verbatim, so a rendered email matches what the monolith
// produced.
//
// Four things are deliberately NOT the same as the original:
//
//  1. The Send*Notification methods RETURN AN ERROR. The monolith's versions
//     logged failures and returned nothing, which was survivable inside a
//     fire-and-forget goroutine but is not here: the consumer has to tell a
//     transient failure (retry, leave the offset uncommitted) from a permanent
//     one (dead-letter it), and it cannot do that with a swallowed error.
//  2. They take an events.TaskPayload rather than a models.Task. The payload is
//     what arrives on the wire, and taking it directly is what keeps gorm, and
//     any database dependency, out of this package.
//  3. There is no services.ChangeDetail. The plan expected to port that struct
//     and add JSON tags to it; since the diff already arrives as
//     events.ChangeDetail carrying the right tags, a second identical struct
//     would be pure duplication. The Go field names still match the monolith's,
//     which is what the plan actually needed.
//  4. The SMTP call sits behind the one-method mailSender interface, so the
//     rendering can be tested without a mail server -- and the original logged
//     SMTP_USERNAME in the clear at startup, which this does not.
package services

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"

	"task-management-notification-service/internal/events"
)

// mailSender is the seam the tests use. Production wires smtpSender below; the
// tests wire a fake that captures the rendered bytes.
type mailSender interface {
	Send(to []string, msg []byte) error
}

// EmailService renders and delivers task notifications.
type EmailService struct {
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	FromEmail    string

	sender mailSender
}

// NewEmailService reads SMTP configuration from the environment once.
//
// With SMTP_USERNAME or SMTP_PASSWORD unset the service still constructs and
// still succeeds -- it logs what it would have sent instead of sending. That is
// the monolith's behaviour, kept on purpose: a missing credential should
// degrade notifications, not take the consumer down.
// TestUnconfiguredSMTPSendsNothing turns it from an accident into a guarantee.
func NewEmailService() *EmailService {
	username := os.Getenv("SMTP_USERNAME")
	password := os.Getenv("SMTP_PASSWORD")

	// Both masked. The monolith printed the username verbatim.
	log.Printf("[NOTIFICATION-SERVICE] email: username=%s password=%s",
		mask(username), mask(password))

	svc := &EmailService{
		SMTPHost:     valueOr(os.Getenv("SMTP_HOST"), "smtp.gmail.com"),
		SMTPPort:     valueOr(os.Getenv("SMTP_PORT"), "587"),
		SMTPUsername: username,
		SMTPPassword: password,
		FromEmail:    valueOr(os.Getenv("SMTP_FROM"), username),
	}
	svc.sender = &smtpSender{svc: svc}
	return svc
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// Configured reports whether real credentials are present.
func (e *EmailService) Configured() bool {
	return e.SMTPUsername != "" && e.SMTPPassword != ""
}

// mask reduces a credential to something safe to log. Applied to the username
// as well as the password: an address is not a secret, but it is personal data
// and it does not belong in container logs.
func mask(v string) string {
	if v == "" {
		return "(not set)"
	}
	if len(v) < 6 {
		return "***"
	}
	return v[:2] + "***" + v[len(v)-2:]
}

// smtpSender is the production mailSender: a thin wrapper over net/smtp.
type smtpSender struct{ svc *EmailService }

func (s *smtpSender) Send(to []string, msg []byte) error {
	auth := smtp.PlainAuth("", s.svc.SMTPUsername, s.svc.SMTPPassword, s.svc.SMTPHost)
	return smtp.SendMail(s.svc.SMTPHost+":"+s.svc.SMTPPort, auth, s.svc.FromEmail, to, msg)
}

// sendEmail assembles the MIME message and hands it to the sender.
//
// The blank line between the headers and the body is load-bearing: without it
// every mail client reads the first line of HTML as another header and shows an
// empty message. TestMessageHasProperHeaders pins it.
func (e *EmailService) sendEmail(to, subject, body string) error {
	if !e.Configured() {
		log.Printf("[NOTIFICATION-SERVICE] SMTP not configured - would send: %q to %s", subject, to)
		return nil
	}

	msg := strings.Join([]string{
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("From: %s", e.FromEmail),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-version: 1.0;",
		`Content-Type: text/html; charset="UTF-8";`,
		"",
		body,
	}, "\r\n")

	if err := e.sender.Send([]string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send %q to %s: %w", subject, to, err)
	}

	log.Printf("[NOTIFICATION-SERVICE] EMAIL SENT: %q to %s", subject, to)
	return nil
}

// SendTaskCreatedNotification renders and sends the new-task email.
func (e *EmailService) SendTaskCreatedNotification(task events.TaskPayload, userEmail string) error {
	subject := fmt.Sprintf("✅ New Task Created: %s", task.Title)
	body := e.createEmailTemplate("Task Created Successfully!", fmt.Sprintf(`
        <div class="content-section">
            <h3 style="color: #10b981; margin: 0 0 16px 0;">📋 Task Details</h3>
            <div class="detail-row">
                <span class="label">Title:</span>
                <span class="value">%s</span>
            </div>
            <div class="detail-row">
                <span class="label">Description:</span>
                <span class="value">%s</span>
            </div>
            <div class="detail-row">
                <span class="label">Status:</span>
                <span class="status-badge status-%s">%s</span>
            </div>
            %s
            %s
        </div>
        <div class="cta-section">
            <p style="margin: 0 0 16px 0; color: #6b7280;">Ready to get started on this task?</p>
            <a href="#" class="cta-button">View Task Details</a>
        </div>
    `,
		task.Title,
		e.getDisplayValue(task.Description, "No description"),
		strings.ToLower(strings.ReplaceAll(task.Status, " ", "-")),
		task.Status,
		e.getPriorityHTML(task.Priority),
		e.getEstimateHTML(task.Estimate),
	))

	return e.sendEmail(userEmail, subject, body)
}

// SendTaskUpdatedNotification renders and sends the change-summary email. The
// "What Changed" block lists exactly the fields in changes and nothing else --
// see TestUpdateEmailListsExactlyTheChangedFields.
func (e *EmailService) SendTaskUpdatedNotification(task events.TaskPayload, userEmail string, changes []events.ChangeDetail) error {
	subject := fmt.Sprintf("🔄 Task Updated: %s", task.Title)
	body := e.createEmailTemplate("Task Updated!", fmt.Sprintf(`
        <div class="content-section">
            <h3 style="color: #3b82f6; margin: 0 0 16px 0;">📝 What Changed</h3>
            <div class="changes-container">
                %s
            </div>
        </div>
        <div class="content-section">
            <h3 style="color: #6b7280; margin: 0 0 16px 0;">📋 Current Details</h3>
            <div class="detail-row">
                <span class="label">Title:</span>
                <span class="value">%s</span>
            </div>
            <div class="detail-row">
                <span class="label">Status:</span>
                <span class="status-badge status-%s">%s</span>
            </div>
            %s
            %s
        </div>
    `,
		e.getChangesHTML(changes),
		task.Title,
		strings.ToLower(strings.ReplaceAll(task.Status, " ", "-")),
		task.Status,
		e.getPriorityHTML(task.Priority),
		e.getEstimateHTML(task.Estimate),
	))

	return e.sendEmail(userEmail, subject, body)
}

func (e *EmailService) createEmailTemplate(title, content string) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>%s</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            line-height: 1.6; 
            color: #374151;
            background-color: #f9fafb;
        }
        .container {
            max-width: 600px;
            margin: 0 auto;
            background: #ffffff;
            border-radius: 12px;
            overflow: hidden;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.05);
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            padding: 32px 24px;
            text-align: center;
        }
        .header h1 {
            color: #ffffff;
            font-size: 24px;
            font-weight: 600;
            margin: 0;
        }
        .body {
            padding: 32px 24px;
        }
        .content-section {
            margin-bottom: 32px;
        }
        .detail-row {
            display: flex;
            align-items: center;
            margin-bottom: 12px;
            padding: 12px;
            background: #f8fafc;
            border-radius: 8px;
        }
        .label {
            font-weight: 600;
            color: #4b5563;
            min-width: 100px;
            margin-right: 16px;
        }
        .value {
            color: #1f2937;
        }
        .status-badge {
            padding: 4px 12px;
            border-radius: 20px;
            font-size: 14px;
            font-weight: 500;
        }
        .status-not-started { background: #fef3c7; color: #92400e; }
        .status-in-progress { background: #dbeafe; color: #1e40af; }
        .status-done { background: #d1fae5; color: #065f46; }
        .status-blocked { background: #fee2e2; color: #dc2626; }
        .priority-high, .priority-urgent { color: #dc2626; font-weight: 600; }
        .priority-medium { color: #d97706; font-weight: 500; }
        .priority-low { color: #059669; }
        .estimate-badge {
            background: #e0e7ff;
            color: #3730a3;
            padding: 4px 8px;
            border-radius: 6px;
            font-size: 12px;
            font-weight: 600;
        }
        .changes-container {
            background: #eff6ff;
            padding: 16px;
            border-radius: 8px;
            border-left: 4px solid #3b82f6;
        }
        .change-item {
            color: #1e40af;
            font-weight: 500;
            margin-bottom: 4px;
        }
        .cta-section {
            text-align: center;
            padding: 24px;
            background: #f8fafc;
            border-radius: 8px;
        }
        .cta-button {
            display: inline-block;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: #ffffff;
            padding: 12px 24px;
            border-radius: 8px;
            text-decoration: none;
            font-weight: 600;
            transition: transform 0.2s;
        }
        .cta-button:hover {
            transform: translateY(-1px);
        }
        .footer {
            background: #1f2937;
            padding: 24px;
            text-align: center;
        }
        .footer p {
            color: #9ca3af;
            font-size: 14px;
            margin: 0;
        }
		.change-from {
			background: #fee2e2;
			color: #dc2626;
			padding: 2px 6px;
			border-radius: 4px;
			font-size: 12px;
		}
		.change-to {
			background: #dcfce7;
			color: #166534;
			padding: 2px 6px;
			border-radius: 4px;
			font-size: 12px;
			font-weight: 600;
		}
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>%s</h1>
        </div>
        <div class="body">
            %s
        </div>
        <div class="footer">
            <p>Task Management API • Built with ❤️ by Luis</p>
        </div>
    </div>
</body>
</html>
    `, title, title, content)
}

// Helper methods

func (e *EmailService) getDisplayValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (e *EmailService) getPriorityHTML(priority string) string {
	if priority == "" {
		return ""
	}

	priorityColors := map[string]string{
		"high":   "#dc2626",
		"urgent": "#dc2626",
		"medium": "#d97706",
		"low":    "#059669",
	}

	style := ""
	if color, exists := priorityColors[strings.ToLower(priority)]; exists {
		style = fmt.Sprintf(`style="color: %s; font-weight: 600;"`, color)
	}

	return fmt.Sprintf(`
        <div class="detail-row">
            <span class="label">Priority:</span>
            <span class="value" %s>%s</span>
        </div>
    `, style, priority)
}

func (e *EmailService) getEstimateHTML(estimate string) string {
	if estimate == "" {
		return ""
	}
	return fmt.Sprintf(`
        <div class="detail-row">
            <span class="label">Estimate:</span>
            <span class="estimate-badge">%s</span>
        </div>
    `, estimate)
}

func (e *EmailService) getChangesHTML(changes []events.ChangeDetail) string {
	if len(changes) == 0 {
		return `<div class="change-item">📝 Task details updated</div>`
	}

	html := ""
	for _, change := range changes {
		fromValue := e.getDisplayValue(change.From, "empty")
		toValue := e.getDisplayValue(change.To, "empty")

		html += fmt.Sprintf(`
            <div class="change-item">
                ✏️ <strong>%s</strong> changed from <strong>%s</strong> to <strong>%s</strong>
            </div>
        `, change.Field, fromValue, toValue)
	}
	return html
}
