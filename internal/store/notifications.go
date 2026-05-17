package store

import (
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// DueTaskReminders returns one (task, recipient) pair per task that's
// due within the window and still owed by at least one recipient. The
// worker uses last_reminded_at to dedup per task per day, so we don't
// have to filter individual recipients here.
//
//   - Individually-assigned task: one row for the named assignee
//     (unless they've already submitted).
//   - Broadcast task (assigned_to=''): one row for every mentee in
//     the room who hasn't submitted.
//
// The dedup grain is per-task-per-day (one round fans all recipients
// at once, then the worker stamps last_reminded_at once per task).
func (s *Store) DueTaskReminders(now time.Time, window time.Duration) ([]domain.TaskReminder, error) {
	start := now.UTC().Format("2006-01-02")
	end := now.UTC().Add(window).Format("2006-01-02")
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.due_date, r.id, r.name, r.mode, u.id, u.name, u.email, u.language
		FROM tasks t
		JOIN rooms r ON r.id = t.room_id
		JOIN memberships m ON m.room_id = t.room_id AND m.role = ?
		JOIN users u ON u.id = m.user_id
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = u.id
		WHERE t.deleted_at = ''
		  AND t.due_date != ''
		  AND t.due_date <= ?
		  AND (t.last_reminded_at = '' OR t.last_reminded_at < ?)
		  AND (t.assigned_to = '' OR t.assigned_to = u.id)
		  AND (sub.id IS NULL OR sub.status = 'revise')
		ORDER BY t.due_date ASC, t.id ASC, u.name ASC`, domain.RoleMentee, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskReminder
	for rows.Next() {
		var rem domain.TaskReminder
		if err := rows.Scan(&rem.TaskID, &rem.Title, &rem.Detail, &rem.DueDate, &rem.RoomID, &rem.RoomName, &rem.RoomMode,
			&rem.AssigneeID, &rem.AssigneeName, &rem.AssigneeEmail, &rem.AssigneeLanguage); err != nil {
			return nil, err
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

func (s *Store) MarkTaskReminded(taskID string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET last_reminded_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), taskID)
	return err
}

// NotificationPrefsFor returns the user's stored preferences, or a default
// "off" preference if no row exists yet. Always returns a usable value.
func (s *Store) NotificationPrefsFor(userID string) domain.NotificationPrefs {
	prefs := domain.NotificationPrefs{
		UserID:  userID,
		Enabled: false,
		Channel: domain.NotifChannelOff,
	}
	var enabled int
	err := s.db.QueryRow(`SELECT enabled, channel, whatsapp_number, telegram_chat_id, updated_at
		FROM notification_prefs WHERE user_id = ?`, userID).
		Scan(&enabled, &prefs.Channel, &prefs.WhatsAppNumber, &prefs.TelegramChatID, &prefs.UpdatedAt)
	if err != nil {
		return prefs
	}
	prefs.Enabled = enabled == 1
	return prefs
}

// SetNotificationPrefs upserts the user's preferences. The caller is
// expected to validate channel via domain.ValidNotifChannel and normalise
// contact fields. UserID on the struct identifies the row.
func (s *Store) SetNotificationPrefs(prefs domain.NotificationPrefs) error {
	e := 0
	if prefs.Enabled {
		e = 1
	}
	_, err := s.db.Exec(`INSERT INTO notification_prefs(user_id, enabled, channel, whatsapp_number, telegram_chat_id, updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET
			enabled=excluded.enabled,
			channel=excluded.channel,
			whatsapp_number=excluded.whatsapp_number,
			telegram_chat_id=excluded.telegram_chat_id,
			updated_at=excluded.updated_at`,
		prefs.UserID, e, prefs.Channel, prefs.WhatsAppNumber, prefs.TelegramChatID, auth.Now())
	return err
}
