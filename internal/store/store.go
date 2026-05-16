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

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
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
	return s.applyMigration(1, []string{
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
	if _, err = tx.Exec(`INSERT INTO users(id,name,email,password_hash,created_at) VALUES(?,?,?,?,?)`, uid, name, email, passwordHash, now); err != nil {
		return "", err
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

func (s *Store) IsMember(roomID, userID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND user_id = ?`, roomID, userID).Scan(&n)
	return n == 1
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
	query := `SELECT t.id, t.title, t.detail, t.status, u.name, t.created_at
		FROM tasks t JOIN users u ON u.id = t.assigned_to
		WHERE t.room_id = ?`
	args := []any{roomID}
	if role != domain.RoleMentor {
		query += ` AND t.assigned_to = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY CASE t.status WHEN 'todo' THEN 0 WHEN 'doing' THEN 1 ELSE 2 END, t.created_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTask(roomID, assignedTo, assignedBy, title, detail string) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	n := auth.Now()
	_, err = s.db.Exec(`INSERT INTO tasks(id,room_id,assigned_to,assigned_by,title,detail,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, roomID, assignedTo, assignedBy, title, detail, "todo", n, n)
	return err
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
