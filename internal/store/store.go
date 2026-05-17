package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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
		engagement_notif_enabled INTEGER NOT NULL DEFAULT 1,
		onboarded_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		id_hash TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT ''
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
		created_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
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
		created_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
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
		updated_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
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
		created_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
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

	// Full-text search (FTS5). One virtual table per searchable resource.
	// We use the simple non-external-content shape so the source row's
	// TEXT id can live alongside the indexed columns. `source_id` is
	// UNINDEXED — it's only there so search hits can be joined back to
	// the real row. Triggers below keep each FTS table in lock-step with
	// its source on INSERT/UPDATE/DELETE.
	`CREATE VIRTUAL TABLE IF NOT EXISTS reports_fts USING fts5(
		source_id UNINDEXED, room_id UNINDEXED, user_id UNINDEXED,
		learned, practiced, blocker, next_plan,
		tokenize='unicode61 remove_diacritics 2')`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS comments_fts USING fts5(
		source_id UNINDEXED, report_id UNINDEXED, user_id UNINDEXED,
		body,
		tokenize='unicode61 remove_diacritics 2')`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(
		source_id UNINDEXED, room_id UNINDEXED, assigned_to UNINDEXED,
		title, detail,
		tokenize='unicode61 remove_diacritics 2')`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS assignments_fts USING fts5(
		source_id UNINDEXED, room_id UNINDEXED,
		title, instructions,
		tokenize='unicode61 remove_diacritics 2')`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS submissions_fts USING fts5(
		source_id UNINDEXED, assignment_id UNINDEXED, student_id UNINDEXED,
		note, feedback,
		tokenize='unicode61 remove_diacritics 2')`,

	// Sync triggers. Soft-delete is an UPDATE (deleted_at goes non-empty),
	// so AFTER UPDATE re-syncs the row. The search query filters
	// deleted_at on the source-table join, which is cheaper than
	// maintaining a separate "deleted from FTS" trigger.
	`CREATE TRIGGER IF NOT EXISTS reports_ai AFTER INSERT ON reports BEGIN
		INSERT INTO reports_fts(source_id, room_id, user_id, learned, practiced, blocker, next_plan)
		VALUES (new.id, new.room_id, new.user_id, new.learned, new.practiced, new.blocker, new.next_plan);
	END`,
	`CREATE TRIGGER IF NOT EXISTS reports_ad AFTER DELETE ON reports BEGIN
		DELETE FROM reports_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS reports_au AFTER UPDATE ON reports BEGIN
		DELETE FROM reports_fts WHERE source_id = old.id;
		INSERT INTO reports_fts(source_id, room_id, user_id, learned, practiced, blocker, next_plan)
		VALUES (new.id, new.room_id, new.user_id, new.learned, new.practiced, new.blocker, new.next_plan);
	END`,
	`CREATE TRIGGER IF NOT EXISTS comments_ai AFTER INSERT ON comments BEGIN
		INSERT INTO comments_fts(source_id, report_id, user_id, body)
		VALUES (new.id, new.report_id, new.user_id, new.body);
	END`,
	`CREATE TRIGGER IF NOT EXISTS comments_ad AFTER DELETE ON comments BEGIN
		DELETE FROM comments_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS comments_au AFTER UPDATE ON comments BEGIN
		DELETE FROM comments_fts WHERE source_id = old.id;
		INSERT INTO comments_fts(source_id, report_id, user_id, body)
		VALUES (new.id, new.report_id, new.user_id, new.body);
	END`,
	`CREATE TRIGGER IF NOT EXISTS tasks_ai AFTER INSERT ON tasks BEGIN
		INSERT INTO tasks_fts(source_id, room_id, assigned_to, title, detail)
		VALUES (new.id, new.room_id, new.assigned_to, new.title, new.detail);
	END`,
	`CREATE TRIGGER IF NOT EXISTS tasks_ad AFTER DELETE ON tasks BEGIN
		DELETE FROM tasks_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS tasks_au AFTER UPDATE ON tasks BEGIN
		DELETE FROM tasks_fts WHERE source_id = old.id;
		INSERT INTO tasks_fts(source_id, room_id, assigned_to, title, detail)
		VALUES (new.id, new.room_id, new.assigned_to, new.title, new.detail);
	END`,
	`CREATE TRIGGER IF NOT EXISTS assignments_ai AFTER INSERT ON assignments BEGIN
		INSERT INTO assignments_fts(source_id, room_id, title, instructions)
		VALUES (new.id, new.room_id, new.title, new.instructions);
	END`,
	`CREATE TRIGGER IF NOT EXISTS assignments_ad AFTER DELETE ON assignments BEGIN
		DELETE FROM assignments_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS assignments_au AFTER UPDATE ON assignments BEGIN
		DELETE FROM assignments_fts WHERE source_id = old.id;
		INSERT INTO assignments_fts(source_id, room_id, title, instructions)
		VALUES (new.id, new.room_id, new.title, new.instructions);
	END`,
	`CREATE TRIGGER IF NOT EXISTS submissions_ai AFTER INSERT ON submissions BEGIN
		INSERT INTO submissions_fts(source_id, assignment_id, student_id, note, feedback)
		VALUES (new.id, new.assignment_id, new.student_id, new.note, new.feedback);
	END`,
	`CREATE TRIGGER IF NOT EXISTS submissions_ad AFTER DELETE ON submissions BEGIN
		DELETE FROM submissions_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS submissions_au AFTER UPDATE ON submissions BEGIN
		DELETE FROM submissions_fts WHERE source_id = old.id;
		INSERT INTO submissions_fts(source_id, assignment_id, student_id, note, feedback)
		VALUES (new.id, new.assignment_id, new.student_id, new.note, new.feedback);
	END`,
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
	_, err := s.db.Exec(`INSERT INTO sessions(id_hash,user_id,csrf,expires_at,created_at) VALUES(?,?,?,?,?)`,
		auth.HashToken(token), userID, csrf, expires.UTC().Format(time.RFC3339), auth.Now())
	return err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id_hash = ?`, auth.HashToken(token))
	return err
}

func (s *Store) CurrentUser(token string) (*domain.User, error) {
	var u domain.User
	var expires, onboardedAt string
	var engagement int
	err := s.db.QueryRow(`SELECT u.id, u.name, u.email, u.language, u.engagement_notif_enabled, u.onboarded_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ?`, auth.HashToken(token)).Scan(&u.ID, &u.Name, &u.Email, &u.Language, &engagement, &onboardedAt, &expires)
	if err != nil {
		return nil, err
	}
	if auth.ParseTime(expires).Before(time.Now().UTC()) {
		_ = s.DeleteSession(token)
		return nil, errors.New("expired session")
	}
	u.EngagementEnabled = engagement == 1
	u.Onboarded = onboardedAt != ""
	return &u, nil
}

// UserByID returns the full profile record. Used by /profile so the page
// can render current name/email/language/engagement-toggle without
// touching the session row.
func (s *Store) UserByID(userID string) (*domain.User, error) {
	var u domain.User
	var engagement int
	var onboardedAt string
	err := s.db.QueryRow(`SELECT id, name, email, language, engagement_notif_enabled, onboarded_at FROM users WHERE id = ?`,
		userID).Scan(&u.ID, &u.Name, &u.Email, &u.Language, &engagement, &onboardedAt)
	if err != nil {
		return nil, err
	}
	u.EngagementEnabled = engagement == 1
	u.Onboarded = onboardedAt != ""
	return &u, nil
}

// MarkOnboarded stamps users.onboarded_at so the onboarding page
// stops auto-redirecting on subsequent home visits. Idempotent — the
// COALESCE keeps the original timestamp if the user revisits the
// onboarding URL after completing it.
func (s *Store) MarkOnboarded(userID string) error {
	_, err := s.db.Exec(`UPDATE users SET onboarded_at = COALESCE(NULLIF(onboarded_at, ''), ?) WHERE id = ?`,
		auth.Now(), userID)
	return err
}

// SetUserLanguage persists the user's preferred UI language. Validation of
// the language tag is left to the caller (i18n.IsValid).
func (s *Store) SetUserLanguage(userID, language string) error {
	_, err := s.db.Exec(`UPDATE users SET language = ? WHERE id = ?`, language, userID)
	return err
}

// ErrEmailTaken is returned by UpdateUserProfile when the email conflicts
// with another account's. Callers re-render the form with this hint
// instead of leaking a database-level error.
var ErrEmailTaken = errors.New("email already in use")

// UpdateUserProfile mutates the four user-controlled profile fields in one
// statement. Email collisions surface as ErrEmailTaken so the handler can
// distinguish them from generic errors. Caller validates inputs (name
// non-empty, email well-formed, language supported).
func (s *Store) UpdateUserProfile(userID, name, email, language string, engagementEnabled bool) error {
	e := 0
	if engagementEnabled {
		e = 1
	}
	_, err := s.db.Exec(`UPDATE users SET name = ?, email = ?, language = ?, engagement_notif_enabled = ? WHERE id = ?`,
		name, email, language, e, userID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
		return ErrEmailTaken
	}
	return err
}

// UserPasswordHash returns the current argon2id hash for the user. Used
// by /profile/password to verify the current password before accepting a
// new one.
func (s *Store) UserPasswordHash(userID string) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	return hash, err
}

// UpdateUserPassword stores a new argon2id hash and revokes every other
// active session for the user in one transaction, so a credential
// rotation immediately ejects any session that might already be
// compromised. The current session token must be passed so the caller
// stays signed in.
func (s *Store) UpdateUserPassword(userID, newHash, keepToken string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`,
		userID, auth.HashToken(keepToken)); err != nil {
		return err
	}
	return tx.Commit()
}

