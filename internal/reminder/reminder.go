// Package reminder periodically scans tasks with upcoming or overdue
// deadlines and dispatches them through pluggable channels (log, email, and
// later WhatsApp / etc.) based on each recipient's stored preferences.
//
// To wire a new delivery channel:
//
//  1. Add a new implementation of Notifier in this package (see EmailNotifier
//     for the SMTP example).
//  2. Register it under a channel name when constructing Worker in
//     cmd/sinau/main.go.
//  3. Document the channel name as a valid value for
//     notification_prefs.channel in store.Migrate.
//
// Delivery contract:
//
//   - NotifyTaskDue MUST be safe to retry. The worker uses last_reminded_at
//     to dedup to one notification per task per day across all recipients,
//     but a successful Notify + failed MarkTaskReminded leaves the same
//     reminder eligible on the next tick.
//   - Notifiers should respect ctx for cancellation and timeouts.
//   - A non-nil error skips MarkTaskReminded for that task. Use it for
//     transient transport errors; swallow permanent ones internally.
package reminder

import (
	"context"
	"log"
	"time"

	"sinau/internal/domain"
	"sinau/internal/store"
)

// Recipient identifies who a notification is being sent to and how.
// Channel is one of domain.NotifChannel* and must match a key in the
// Worker's notifier registry, otherwise the message is dropped (logged).
//
// The channel-specific contact fields (Email, WhatsApp, Telegram) are
// populated from the user's notification_prefs row at dispatch time. New
// channels add a new field here and a new Notifier implementation; the
// worker, store, and web layers stay untouched.
type Recipient struct {
	UserID   string
	Name     string
	Channel  string
	Role     string // "mentor" or "mentee" — useful for future filters / templates.
	Language string // BCP-47 tag from users.language; falls through to i18n.Default if empty.
	Email    string
	WhatsApp string // E.164 phone number, e.g. "+6281234567890".
	Telegram string // Telegram chat ID (numeric string; channels use negative IDs).
}

// Notifier delivers a deadline reminder to one recipient via the channel
// it implements. Both reminder shapes — mentorship tasks and classroom
// assignments — flow through the same notifier so adding a new channel
// stays a single-file change. Implementations are expected to be safe
// for concurrent use; the current worker calls them serially but that is
// not part of the contract.
type Notifier interface {
	NotifyTaskDue(ctx context.Context, to Recipient, rem domain.TaskReminder) error
	NotifyAssignmentDue(ctx context.Context, to Recipient, rem domain.AssignmentReminder) error
	// NotifyEngagement delivers a non-deadline event (comment on a
	// report, new submission, posted feedback). Fired from web
	// handlers, not the worker, so retries/dedup are the caller's
	// responsibility. Implementations should fall back to the log
	// channel rather than returning errors for "not configured" cases,
	// matching the contract used by NotifyTaskDue.
	NotifyEngagement(ctx context.Context, to Recipient, ev EngagementEvent) error
}

// LogNotifier writes deadline reminders to the standard logger. It is the
// safe default channel — registered both for "log" and as a fallback for
// "email" when no SMTP config has been provided.
type LogNotifier struct{}

func (LogNotifier) NotifyTaskDue(_ context.Context, to Recipient, rem domain.TaskReminder) error {
	log.Printf("reminder via=log kind=task channel=%s task=%q due=%s room=%q to=%q email=%s role=%s",
		to.Channel, rem.Title, rem.DueDate, rem.RoomName, to.Name, to.Email, to.Role)
	return nil
}

func (LogNotifier) NotifyAssignmentDue(_ context.Context, to Recipient, rem domain.AssignmentReminder) error {
	log.Printf("reminder via=log kind=assignment channel=%s assignment=%q due=%s class=%q to=%q email=%s",
		to.Channel, rem.Title, rem.DueDate, rem.RoomName, to.Name, to.Email)
	return nil
}

func (LogNotifier) NotifyEngagement(_ context.Context, to Recipient, ev EngagementEvent) error {
	log.Printf("engagement via=log kind=%s channel=%s actor=%q room=%q title=%q to=%q email=%s",
		ev.Kind, to.Channel, ev.ActorName, ev.RoomName, ev.Title, to.Name, to.Email)
	return nil
}

// Worker scans due tasks and routes one notification per recipient through
// the appropriate channel notifier.
type Worker struct {
	store     *store.Store
	notifiers map[string]Notifier
	every     time.Duration
	window    time.Duration
}

