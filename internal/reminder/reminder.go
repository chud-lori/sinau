// Package reminder periodically scans tasks with upcoming or overdue
// deadlines and dispatches them through a pluggable Notifier.
//
// The Notifier interface is the only seam needed to wire real delivery
// channels (email, WhatsApp, Telegram, Discord, web push, etc.) on top of the
// existing task storage and dedup logic. Add new implementations alongside
// LogNotifier in this package, then select them from cmd/sinau/main.go.
//
// Delivery contract:
//
//   - NotifyTaskDue MUST be safe to retry. The worker uses last_reminded_at
//     to dedup to one notification per task per day, but if NotifyTaskDue
//     succeeds and MarkTaskReminded then fails (e.g. DB hiccup), the same
//     reminder may be sent again on the next tick.
//   - NotifyTaskDue should respect the supplied context for cancellation
//     and any per-call timeouts.
//   - Returning a non-nil error skips MarkTaskReminded for that task so the
//     next run will retry. Use this for transient failures (network, 5xx)
//     and swallow permanent failures (bad address) internally.
package reminder

import (
	"context"
	"log"
	"time"

	"sinau/internal/domain"
	"sinau/internal/store"
)

// Notifier delivers a single task-due notification. Implementations are
// expected to be safe for concurrent use; the current worker calls them
// serially but that is not part of the contract.
type Notifier interface {
	NotifyTaskDue(context.Context, domain.TaskReminder) error
}

// LogNotifier writes deadline reminders to the standard logger. It is the
// default delivery channel and a useful stand-in until a real notifier is
// configured.
type LogNotifier struct{}

func (LogNotifier) NotifyTaskDue(ctx context.Context, rem domain.TaskReminder) error {
	log.Printf("deadline reminder: task=%q due=%s room=%q learner=%q email=%s",
		rem.Title, rem.DueDate, rem.RoomName, rem.AssigneeName, rem.AssigneeEmail)
	return nil
}

// MultiNotifier fans out one reminder to every wrapped notifier. It is the
// recommended way to send the same alert to multiple channels (e.g. log +
// email + WhatsApp). Errors from individual notifiers are logged and
// swallowed so a single broken channel does not block delivery on the rest;
// MultiNotifier itself always returns nil so the worker proceeds to mark the
// task as reminded.
type MultiNotifier []Notifier

func (m MultiNotifier) NotifyTaskDue(ctx context.Context, rem domain.TaskReminder) error {
	for _, n := range m {
		if n == nil {
			continue
		}
		if err := n.NotifyTaskDue(ctx, rem); err != nil {
			log.Printf("notifier %T failed for task=%s: %v", n, rem.TaskID, err)
		}
	}
	return nil
}

type Worker struct {
	store    *store.Store
	notifier Notifier
	every    time.Duration
	window   time.Duration
}

func NewWorker(store *store.Store, notifier Notifier, every, window time.Duration) *Worker {
	if notifier == nil {
		notifier = LogNotifier{}
	}
	if every <= 0 {
		every = time.Hour
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	return &Worker{store: store, notifier: notifier, every: every, window: window}
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
		if err := w.notifier.NotifyTaskDue(ctx, rem); err != nil {
			log.Printf("deadline reminder send failed task=%s: %v", rem.TaskID, err)
			continue
		}
		if err := w.store.MarkTaskReminded(rem.TaskID, time.Now().UTC()); err != nil {
			log.Printf("deadline reminder mark failed task=%s: %v", rem.TaskID, err)
		}
	}
}
