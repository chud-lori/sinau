// Package store is the persistence layer — every SQL query in the app
// lives here. It deliberately exposes a single *Store handle with one
// method per use case rather than a generic repository, so callers get
// query-specific signatures and the schema stays grep-able from the
// method bodies.
//
// File layout inside the package (everything compiles into one package,
// the split is purely for navigability):
//
//   store.go         — Store handle, Open/Close, shared link helpers
//   migrate.go       — Migrate, schemaV1, columnExists
//   users.go         — users, sessions, profile, password, CSRF
//   rooms.go         — rooms, memberships, RoomData aggregate
//   invites.go       — invite create / preview / claim / listing
//   reports.go       — reports + comments
//   tasks.go         — unified tasks + task_submissions
//   notifications.go — DueTaskReminders + notification_prefs
//   points.go        — points_ledger, leaderboards, rank
//   search.go        — FTS5 cross-source search
//   dashboards.go    — MentorDashboard, MenteeDashboard, attention
//   metrics.go       — CoachMetrics, GrowthMetrics, StudentGrades
//
// Add a new domain → add a new file with the same package store.
package store

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// Store is the single handle every other package depends on for
// persistence. The underlying *sql.DB is held privately so callers
// can't bypass the typed methods; DB() is exposed for tests only.
type Store struct {
	db *sql.DB
}

// Open initialises a SQLite database with the connection knobs the app
// needs (foreign keys on, WAL journal, single writer) and runs Migrate
// so callers get a ready-to-use *Store.
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

// DB returns the underlying handle. Test helpers use this to probe
// schema state; production callers should not.
func (s *Store) DB() *sql.DB {
	return s.db
}

// insertLinksTx writes a slice of labelled links to the given child
// table (report_links or task_submission_links) inside the caller's
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

// linksByParent is the shared batch loader for report_links and
// task_submission_links. It builds a single IN(...) query, scans
// (parent_id, link) rows, and groups them in Go so callers get an O(1)
// map lookup per parent. Ordering within each group follows position
// then created_at — the same ordering used by the user who built the
// list.
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
