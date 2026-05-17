package store

import (
	"sinau/internal/domain"
)

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
