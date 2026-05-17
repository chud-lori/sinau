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

// Notifier delivers one task-due notification to one recipient via the
// channel it implements. Implementations are expected to be safe for
// concurrent use; the current worker calls them serially per task but that
// is not part of the contract.
type Notifier interface {
	NotifyTaskDue(ctx context.Context, to Recipient, rem domain.TaskReminder) error
}

// LogNotifier writes deadline reminders to the standard logger. It is the
// safe default channel — registered both for "log" and as a fallback for
// "email" when no SMTP config has been provided.
type LogNotifier struct{}

func (LogNotifier) NotifyTaskDue(_ context.Context, to Recipient, rem domain.TaskReminder) error {
	log.Printf("reminder via=log channel=%s task=%q due=%s room=%q to=%q email=%s role=%s",
		to.Channel, rem.Title, rem.DueDate, rem.RoomName, to.Name, to.Email, to.Role)
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
	reminders, err := w.store.DueTaskReminders(now, w.window)
	if err != nil {
		log.Printf("deadline reminder query failed: %v", err)
		return
	}
	for _, rem := range reminders {
		if ctx.Err() != nil {
			return
		}
		w.dispatch(ctx, rem)
		if err := w.store.MarkTaskReminded(rem.TaskID, time.Now().UTC()); err != nil {
			log.Printf("deadline reminder mark failed task=%s: %v", rem.TaskID, err)
		}
	}
}

func (w *Worker) dispatch(ctx context.Context, rem domain.TaskReminder) {
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
