package reminder

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	"sinau/internal/domain"
	"sinau/internal/i18n"
)

// SMTPConfig describes how to reach an outbound SMTP relay. When Host is
// empty the EmailNotifier silently degrades to its fallback (the log
// channel), so deployments without SMTP still function.
type SMTPConfig struct {
	Host     string // "smtp.example.com:587". Empty disables email delivery.
	Username string
	Password string
	From     string // RFC 5322 address used as the envelope and From: header.
	STARTTLS bool   // true for port 587; false to skip (use only for local relays).
}

// EmailNotifier sends task-due reminders over SMTP. If SMTP is not
// configured (cfg.Host == "") it logs a warning the first time and routes
// every message to the Fallback notifier so the worker is never silently
// broken.
type EmailNotifier struct {
	cfg      SMTPConfig
	fallback Notifier
}

func NewEmailNotifier(cfg SMTPConfig, fallback Notifier) *EmailNotifier {
	if fallback == nil {
		fallback = LogNotifier{}
	}
	return &EmailNotifier{cfg: cfg, fallback: fallback}
}

// Configured reports whether real SMTP delivery will be attempted.
func (e *EmailNotifier) Configured() bool {
	return strings.TrimSpace(e.cfg.Host) != "" && strings.TrimSpace(e.cfg.From) != ""
}

func (e *EmailNotifier) NotifyTaskDue(ctx context.Context, to Recipient, rem domain.TaskReminder) error {
	if !e.Configured() {
		// Visible-once log line so operators can tell email is degraded.
		log.Printf("email notifier not configured (SINAU_SMTP_HOST/From unset); routing to fallback")
		return e.fallback.NotifyTaskDue(ctx, to, rem)
	}
	if to.Email == "" {
		return fmt.Errorf("recipient %s has no email address", to.UserID)
	}
	lang := recipientLang(to)
	subject := i18n.Tf(lang, "notif.task_due.subject", rem.Title, rem.DueDate)
	body := buildTaskBody(lang, to, rem)
	return e.send(ctx, to.Email, subject, body)
}

func (e *EmailNotifier) NotifyAssignmentDue(ctx context.Context, to Recipient, rem domain.AssignmentReminder) error {
	if !e.Configured() {
		log.Printf("email notifier not configured (SINAU_SMTP_HOST/From unset); routing to fallback")
		return e.fallback.NotifyAssignmentDue(ctx, to, rem)
	}
	if to.Email == "" {
		return fmt.Errorf("recipient %s has no email address", to.UserID)
	}
	lang := recipientLang(to)
	subject := i18n.Tf(lang, "notif.assignment_due.subject", rem.Title, rem.DueDate)
	body := buildAssignmentBody(lang, to, rem)
	return e.send(ctx, to.Email, subject, body)
}

func recipientLang(to Recipient) i18n.Lang {
	lang := i18n.Lang(to.Language)
	if !i18n.IsValid(lang) {
		return i18n.Default
	}
	return lang
}

func buildTaskBody(lang i18n.Lang, to Recipient, rem domain.TaskReminder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.task_due.greeting", to.Name))
	fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.task_due.body", rem.Title, rem.RoomName, rem.DueDate))
	if rem.Detail != "" {
		fmt.Fprintf(&b, "%s\n%s\n\n", i18n.T(lang, "notif.task_due.details"), rem.Detail)
	}
	fmt.Fprintf(&b, "%s\n\n%s\n", i18n.T(lang, "notif.task_due.footer"), i18n.T(lang, "notif.task_due.signature"))
	return b.String()
}

func buildAssignmentBody(lang i18n.Lang, to Recipient, rem domain.AssignmentReminder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.assignment_due.greeting", to.Name))
	fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.assignment_due.body", rem.Title, rem.RoomName, rem.DueDate))
	if rem.Instructions != "" {
		fmt.Fprintf(&b, "%s\n%s\n\n", i18n.T(lang, "notif.assignment_due.instructions"), rem.Instructions)
	}
	fmt.Fprintf(&b, "%s\n\n%s\n", i18n.T(lang, "notif.assignment_due.footer"), i18n.T(lang, "notif.task_due.signature"))
	return b.String()
}

func (e *EmailNotifier) NotifyEngagement(ctx context.Context, to Recipient, ev EngagementEvent) error {
	if !e.Configured() {
		log.Printf("email notifier not configured (SINAU_SMTP_HOST/From unset); routing to fallback")
		return e.fallback.NotifyEngagement(ctx, to, ev)
	}
	if to.Email == "" {
		return fmt.Errorf("recipient %s has no email address", to.UserID)
	}
	lang := recipientLang(to)
	subject := engagementSubject(lang, ev)
	body := buildEngagementBody(lang, to, ev)
	return e.send(ctx, to.Email, subject, body)
}

// engagementSubject picks the localised subject line for an engagement
// event. Defined once here so all delivery channels share wording.
func engagementSubject(lang i18n.Lang, ev EngagementEvent) string {
	switch ev.Kind {
	case EngagementReportComment:
		return i18n.Tf(lang, "notif.report_comment.subject", ev.ActorName, ev.RoomName)
	case EngagementSubmissionMade:
		return i18n.Tf(lang, "notif.submission_made.subject", ev.ActorName, ev.Title)
	case EngagementFeedbackPosted:
		return i18n.Tf(lang, "notif.feedback_posted.subject", ev.Title)
	}
	return "Sinau update"
}

func buildEngagementBody(lang i18n.Lang, to Recipient, ev EngagementEvent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.engagement.greeting", to.Name))
	switch ev.Kind {
	case EngagementReportComment:
		fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.report_comment.body", ev.ActorName, ev.RoomName))
	case EngagementSubmissionMade:
		fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.submission_made.body", ev.ActorName, ev.Title, ev.RoomName))
	case EngagementFeedbackPosted:
		if ev.Score != "" {
			fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.feedback_posted.body_score", ev.Title, ev.Score))
		} else {
			fmt.Fprintf(&b, "%s\n\n", i18n.Tf(lang, "notif.feedback_posted.body", ev.Title))
		}
	}
	if ev.Snippet != "" {
		fmt.Fprintf(&b, "%s\n%s\n\n", i18n.T(lang, "notif.engagement.snippet"), ev.Snippet)
	}
	fmt.Fprintf(&b, "%s\n\n%s\n", i18n.T(lang, "notif.engagement.footer"), i18n.T(lang, "notif.task_due.signature"))
	return b.String()
}

// send dials SMTP, optionally upgrades with STARTTLS, authenticates with
// PLAIN, and hands the message off. Wraps the dial in ctx so a hung relay
// does not block the worker forever.
func (e *EmailNotifier) send(ctx context.Context, to, subject, body string) error {
	host := e.cfg.Host
	serverName := host
	if i := strings.LastIndex(host, ":"); i > 0 {
		serverName = host[:i]
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", host, err)
	}
	c, err := smtp.NewClient(conn, serverName)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if e.cfg.STARTTLS {
		if err := c.StartTLS(&tls.Config{ServerName: serverName}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if e.cfg.Username != "" {
		auth := smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, serverName)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(e.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	msg := buildMessage(e.cfg.From, to, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

func buildMessage(from, to, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprint(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprint(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprint(&b, "\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return b.String()
}
