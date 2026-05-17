package store

import (
	"strings"

	"sinau/internal/domain"
)

// Search runs a single full-text query across reports, comments,
// tasks, and task submissions, scoped to rows the user is allowed to
// see. Returns at most limit hits in total, ordered by FTS5 BM25 rank.
//
// Visibility per source mirrors the existing room views:
//   - reports:     mentors see all room reports, mentees only their own
//   - comments:    all room members
//   - tasks:       mentors see all room tasks, mentees only broadcast +
//                  individually-assigned-to-them
//   - submissions: mentors see all in their rooms; students see their own
//
// The query string is passed as-is to FTS5 (which supports prefix
// matching with `term*`, phrase queries with `"two words"`, etc.).
// Callers should sanitize untrusted input upstream — at minimum
// rejecting empty or whitespace-only strings.
func (s *Store) Search(userID, query string, limit int) ([]domain.SearchHit, error) {
	if limit <= 0 {
		limit = 30
	}
	q := sanitizeFTSQuery(query)
	if q == "" {
		return nil, nil
	}
	const sql = `
		-- Reports
		SELECT 'report' AS kind, r.id, r.room_id, rm.name, rm.mode,
			COALESCE(u.name, '') AS author, COALESCE(u.name, '') AS title,
			snippet(reports_fts, -1, char(2), char(3), '…', 24) AS snip,
			r.created_at, bm25(reports_fts) AS rk
		FROM reports_fts
		JOIN reports r ON r.id = reports_fts.source_id
		JOIN rooms rm ON rm.id = r.room_id
		JOIN users u ON u.id = r.user_id
		JOIN memberships m ON m.room_id = r.room_id AND m.user_id = ?
		WHERE reports_fts MATCH ?
		  AND r.deleted_at = ''
		  AND (m.role = 'mentor' OR r.user_id = ?)
		UNION ALL
		-- Comments
		SELECT 'comment', c.id, rm.id, rm.name, rm.mode,
			COALESCE(u.name, ''), COALESCE(u.name, ''),
			snippet(comments_fts, -1, char(2), char(3), '…', 24),
			c.created_at, bm25(comments_fts)
		FROM comments_fts
		JOIN comments c ON c.id = comments_fts.source_id
		JOIN reports r ON r.id = c.report_id
		JOIN rooms rm ON rm.id = r.room_id
		JOIN users u ON u.id = c.user_id
		JOIN memberships m ON m.room_id = r.room_id AND m.user_id = ?
		WHERE comments_fts MATCH ?
		  AND c.deleted_at = ''
		  AND r.deleted_at = ''
		UNION ALL
		-- Tasks: visible to room mentors; mentees only see broadcast
		-- tasks or ones individually assigned to them.
		SELECT 'task', t.id, t.room_id, rm.name, rm.mode,
			COALESCE(u.name, ''), t.title,
			snippet(tasks_fts, -1, char(2), char(3), '…', 24),
			t.created_at, bm25(tasks_fts)
		FROM tasks_fts
		JOIN tasks t ON t.id = tasks_fts.source_id
		JOIN rooms rm ON rm.id = t.room_id
		LEFT JOIN users u ON u.id = t.assigned_to
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ?
		WHERE tasks_fts MATCH ?
		  AND t.deleted_at = ''
		  AND (m.role = 'mentor' OR t.assigned_to = '' OR t.assigned_to = ?)
		UNION ALL
		-- Submissions
		SELECT 'submission', sub.id, rm.id, rm.name, rm.mode,
			COALESCE(u.name, ''), t.title,
			snippet(task_submissions_fts, -1, char(2), char(3), '…', 24),
			sub.submitted_at, bm25(task_submissions_fts)
		FROM task_submissions_fts
		JOIN task_submissions sub ON sub.id = task_submissions_fts.source_id
		JOIN tasks t ON t.id = sub.task_id
		JOIN rooms rm ON rm.id = t.room_id
		JOIN users u ON u.id = sub.student_id
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ?
		WHERE task_submissions_fts MATCH ?
		  AND t.deleted_at = ''
		  AND (m.role = 'mentor' OR sub.student_id = ?)
		ORDER BY rk
		LIMIT ?`
	rows, err := s.db.Query(sql,
		userID, q, userID,
		userID, q,
		userID, q, userID,
		userID, q, userID,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SearchHit
	for rows.Next() {
		var h domain.SearchHit
		var rk float64
		if err := rows.Scan(&h.Kind, &h.ID, &h.RoomID, &h.RoomName, &h.RoomMode, &h.Author, &h.Title, &h.Snippet, &h.CreatedAt, &rk); err != nil {
			return nil, err
		}
		h.DeepLinkPath = searchDeepLink(h)
		if h.Kind == "report" || h.Kind == "comment" {
			h.Title = "Report by " + h.Author
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func searchDeepLink(h domain.SearchHit) string {
	switch h.Kind {
	case "report":
		return "/rooms/" + h.RoomID + "/reports/" + h.ID
	case "comment":
		// Comments link back to the room — the report id isn't
		// available on the SearchHit row. Good enough for v1.
		return "/rooms/" + h.RoomID
	case "task":
		return "/rooms/" + h.RoomID + "/tasks/" + h.ID
	case "submission":
		// Submissions don't have a standalone page; the mentor/teacher
		// review queue is on the task detail page.
		return "/rooms/" + h.RoomID
	}
	return "/rooms/" + h.RoomID
}

// sanitizeFTSQuery turns user input into an FTS5-safe MATCH expression.
// It strips characters that have syntactic meaning in FTS5 (so a paste
// like "what?!" doesn't crash the parser) and turns the trimmed result
// into a prefix-match query when the user types a single bare word,
// since "hand" matching "handwriting" is what most search UIs do today.
func sanitizeFTSQuery(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// FTS5 treats these as operators / column markers; strip them.
	for _, ch := range []string{"\"", "'", ":", "(", ")", "*", "+", "-", "^"} {
		s = strings.ReplaceAll(s, ch, " ")
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	// Single-word queries become prefix matches.
	if len(fields) == 1 {
		return fields[0] + "*"
	}
	// Multi-word queries: AND every term (FTS5 default is implicit AND).
	return strings.Join(fields, " ")
}
