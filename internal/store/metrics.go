package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"sinau/internal/domain"
)

// CoachMetrics computes the self-performance numbers behind
// /me/coaching. Pure aggregate query over existing tables — no new
// rows. The window param is the trailing day count (typically 30 or
// 90); a window <= 0 means "all time".
func (s *Store) CoachMetrics(userID string, windowDays int) (domain.CoachMetrics, error) {
	m := domain.CoachMetrics{WindowDays: windowDays}
	windowStart := ""
	if windowDays > 0 {
		windowStart = time.Now().UTC().AddDate(0, 0, -windowDays).Format(time.RFC3339)
	}

	// Comments the mentor left in rooms they mentor.
	q1 := `SELECT COUNT(*) FROM comments c
		JOIN reports r ON r.id = c.report_id
		JOIN memberships m ON m.room_id = r.room_id AND m.user_id = ? AND m.role = 'mentor'
		WHERE c.user_id = ? AND c.deleted_at = ''`
	args1 := []any{userID, userID}
	if windowStart != "" {
		q1 += ` AND c.created_at >= ?`
		args1 = append(args1, windowStart)
	}
	if err := s.db.QueryRow(q1, args1...).Scan(&m.CommentsLeft); err != nil {
		return m, err
	}

	// % of task submissions reviewed in rooms this mentor leads.
	q2 := `SELECT
			SUM(CASE WHEN sub.reviewed_at != '' THEN 1 ELSE 0 END) AS reviewed,
			COUNT(*) AS total
		FROM task_submissions sub
		JOIN tasks t ON t.id = sub.task_id AND t.deleted_at = ''
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ? AND m.role = 'mentor'`
	args2 := []any{userID}
	if windowStart != "" {
		q2 += ` WHERE sub.submitted_at >= ?`
		args2 = append(args2, windowStart)
	}
	if err := s.db.QueryRow(q2, args2...).Scan(&m.SubmissionsReviewed, &m.SubmissionsTotal); err != nil {
		return m, err
	}

	// Avg hours to the mentor's first comment on a report in their rooms.
	// For each report, find the earliest comment by *this* mentor and
	// take (comment_ts - report_ts) in hours. Average across reports
	// where such a comment exists.
	q3 := `SELECT
			COALESCE(AVG((julianday(first_c) - julianday(rp.created_at)) * 24), 0) AS avg_h,
			COUNT(*) AS hits
		FROM (
			SELECT c.report_id, MIN(c.created_at) AS first_c
			FROM comments c
			WHERE c.user_id = ? AND c.deleted_at = ''
			GROUP BY c.report_id
		) AS fc
		JOIN reports rp ON rp.id = fc.report_id AND rp.deleted_at = ''
		JOIN memberships m ON m.room_id = rp.room_id AND m.user_id = ? AND m.role = 'mentor'`
	args3 := []any{userID, userID}
	if windowStart != "" {
		q3 += ` WHERE rp.created_at >= ?`
		args3 = append(args3, windowStart)
	}
	if err := s.db.QueryRow(q3, args3...).Scan(&m.AvgFirstCommentHours, &m.FirstCommentCount); err != nil {
		return m, err
	}

	// Active vs lapsed mentees — "active" means they've posted a report
	// or moved a task in the last 14 days.
	const lapseDays = 14
	cutoff := time.Now().UTC().AddDate(0, 0, -lapseDays).Format(time.RFC3339)
	if err := s.db.QueryRow(`SELECT
			SUM(CASE WHEN lr.last_activity >= ? THEN 1 ELSE 0 END) AS active,
			SUM(CASE WHEN lr.last_activity < ? OR lr.last_activity IS NULL THEN 1 ELSE 0 END) AS lapsed
		FROM (
			SELECT ml.user_id, MAX(activity) AS last_activity FROM (
				SELECT rp.user_id, rp.created_at AS activity FROM reports rp WHERE rp.deleted_at = ''
				UNION ALL
				SELECT sub.student_id, sub.submitted_at FROM task_submissions sub
			) ml GROUP BY ml.user_id
		) lr
		JOIN memberships mm ON mm.user_id = lr.user_id AND mm.role = 'mentee'
		WHERE mm.room_id IN (SELECT room_id FROM memberships WHERE user_id = ? AND role = 'mentor')`,
		cutoff, cutoff, userID).Scan(&m.ActiveMentees, &m.LapsedMentees); err != nil {
		return m, err
	}
	return m, nil
}

