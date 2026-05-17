package store

import (
	"errors"
	"strings"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// ErrSetupComplete is returned by CreateInitialRoomCreator when a user
// already exists — the first-time setup form should disappear after the
// first successful bootstrap.
var ErrSetupComplete = errors.New("setup already completed")

// ErrEmailTaken is returned by UpdateUserProfile when the email conflicts
// with another account's. Callers re-render the form with this hint
// instead of leaking a database-level error.
var ErrEmailTaken = errors.New("email already in use")

func (s *Store) UserCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
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
