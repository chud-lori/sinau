package store

import (
	"sinau/internal/auth"
	"sinau/internal/domain"
)

func (s *Store) Reports(roomID, userID, role string) ([]domain.Report, error) {
	query := `SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.created_at, r.edited_at,
			SUM(CASE WHEN c.id IS NOT NULL AND c.deleted_at = '' THEN 1 ELSE 0 END)
		FROM reports r JOIN users u ON u.id = r.user_id
		LEFT JOIN comments c ON c.report_id = r.id
		WHERE r.room_id = ? AND r.deleted_at = ''`
	args := []any{roomID}
	if role != domain.RoleMentor {
		query += ` AND r.user_id = ?`
		args = append(args, userID)
	}
	query += ` GROUP BY r.id ORDER BY r.created_at DESC LIMIT 80`
	rows, err := s.db.Query(query, args...)
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

// CreateReport persists the mentee's check-in and its attached links in
// a single transaction. Empty Links is fine — the report just renders
// without external attachments.
func (s *Store) CreateReport(roomID, userID, learned, practiced, blocker, nextPlan string, links []domain.Link) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := auth.Now()
	if _, err := tx.Exec(`INSERT INTO reports(id,room_id,user_id,learned,practiced,blocker,next_plan,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, roomID, userID, learned, practiced, blocker, nextPlan, now); err != nil {
		return err
	}
	if err := insertLinksTx(tx, "report_links", "report_id", id, links, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReportByID(roomID, reportID string) (domain.Report, error) {
	var r domain.Report
	err := s.db.QueryRow(`SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.created_at, r.edited_at,
			SUM(CASE WHEN c.id IS NOT NULL AND c.deleted_at = '' THEN 1 ELSE 0 END)
		FROM reports r JOIN users u ON u.id = r.user_id
		LEFT JOIN comments c ON c.report_id = r.id
		WHERE r.room_id = ? AND r.id = ? AND r.deleted_at = ''
		GROUP BY r.id`, roomID, reportID).Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.CreatedAt, &r.EditedAt, &r.Comments)
	if err != nil {
		return r, err
	}
	groups, err := s.linksByParent("report_links", "report_id", []string{r.ID})
	if err != nil {
		return r, err
	}
	r.Links = groups[r.ID]
	return r, nil
}

// UpdateReport overwrites the four free-text sections and replaces the
// link list in a single transaction. edited_at is stamped on success so
// the UI can show "edited <when>". Caller verifies the editor is the
// author or a room mentor.
func (s *Store) UpdateReport(reportID, learned, practiced, blocker, nextPlan string, links []domain.Link) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := auth.Now()
	res, err := tx.Exec(`UPDATE reports SET learned = ?, practiced = ?, blocker = ?, next_plan = ?, edited_at = ?
		WHERE id = ? AND deleted_at = ''`, learned, practiced, blocker, nextPlan, now, reportID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if _, err := tx.Exec(`DELETE FROM report_links WHERE report_id = ?`, reportID); err != nil {
		return false, err
	}
	if err := insertLinksTx(tx, "report_links", "report_id", reportID, links, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DeleteReport soft-deletes a report (and by visibility its comments,
// since Comments() filters on the report-deletion path indirectly via
// the report_id no longer being reachable from any view).
func (s *Store) DeleteReport(reportID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reports SET deleted_at = ? WHERE id = ? AND deleted_at = ''`,
		auth.Now(), reportID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// attachReportLinks loads link rows for the given reports in a single
// IN(...) query and assigns each report its Links slice. Single round-
// trip — avoids N+1 against report_links.
func (s *Store) attachReportLinks(reports []domain.Report) ([]domain.Report, error) {
	if len(reports) == 0 {
		return reports, nil
	}
	ids := make([]string, len(reports))
	for i, r := range reports {
		ids[i] = r.ID
	}
	groups, err := s.linksByParent("report_links", "report_id", ids)
	if err != nil {
		return nil, err
	}
	for i := range reports {
		reports[i].Links = groups[reports[i].ID]
	}
	return reports, nil
}

func (s *Store) Comments(reportID string) ([]domain.Comment, error) {
	rows, err := s.db.Query(`SELECT c.id, c.user_id, u.name, c.body, c.created_at, c.edited_at
		FROM comments c JOIN users u ON u.id = c.user_id
		WHERE c.report_id = ? AND c.deleted_at = '' ORDER BY c.created_at`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Comment
	for rows.Next() {
		var c domain.Comment
		if err := rows.Scan(&c.ID, &c.AuthorID, &c.Author, &c.Body, &c.CreatedAt, &c.EditedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateComment inserts a discussion comment on a report and returns the
// new ID so the caller (web handler) can fire engagement notifications
// referencing it without re-querying.
func (s *Store) CreateComment(reportID, userID, body string) (string, error) {
	id, err := auth.NewID()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`INSERT INTO comments(id,report_id,user_id,body,created_at) VALUES(?,?,?,?,?)`,
		id, reportID, userID, body, auth.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// CommentAuthor returns the author user_id of a non-deleted comment plus
// the parent report_id. Used by permission checks on edit/delete.
func (s *Store) CommentAuthor(commentID string) (userID, reportID string, err error) {
	err = s.db.QueryRow(`SELECT user_id, report_id FROM comments WHERE id = ? AND deleted_at = ''`,
		commentID).Scan(&userID, &reportID)
	return
}

// EditComment overwrites the body and stamps edited_at. The caller is
// expected to have verified that the editor is the author or a room
// mentor. Returns (false, nil) when the comment is missing or already
// deleted so the handler can render a 404.
func (s *Store) EditComment(commentID, body string) (bool, error) {
	res, err := s.db.Exec(`UPDATE comments SET body = ?, edited_at = ? WHERE id = ? AND deleted_at = ''`,
		body, auth.Now(), commentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteComment soft-deletes a comment by stamping deleted_at. The row
// stays in the table for audit but is excluded from Comments().
func (s *Store) DeleteComment(commentID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE comments SET deleted_at = ? WHERE id = ? AND deleted_at = ''`,
		auth.Now(), commentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
