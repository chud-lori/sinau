package store

import (
	"database/sql"
	"errors"
	"strconv"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// Tasks lists every task in the room, ordered by deadline urgency,
// with viewer-specific submission state attached when called by a
// mentee/student. Broadcast tasks (assigned_to='') count as "everyone
// in the room"; individual tasks (assigned_to=specific user) are only
// returned to that user or to mentors.
//
// The viewer-status fields are computed via a LEFT JOIN on
// task_submissions filtered to the caller. For mentor/teacher calls
// they stay empty — mentors use TaskSubmissions() for the review queue
// instead.
func (s *Store) Tasks(roomID, userID, role string) ([]domain.Task, error) {
	totalStudents := 0
	if role == domain.RoleMentor {
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND role = ?`,
			roomID, domain.RoleMentee).Scan(&totalStudents); err != nil {
			return nil, err
		}
	}

	query := `SELECT t.id, t.room_id, t.created_by, t.title, t.detail, t.resource_url,
			t.assigned_to,
			COALESCE(u.name, '') AS assignee_name,
			t.due_date, t.created_at, t.edited_at,
			COALESCE(sub.id, ''), COALESCE(sub.status, ''),
			COALESCE(sub.feedback, ''), COALESCE(sub.score, ''),
			COALESCE(sub_counts.submitted, 0) AS submitted
		FROM tasks t
		LEFT JOIN users u ON u.id = t.assigned_to
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ?
		LEFT JOIN (
			SELECT task_id, COUNT(*) AS submitted FROM task_submissions GROUP BY task_id
		) sub_counts ON sub_counts.task_id = t.id
		WHERE t.room_id = ? AND t.deleted_at = ?`
	args := []any{userID, roomID, ""}
	if role != domain.RoleMentor {
		// Mentees see broadcast tasks AND tasks individually assigned
		// to them; never anyone else's individual task.
		query += ` AND (t.assigned_to = '' OR t.assigned_to = ?)`
		args = append(args, userID)
	}
	query += ` ORDER BY CASE
		WHEN COALESCE(sub.status, '') = 'revise' THEN 0
		WHEN COALESCE(sub.status, '') = '' AND t.due_date != '' AND t.due_date < date('now') THEN 1
		WHEN COALESCE(sub.status, '') = '' AND t.due_date != '' THEN 2
		WHEN COALESCE(sub.status, '') = '' THEN 3
		WHEN sub.status = 'submitted' THEN 4
		ELSE 5 END, t.due_date ASC, t.created_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	var subIDs []string
	subToTaskIdx := map[string]int{}
	for rows.Next() {
		var t domain.Task
		var submissionID string
		if err := rows.Scan(&t.ID, &t.RoomID, &t.CreatedByID, &t.Title, &t.Detail, &t.ResourceURL,
			&t.AssigneeID, &t.Assignee, &t.DueDate, &t.CreatedAt, &t.EditedAt,
			&submissionID, &t.MySubmissionStatus, &t.MyFeedback, &t.MyScore,
			&t.Submitted); err != nil {
			return nil, err
		}
		t.TotalStudents = totalStudents
		t.DueState = dueStateFromStatus(t.DueDate, t.MySubmissionStatus, time.Now().UTC())
		t.MySubmissionID = submissionID
		out = append(out, t)
		if submissionID != "" {
			subIDs = append(subIDs, submissionID)
			subToTaskIdx[submissionID] = len(out) - 1
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(subIDs) > 0 {
		groups, err := s.linksByParent("task_submission_links", "submission_id", subIDs)
		if err != nil {
			return nil, err
		}
		for subID, idx := range subToTaskIdx {
			out[idx].MySubmissionLinks = groups[subID]
		}
	}
	return out, nil
}

// TaskByID loads a task plus the viewer's submission summary for the
// detail page. Returns sql.ErrNoRows when the task is missing or
// soft-deleted. Caller still verifies room membership.
func (s *Store) TaskByID(roomID, taskID, viewerID string) (domain.Task, error) {
	var t domain.Task
	var submissionID string
	err := s.db.QueryRow(`SELECT t.id, t.room_id, t.created_by, t.title, t.detail, t.resource_url,
			t.assigned_to, COALESCE(u.name, ''),
			t.due_date, t.created_at, t.edited_at,
			COALESCE(sub.id, ''), COALESCE(sub.status, ''),
			COALESCE(sub.feedback, ''), COALESCE(sub.score, '')
		FROM tasks t
		LEFT JOIN users u ON u.id = t.assigned_to
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ?
		WHERE t.id = ? AND t.room_id = ? AND t.deleted_at = ''`, viewerID, taskID, roomID).
		Scan(&t.ID, &t.RoomID, &t.CreatedByID, &t.Title, &t.Detail, &t.ResourceURL,
			&t.AssigneeID, &t.Assignee, &t.DueDate, &t.CreatedAt, &t.EditedAt,
			&submissionID, &t.MySubmissionStatus, &t.MyFeedback, &t.MyScore)
	if err != nil {
		return t, err
	}
	t.MySubmissionID = submissionID
	t.DueState = dueStateFromStatus(t.DueDate, t.MySubmissionStatus, time.Now().UTC())
	if submissionID != "" {
		groups, err := s.linksByParent("task_submission_links", "submission_id", []string{submissionID})
		if err != nil {
			return t, err
		}
		t.MySubmissionLinks = groups[submissionID]
	}
	return t, nil
}

// CreateTask inserts a task in either mode. An empty assignedTo means
// broadcast (every mentee/student in the room can submit). Non-empty
// must be a mentee membership in the room. resourceURL and detail are
// optional. Returns the new task ID.
func (s *Store) CreateTask(roomID, createdBy, assignedTo, title, detail, resourceURL, dueDate string) (string, error) {
	id, err := auth.NewID()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`INSERT INTO tasks(id, room_id, created_by, assigned_to, title, detail, resource_url, due_date, created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		id, roomID, createdBy, assignedTo, title, detail, resourceURL, dueDate, auth.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// UpdateTask edits the four authoring fields on a task. There's no
// review-lock guard — task content can still be edited after a
// student has been graded, with the "edited" indicator surfacing the
// change in the UI.
func (s *Store) UpdateTask(taskID, title, detail, resourceURL, dueDate string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET title = ?, detail = ?, resource_url = ?, due_date = ?, edited_at = ?
		WHERE id = ? AND deleted_at = ''`,
		title, detail, resourceURL, dueDate, auth.Now(), taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTask soft-deletes a task. Submissions stay in the DB but their
// rows become invisible to every read query (which joins tasks and
// filters deleted_at). Caller verifies the deleter is a room mentor.
func (s *Store) DeleteTask(taskID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET deleted_at = ? WHERE id = ? AND deleted_at = ''`,
		auth.Now(), taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// TaskRoom returns the room_id of a non-deleted task. Used by edit /
// delete permission checks.
func (s *Store) TaskRoom(taskID string) (string, error) {
	var roomID string
	err := s.db.QueryRow(`SELECT room_id FROM tasks WHERE id = ? AND deleted_at = ''`, taskID).Scan(&roomID)
	return roomID, err
}

// TaskSubmissions returns the mentor/teacher review queue for the
// room. Sorts unreviewed work first. Submission links are attached in
// one IN(...) round trip.
func (s *Store) TaskSubmissions(roomID string) ([]domain.Submission, error) {
	rows, err := s.db.Query(`SELECT sub.id, sub.task_id, t.title, sub.student_id, u.name, u.email,
			sub.note, sub.status, sub.feedback, sub.score, sub.reviewed_by, sub.submitted_at, sub.reviewed_at
		FROM task_submissions sub
		JOIN tasks t ON t.id = sub.task_id
		JOIN users u ON u.id = sub.student_id
		WHERE t.room_id = ? AND t.deleted_at = ''
		ORDER BY CASE sub.status WHEN 'submitted' THEN 0 WHEN 'revise' THEN 1 ELSE 2 END, sub.submitted_at DESC`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Submission
	for rows.Next() {
		var sub domain.Submission
		if err := rows.Scan(&sub.ID, &sub.TaskID, &sub.TaskTitle, &sub.StudentID, &sub.StudentName, &sub.StudentEmail,
			&sub.Note, &sub.Status, &sub.Feedback, &sub.Score, &sub.ReviewedBy, &sub.SubmittedAt, &sub.ReviewedAt); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i, sub := range out {
		ids[i] = sub.ID
	}
	groups, err := s.linksByParent("task_submission_links", "submission_id", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Links = groups[out[i].ID]
	}
	return out, nil
}

// SubmitTask writes (or replaces) the student's submission to a task.
// Resubmission clears review state and swaps the link list in one
// transaction so the row is never half-updated. Caller validates the
// student is a mentee in this room AND that the task either is
// broadcast (assigned_to='') or individually assigned to this user.
func (s *Store) SubmitTask(roomID, taskID, studentID, note string, links []domain.Link) error {
	if !s.IsMentee(roomID, studentID) {
		return errors.New("student is not a mentee in this room")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := auth.Now()

	// Verify the task lives in this room and is open to this student.
	var assignedTo string
	switch err := tx.QueryRow(`SELECT assigned_to FROM tasks
		WHERE id = ? AND room_id = ? AND deleted_at = ''`, taskID, roomID).Scan(&assignedTo); err {
	case sql.ErrNoRows:
		return sql.ErrNoRows
	case nil:
		// ok
	default:
		return err
	}
	if assignedTo != "" && assignedTo != studentID {
		return errors.New("task is not assigned to this student")
	}

	var existingID string
	switch err := tx.QueryRow(`SELECT id FROM task_submissions WHERE task_id = ? AND student_id = ?`,
		taskID, studentID).Scan(&existingID); err {
	case nil, sql.ErrNoRows:
	default:
		return err
	}

	submissionID := existingID
	if submissionID == "" {
		submissionID, err = auth.NewID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO task_submissions(id, task_id, student_id, note, status, feedback, score, reviewed_by, submitted_at, reviewed_at)
			VALUES(?,?,?,?,'submitted','','','',?,'')`,
			submissionID, taskID, studentID, note, now); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE task_submissions
			SET note = ?, status = 'submitted', feedback = '', score = '', reviewed_by = '', submitted_at = ?, reviewed_at = ''
			WHERE id = ?`, note, now, submissionID); err != nil {
			return err
		}
		// Clear the old points ledger entry too; if the new submission
		// gets reviewed again the score may differ. UNIQUE(source,
		// source_id) would otherwise block re-award.
		if _, err := tx.Exec(`DELETE FROM points_ledger WHERE source = 'submission' AND source_id = ?`,
			submissionID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM task_submission_links WHERE submission_id = ?`, submissionID); err != nil {
			return err
		}
	}
	if err := insertLinksTx(tx, "task_submission_links", "submission_id", submissionID, links, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SubmissionContext returns the student_id, task title, and room_id
// for a given submission. Used by web handlers to fan engagement
// notifications without a re-query after the underlying write.
func (s *Store) SubmissionContext(submissionID string) (studentID, taskTitle, roomID string, err error) {
	err = s.db.QueryRow(`SELECT sub.student_id, t.title, t.room_id
		FROM task_submissions sub JOIN tasks t ON t.id = sub.task_id
		WHERE sub.id = ?`, submissionID).Scan(&studentID, &taskTitle, &roomID)
	return
}

// ReviewTaskSubmission writes the mentor/teacher review for one
// submission and, in mentorship rooms, records the points award in the
// ledger so the leaderboard updates atomically.
//
// status must be "reviewed" or "revise". For "reviewed" with a non-
// empty score in a mentorship room (1–5), a points_ledger row is
// inserted. classroom rooms keep the score on the submission but
// don't generate ledger rows — classroom scoring is for gradebook
// reporting, not the competitive leaderboard.
//
// Returns (false, nil) when the submission is missing, already
// reviewed, or in the wrong room — handler renders a 404/409.
func (s *Store) ReviewTaskSubmission(roomID, submissionID, status, feedback, score, reviewerID string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var (
		studentID string
		taskID    string
		roomMode  string
	)
	if err := tx.QueryRow(`SELECT sub.student_id, sub.task_id, rm.mode
		FROM task_submissions sub
		JOIN tasks t ON t.id = sub.task_id
		JOIN rooms rm ON rm.id = t.room_id
		WHERE sub.id = ? AND t.room_id = ? AND sub.reviewed_at = '' AND t.deleted_at = ''`,
		submissionID, roomID).Scan(&studentID, &taskID, &roomMode); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	now := auth.Now()
	if _, err := tx.Exec(`UPDATE task_submissions
		SET status = ?, feedback = ?, score = ?, reviewed_by = ?, reviewed_at = ?
		WHERE id = ? AND reviewed_at = ''`,
		status, feedback, score, reviewerID, now, submissionID); err != nil {
		return false, err
	}

	// Mentorship review with a 1–5 score generates a leaderboard entry.
	if status == "reviewed" && roomMode == domain.RoomModeMentorship && score != "" {
		points, err := strconv.Atoi(score)
		if err == nil && points >= 1 && points <= 5 {
			ledgerID, err := auth.NewID()
			if err != nil {
				return false, err
			}
			if _, err := tx.Exec(`INSERT INTO points_ledger(id, user_id, room_id, source, source_id, amount, awarded_by, awarded_at)
				VALUES(?,?,?,?,?,?,?,?)`,
				ledgerID, studentID, roomID, "submission", submissionID, points, reviewerID, now); err != nil {
				return false, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// dueStateFromStatus is the per-task urgency tag. A task whose viewer
// submission is already 'reviewed' has no urgency regardless of the
// deadline; a 'revise' submission goes back to the due-date track.
// Returns "overdue", "due-soon", or "".
func dueStateFromStatus(dueDate, submissionStatus string, now time.Time) string {
	if submissionStatus == "reviewed" {
		return ""
	}
	if dueDate == "" {
		return ""
	}
	due, err := time.Parse("2006-01-02", dueDate)
	if err != nil {
		return ""
	}
	today, _ := time.Parse("2006-01-02", now.UTC().Format("2006-01-02"))
	if due.Before(today) {
		return "overdue"
	}
	if !due.After(today.Add(48 * time.Hour)) {
		return "due-soon"
	}
	return ""
}