// GrowthMetrics builds the read-model behind /me/growth. windowWeeks
// must be > 0; weeks below the window are zero-padded so the sparkline
// always has the same shape. Topic frequency uses a small stopword
// list — good enough for v1, not a real NLP pipeline.
//
// Task progress is computed from task_submissions in the new unified
// model: a viewer task counts as "done" once the mentor has reviewed
// the student's submission. Both broadcast tasks (assigned_to='') and
// individually-assigned ones are counted via the same join.
func (s *Store) GrowthMetrics(userID string, windowWeeks int) (domain.GrowthMetrics, error) {
	if windowWeeks <= 0 {
		windowWeeks = 12
	}
	g := domain.GrowthMetrics{WindowWeeks: windowWeeks}
	cutoff := time.Now().UTC().AddDate(0, 0, -windowWeeks*7).Format(time.RFC3339)

	// Reports per week (last N weeks).
	type weekRow struct {
		weekStart string
		count     int
	}
	rows, err := s.db.Query(`SELECT strftime('%Y-%W', created_at) AS wk, COUNT(*)
		FROM reports
		WHERE user_id = ? AND deleted_at = '' AND created_at >= ?
		GROUP BY wk
		ORDER BY wk`, userID, cutoff)
	if err != nil {
		return g, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var wr weekRow
		if err := rows.Scan(&wr.weekStart, &wr.count); err != nil {
			rows.Close()
			return g, err
		}
		counts[wr.weekStart] = wr.count
		g.ReportsAll += wr.count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return g, err
	}

	// Build the windowed buckets. Iterate the last N ISO weeks from now
	// backwards so zero-weeks render as gaps.
	now := time.Now().UTC()
	for i := windowWeeks - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i*7)
		year, week := d.ISOWeek()
		key := fmt.Sprintf("%04d-%02d", year, week)
		// Anchor label = Monday of that ISO week.
		monday := isoMonday(d)
		g.Weeks = append(g.Weeks, domain.WeekCount{
			Label: monday.Format("Jan 2"),
			Count: counts[key],
		})
	}

	// Streak: consecutive weeks ending with the current week that have count > 0.
	for i := len(g.Weeks) - 1; i >= 0; i-- {
		if g.Weeks[i].Count > 0 {
			g.Streak++
		} else {
			break
		}
	}

	// Task completion in the window. The unified model derives "done"
	// from the viewer's task_submissions row being reviewed; everything
	// else is "open". Both broadcast tasks (assigned_to='') and
	// individual assignments count, as long as the user is a mentee in
	// the room and the task hasn't been soft-deleted.
	if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN sub.status = 'reviewed' THEN 1 ELSE 0 END), 0) AS done,
			COALESCE(SUM(CASE WHEN sub.id IS NULL OR sub.status != 'reviewed' THEN 1 ELSE 0 END), 0) AS open
		FROM tasks t
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ? AND m.role = 'mentee'
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = m.user_id
		WHERE t.deleted_at = ''
		  AND (t.assigned_to = '' OR t.assigned_to = m.user_id)
		  AND t.created_at >= ?`,
		userID, cutoff).Scan(&g.TaskDone, &g.TaskOpen); err != nil {
		return g, err
	}
	if total := g.TaskDone + g.TaskOpen; total > 0 {
		g.TaskRate = float64(g.TaskDone) / float64(total)
	}

	// Blocker count in the window (helps the mentee see they're clearing them).
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM reports
		WHERE user_id = ? AND deleted_at = '' AND blocker != '' AND created_at >= ?`,
		userID, cutoff).Scan(&g.Blockers); err != nil {
		return g, err
	}

	// Topic frequency from learned + blocker. Small in-memory job — we
	// only ever pull this user's own reports, capped by the window.
	topicRows, err := s.db.Query(`SELECT learned, blocker FROM reports
		WHERE user_id = ? AND deleted_at = '' AND created_at >= ?`, userID, cutoff)
	if err != nil {
		return g, err
	}
	defer topicRows.Close()
	freq := map[string]int{}
	for topicRows.Next() {
		var learned, blocker string
		if err := topicRows.Scan(&learned, &blocker); err != nil {
			return g, err
		}
		addWords(freq, learned)
		addWords(freq, blocker)
	}
	if err := topicRows.Err(); err != nil {
		return g, err
	}
	g.Topics = topTopics(freq, 10)
	return g, nil
}

