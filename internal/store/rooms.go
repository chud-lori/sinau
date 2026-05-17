package store

import (
	"errors"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// normalizeRoomMode used to silently coerce any unknown value to
// mentorship. That swallowed typos at the boundary. Callers should
// validate with domain.ValidRoomMode first; this remains only as a final
// safety net inside the store.
func normalizeRoomMode(mode string) string {
	if domain.ValidRoomMode(mode) {
		return mode
	}
	return domain.RoomModeMentorship
}

func (s *Store) RoomsFor(userID string) ([]domain.Room, error) {
	rows, err := s.db.Query(`SELECT r.id, r.name, r.mode, r.created_at, m.role, r.leaderboard_visible
		FROM rooms r JOIN memberships m ON m.room_id = r.id
		WHERE m.user_id = ? ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Room
	for rows.Next() {
		var r domain.Room
		var vis int
		if err := rows.Scan(&r.ID, &r.Name, &r.Mode, &r.CreatedAt, &r.Role, &vis); err != nil {
			return nil, err
		}
		r.LeaderboardVisible = vis == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateRoom(name, mentorID, mode string) (string, error) {
	if !s.CanCreateRooms(mentorID) {
		return "", errors.New("user cannot create rooms")
	}
	roomID, err := auth.NewID()
	if err != nil {
		return "", err
	}
	mode = normalizeRoomMode(mode)
	now := auth.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO rooms(id,name,mode,created_by,created_at) VALUES(?,?,?,?,?)`, roomID, name, mode, mentorID, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO memberships(room_id,user_id,role,created_at) VALUES(?,?,?,?)`, roomID, mentorID, domain.RoleMentor, now); err != nil {
		return "", err
	}
	return roomID, tx.Commit()
}

func (s *Store) RoomAccess(roomID, userID string) (domain.Room, string, bool) {
	var rm domain.Room
	var role string
	var vis int
	err := s.db.QueryRow(`SELECT r.id, r.name, r.mode, r.created_at, m.role, r.leaderboard_visible
		FROM rooms r JOIN memberships m ON m.room_id = r.id
		WHERE r.id = ? AND m.user_id = ?`, roomID, userID).Scan(&rm.ID, &rm.Name, &rm.Mode, &rm.CreatedAt, &role, &vis)
	rm.LeaderboardVisible = vis == 1
	return rm, role, err == nil
}

// SetRoomLeaderboardVisible toggles whether mentees can see the full
// per-room leaderboard. Mentor-only at the handler layer.
func (s *Store) SetRoomLeaderboardVisible(roomID string, visible bool) error {
	v := 0
	if visible {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE rooms SET leaderboard_visible = ? WHERE id = ?`, v, roomID)
	return err
}

// IsMentee reports whether userID is enrolled in roomID specifically as a
// mentee. Tasks are only assignable to mentees, so mentor-only checks like
// "is this person in the room" are insufficient.
func (s *Store) IsMentee(roomID, userID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND user_id = ? AND role = ?`,
		roomID, userID, domain.RoleMentee).Scan(&n)
	return n == 1
}

// MenteeIDs returns every mentee user_id in the room, ordered by name for
// stable display. Used by the "assign task to all mentees" flow.
func (s *Store) MenteeIDs(roomID string) ([]string, error) {
	return s.membersByRole(roomID, domain.RoleMentee)
}

// MentorIDs returns every mentor user_id in the room. Used to fan
// engagement notifications (submission received) out to the whole
// teaching side.
func (s *Store) MentorIDs(roomID string) ([]string, error) {
	return s.membersByRole(roomID, domain.RoleMentor)
}

func (s *Store) membersByRole(roomID, role string) ([]string, error) {
	rows, err := s.db.Query(`SELECT m.user_id FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.room_id = ? AND m.role = ?
		ORDER BY u.name`, roomID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) Members(roomID string) ([]domain.Member, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, m.role, m.created_at,
		COALESCE((SELECT MAX(r.created_at) FROM reports r WHERE r.room_id = m.room_id AND r.user_id = u.id AND r.deleted_at = ''), '') AS last_report,
		(SELECT COUNT(*) FROM tasks t
			LEFT JOIN task_submissions sub ON sub.task_id = t.id AND sub.student_id = u.id
			WHERE t.room_id = m.room_id AND t.deleted_at = ''
			  AND (t.assigned_to = '' OR t.assigned_to = u.id)
			  AND (sub.id IS NULL OR sub.status != 'reviewed')) AS open_tasks
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.room_id = ?
		ORDER BY m.role DESC, u.name`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Member
	for rows.Next() {
		var m domain.Member
		if err := rows.Scan(&m.UserID, &m.Name, &m.Email, &m.Role, &m.CreatedAt, &m.LastReport, &m.OpenTasks); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RoomData is the aggregated read-model for /rooms/{id}. It fans the
// per-section queries (reports / tasks / members / submissions /
// invites / leaderboard) and returns the bundle the room template
// renders. Mentor- and mentee-only sections are skipped based on role
// so the cost matches the view.
func (s *Store) RoomData(roomID, userID, role string) (domain.RoomData, error) {
	members, err := s.Members(roomID)
	if err != nil {
		return domain.RoomData{}, err
	}
	reports, err := s.Reports(roomID, userID, role)
	if err != nil {
		return domain.RoomData{}, err
	}
	tasks, err := s.Tasks(roomID, userID, role)
	if err != nil {
		return domain.RoomData{}, err
	}
	invites := []domain.Invite{}
	if role == domain.RoleMentor {
		invites, err = s.Invites(roomID)
		if err != nil {
			return domain.RoomData{}, err
		}
	}
	st := domain.Stats{}
	for _, r := range reports {
		if r.Blocker != "" {
			st.BlockedReports++
		}
		if r.Comments == 0 {
			st.WaitingReports++
		}
	}
	for _, m := range members {
		if m.Role == domain.RoleMentee && m.LastReport == "" {
			st.InactiveMentees++
		}
	}
	for _, t := range tasks {
		// "Open" = not yet reviewed for the viewer. From a mentor's
		// perspective there's no single viewer-status, so we count
		// tasks where at least one student is still owed work; from a
		// mentee's perspective it's their own status.
		if t.MySubmissionStatus == "" || t.MySubmissionStatus == "submitted" || t.MySubmissionStatus == "revise" {
			st.OpenTasks++
		}
		switch t.DueState {
		case "due-soon":
			st.DueSoonTasks++
		case "overdue":
			st.OverdueTasks++
		}
	}
	submissions := []domain.Submission{}
	pending := 0
	if role == domain.RoleMentor {
		submissions, err = s.TaskSubmissions(roomID)
		if err != nil {
			return domain.RoomData{}, err
		}
		for _, sub := range submissions {
			if sub.Status == "submitted" {
				pending++
			}
		}
	}
	// Fetch the leaderboard only when it'll actually be rendered: mentors
	// always see it, mentees only when the mentor has flipped the
	// visibility toggle. We still need the viewer's own rank/points, so
	// compute those from the same board (avoiding a second pass that the
	// old UserRankInRoom call would have required).
	var leaderboardVisible int
	_ = s.db.QueryRow(`SELECT leaderboard_visible FROM rooms WHERE id = ?`, roomID).Scan(&leaderboardVisible)
	var board []domain.LeaderboardEntry
	rank := domain.Rank{}
	myPoints := 0
	if role == domain.RoleMentor || leaderboardVisible == 1 {
		board, err = s.RoomLeaderboard(roomID)
		if err != nil {
			return domain.RoomData{}, err
		}
		rank.Total = len(board)
		for _, e := range board {
			if e.UserID == userID {
				rank.Position = e.Rank
				myPoints = e.Points
				break
			}
		}
	} else {
		rank, myPoints, err = s.menteeScore(roomID, userID)
		if err != nil {
			return domain.RoomData{}, err
		}
	}
	return domain.RoomData{
		Members:        members,
		Reports:        reports,
		Tasks:          tasks,
		Invites:        invites,
		Submissions:    submissions,
		PendingReviews: pending,
		Stats:          st,
		Leaderboard:    board,
		MyPoints:       myPoints,
		MyRank:         rank,
	}, nil
}

// menteeScore returns the viewer's own points + dense rank in the room
// without fetching the full leaderboard. Used when the room's leaderboard
// is hidden so a mentee can still see their own progress.
func (s *Store) menteeScore(roomID, userID string) (domain.Rank, int, error) {
	var totalMentees int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND role = ?`,
		roomID, domain.RoleMentee).Scan(&totalMentees); err != nil {
		return domain.Rank{}, 0, err
	}
	var myPoints int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM points_ledger WHERE room_id = ? AND user_id = ?`,
		roomID, userID).Scan(&myPoints)
	// Dense rank: 1 + number of distinct higher scores in the room.
	var higher int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT DISTINCT COALESCE(SUM(amount), 0) AS s FROM points_ledger
		WHERE room_id = ? GROUP BY user_id HAVING s > ?
	)`, roomID, myPoints).Scan(&higher); err != nil {
		return domain.Rank{}, 0, err
	}
	return domain.Rank{Position: higher + 1, Total: totalMentees}, myPoints, nil
}
