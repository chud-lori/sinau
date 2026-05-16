package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

type Store struct {
	db *sql.DB
}

type InviteClaim struct {
	RoomID    string
	Role      string
	ExpiresAt string
	UsedAt    string
}

var ErrSetupComplete = errors.New("setup already completed")

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Migrate() error {
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := s.applyMigration(1, []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			csrf TEXT NOT NULL,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rooms (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_by TEXT NOT NULL REFERENCES users(id),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memberships (
			room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role TEXT NOT NULL CHECK(role IN ('mentor','learner')),
			created_at TEXT NOT NULL,
			PRIMARY KEY(room_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS invites (
			code_hash TEXT PRIMARY KEY,
			room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
			role TEXT NOT NULL CHECK(role IN ('mentor','learner')),
			created_by TEXT NOT NULL REFERENCES users(id),
			expires_at TEXT NOT NULL,
			used_by TEXT REFERENCES users(id),
			used_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS reports (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			learned TEXT NOT NULL,
			practiced TEXT NOT NULL,
			blocker TEXT NOT NULL,
			next_plan TEXT NOT NULL,
			link_url TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS comments (
			id TEXT PRIMARY KEY,
			report_id TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
			assigned_to TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			assigned_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			detail TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('todo','doing','done')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_reports_room_created ON reports(room_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_reports_room_user ON reports(room_id, user_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_comments_report_created ON comments(report_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_room_status ON tasks(room_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_room_assignee ON tasks(room_id, assigned_to, status)`,
	}); err != nil {
		return err
	}
	return s.applyMigration(2, []string{
		`ALTER TABLE tasks ADD COLUMN due_date TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tasks ADD COLUMN last_reminded_at TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_due_date ON tasks(due_date, status)`,
	})
}

func (s *Store) applyMigration(version int, stmts []string) error {
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, auth.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UserCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func (s *Store) CreateInitialRoom(name, email, passwordHash, roomName string) (string, error) {
	now := auth.Now()
	uid, err := auth.NewID()
	if err != nil {
		return "", err
	}
	rid, err := auth.NewID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO users(id,name,email,password_hash,created_at)
		SELECT ?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM users)`, uid, name, email, passwordHash, now)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrSetupComplete
	}
	if _, err = tx.Exec(`INSERT INTO rooms(id,name,created_by,created_at) VALUES(?,?,?,?)`, rid, roomName, uid, now); err != nil {
		return "", err
	}
	if _, err = tx.Exec(`INSERT INTO memberships(room_id,user_id,role,created_at) VALUES(?,?,?,?)`, rid, uid, domain.RoleMentor, now); err != nil {
		return "", err
	}
	return uid, tx.Commit()
}

func (s *Store) UserPasswordByEmail(email string) (string, string, error) {
	var uid, hash string
	err := s.db.QueryRow(`SELECT id, password_hash FROM users WHERE email = ?`, email).Scan(&uid, &hash)
	return uid, hash, err
}

func (s *Store) CreateSession(userID, token, csrf string, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions(id_hash,user_id,csrf,expires_at) VALUES(?,?,?,?)`, auth.HashToken(token), userID, csrf, expires.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, auth.HashToken(token))
	return err
}

func (s *Store) CurrentUser(token string) (*domain.User, error) {
	var u domain.User
	var expires string
	err := s.db.QueryRow(`SELECT u.id, u.name, u.email, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ?`, auth.HashToken(token)).Scan(&u.ID, &u.Name, &u.Email, &expires)
	if err != nil {
		return nil, err
	}
	if auth.ParseTime(expires).Before(time.Now().UTC()) {
		_ = s.DeleteSession(token)
		return nil, errors.New("expired session")
	}
	return &u, nil
}

func (s *Store) CSRF(token string) string {
	var csrf string
	_ = s.db.QueryRow(`SELECT csrf FROM sessions WHERE id_hash = ?`, auth.HashToken(token)).Scan(&csrf)
	return csrf
}

func (s *Store) RoomsFor(userID string) ([]domain.Room, error) {
	rows, err := s.db.Query(`SELECT r.id, r.name, r.created_at, m.role
		FROM rooms r JOIN memberships m ON m.room_id = r.id
		WHERE m.user_id = ? ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Room
	for rows.Next() {
		var r domain.Room
		if err := rows.Scan(&r.ID, &r.Name, &r.CreatedAt, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) IsMentor(userID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE user_id = ? AND role = ?`, userID, domain.RoleMentor).Scan(&n)
	return n > 0
}

func (s *Store) MentorDashboard(userID string) (domain.MentorDashboard, error) {
	rooms, err := s.RoomsFor(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	summary, err := s.mentorSummary(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	attention, err := s.mentorAttention(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	learners, err := s.learnerProgress(userID)
	if err != nil {
		return domain.MentorDashboard{}, err
	}
	return domain.MentorDashboard{Rooms: rooms, Summary: summary, AttentionItems: attention, Learners: learners}, nil
}

func (s *Store) LearnerDashboard(userID string) (domain.LearnerDashboard, error) {
	rooms, err := s.RoomsFor(userID)
	if err != nil {
		return domain.LearnerDashboard{}, err
	}
	tasks, err := s.learnerDashboardTasks(userID)
	if err != nil {
		return domain.LearnerDashboard{}, err
	}
	reports, err := s.learnerRecentReports(userID)
	if err != nil {
		return domain.LearnerDashboard{}, err
	}
	summary := domain.DashboardSummary{Rooms: len(rooms)}
	for _, t := range tasks {
		if t.Status != "done" {
			summary.OpenTasks++
		}
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
	return domain.LearnerDashboard{Rooms: rooms, Summary: summary, Tasks: tasks, RecentReports: reports}, nil
}

func (s *Store) mentorSummary(userID string) (domain.DashboardSummary, error) {
	var out domain.DashboardSummary
	queries := []struct {
		dst *int
		sql string
	}{
		{&out.Rooms, `SELECT COUNT(*) FROM memberships WHERE user_id = ? AND role = 'mentor'`},
		{&out.ActiveLearners, `SELECT COUNT(DISTINCT ml.user_id) FROM memberships mr JOIN memberships ml ON ml.room_id = mr.room_id AND ml.role = 'learner' WHERE mr.user_id = ? AND mr.role = 'mentor'`},
		{&out.WaitingFeedback, `SELECT COUNT(*) FROM (
			SELECT rp.id FROM memberships mr
			JOIN reports rp ON rp.room_id = mr.room_id
			LEFT JOIN comments c ON c.report_id = rp.id
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			GROUP BY rp.id HAVING COUNT(c.id) = 0)`},
		{&out.Blockers, `SELECT COUNT(*) FROM memberships mr JOIN reports rp ON rp.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.blocker != ''`},
		{&out.OpenTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done'`},
		{&out.DueSoonTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.due_date != '' AND t.due_date >= date('now') AND t.due_date <= date('now', '+2 day')`},
		{&out.OverdueTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.due_date != '' AND t.due_date < date('now')`},
		{&out.ReportsThisWeek, `SELECT COUNT(*) FROM memberships mr JOIN reports rp ON rp.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.created_at >= datetime('now', '-7 day')`},
	}
	for _, q := range queries {
		if err := s.db.QueryRow(q.sql, userID).Scan(q.dst); err != nil {
			return out, err
		}
	}
	rows, err := s.learnerProgress(userID)
	if err != nil {
		return out, err
	}
	for _, learner := range rows {
		if learner.Status == "quiet" {
			out.InactiveLearners++
		}
	}
	return out, nil
}

func (s *Store) learnerDashboardTasks(userID string) ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.status, r.name, t.due_date, t.created_at
		FROM tasks t
		JOIN rooms r ON r.id = t.room_id
		WHERE t.assigned_to = ? AND t.status != 'done'
		ORDER BY CASE WHEN t.due_date != '' AND t.due_date < date('now') THEN 0 WHEN t.due_date != '' THEN 1 ELSE 2 END, t.due_date ASC, t.created_at DESC
		LIMIT 12`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.DueDate, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.DueState = dueState(t.DueDate, t.Status, time.Now().UTC())
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) learnerRecentReports(userID string) ([]domain.Report, error) {
	rows, err := s.db.Query(`SELECT rp.id, rp.room_id, rp.user_id, r.name, rp.learned, rp.practiced, rp.blocker, rp.next_plan, rp.link_url, rp.created_at, COUNT(c.id)
		FROM reports rp
		JOIN rooms r ON r.id = rp.room_id
		LEFT JOIN comments c ON c.report_id = rp.id
		WHERE rp.user_id = ?
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
		if err := rows.Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.LinkURL, &r.CreatedAt, &r.Comments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func learnerStatus(lp domain.LearnerProgress) string {
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
		JOIN tasks t ON t.room_id = mr.room_id
		JOIN rooms r ON r.id = t.room_id
		JOIN users u ON u.id = t.assigned_to
		WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.due_date != '' AND t.due_date < date('now')
		UNION ALL
		SELECT 'blocker', r.id, r.name, u.id, u.name, 'Blocked report', rp.blocker, '', rp.created_at
		FROM memberships mr
		JOIN reports rp ON rp.room_id = mr.room_id
		JOIN rooms r ON r.id = rp.room_id
		JOIN users u ON u.id = rp.user_id
		WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.blocker != ''
		UNION ALL
		SELECT 'feedback', r.id, r.name, u.id, u.name, 'Report needs feedback', rp.learned, '', rp.created_at
		FROM memberships mr
		JOIN reports rp ON rp.room_id = mr.room_id
		JOIN rooms r ON r.id = rp.room_id
		JOIN users u ON u.id = rp.user_id
		LEFT JOIN comments c ON c.report_id = rp.id
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

func (s *Store) learnerProgress(userID string) ([]domain.LearnerProgress, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, r.id, r.name,
		COALESCE((SELECT MAX(rp.created_at) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id), '') AS last_report,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.created_at >= datetime('now', '-7 day')) AS reports_week,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done') AS open_tasks,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done' AND t.due_date != '' AND t.due_date < date('now')) AS overdue_tasks,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.blocker != '') AS blockers
		FROM memberships mr
		JOIN rooms r ON r.id = mr.room_id
		JOIN memberships ml ON ml.room_id = r.id AND ml.role = 'learner'
		JOIN users u ON u.id = ml.user_id
		WHERE mr.user_id = ? AND mr.role = 'mentor'
		ORDER BY overdue_tasks DESC, blockers DESC, last_report ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LearnerProgress
	for rows.Next() {
		var lp domain.LearnerProgress
		if err := rows.Scan(&lp.UserID, &lp.Name, &lp.Email, &lp.RoomID, &lp.RoomName, &lp.LastReport, &lp.ReportsThisWeek, &lp.OpenTasks, &lp.OverdueTasks, &lp.Blockers); err != nil {
			return nil, err
		}
		lp.Status = learnerStatus(lp)
		out = append(out, lp)
	}
	return out, rows.Err()
}

func (s *Store) CreateRoom(name, mentorID string) (string, error) {
	roomID, err := auth.NewID()
	if err != nil {
		return "", err
	}
	now := auth.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO rooms(id,name,created_by,created_at) VALUES(?,?,?,?)`, roomID, name, mentorID, now); err != nil {
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
	err := s.db.QueryRow(`SELECT r.id, r.name, r.created_at, m.role
		FROM rooms r JOIN memberships m ON m.room_id = r.id
		WHERE r.id = ? AND m.user_id = ?`, roomID, userID).Scan(&rm.ID, &rm.Name, &rm.CreatedAt, &role)
	return rm, role, err == nil
}

// IsLearner reports whether userID is enrolled in roomID specifically as a
// learner. Tasks are only assignable to learners, so mentor-only checks like
// "is this person in the room" are insufficient.
func (s *Store) IsLearner(roomID, userID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND user_id = ? AND role = ?`,
		roomID, userID, domain.RoleLearner).Scan(&n)
	return n == 1
}

// LearnerIDs returns every learner user_id in the room, ordered by name for
// stable display. Used by the "assign task to all learners" flow.
func (s *Store) LearnerIDs(roomID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT m.user_id FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.room_id = ? AND m.role = ?
		ORDER BY u.name`, roomID, domain.RoleLearner)
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

func (s *Store) CreateInvite(roomID, role, createdBy, code string, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO invites(code_hash,room_id,role,created_by,expires_at) VALUES(?,?,?,?,?)`, auth.HashToken(code), roomID, role, createdBy, expires.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) InviteClaim(code string) (InviteClaim, error) {
	var claim InviteClaim
	err := s.db.QueryRow(`SELECT room_id, role, expires_at, COALESCE(used_at, '') FROM invites WHERE code_hash = ?`, auth.HashToken(code)).Scan(&claim.RoomID, &claim.Role, &claim.ExpiresAt, &claim.UsedAt)
	return claim, err
}

func (s *Store) JoinWithInvite(code, name, email, passwordHash string) (string, string, error) {
	claim, err := s.InviteClaim(code)
	if err != nil {
		return "", "", err
	}
	if claim.UsedAt != "" || auth.ParseTime(claim.ExpiresAt).Before(time.Now().UTC()) {
		return "", "", errors.New("invite invalid")
	}
	now := auth.Now()
	uid, err := auth.NewID()
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO users(id,name,email,password_hash,created_at) VALUES(?,?,?,?,?)`, uid, name, email, passwordHash, now); err != nil {
		return "", "", err
	}
	if _, err = tx.Exec(`INSERT INTO memberships(room_id,user_id,role,created_at) VALUES(?,?,?,?)`, claim.RoomID, uid, claim.Role, now); err != nil {
		return "", "", err
	}
	res, err := tx.Exec(`UPDATE invites SET used_by = ?, used_at = ? WHERE code_hash = ? AND used_at IS NULL`, uid, now, auth.HashToken(code))
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", errors.New("invite already used")
	}
	return uid, claim.RoomID, tx.Commit()
}

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
		if m.Role == domain.RoleLearner && m.LastReport == "" {
			st.InactiveLearners++
		}
	}
	for _, t := range tasks {
		if t.Status != "done" {
			st.OpenTasks++
		}
		if t.DueState == "due-soon" {
			st.DueSoonTasks++
		}
		if t.DueState == "overdue" {
			st.OverdueTasks++
		}
	}
	return domain.RoomData{Members: members, Reports: reports, Tasks: tasks, Invites: invites, Stats: st}, nil
}

func (s *Store) Members(roomID string) ([]domain.Member, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, m.role, m.created_at,
		COALESCE((SELECT MAX(r.created_at) FROM reports r WHERE r.room_id = m.room_id AND r.user_id = u.id), '') AS last_report,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = m.room_id AND t.assigned_to = u.id AND t.status != 'done') AS open_tasks
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

func (s *Store) Reports(roomID, userID, role string) ([]domain.Report, error) {
	query := `SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.link_url, r.created_at, COUNT(c.id)
		FROM reports r JOIN users u ON u.id = r.user_id
		LEFT JOIN comments c ON c.report_id = r.id
		WHERE r.room_id = ?`
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
		if err := rows.Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.LinkURL, &r.CreatedAt, &r.Comments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateReport(roomID, userID, learned, practiced, blocker, nextPlan, link string) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO reports(id,room_id,user_id,learned,practiced,blocker,next_plan,link_url,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, roomID, userID, learned, practiced, blocker, nextPlan, link, auth.Now())
	return err
}

func (s *Store) ReportByID(roomID, reportID string) (domain.Report, error) {
	var r domain.Report
	err := s.db.QueryRow(`SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.link_url, r.created_at, COUNT(c.id)
		FROM reports r JOIN users u ON u.id = r.user_id
		LEFT JOIN comments c ON c.report_id = r.id
		WHERE r.room_id = ? AND r.id = ?
		GROUP BY r.id`, roomID, reportID).Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.LinkURL, &r.CreatedAt, &r.Comments)
	return r, err
}

func (s *Store) Comments(reportID string) ([]domain.Comment, error) {
	rows, err := s.db.Query(`SELECT c.id, u.name, c.body, c.created_at
		FROM comments c JOIN users u ON u.id = c.user_id
		WHERE c.report_id = ? ORDER BY c.created_at`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Comment
	for rows.Next() {
		var c domain.Comment
		if err := rows.Scan(&c.ID, &c.Author, &c.Body, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateComment(reportID, userID, body string) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO comments(id,report_id,user_id,body,created_at) VALUES(?,?,?,?,?)`, id, reportID, userID, body, auth.Now())
	return err
}

func (s *Store) Tasks(roomID, userID, role string) ([]domain.Task, error) {
	query := `SELECT t.id, t.title, t.detail, t.status, u.name, t.due_date, t.created_at
		FROM tasks t JOIN users u ON u.id = t.assigned_to
		WHERE t.room_id = ?`
	args := []any{roomID}
	if role != domain.RoleMentor {
		query += ` AND t.assigned_to = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY CASE WHEN t.status != 'done' AND t.due_date != '' AND t.due_date < date('now') THEN 0 WHEN t.status != 'done' AND t.due_date != '' THEN 1 WHEN t.status = 'todo' THEN 2 WHEN t.status = 'doing' THEN 3 ELSE 4 END, t.due_date ASC, t.created_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.DueDate, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.DueState = dueState(t.DueDate, t.Status, time.Now().UTC())
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTask(roomID, assignedTo, assignedBy, title, detail, dueDate string) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	n := auth.Now()
	_, err = s.db.Exec(`INSERT INTO tasks(id,room_id,assigned_to,assigned_by,title,detail,status,due_date,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, id, roomID, assignedTo, assignedBy, title, detail, "todo", dueDate, n, n)
	return err
}

// CreateTaskForLearners inserts one task per current learner in the room
// inside a single transaction, so either every learner gets the task or none
// do. Returns the number of tasks created (zero if the room has no
// learners).
func (s *Store) CreateTaskForLearners(roomID, assignedBy, title, detail, dueDate string) (int, error) {
	learnerIDs, err := s.LearnerIDs(roomID)
	if err != nil {
		return 0, err
	}
	if len(learnerIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO tasks(id,room_id,assigned_to,assigned_by,title,detail,status,due_date,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	now := auth.Now()
	for _, learnerID := range learnerIDs {
		id, err := auth.NewID()
		if err != nil {
			return 0, err
		}
		if _, err := stmt.Exec(id, roomID, learnerID, assignedBy, title, detail, "todo", dueDate, now, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(learnerIDs), nil
}

func (s *Store) UpdateTaskStatus(roomID, taskID, userID, role, status string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ? AND room_id = ? AND (? = 'mentor' OR assigned_to = ?)`, status, auth.Now(), taskID, roomID, role, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) Invites(roomID string) ([]domain.Invite, error) {
	rows, err := s.db.Query(`SELECT substr(code_hash, 1, 10), role, expires_at, used_at
		FROM invites WHERE room_id = ? ORDER BY expires_at DESC LIMIT 20`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Invite
	for rows.Next() {
		var inv domain.Invite
		if err := rows.Scan(&inv.Code, &inv.Role, &inv.ExpiresAt, &inv.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *Store) DueTaskReminders(now time.Time, window time.Duration) ([]domain.TaskReminder, error) {
	start := now.UTC().Format("2006-01-02")
	end := now.UTC().Add(window).Format("2006-01-02")
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.due_date, r.id, r.name, u.id, u.name, u.email
		FROM tasks t
		JOIN rooms r ON r.id = t.room_id
		JOIN users u ON u.id = t.assigned_to
		WHERE t.status != 'done'
		  AND t.due_date != ''
		  AND t.due_date <= ?
		  AND (t.last_reminded_at = '' OR t.last_reminded_at < ?)
		ORDER BY t.due_date ASC`, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskReminder
	for rows.Next() {
		var rem domain.TaskReminder
		if err := rows.Scan(&rem.TaskID, &rem.Title, &rem.Detail, &rem.DueDate, &rem.RoomID, &rem.RoomName, &rem.AssigneeID, &rem.AssigneeName, &rem.AssigneeEmail); err != nil {
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

func dueState(dueDate, status string, now time.Time) string {
	if dueDate == "" || status == "done" {
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