// isoMonday returns the Monday of the ISO week containing d, in UTC.
func isoMonday(d time.Time) time.Time {
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	return d.AddDate(0, 0, -(wd - 1)).UTC()
}

// stopwords is a minimal English+Indonesian list, intentionally short.
// The growth dashboard is a self-reflection tool, not a research one —
// some noise is fine and the user can read past it.
var stopwords = map[string]struct{}{
	// English
	"the": {}, "and": {}, "for": {}, "are": {}, "you": {}, "with": {},
	"this": {}, "that": {}, "have": {}, "was": {}, "from": {}, "but": {},
	"not": {}, "they": {}, "his": {}, "her": {}, "she": {}, "him": {},
	"all": {}, "any": {}, "can": {}, "had": {}, "has": {}, "how": {},
	"its": {}, "let": {}, "may": {}, "out": {}, "our": {}, "put": {},
	"too": {}, "use": {}, "way": {}, "who": {}, "why": {}, "did": {},
	"got": {}, "now": {}, "one": {}, "two": {}, "off": {}, "yes": {},
	"day": {}, "new": {}, "old": {}, "see": {}, "set": {}, "try": {},
	"about": {}, "into": {}, "more": {}, "some": {}, "what": {}, "when": {},
	"will": {}, "your": {}, "just": {}, "like": {}, "than": {}, "then": {},
	"them": {}, "very": {}, "make": {}, "work": {}, "back": {}, "much": {},
	"need": {}, "want": {}, "also": {}, "been": {}, "down": {}, "even": {},
	"good": {}, "here": {}, "know": {}, "look": {}, "made": {}, "many": {},
	"most": {}, "over": {}, "such": {}, "time": {}, "well": {}, "were": {},
	"would": {}, "could": {}, "should": {}, "doing": {}, "today": {},
	// Indonesian
	"yang": {}, "dan": {}, "di": {}, "ke": {}, "dari": {}, "untuk": {},
	"saya": {}, "kamu": {}, "ini": {}, "itu": {}, "dengan": {}, "tidak": {},
	"juga": {}, "atau": {}, "tapi": {}, "ada": {}, "akan": {}, "sudah": {},
	"sama": {}, "buat": {}, "pada": {}, "lagi": {}, "bisa": {}, "kalau": {},
	"belum": {}, "karena": {}, "biar": {}, "lebih": {}, "harus": {}, "masih": {},
}

