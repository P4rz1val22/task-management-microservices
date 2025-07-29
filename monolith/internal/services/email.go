package services

import (
	"fmt"
	"github.com/P4rz1val22/task-management-api/internal/models"
	"log"
	"net/smtp"
	"os"
	"strings"
)

type EmailService struct {
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	FromEmail    string
}

type ChangeDetail struct {
	Field string
	From  string
	To    string
}

func NewEmailService() *EmailService {
	username := os.Getenv("SMTP_USERNAME")
	password := os.Getenv("SMTP_PASSWORD")

	// Debug logging
	log.Printf("🔧 Email service initialization:")
	log.Printf("   SMTP_USERNAME: %s", username)
	log.Printf("   SMTP_PASSWORD: %s", maskPassword(password))

	return &EmailService{
		SMTPHost:     "smtp.gmail.com",
		SMTPPort:     "587",
		SMTPUsername: username,
		SMTPPassword: password,
		FromEmail:    username,
	}
}

func maskPassword(password string) string {
	if password == "" {
		return "(not set)"
	}
	if len(password) < 4 {
		return "***"
	}
	return password[:2] + "***" + password[len(password)-2:]
}

func (e *EmailService) sendEmail(to, subject, body string) error {
	// Check if SMTP is configured
	if e.SMTPUsername == "" || e.SMTPPassword == "" {
		log.Printf("📧 SMTP not configured - would send: %s to %s", subject, to)
		return nil
	}

	auth := smtp.PlainAuth("", e.SMTPUsername, e.SMTPPassword, e.SMTPHost)

	msg := []string{
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("From: %s", e.FromEmail),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-version: 1.0;",
		"Content-Type: text/html; charset=\"UTF-8\";",
		"",
		body,
	}

	err := smtp.SendMail(
		e.SMTPHost+":"+e.SMTPPort,
		auth,
		e.FromEmail,
		[]string{to},
		[]byte(strings.Join(msg, "\r\n")),
	)

	if err != nil {
		log.Printf("❌ Email failed to send: %v", err)
		return err
	}

	log.Printf("📧 EMAIL SENT: %s to %s", subject, to)
	return nil
}

func (e *EmailService) SendTaskCreatedNotification(task models.Task, userEmail string) {
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

	if err := e.sendEmail(userEmail, subject, body); err != nil {
		log.Printf("❌ Failed to send task creation email: %v", err)
	}
}

func (e *EmailService) SendTaskUpdatedNotification(task models.Task, userEmail string, changes []ChangeDetail) {
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

	if err := e.sendEmail(userEmail, subject, body); err != nil {
		log.Printf("❌ Failed to send task update email: %v", err)
	}
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

func (e *EmailService) getChangesHTML(changes []ChangeDetail) string {
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