// UserSessionCount returns the number of currently-active sessions for
// the user (expired rows are excluded so the /profile UI shows what the
// user actually has). Used by the "Sign out other sessions" affordance.
func (s *Store) UserSessionCount(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = ? AND expires_at > ?`,
		userID, auth.Now()).Scan(&n)
	return n, err
}

// RevokeOtherSessions deletes every session for the user except the one
// matching keepToken. Returns the number of sessions revoked.
func (s *Store) RevokeOtherSessions(userID, keepToken string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id_hash != ?`,
		userID, auth.HashToken(keepToken))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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
			JOIN reports rp ON rp.room_id = mr.room_id AND rp.deleted_at = ''
			LEFT JOIN comments c ON c.report_id = rp.id AND c.deleted_at = ''
			WHERE mr.user_id = ? AND mr.role = 'mentor'
			GROUP BY rp.id HAVING COUNT(c.id) = 0)`},
		{&out.Blockers, `SELECT COUNT(*) FROM memberships mr JOIN reports rp ON rp.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND rp.blocker != '' AND rp.deleted_at = ''`},
		{&out.OpenTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.deleted_at = ''`},
		{&out.DueSoonTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.deleted_at = '' AND t.due_date != '' AND t.due_date >= date('now') AND t.due_date <= date('now', '+2 day')`},
		{&out.OverdueTasks, `SELECT COUNT(*) FROM memberships mr JOIN tasks t ON t.room_id = mr.room_id WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.deleted_at = '' AND t.due_date != '' AND t.due_date < date('now')`},
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

func (s *Store) menteeDashboardTasks(userID string) ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT t.id, t.title, t.detail, t.status, r.name, t.assigned_to, t.due_date, t.created_at,
			t.points_awarded, t.reviewed_at, t.reviewed_by
		FROM tasks t
		JOIN rooms r ON r.id = t.room_id
		WHERE t.assigned_to = ? AND t.status != 'done' AND t.deleted_at = ''
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
		WHERE mr.user_id = ? AND mr.role = 'mentor' AND t.status != 'done' AND t.deleted_at = '' AND t.due_date != '' AND t.due_date < date('now')
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
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done' AND t.deleted_at = '') AS open_tasks,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = r.id AND t.assigned_to = u.id AND t.status != 'done' AND t.deleted_at = '' AND t.due_date != '' AND t.due_date < date('now')) AS overdue_tasks,
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
		COALESCE((SELECT MAX(r.created_at) FROM reports r WHERE r.room_id = m.room_id AND r.user_id = u.id AND r.deleted_at = ''), '') AS last_report,
		(SELECT COUNT(*) FROM tasks t WHERE t.room_id = m.room_id AND t.assigned_to = u.id AND t.status != 'done' AND t.deleted_at = '') AS open_tasks
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

func (s *Store) Tasks(roomID, userID, role string) ([]domain.Task, error) {
	query := `SELECT t.id, t.title, t.detail, t.status, u.name, u.id, t.assigned_by, t.due_date, t.created_at, t.edited_at,
			t.points_awarded, t.reviewed_at, t.reviewed_by
		FROM tasks t JOIN users u ON u.id = t.assigned_to
		WHERE t.room_id = ? AND t.deleted_at = ''`
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
		if err := rows.Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.AssigneeID, &t.AssignedByID, &t.DueDate, &t.CreatedAt, &t.EditedAt,
			&t.PointsAwarded, &t.ReviewedAt, &t.ReviewedBy); err != nil {
			return nil, err
		}
		t.DueState = dueState(t.DueDate, t.Status, time.Now().UTC())
		out = append(out, t)
	}
	return out, rows.Err()
}

// TaskRoomAndAssignee returns the (room, assignee) pair for a non-deleted
// task. Used by edit/delete permission checks at the handler layer.
func (s *Store) TaskRoomAndAssignee(taskID string) (roomID, assignedTo string, err error) {
	err = s.db.QueryRow(`SELECT room_id, assigned_to FROM tasks WHERE id = ? AND deleted_at = ''`, taskID).
		Scan(&roomID, &assignedTo)
	return
}

// TaskByID loads a task for the edit form. Returns sql.ErrNoRows if the
// row is missing, deleted, or already reviewed (since reviewed tasks
// are no longer editable). Caller still verifies room membership.
func (s *Store) TaskByID(roomID, taskID string) (domain.Task, error) {
	var t domain.Task
	err := s.db.QueryRow(`SELECT t.id, t.title, t.detail, t.status, u.name, u.id, t.assigned_by, t.due_date, t.created_at, t.edited_at,
			t.points_awarded, t.reviewed_at, t.reviewed_by
		FROM tasks t JOIN users u ON u.id = t.assigned_to
		WHERE t.id = ? AND t.room_id = ? AND t.deleted_at = ''`, taskID, roomID).
		Scan(&t.ID, &t.Title, &t.Detail, &t.Status, &t.Assignee, &t.AssigneeID, &t.AssignedByID, &t.DueDate, &t.CreatedAt, &t.EditedAt,
			&t.PointsAwarded, &t.ReviewedAt, &t.ReviewedBy)
	if err != nil {
		return t, err
	}
	t.DueState = dueState(t.DueDate, t.Status, time.Now().UTC())
	return t, nil
}

// UpdateTask mutates the editable task fields. reviewed_at = '' guards
// against editing a task that's already been graded; once points are
// awarded the content is locked. edited_at stamps the change.
func (s *Store) UpdateTask(taskID, title, detail, dueDate string) (bool, error) {
	now := auth.Now()
	res, err := s.db.Exec(`UPDATE tasks SET title = ?, detail = ?, due_date = ?, edited_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND reviewed_at = ''`, title, detail, dueDate, now, now, taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteTask soft-deletes a task. Once awarded points are present
// (reviewed_at != '') deletion is rejected so a mentor cannot undo a
// review by deleting the task and the ledger row in tandem.
func (s *Store) DeleteTask(taskID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET deleted_at = ? WHERE id = ? AND deleted_at = '' AND reviewed_at = ''`,
		auth.Now(), taskID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
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
		rows, err := s.db.Query(`SELECT a.id, a.room_id, a.title, a.instructions, a.resource_url, a.due_date, a.created_at, a.edited_at,
				COALESCE(sub.cnt, 0) AS submitted
			FROM assignments a
			LEFT JOIN (
				SELECT assignment_id, COUNT(*) AS cnt
				FROM submissions
				GROUP BY assignment_id
			) sub ON sub.assignment_id = a.id
			WHERE a.room_id = ? AND a.deleted_at = ''
			ORDER BY CASE WHEN a.due_date != '' AND a.due_date < date('now') THEN 0 WHEN a.due_date != '' THEN 1 ELSE 2 END, a.due_date ASC, a.created_at DESC`, roomID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []domain.Assignment
		for rows.Next() {
			var a domain.Assignment
			if err := rows.Scan(&a.ID, &a.RoomID, &a.Title, &a.Instructions, &a.ResourceURL, &a.DueDate, &a.CreatedAt, &a.EditedAt, &a.Submitted); err != nil {
				return nil, err
			}
			a.TotalMentees = totalMentees
			out = append(out, a)
		}
		return out, rows.Err()
	}
	rows, err := s.db.Query(`SELECT a.id, a.room_id, a.title, a.instructions, a.resource_url, a.due_date, a.created_at, a.edited_at,
		COALESCE(sub.id, ''), COALESCE(sub.status, ''), COALESCE(sub.feedback, ''), COALESCE(sub.score, '')
		FROM assignments a
		LEFT JOIN submissions sub ON sub.assignment_id = a.id AND sub.student_id = ?
		WHERE a.room_id = ? AND a.deleted_at = ''
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
		if err := rows.Scan(&a.ID, &a.RoomID, &a.Title, &a.Instructions, &a.ResourceURL, &a.DueDate, &a.CreatedAt, &a.EditedAt, &submissionID, &a.MySubmissionStatus, &a.MyFeedback, &a.MyScore); err != nil {
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
		WHERE a.room_id = ? AND a.deleted_at = ''
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

// CreateAssignment inserts a classroom assignment and returns its new
// ID. Caller is responsible for validating dueDate format and ensuring
// the room is in classroom mode.
func (s *Store) CreateAssignment(roomID, createdBy, title, instructions, resourceURL, dueDate string) (string, error) {
	id, err := auth.NewID()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(`INSERT INTO assignments(id,room_id,created_by,title,instructions,resource_url,due_date,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, roomID, createdBy, title, instructions, resourceURL, dueDate, auth.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// AssignmentRoom returns the room_id of a non-deleted assignment. Used
// by edit/delete permission checks. Returns sql.ErrNoRows when the
// assignment is missing or already deleted.
func (s *Store) AssignmentRoom(assignmentID string) (string, error) {
	var roomID string
	err := s.db.QueryRow(`SELECT room_id FROM assignments WHERE id = ? AND deleted_at = ''`, assignmentID).Scan(&roomID)
	return roomID, err
}

// AssignmentByID loads an assignment for the teacher's edit form.
// Returns sql.ErrNoRows when the row is missing or already deleted.
func (s *Store) AssignmentByID(roomID, assignmentID string) (domain.Assignment, error) {
	var a domain.Assignment
	err := s.db.QueryRow(`SELECT id, room_id, title, instructions, resource_url, due_date, created_at, edited_at
		FROM assignments WHERE id = ? AND room_id = ? AND deleted_at = ''`, assignmentID, roomID).
		Scan(&a.ID, &a.RoomID, &a.Title, &a.Instructions, &a.ResourceURL, &a.DueDate, &a.CreatedAt, &a.EditedAt)
	return a, err
}

// UpdateAssignment mutates the editable fields. edited_at is stamped so
// the UI can show "edited <when>". Returns (false, nil) when the
// assignment is missing or already deleted.
func (s *Store) UpdateAssignment(assignmentID, title, instructions, resourceURL, dueDate string) (bool, error) {
	res, err := s.db.Exec(`UPDATE assignments SET title = ?, instructions = ?, resource_url = ?, due_date = ?, edited_at = ?
		WHERE id = ? AND deleted_at = ''`, title, instructions, resourceURL, dueDate, auth.Now(), assignmentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteAssignment soft-deletes a classroom assignment. Submissions
// already attached stay in the DB but are hidden from Submissions()
// since that query joins assignments and now filters deleted_at.
func (s *Store) DeleteAssignment(assignmentID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE assignments SET deleted_at = ? WHERE id = ? AND deleted_at = ''`,
		auth.Now(), assignmentID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
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

// SubmissionContext returns the student_id and assignment title for a
// submission. Used by web handlers to fan an engagement notification
// out to the student after a review is posted, without re-querying.
func (s *Store) SubmissionContext(submissionID string) (studentID, assignmentTitle string, err error) {
	err = s.db.QueryRow(`SELECT sub.student_id, a.title
		FROM submissions sub JOIN assignments a ON a.id = sub.assignment_id
		WHERE sub.id = ?`, submissionID).Scan(&studentID, &assignmentTitle)
	return
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
		  AND t.deleted_at = ''
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
		  AND a.deleted_at = ''
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

// Search runs a single full-text query across reports, comments, tasks,
// assignments, and submissions, scoped to rows the user is allowed to
// see. Returns at most limit hits per source, ordered by FTS5 BM25 rank.
//
// Visibility per source mirrors the existing room views:
//   - reports:      mentors see all room reports, mentees only their own
//   - comments:     all room members
//   - tasks:        mentors see all room tasks, mentees only assigned-to-them
//   - assignments:  all classroom-room members
//   - submissions:  teachers see all in their classroom rooms; students see their own
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
		-- Tasks
		SELECT 'task', t.id, t.room_id, rm.name, rm.mode,
			COALESCE(u.name, ''), t.title,
			snippet(tasks_fts, -1, char(2), char(3), '…', 24),
			t.created_at, bm25(tasks_fts)
		FROM tasks_fts
		JOIN tasks t ON t.id = tasks_fts.source_id
		JOIN rooms rm ON rm.id = t.room_id
		JOIN users u ON u.id = t.assigned_to
		JOIN memberships m ON m.room_id = t.room_id AND m.user_id = ?
		WHERE tasks_fts MATCH ?
		  AND t.deleted_at = ''
		  AND (m.role = 'mentor' OR t.assigned_to = ?)
		UNION ALL
		-- Assignments
		SELECT 'assignment', a.id, a.room_id, rm.name, rm.mode,
			'', a.title,
			snippet(assignments_fts, -1, char(2), char(3), '…', 24),
			a.created_at, bm25(assignments_fts)
		FROM assignments_fts
		JOIN assignments a ON a.id = assignments_fts.source_id
		JOIN rooms rm ON rm.id = a.room_id
		JOIN memberships m ON m.room_id = a.room_id AND m.user_id = ?
		WHERE assignments_fts MATCH ?
		  AND a.deleted_at = ''
		  AND rm.mode = 'classroom'
		UNION ALL
		-- Submissions
		SELECT 'submission', sub.id, rm.id, rm.name, rm.mode,
			COALESCE(u.name, ''), a.title,
			snippet(submissions_fts, -1, char(2), char(3), '…', 24),
			sub.submitted_at, bm25(submissions_fts)
		FROM submissions_fts
		JOIN submissions sub ON sub.id = submissions_fts.source_id
		JOIN assignments a ON a.id = sub.assignment_id
		JOIN rooms rm ON rm.id = a.room_id
		JOIN users u ON u.id = sub.student_id
		JOIN memberships m ON m.room_id = a.room_id AND m.user_id = ?
		WHERE submissions_fts MATCH ?
		  AND a.deleted_at = ''
		  AND (m.role = 'mentor' OR sub.student_id = ?)
		ORDER BY rk
		LIMIT ?`
	rows, err := s.db.Query(sql,
		userID, q, userID,
		userID, q,
		userID, q, userID,
		userID, q,
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
		// comments live on a report — caller knows roomID; we used
		// h.ID for the comment id, but the SQL above puts the
		// comment's id in ID. We don't store the report id on the
		// hit struct since the link goes back to the room view as a
		// reasonable approximation. A future iteration can fan out
		// to the exact report.
		return "/rooms/" + h.RoomID
	case "task", "submission":
		return "/rooms/" + h.RoomID
	case "assignment":
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

	// % of submissions reviewed in classrooms this mentor leads.
	q2 := `SELECT
			SUM(CASE WHEN sub.reviewed_at != '' THEN 1 ELSE 0 END) AS reviewed,
			COUNT(*) AS total
		FROM submissions sub
		JOIN assignments a ON a.id = sub.assignment_id AND a.deleted_at = ''
		JOIN memberships m ON m.room_id = a.room_id AND m.user_id = ? AND m.role = 'mentor'`
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
				SELECT t.assigned_to, t.updated_at FROM tasks t WHERE t.deleted_at = ''
				UNION ALL
				SELECT sub.student_id, sub.submitted_at FROM submissions sub
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

	// Task completion rate in the window.
	if err := s.db.QueryRow(`SELECT
			SUM(CASE WHEN status = 'done' THEN 1 ELSE 0 END) AS done,
			SUM(CASE WHEN status != 'done' THEN 1 ELSE 0 END) AS open
		FROM tasks WHERE assigned_to = ? AND deleted_at = '' AND created_at >= ?`,
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
// in as a mentee, each containing every assignment in that room with
// the student's submission state. Used by /me/grades.
//
// Status is derived per assignment:
//   - "missing"  — no submission and the deadline has passed
//   - "—"        — no submission, deadline not yet passed
//   - "late"     — submitted after the deadline, awaiting review
//   - "submitted"/ "revise" — submission status as-is
//   - "reviewed" — submission has been reviewed
func (s *Store) StudentGrades(userID string) ([]domain.GradeRoom, error) {
	rows, err := s.db.Query(`SELECT rm.id, rm.name, a.id, a.title, a.due_date,
			COALESCE(sub.status, ''), COALESCE(sub.score, ''),
			COALESCE(sub.feedback, ''), COALESCE(sub.submitted_at, '')
		FROM memberships m
		JOIN rooms rm ON rm.id = m.room_id AND rm.mode = 'classroom'
		JOIN assignments a ON a.room_id = rm.id AND a.deleted_at = ''
		LEFT JOIN submissions sub ON sub.assignment_id = a.id AND sub.student_id = ?
		WHERE m.user_id = ? AND m.role = 'mentee'
		ORDER BY rm.name, a.due_date DESC, a.created_at DESC`, userID, userID)
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
