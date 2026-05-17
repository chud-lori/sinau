package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

// Migrate ensures the database is on the current schema. Pre-1.0, history
// is squashed into a single migration: any new install gets the final
// shape in one transaction, with no incremental ALTERs. If you need to
// evolve the schema post-launch, add a migration 2 alongside this; do not
// edit migration 1 in place — that path is closed.
func (s *Store) Migrate() error {
	bootstrap := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range bootstrap {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.applyMigration(1, schemaV1)
}

// schemaV1 is the consolidated initial schema. Every table lands here in
// its final shape — no incremental ALTERs, no in-place edits. If you need
// schema changes after release, add migration 2 below this slice, do not
// touch these statements.
var schemaV1 = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		email TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		can_create_rooms INTEGER NOT NULL DEFAULT 0,
		language TEXT NOT NULL DEFAULT 'en',
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
		mode TEXT NOT NULL DEFAULT 'mentorship' CHECK(mode IN ('mentorship','classroom')),
		leaderboard_visible INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL REFERENCES users(id),
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS memberships (
		room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role TEXT NOT NULL CHECK(role IN ('mentor','mentee')),
		created_at TEXT NOT NULL,
		PRIMARY KEY(room_id, user_id)
	)`,
	`CREATE TABLE IF NOT EXISTS invites (
		code_hash TEXT PRIMARY KEY,
		room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
		role TEXT NOT NULL CHECK(role IN ('mentor','mentee')),
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
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS report_links (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
		label TEXT NOT NULL,
		url TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
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
		due_date TEXT NOT NULL DEFAULT '',
		last_reminded_at TEXT NOT NULL DEFAULT '',
		points_awarded INTEGER NOT NULL DEFAULT 0,
		reviewed_at TEXT NOT NULL DEFAULT '',
		reviewed_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS assignments (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
		created_by TEXT NOT NULL REFERENCES users(id),
		title TEXT NOT NULL,
		instructions TEXT NOT NULL,
		resource_url TEXT NOT NULL,
		due_date TEXT NOT NULL,
		last_reminded_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS submissions (
		id TEXT PRIMARY KEY,
		assignment_id TEXT NOT NULL REFERENCES assignments(id) ON DELETE CASCADE,
		student_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		note TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('submitted','reviewed','revise')),
		feedback TEXT NOT NULL,
		score TEXT NOT NULL DEFAULT '',
		submitted_at TEXT NOT NULL,
		reviewed_at TEXT NOT NULL,
		UNIQUE(assignment_id, student_id)
	)`,
	`CREATE TABLE IF NOT EXISTS submission_links (
		id TEXT PRIMARY KEY,
		submission_id TEXT NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
		label TEXT NOT NULL,
		url TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS notification_prefs (
		user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		enabled INTEGER NOT NULL DEFAULT 0,
		channel TEXT NOT NULL DEFAULT 'off',
		whatsapp_number TEXT NOT NULL DEFAULT '',
		telegram_chat_id TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS points_ledger (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
		source TEXT NOT NULL,
		source_id TEXT NOT NULL,
		amount INTEGER NOT NULL,
		awarded_by TEXT NOT NULL,
		awarded_at TEXT NOT NULL,
		UNIQUE(source, source_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_reports_room_created ON reports(room_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_reports_room_user ON reports(room_id, user_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_comments_report_created ON comments(report_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_room_status ON tasks(room_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_room_assignee ON tasks(room_id, assigned_to, status)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_due_date ON tasks(due_date, status)`,
	`CREATE INDEX IF NOT EXISTS idx_assignments_room_due ON assignments(room_id, due_date, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_submissions_assignment ON submissions(assignment_id, submitted_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_submissions_student ON submissions(student_id, submitted_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_report_links_report ON report_links(report_id, position)`,
	`CREATE INDEX IF NOT EXISTS idx_submission_links_submission ON submission_links(submission_id, position)`,
	`CREATE INDEX IF NOT EXISTS idx_points_ledger_user ON points_ledger(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_points_ledger_room_user ON points_ledger(room_id, user_id)`,
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

// columnExists is a small introspection helper. With migrations
// consolidated it's no longer used by Migrate; it's retained for tests
// and as a primitive for any future schema evolution.
func (s *Store) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) UserCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

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

func (s *Store) CreateInitialRoomCreator(name, email, passwordHash string) (string, error) {
	now := auth.Now()
	uid, err := auth.NewID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO users(id,name,email,password_hash,can_create_rooms,created_at)
		SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM users)`, uid, name, email, passwordHash, 1, now)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrSetupComplete
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
	err := s.db.QueryRow(`SELECT u.id, u.name, u.email, u.language, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ?`, auth.HashToken(token)).Scan(&u.ID, &u.Name, &u.Email, &u.Language, &expires)
	if err != nil {
		return nil, err
	}
	if auth.ParseTime(expires).Before(time.Now().UTC()) {
		_ = s.DeleteSession(token)
		return nil, errors.New("expired session")
	}
	return &u, nil
}

// SetUserLanguage persists the user's preferred UI language. Validation of
// the language tag is left to the caller (i18n.IsValid).
func (s *Store) SetUserLanguage(userID, language string) error {
	_, err := s.db.Exec(`UPDATE users SET language = ? WHERE id = ?`, language, userID)
	return err
}

func (s *Store) CSRF(token string) string {
	var csrf string
	_ = s.db.QueryRow(`SELECT csrf FROM sessions WHERE id_hash = ?`, auth.HashToken(token)).Scan(&csrf)
	return csrf
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

func (s *Store) CanCreateRooms(userID string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ? AND can_create_rooms = 1`, userID).Scan(&n)
	return n > 0
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
	for _, m := range mentees {
		if m.Status == "quiet" {
			out.InactiveMentees++
		}
	}
	return out, nil
}

func (s *Store) menteeDashboardTasks(userID string) ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.status, r.name, t.assigned_to, t.due_date, t.created_at,
			t.points_awarded, t.reviewed_at, t.reviewed_by
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
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.AssigneeID, &t.DueDate, &t.CreatedAt,
			&t.PointsAwarded, &t.ReviewedAt, &t.ReviewedBy); err != nil {
			return nil, err
		}
		t.DueState = dueState(t.DueDate, t.Status, time.Now().UTC())
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) menteeRecentReports(userID string) ([]domain.Report, error) {
	rows, err := s.db.Query(`SELECT rp.id, rp.room_id, rp.user_id, r.name, rp.learned, rp.practiced, rp.blocker, rp.next_plan, rp.created_at, COUNT(c.id)
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
		if err := rows.Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.CreatedAt, &r.Comments); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachReportLinks(out)
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

// linksByParent is the shared batch loader for report_links and
// submission_links. It builds a single IN(...) query, scans (parent_id,
// link) rows, and groups them in Go so callers get an O(1) map lookup
// per parent. Ordering within each group follows position then
// created_at — the same ordering used by the user who built the list.
func (s *Store) linksByParent(table, parentCol string, ids []string) (map[string][]domain.Link, error) {
	out := map[string][]domain.Link{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(ids)-1) + "?"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := fmt.Sprintf(`SELECT id, %s, label, url, position FROM %s WHERE %s IN (%s) ORDER BY %s, position, created_at`,
		parentCol, table, parentCol, placeholders, parentCol)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.Link
		var parentID string
		if err := rows.Scan(&l.ID, &parentID, &l.Label, &l.URL, &l.Position); err != nil {
			return nil, err
		}
		out[parentID] = append(out[parentID], l)
	}
	return out, rows.Err()
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

func (s *Store) menteeProgress(userID string) ([]domain.MenteeProgress, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, u.email, r.id, r.name,
		COALESCE((SELECT MAX(rp.created_at) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id), '') AS last_report,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.created_at >= datetime('now', '-7 day')) AS reports_week,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done') AS open_tasks,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done' AND t.due_date != '' AND t.due_date < date('now')) AS overdue_tasks,
		(SELECT COUNT(*) FROM reports rp WHERE rp.room_id = r.id AND rp.user_id = u.id AND rp.blocker != '') AS blockers
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
	rows, err := s.db.Query(`SELECT m.user_id FROM memberships m
		JOIN users u ON u.id = m.user_id
		WHERE m.room_id = ? AND m.role = ?
		ORDER BY u.name`, roomID, domain.RoleMentee)
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

// InvitePreview returns the public-safe view of an invite for the join
// page (room name, mode, role being claimed). Used so the joiner sees what
// they're about to sign into instead of typing credentials blind. Returns
// a preview with Valid=false when the code does not exist, is expired, or
// has already been used — the caller can use that to hide the form or
// show a clean error.
func (s *Store) InvitePreview(code string) domain.InvitePreview {
	if code == "" {
		return domain.InvitePreview{}
	}
	var preview domain.InvitePreview
	var expiresAt, usedAt string
	err := s.db.QueryRow(`SELECT r.name, r.mode, i.role, i.expires_at, COALESCE(i.used_at, '')
		FROM invites i JOIN rooms r ON r.id = i.room_id
		WHERE i.code_hash = ?`, auth.HashToken(code)).
		Scan(&preview.RoomName, &preview.RoomMode, &preview.Role, &expiresAt, &usedAt)
	if err != nil {
		return domain.InvitePreview{}
	}
	if usedAt != "" || auth.ParseTime(expiresAt).Before(time.Now().UTC()) {
		return domain.InvitePreview{}
	}
	preview.Valid = true
	return preview
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
		if m.Role == domain.RoleMentee && m.LastReport == "" {
			st.InactiveMentees++
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
	assignments, err := s.Assignments(roomID, userID, role)
	if err != nil {
		return domain.RoomData{}, err
	}
	submissions := []domain.Submission{}
	if role == domain.RoleMentor {
		submissions, err = s.Submissions(roomID)
		if err != nil {
			return domain.RoomData{}, err
		}
	}
	pending := 0
	for _, sub := range submissions {
		if sub.Status == "submitted" {
			pending++
		}
	}
	classroom := domain.ClassroomData{
		Assignments:    assignments,
		Submissions:    submissions,
		PendingReviews: pending,
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
		// Mentee who can't see the full board still gets their own
		// score; cheap single-row query.
		rank, myPoints, err = s.menteeScore(roomID, userID)
		if err != nil {
			return domain.RoomData{}, err
		}
	}
	return domain.RoomData{
		Members:     members,
		Reports:     reports,
		Tasks:       tasks,
		Invites:     invites,
		Classroom:   classroom,
		Stats:       st,
		Leaderboard: board,
		MyPoints:    myPoints,
		MyRank:      rank,
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
	query := `SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.created_at, COUNT(c.id)
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
		if err := rows.Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.CreatedAt, &r.Comments); err != nil {
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
	err := s.db.QueryRow(`SELECT r.id, r.room_id, r.user_id, u.name, r.learned, r.practiced, r.blocker, r.next_plan, r.created_at, COUNT(c.id)
		FROM reports r JOIN users u ON u.id = r.user_id
		LEFT JOIN comments c ON c.report_id = r.id
		WHERE r.room_id = ? AND r.id = ?
		GROUP BY r.id`, roomID, reportID).Scan(&r.ID, &r.RoomID, &r.UserID, &r.Author, &r.Learned, &r.Practiced, &r.Blocker, &r.NextPlan, &r.CreatedAt, &r.Comments)
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

// insertLinksTx writes a slice of labelled links to the given child
// table (report_links or submission_links) inside the caller's
// transaction. Caller is responsible for delete-before-insert when the
// parent already has rows (e.g. resubmission flow).
func insertLinksTx(tx *sql.Tx, table, parentCol, parentID string, links []domain.Link, now string) error {
	if len(links) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(fmt.Sprintf(`INSERT INTO %s(id, %s, label, url, position, created_at) VALUES(?,?,?,?,?,?)`, table, parentCol))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, l := range links {
		id, err := auth.NewID()
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(id, parentID, l.Label, l.URL, i, now); err != nil {
			return err
		}
	}
	return nil
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
	query := `SELECT t.id, t.title, t.detail, t.status, u.name, u.id, t.due_date, t.created_at,
			t.points_awarded, t.reviewed_at, t.reviewed_by
		FROM tasks t JOIN users u ON u.id = t.assigned_to
		WHERE t.room_id = ?`
	args := []any{roomID}
	if role != domain.RoleMentor {
		query += ` AND t.assigned_to = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY CASE
		WHEN t.status = 'done' AND t.reviewed_at = '' THEN 0
		WHEN t.status != 'done' AND t.due_date != '' AND t.due_date < date('now') THEN 1
		WHEN t.status != 'done' AND t.due_date != '' THEN 2
		WHEN t.status = 'todo' THEN 3
		WHEN t.status = 'doing' THEN 4
		ELSE 5 END, t.due_date ASC, t.created_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.AssigneeID, &t.DueDate, &t.CreatedAt,
			&t.PointsAwarded, &t.ReviewedAt, &t.ReviewedBy); err != nil {
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

// CreateTaskForMentees inserts one task per current mentee in the room
// inside a single transaction, so either every mentee gets the task or none
// do. Returns the number of tasks created (zero if the room has no
// mentees).
func (s *Store) CreateTaskForMentees(roomID, assignedBy, title, detail, dueDate string) (int, error) {
	menteeIDs, err := s.MenteeIDs(roomID)
	if err != nil {
		return 0, err
	}
	if len(menteeIDs) == 0 {
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
	for _, menteeID := range menteeIDs {
		id, err := auth.NewID()
		if err != nil {
			return 0, err
		}
		if _, err := stmt.Exec(id, roomID, menteeID, assignedBy, title, detail, "todo", dueDate, now, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(menteeIDs), nil
}

func (s *Store) UpdateTaskStatus(roomID, taskID, userID, role, status string) (bool, error) {
	// reviewed_at = '' guard: once a mentor has awarded points, the task is
	// closed and its status is no longer mutable.
	res, err := s.db.Exec(`UPDATE tasks SET status = ?, updated_at = ?
		WHERE id = ? AND room_id = ? AND reviewed_at = '' AND (? = 'mentor' OR assigned_to = ?)`,
		status, auth.Now(), taskID, roomID, role, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) Assignments(roomID, userID, role string) ([]domain.Assignment, error) {
	if role == domain.RoleMentor {
		// total_mentees is the same value for every row in this result
		// set, so resolve it once instead of running a correlated
		// subquery per assignment. Submissions count uses a grouped
		// LEFT JOIN for the same reason.
		var totalMentees int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE room_id = ? AND role = ?`,
			roomID, domain.RoleMentee).Scan(&totalMentees); err != nil {
			return nil, err
		}
		rows, err := s.db.Query(`SELECT a.id, a.room_id, a.title, a.instructions, a.resource_url, a.due_date, a.created_at,
				COALESCE(sub.cnt, 0) AS submitted
			FROM assignments a
			LEFT JOIN (
				SELECT assignment_id, COUNT(*) AS cnt
				FROM submissions
				GROUP BY assignment_id
			) sub ON sub.assignment_id = a.id
			WHERE a.room_id = ?
			ORDER BY CASE WHEN a.due_date != '' AND a.due_date < date('now') THEN 0 WHEN a.due_date != '' THEN 1 ELSE 2 END, a.due_date ASC, a.created_at DESC`, roomID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []domain.Assignment
		for rows.Next() {
			var a domain.Assignment
			if err := rows.Scan(&a.ID, &a.RoomID, &a.Title, &a.Instructions, &a.ResourceURL, &a.DueDate, &a.CreatedAt, &a.Submitted); err != nil {
				return nil, err
			}
			a.TotalMentees = totalMentees
			out = append(out, a)
		}
		return out, rows.Err()
	}
	rows, err := s.db.Query(`SELECT a.id, a.room_id, a.title, a.instructions, a.resource_url, a.due_date, a.created_at,
		COALESCE(sub.id, ''), COALESCE(sub.status, ''), COALESCE(sub.feedback, ''), COALESCE(sub.score, '')
		FROM assignments a
		LEFT JOIN submissions sub ON sub.assignment_id = a.id AND sub.student_id = ?
		WHERE a.room_id = ?
		ORDER BY CASE
			WHEN sub.status = 'revise' THEN 0
			WHEN sub.status IS NULL AND a.due_date != '' AND a.due_date < date('now') THEN 1
			WHEN sub.status IS NULL AND a.due_date != '' THEN 2
			WHEN sub.status IS NULL THEN 3
			WHEN sub.status = 'submitted' THEN 4
			ELSE 5 END, a.due_date ASC, a.created_at DESC`, userID, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Assignment
	var subIDs []string
	subIDToAsgnIdx := map[string]int{}
	for rows.Next() {
		var a domain.Assignment
		var submissionID string
		if err := rows.Scan(&a.ID, &a.RoomID, &a.Title, &a.Instructions, &a.ResourceURL, &a.DueDate, &a.CreatedAt, &submissionID, &a.MySubmissionStatus, &a.MyFeedback, &a.MyScore); err != nil {
			return nil, err
		}
		out = append(out, a)
		if submissionID != "" {
			subIDs = append(subIDs, submissionID)
			subIDToAsgnIdx[submissionID] = len(out) - 1
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	groups, err := s.linksByParent("submission_links", "submission_id", subIDs)
	if err != nil {
		return nil, err
	}
	for subID, idx := range subIDToAsgnIdx {
		out[idx].MySubmissionLinks = groups[subID]
	}
	return out, nil
}

func (s *Store) Submissions(roomID string) ([]domain.Submission, error) {
	rows, err := s.db.Query(`SELECT sub.id, sub.assignment_id, a.title, sub.student_id, u.name, u.email,
		sub.note, sub.status, sub.feedback, sub.score, sub.submitted_at, sub.reviewed_at
		FROM submissions sub
		JOIN assignments a ON a.id = sub.assignment_id
		JOIN users u ON u.id = sub.student_id
		WHERE a.room_id = ?
		ORDER BY CASE sub.status WHEN 'submitted' THEN 0 WHEN 'revise' THEN 1 ELSE 2 END, sub.submitted_at DESC`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Submission
	for rows.Next() {
		var sub domain.Submission
		if err := rows.Scan(&sub.ID, &sub.AssignmentID, &sub.AssignmentTitle, &sub.StudentID, &sub.StudentName, &sub.StudentEmail, &sub.Note, &sub.Status, &sub.Feedback, &sub.Score, &sub.SubmittedAt, &sub.ReviewedAt); err != nil {
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
	groups, err := s.linksByParent("submission_links", "submission_id", ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Links = groups[out[i].ID]
	}
	return out, nil
}

func (s *Store) CreateAssignment(roomID, createdBy, title, instructions, resourceURL, dueDate string) error {
	id, err := auth.NewID()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO assignments(id,room_id,created_by,title,instructions,resource_url,due_date,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, roomID, createdBy, title, instructions, resourceURL, dueDate, auth.Now())
	return err
}

// SubmitAssignment writes (or replaces) the mentee's submission for an
// assignment. Resubmission clears any prior review state and swaps out
// the link list — old links are deleted, new links inserted in the same
// transaction so the row is never half-updated.
func (s *Store) SubmitAssignment(roomID, assignmentID, studentID, note string, links []domain.Link) error {
	if !s.IsMentee(roomID, studentID) {
		return errors.New("student is not a mentee in this room")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := auth.Now()

	// Make sure the assignment exists in this room before doing anything.
	var asgnExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id = ? AND room_id = ?`, assignmentID, roomID).Scan(&asgnExists); err != nil {
		return err
	}
	if asgnExists != 1 {
		return sql.ErrNoRows
	}

	// Find an existing submission ID (if any) so we can delete its links
	// before inserting the new set. Avoids leaving orphan links on resubmit.
	var existingID string
	switch err := tx.QueryRow(`SELECT id FROM submissions WHERE assignment_id = ? AND student_id = ?`, assignmentID, studentID).Scan(&existingID); err {
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
		if _, err := tx.Exec(`INSERT INTO submissions(id, assignment_id, student_id, note, status, feedback, score, submitted_at, reviewed_at)
			VALUES(?,?,?,?,'submitted','','',?,'')`, submissionID, assignmentID, studentID, note, now); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE submissions SET note = ?, status = 'submitted', feedback = '', score = '', submitted_at = ?, reviewed_at = '' WHERE id = ?`,
			note, now, submissionID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM submission_links WHERE submission_id = ?`, submissionID); err != nil {
			return err
		}
	}
	if err := insertLinksTx(tx, "submission_links", "submission_id", submissionID, links, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ReviewSubmission writes the teacher's review for a single submission.
//
// It is idempotent against accidental double-submits: the WHERE clause
// requires reviewed_at = '', which is only true for submissions in
// status='submitted'. Once a teacher has reviewed (or asked for a revise),
// the row is locked from further edits until the student resubmits, which
// clears reviewed_at again in SubmitAssignment. Re-reviewing returns
// (false, nil) so the handler can render a 409.
func (s *Store) ReviewSubmission(roomID, submissionID, status, feedback, score string) (bool, error) {
	res, err := s.db.Exec(`UPDATE submissions
		SET status = ?, feedback = ?, score = ?, reviewed_at = ?
		WHERE id = ? AND reviewed_at = ''
		  AND assignment_id IN (SELECT id FROM assignments WHERE room_id = ?)`,
		status, feedback, score, auth.Now(), submissionID, roomID)
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
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.due_date, r.id, r.name, u.id, u.name, u.email, u.language
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
		if err := rows.Scan(&rem.TaskID, &rem.Title, &rem.Detail, &rem.DueDate, &rem.RoomID, &rem.RoomName, &rem.AssigneeID, &rem.AssigneeName, &rem.AssigneeEmail, &rem.AssigneeLanguage); err != nil {
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

// DueAssignmentReminders returns one record per (assignment, mentee) pair
// where the assignment is due within the reminder window, hasn't been
// reminded today, and the mentee hasn't submitted yet. The worker fans
// each record out through the notifier registry exactly like a task
// reminder, then calls MarkAssignmentReminded once per unique
// AssignmentID to dedup until the next day.
//
// The dedup grain is per-assignment-per-day (one round fans out to all
// unsubmitted mentees at once), not per-(assignment, mentee). That
// matches the once-per-task-per-day shape used for mentorship tasks and
// keeps the schema lean — no separate join table for reminder state.
func (s *Store) DueAssignmentReminders(now time.Time, window time.Duration) ([]domain.AssignmentReminder, error) {
	start := now.UTC().Format("2006-01-02")
	end := now.UTC().Add(window).Format("2006-01-02")
	rows, err := s.db.Query(`SELECT a.id, a.title, a.instructions, a.due_date, r.id, r.name, u.id, u.name, u.email, u.language
		FROM assignments a
		JOIN rooms r ON r.id = a.room_id
		JOIN memberships m ON m.room_id = a.room_id AND m.role = ?
		JOIN users u ON u.id = m.user_id
		LEFT JOIN submissions sub ON sub.assignment_id = a.id AND sub.student_id = u.id
		WHERE sub.id IS NULL
		  AND a.due_date != ''
		  AND a.due_date <= ?
		  AND (a.last_reminded_at = '' OR a.last_reminded_at < ?)
		ORDER BY a.due_date ASC, a.id ASC, u.name ASC`, domain.RoleMentee, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AssignmentReminder
	for rows.Next() {
		var rem domain.AssignmentReminder
		if err := rows.Scan(&rem.AssignmentID, &rem.Title, &rem.Instructions, &rem.DueDate, &rem.RoomID, &rem.RoomName, &rem.MenteeID, &rem.MenteeName, &rem.MenteeEmail, &rem.MenteeLanguage); err != nil {
			return nil, err
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

func (s *Store) MarkAssignmentReminded(assignmentID string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE assignments SET last_reminded_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), assignmentID)
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

// ReviewTask awards points (1-5) for a completed task. It runs in one
// transaction: it sets reviewed_at/reviewed_by/points_awarded on the task
// AND inserts the matching points_ledger row. The ledger row's
// UNIQUE(source, source_id) constraint guarantees a task can only be awarded
// once even under racing reviews.
//
// Returns (true, nil) when the award was recorded, (false, nil) when the
// task is not eligible (not done, already reviewed, or not in this room),
// and (_, err) on storage errors.
func (s *Store) ReviewTask(roomID, taskID, mentorID string, points int) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var assigneeID string
	err = tx.QueryRow(`SELECT assigned_to FROM tasks
		WHERE id = ? AND room_id = ? AND status = 'done' AND reviewed_at = ''`, taskID, roomID).Scan(&assigneeID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	now := auth.Now()
	if _, err := tx.Exec(`UPDATE tasks SET points_awarded = ?, reviewed_at = ?, reviewed_by = ?, updated_at = ?
		WHERE id = ? AND room_id = ? AND reviewed_at = ''`,
		points, now, mentorID, now, taskID, roomID); err != nil {
		return false, err
	}

	ledgerID, err := auth.NewID()
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO points_ledger(id, user_id, room_id, source, source_id, amount, awarded_by, awarded_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		ledgerID, assigneeID, roomID, "task", taskID, points, mentorID, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// UserPointsTotal sums all points the user has ever earned, across rooms.
func (s *Store) UserPointsTotal(userID string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM points_ledger WHERE user_id = ?`, userID).Scan(&n)
	return n
}

// RoomLeaderboard returns every mentee in the room ranked by points (desc).
// Mentees with zero points still appear so newcomers see themselves.
//
// The points sum is pulled in via one grouped LEFT JOIN instead of a
// per-row correlated subquery: O(N+M) rather than O(N×M) at the storage
// engine.
func (s *Store) RoomLeaderboard(roomID string) ([]domain.LeaderboardEntry, error) {
	rows, err := s.db.Query(`SELECT u.id, u.name, COALESCE(p.points, 0) AS points
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		LEFT JOIN (
			SELECT user_id, SUM(amount) AS points
			FROM points_ledger
			WHERE room_id = ?
			GROUP BY user_id
		) p ON p.user_id = u.id
		WHERE m.room_id = ? AND m.role = ?
		ORDER BY points DESC, u.name ASC`, roomID, roomID, domain.RoleMentee)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LeaderboardEntry
	rank := 0
	prev := -1
	for rows.Next() {
		var e domain.LeaderboardEntry
		if err := rows.Scan(&e.UserID, &e.Name, &e.Points); err != nil {
			return nil, err
		}
		// Dense ranking: ties share a rank, next distinct score advances.
		if e.Points != prev {
			rank++
			prev = e.Points
		}
		e.Rank = rank
		out = append(out, e)
	}
	return out, rows.Err()
}

// UserRankInRoom returns the mentee's 1-indexed position on the room
// leaderboard along with the total number of mentees. Position 0 means the
// user has no recorded membership as a mentee in the room.
func (s *Store) UserRankInRoom(userID, roomID string) (domain.Rank, error) {
	board, err := s.RoomLeaderboard(roomID)
	if err != nil {
		return domain.Rank{}, err
	}
	r := domain.Rank{Total: len(board)}
	for _, entry := range board {
		if entry.UserID == userID {
			r.Position = entry.Rank
			return r, nil
		}
	}
	return r, nil
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