func addWords(freq map[string]int, text string) {
	for _, raw := range strings.FieldsFunc(text, func(r rune) bool {
		return !isWordChar(r)
	}) {
		w := strings.ToLower(raw)
		if len(w) < 4 || len(w) > 24 {
			continue
		}
		if _, ok := stopwords[w]; ok {
			continue
		}
		freq[w]++
	}
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func topTopics(freq map[string]int, n int) []domain.TopicCount {
	out := make([]domain.TopicCount, 0, len(freq))
	for w, c := range freq {
		if c < 2 {
			continue
		}
		out = append(out, domain.TopicCount{Word: w, Count: c})
	}
	// Selection sort up to n — cheaper than full sort for tiny n.
	for i := 0; i < n && i < len(out); i++ {
		best := i
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[best].Count {
				best = j
			}
		}
		out[i], out[best] = out[best], out[i]
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// StudentGrades returns one GradeRoom per classroom the user is enrolled
// in as a mentee, each containing every task in that room with the
// student's submission state. Used by /me/grades. Only classroom rooms
// are included — mentorship awards 1–5 points on the leaderboard and
// is intentionally out of scope for the gradebook view.
//
// Status is derived per task:
//   - "missing"  — no submission and the deadline has passed
//   - "—"        — no submission, deadline not yet passed
//   - "late"     — submitted after the deadline, awaiting review
//   - "submitted"/ "revise" — submission status as-is
//   - "reviewed" — submission has been reviewed
func (s *Store) StudentGrades(userID string) ([]domain.GradeRoom, error) {
	rows, err := s.db.Query(`SELECT rm.id, rm.name, t.id, t.title, t.due_date,
			COALESCE(sub.status, ''), COALESCE(sub.score, ''),
			COALESCE(sub.feedback, ''), COALESCE(sub.submitted_at, '')
		FROM memberships m
		JOIN rooms rm ON rm.id = m.room_id AND rm.mode = 'classroom'
		JOIN tasks t ON t.room_id = rm.id AND t.deleted_at = ''
		  AND (t.assigned_to = '' OR t.assigned_to = ?)
		LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = ?
		WHERE m.user_id = ? AND m.role = 'mentee'
		ORDER BY rm.name, t.due_date DESC, t.created_at DESC`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byRoom := map[string]*domain.GradeRoom{}
	var order []string
	today := time.Now().UTC().Format("2006-01-02")
	for rows.Next() {
		var roomID, roomName, asgnID, title, due, status, score, feedback, submittedAt string
		if err := rows.Scan(&roomID, &roomName, &asgnID, &title, &due, &status, &score, &feedback, &submittedAt); err != nil {
			return nil, err
		}
		gr, ok := byRoom[roomID]
		if !ok {
			gr = &domain.GradeRoom{RoomID: roomID, RoomName: roomName}
			byRoom[roomID] = gr
			order = append(order, roomID)
		}
		row := domain.GradeRow{
			AssignmentID:    asgnID,
			AssignmentTitle: title,
			DueDate:         due,
			Score:           score,
			Feedback:        feedback,
			SubmittedAt:     submittedAt,
			Status:          gradeStatus(status, submittedAt, due, today),
		}
		gr.Rows = append(gr.Rows, row)
		gr.TotalCount++
		if score != "" {
			if v, err := strconv.Atoi(score); err == nil {
				gr.AverageScore += float64(v)
				gr.ScoredCount++
			}
		}
		if row.Status == "reviewed" || row.Status == "submitted" || row.Status == "revise" {
			// On-time if submitted_at date <= due_date.
			if submittedAt != "" && due != "" && submittedAt[:10] <= due {
				gr.OnTimePct++ // running count, normalized below
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]domain.GradeRoom, 0, len(order))
	for _, rid := range order {
		gr := byRoom[rid]
		if gr.ScoredCount > 0 {
			gr.AverageScore /= float64(gr.ScoredCount)
		}
		if gr.TotalCount > 0 {
			gr.OnTimePct = (gr.OnTimePct / float64(gr.TotalCount)) * 100
		}
		out = append(out, *gr)
	}
	return out, nil
}

func gradeStatus(submissionStatus, submittedAt, due, today string) string {
	if submissionStatus == "" {
		if due != "" && due < today {
			return "missing"
		}
		return "—"
	}
	if submissionStatus == "reviewed" {
		return "reviewed"
	}
	// Submitted but not reviewed. Flag "late" iff it landed after the deadline.
	if due != "" && submittedAt != "" && submittedAt[:10] > due {
		return "late"
	}
	return submissionStatus
}
