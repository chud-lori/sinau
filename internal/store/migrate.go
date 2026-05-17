package store

import (
	"database/sql"
	"fmt"

	"sinau/internal/auth"
)

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
	// tasks is the unified work-item table for both mentorship and
	// classroom rooms (the old assignments table was merged in). An
	// empty assigned_to means "for every mentee/student in the room"
	// (classroom mode and mentorship "assign to all" both lean on this);
	// a non-empty assigned_to means "for that one mentee" (mentorship
	// individual assignment). Status is NOT stored here — it's derived
	// from the viewer's task_submissions row.
	`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
		created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		assigned_to TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		detail TEXT NOT NULL,
		resource_url TEXT NOT NULL DEFAULT '',
		due_date TEXT NOT NULL DEFAULT '',
		last_reminded_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		edited_at TEXT NOT NULL DEFAULT '',
		deleted_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS task_submissions (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		student_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		note TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL CHECK(status IN ('submitted','reviewed','revise')),
		feedback TEXT NOT NULL DEFAULT '',
		score TEXT NOT NULL DEFAULT '',
		reviewed_by TEXT NOT NULL DEFAULT '',
		submitted_at TEXT NOT NULL,
		reviewed_at TEXT NOT NULL DEFAULT '',
		UNIQUE(task_id, student_id)
	)`,
	`CREATE TABLE IF NOT EXISTS task_submission_links (
		id TEXT PRIMARY KEY,
		submission_id TEXT NOT NULL REFERENCES task_submissions(id) ON DELETE CASCADE,
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
	`CREATE INDEX IF NOT EXISTS idx_tasks_room_due ON tasks(room_id, due_date, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_room_assignee ON tasks(room_id, assigned_to)`,
	`CREATE INDEX IF NOT EXISTS idx_task_submissions_task ON task_submissions(task_id, submitted_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_submissions_student ON task_submissions(student_id, submitted_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_report_links_report ON report_links(report_id, position)`,
	`CREATE INDEX IF NOT EXISTS idx_task_submission_links_submission ON task_submission_links(submission_id, position)`,
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
	`CREATE VIRTUAL TABLE IF NOT EXISTS task_submissions_fts USING fts5(
		source_id UNINDEXED, task_id UNINDEXED, student_id UNINDEXED,
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
	`CREATE TRIGGER IF NOT EXISTS task_submissions_ai AFTER INSERT ON task_submissions BEGIN
		INSERT INTO task_submissions_fts(source_id, task_id, student_id, note, feedback)
		VALUES (new.id, new.task_id, new.student_id, new.note, new.feedback);
	END`,
	`CREATE TRIGGER IF NOT EXISTS task_submissions_ad AFTER DELETE ON task_submissions BEGIN
		DELETE FROM task_submissions_fts WHERE source_id = old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS task_submissions_au AFTER UPDATE ON task_submissions BEGIN
		DELETE FROM task_submissions_fts WHERE source_id = old.id;
		INSERT INTO task_submissions_fts(source_id, task_id, student_id, note, feedback)
		VALUES (new.id, new.task_id, new.student_id, new.note, new.feedback);
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
