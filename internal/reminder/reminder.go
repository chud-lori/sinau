package reminder

import (
	"context"
	"log"
	"time"

	"sinau/internal/domain"
	"sinau/internal/store"
)

type Notifier interface {
	NotifyTaskDue(context.Context, domain.TaskReminder) error
}

type LogNotifier struct{}

func (LogNotifier) NotifyTaskDue(ctx context.Context, rem domain.TaskReminder) error {
	log.Printf("deadline reminder: task=%q due=%s room=%q learner=%q email=%s", rem.Title, rem.DueDate, rem.RoomName, rem.AssigneeName, rem.AssigneeEmail)
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
	reminders, err := w.store.DueTaskReminders(time.Now().UTC(), w.window)
	if err != nil {
		log.Printf("deadline reminder query failed: %v", err)
		return
	}
	for _, rem := range reminders {
		if err := w.notifier.NotifyTaskDue(ctx, rem); err != nil {
			log.Printf("deadline reminder send failed task=%s: %v", rem.TaskID, err)
			continue
		}
		if err := w.store.MarkTaskReminded(rem.TaskID, time.Now().UTC()); err != nil {
			log.Printf("deadline reminder mark failed task=%s: %v", rem.TaskID, err)
		}
	}
}