func NewWorker(s *store.Store, notifiers map[string]Notifier, every, window time.Duration) *Worker {
	if notifiers == nil {
		notifiers = map[string]Notifier{}
	}
	// Always provide a log channel so prefs.Channel="log" always works.
	if _, ok := notifiers[domain.NotifChannelLog]; !ok {
		notifiers[domain.NotifChannelLog] = LogNotifier{}
	}
	if every <= 0 {
		every = time.Hour
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	return &Worker{store: s, notifiers: notifiers, every: every, window: window}
}

func (w *Worker) Run(ctx context.Context) {
	w.runOnce(ctx)
	ticker := time.NewTicker(w.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	now := time.Now().UTC()
	w.runTaskReminders(ctx, now)
	if ctx.Err() != nil {
		return
	}
	w.runAssignmentReminders(ctx, now)
}

func (w *Worker) runTaskReminders(ctx context.Context, now time.Time) {
	reminders, err := w.store.DueTaskReminders(now, w.window)
	if err != nil {
		log.Printf("task reminder query failed: %v", err)
		return
	}
	for _, rem := range reminders {
		if ctx.Err() != nil {
			return
		}
		w.dispatchTask(ctx, rem)
		if err := w.store.MarkTaskReminded(rem.TaskID, time.Now().UTC()); err != nil {
			log.Printf("task reminder mark failed task=%s: %v", rem.TaskID, err)
		}
	}
}

// runAssignmentReminders fans each due classroom assignment out to every
// mentee in the room who has not yet submitted. The store returns one
// row per (assignment, mentee) pair; we dispatch each row, then mark the
// assignment itself as reminded so the next tick doesn't re-ping the
// same students.
func (w *Worker) runAssignmentReminders(ctx context.Context, now time.Time) {
	reminders, err := w.store.DueAssignmentReminders(now, w.window)
	if err != nil {
		log.Printf("assignment reminder query failed: %v", err)
		return
	}
	reminded := map[string]bool{}
	for _, rem := range reminders {
		if ctx.Err() != nil {
			return
		}
		w.dispatchAssignment(ctx, rem)
		reminded[rem.AssignmentID] = true
	}
	stamp := time.Now().UTC()
	for assignmentID := range reminded {
		if err := w.store.MarkAssignmentReminded(assignmentID, stamp); err != nil {
			log.Printf("assignment reminder mark failed assignment=%s: %v", assignmentID, err)
		}
	}
}

func (w *Worker) dispatchTask(ctx context.Context, rem domain.TaskReminder) {
	prefs := w.store.NotificationPrefsFor(rem.AssigneeID)
	if !prefs.Enabled || prefs.Channel == domain.NotifChannelOff {
		return
	}
	notifier, ok := w.notifiers[prefs.Channel]
	if !ok {
		log.Printf("no notifier registered for channel=%q user=%s", prefs.Channel, rem.AssigneeID)
		return
	}
	to := Recipient{
		UserID:   rem.AssigneeID,
		Name:     rem.AssigneeName,
		Channel:  prefs.Channel,
		Role:     domain.RoleMentee,
		Language: rem.AssigneeLanguage,
		Email:    rem.AssigneeEmail,
		WhatsApp: prefs.WhatsAppNumber,
		Telegram: prefs.TelegramChatID,
	}
	if err := notifier.NotifyTaskDue(ctx, to, rem); err != nil {
		log.Printf("reminder send failed channel=%s task=%s user=%s: %v",
			prefs.Channel, rem.TaskID, rem.AssigneeID, err)
	}
}

func (w *Worker) dispatchAssignment(ctx context.Context, rem domain.AssignmentReminder) {
	prefs := w.store.NotificationPrefsFor(rem.MenteeID)
	if !prefs.Enabled || prefs.Channel == domain.NotifChannelOff {
		return
	}
	notifier, ok := w.notifiers[prefs.Channel]
	if !ok {
		log.Printf("no notifier registered for channel=%q user=%s", prefs.Channel, rem.MenteeID)
		return
	}
	to := Recipient{
		UserID:   rem.MenteeID,
		Name:     rem.MenteeName,
		Channel:  prefs.Channel,
		Role:     domain.RoleMentee,
		Language: rem.MenteeLanguage,
		Email:    rem.MenteeEmail,
		WhatsApp: prefs.WhatsAppNumber,
		Telegram: prefs.TelegramChatID,
	}
	if err := notifier.NotifyAssignmentDue(ctx, to, rem); err != nil {
		log.Printf("reminder send failed channel=%s assignment=%s user=%s: %v",
			prefs.Channel, rem.AssignmentID, rem.MenteeID, err)
	}
}
