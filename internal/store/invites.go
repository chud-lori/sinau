package store

import (
	"errors"
	"time"

	"sinau/internal/auth"
	"sinau/internal/domain"
)

// InviteClaim is the internal lookup record used by JoinWithInvite to
// translate an invite code back to (room, role) and detect expiry / reuse.
type InviteClaim struct {
	RoomID    string
	Role      string
	ExpiresAt string
	UsedAt    string
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

// Invites lists outstanding (and recently-used) invites for the room
// settings panel, truncated to a short prefix of the hash so the mentor
// can identify the code without exposing it.
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
