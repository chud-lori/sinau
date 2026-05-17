package store

import (
	"time"

	"sinau/internal/domain"
)

func (s *Store) MentorDashboard(userID string) (domain.MentorDashboard, error) {
	rooms, err := s.RoomsFor(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	// menteeProgress is the most expensive query in the dashboard
	// (5 correlated subqueries × N mentees). Compute once and share it
	// with mentorSummary, which only needs the "quiet" count from it.
	mentees, err := s.menteeProgress(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	summary, err := s.mentorSummary(userID, mentees)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	attention, err := s.mentorAttention(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	return domain.MentorDashboard{Rooms: rooms, Summary: summary, AttentionItems: attention, Mentees: mentees}, nil
}

func (s *Store) MenteeDashboard(userID string) (domain.MenteeDashboard, error) {
	rooms, err := s.RoomsFor(userID)
	if err != nil {
		return domain.MenteeDashboard{}, err
	}
	tasks, err := s.menteeDashboardTasks(userID)
	if err != nil {
		return domain.MenteeDashboard{}, err
	}
	reports, err := s.menteeRecentReports(userID)
	if err != nil {
		return domain.MenteeDashboard{}, err
	}
	summary := domain.DashboardSummary{Rooms: len(rooms)}
	for _, t := range tasks {
		// menteeDashboardTasks already filters out reviewed submissions,
		// so every row here counts as an open task.
		summary.OpenTasks++
		switch t.DueState {
		case "due-soon":
			summary.DueSoonTasks++
		case "overdue":
			summary.OverdueTasks++
		}
	}
	weekStart := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	for _, r := range reports {
		if r.CreatedAt >= weekStart {
			summary.ReportsThisWeek++
		}
		if r.Blocker != "" {
			summary.Blockers++
		}
	}
	return domain.MenteeDashboard{Rooms: rooms, Summary: summary, Tasks: tasks, RecentReports: reports}, nil
}

// mentorSummary computes the dashboard counters. Callers should pass the
// already-fetched mentee progress slice to avoid running the expensive
// menteeProgress query twice per dashboard render.
func (s *Store) mentorSummary(userID string, mentees []domain.MenteeProgress) (domain.DashboardSummary, error) {
	var out domain.DashboardSummary
	queries := []struct {
		dst *int
		sql string
	}{
		{&out.Rooms, `SELECT COUNT(*) FROM memberships WHERE user_id = ? AND role = 'mentor'`},
		{&out.ActiveMentees, `SELECT COUNT(DISTINCT ml.user_id) FROM memberships mr JOIN memberships ml ON ml.room_id = mr.room_id AND ml.role = 'mentee' WHERE mr.user_id = ? AND mr.role = 'mentor'`},
		{&out.WaitingFeedback, `SELECT COUNT(*) FROM (
			SELECT rp.id FROM memberships mr
			JOIN reports rp ON rp.room_id = mr.room_id AND rp.deleted_at = ''
			LEFT JOIN comments c ON c.report_id = rp.id AND c.deleted_at = ''
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			GROUP BY rp.id HAVING COUNT(c.id) = 0)`},
		{&out.Blockers, `SELECT COUNT(*) FROM memberships mr JOIN reports rp ON rp.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.blocker != '' AND rp.deleted_at = ''`},
		// "Open task" from the mentor's vantage point now means: a task
		// in their room with at least one student who has not been
		// reviewed yet (no submission OR submission still in
		// submitted/revise). LEFT JOIN tracks which student-slot is
		// unreviewed; we expand the (task × mentee) cartesian and
		// count the unreviewed combinations.
		{&out.OpenTasks, `SELECT COUNT(*) FROM memberships mr
			JOIN tasks t ON t.room_id = mr.room_id AND t.deleted_at = ''
			JOIN memberships ml ON ml.room_id = t.room_id AND ml.role = 'mentee'
			  AND (t.assigned_to = '' OR t.assigned_to = ml.user_id)
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ml.user_id
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			  AND (sub.id IS NULL OR sub.status != 'reviewed')`},
		{&out.DueSoonTasks, `SELECT COUNT(DISTINCT t.id) FROM memberships mr
			JOIN tasks t ON t.room_id = mr.room_id AND t.deleted_at = ''
			JOIN memberships ml ON ml.room_id = t.room_id AND ml.role = 'mentee'
			  AND (t.assigned_to = '' OR t.assigned_to = ml.user_id)
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ml.user_id
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			  AND (sub.id IS NULL OR sub.status != 'reviewed')
			  AND t.due_date != '' AND t.due_date >= date('now') AND t.due_date <= date('now', '+2 day')`},
		{&out.OverdueTasks, `SELECT COUNT(DISTINCT t.id) FROM memberships mr
			JOIN tasks t ON t.room_id = mr.room_id AND t.deleted_at = ''
			JOIN memberships ml ON ml.room_id = t.room_id AND ml.role = 'mentee'
			  AND (t.assigned_to = '' OR t.assigned_to = ml.user_id)
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ml.user_id
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			  AND (sub.id IS NULL OR sub.status != 'reviewed')
			  AND t.due_date != '' AND t.due_date < date('now')`},
		{&out.ReportsThisWeek, `SELECT COUNT(*) FROM memberships mr JOIN reports rp ON rp.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.deleted_at = '' AND rp.created_at >= datetime('now', '-7 day')`},
	}
	for _, q := range queries {
		if err := s.db.QueryRow(q.sql, userID).Scan(q.dst); err != nil {
			return out, err
		}
	}
	for _, m := range mentees {
		if m.Status == "quiet" {
			out.InactiveMentees++
		}
	}
	return out, nil
}

// menteeDashboardTasks returns the open tasks across all rooms for
// the mentee landing page. "Open" = no submission OR a submission
// still in submitted/revise state. Reviewed tasks drop off the list.
func (s *Store) menteeDashboardTasks(userID string) ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT t.id, t.room_id, t.title, t.detail,
			t.due_date, t.created_at, COALESCE(sub.status, '')
		FROM tasks t
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ? AND m.role = 'mentee'
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ?
		WHERE t.deleted_at = ''
		  AND (t.assigned_to = '' OR t.assigned_to = ?)
		  AND (sub.id IS NULL OR sub.status != 'reviewed')
		ORDER BY CASE WHEN t.due_date != '' AND t.due_date < date('now') THEN 0 WHEN t.due_date != '' THEN 1 ELSE 2 END, t.due_date ASC, t.created_at DESC
		LIMIT 12`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.RoomID, &t.Title, &t.Detail,
			&t.DueDate, &t.CreatedAt, &t.MySubmissionStatus); err != nil {
			return nil, err
		}
		t.DueState = dueStateFromStatus(t.DueDate, t.MySubmissionStatus, time.Now().UTC())
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) menteeRecentReports(userID string) ([]domain.Report, error) {
	rows, err := s.db.Query(`SELECT rp.id, rp.room_id, rp.user_id, r.name, rp.learned, rp.practiced, rp.blocker, rp.next_plan, rp.created_at, rp.edited_at,
			SUM(CASE WHEN c.id IS NOT NULL AND c.deleted_at = '' THEN 1 ELSE 0 END)
		FROM reports rp
		JOIN rooms r ON r.id = rp.room_id
		LEFT JOIN comments c ON c.report_id = rp.id
		WHERE rp.user_id = ? AND rp.deleted_at = ''
		GROUP BY rp.id
		ORDER BY rp.created_at DESC
		LIMIT 8`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Report
	for rows.Next() {
		var r domain.Report
		if err := rows.Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.CreatedAt, &r.EditedAt, &r.Comments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachReportLinks(out)
}

func menteeStatus(lp domain.MenteeProgress) string {
	if lp.OverdueTasks > 0 {
		return "overdue"
	}
	if lp.Blockers > 0 {
		return "blocked"
	}
	if lp.LastReport == "" {
		return "quiet"
	}
	if lp.ReportsThisWeek > 0 {
		return "active"
	}
	return "quiet"
}

func (s *Store) mentorAttention(userID string) ([]domain.AttentionItem, error) {
	rows, err := s.db.Query(`SELECT 'overdue' AS kind, r.id AS room_id, r.name AS room_name, u.id AS user_id, u.name AS user_name,
			t.title AS title, t.detail AS detail, t.due_date AS due_date, t.created_at AS sort_at
		FROM memberships mr
		JOIN tasks t ON t.room_id = mr.room_id AND t.deleted_at = '' AND t.due_date != '' AND t.due_date < date('now')
		JOIN rooms r ON r.id = t.room_id
		JOIN memberships ml ON ml.room_id = t.room_id AND ml.role = 'mentee'
		  AND (t.assigned_to = '' OR t.assigned_to = ml.user_id)
		JOIN users u ON u.id = ml.user_id
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ml.user_id
		WHERE mr.user_id = ? AND mr.role = 'mentor'
		  AND (sub.id IS NULL OR sub.status != 'reviewed')
		UNION ALL
		SELECT 'blocker', r.id, r.name, u.id, u.name, 'Blocked report', rp.blocker, '', rp.created_at
		FROM memberships mr
		JOIN reports rp ON rp.room_id = mr.room_id
		JOIN rooms r ON r.id = rp.room_id
		JOIN users u ON u.id = rp.user_id
		WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.blocker != '' AND rp.deleted_at = ''
		UNION ALL
		SELECT 'feedback', r.id, r.name, u.id, u.name, 'Report needs feedback', rp.learned, '', rp.created_at
		FROM memberships mr
		JOIN reports rp ON rp.room_id = mr.room_id AND rp.deleted_at = ''
		JOIN rooms r ON r.id = rp.room_id
		JOIN users u ON u.id = rp.user_id
		LEFT JOIN comments c ON c.report_id = rp.id AND c.deleted_at = ''
		WHERE mr.user_id = ? AND mr.role = 'mentor'
		GROUP BY rp.id
		HAVING COUNT(c.id) = 0
		ORDER BY sort_at DESC
		LIMIT 12`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AttentionItem
	for rows.Next() {
		var item domain.AttentionItem
		if err := rows.Scan(&item.Kind, &item.RoomID, &item.RoomName, &item.UserID, &item.UserName, &item.Title, &item.Detail, &item.DueDate, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) menteeProgress(userID string) ([]domain.MenteeProgress, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, r.id, r.name,
		COALESCE((SELECT MAX(rp.created_at) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.deleted_at = ''), '') AS last_report,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.deleted_at = '' AND rp.created_at >= datetime('now', '-7 day')) AS reports_week,
		(SELECT COUNT(*) FROM tasks t
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = u.id
			WHERE t.room_id = r.id AND t.deleted_at = ''
			  AND (t.assigned_to = '' OR t.assigned_to = u.id)
			  AND (sub.id IS NULL OR sub.status != 'reviewed')) AS open_tasks,
		(SELECT COUNT(*) FROM tasks t
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = u.id
			WHERE t.room_id = r.id AND t.deleted_at = ''
			  AND (t.assigned_to = '' OR t.assigned_to = u.id)
			  AND (sub.id IS NULL OR sub.status != 'reviewed')
			  AND t.due_date != '' AND t.due_date < date('now')) AS overdue_tasks,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.deleted_at = '' AND rp.blocker != '') AS blockers
		FROM memberships mr
		JOIN rooms r ON r.id = mr.room_id
		JOIN memberships ml ON ml.room_id = r.id AND ml.role = 'mentee'
		JOIN users u ON u.id = ml.user_id
		WHERE mr.user_id = ? AND mr.role = 'mentor'
		ORDER BY overdue_tasks DESC, blockers DESC, last_report ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MenteeProgress
	for rows.Next() {
		var lp domain.MenteeProgress
		if err := rows.Scan(&lp.UserID, &lp.Name, &lp.Email, &lp.RoomID, &lp.RoomName, &lp.LastReport, &lp.ReportsThisWeek, &lp.OpenTasks, &lp.OverdueTasks, &lp.Blockers); err != nil {
			return nil, err
		}
		lp.Status = menteeStatus(lp)
		out = append(out, lp)
	}
	return out, rows.Err()
}
