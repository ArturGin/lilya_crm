package store

import (
	"context"
	"time"
)

type Session struct {
	ID        int64
	UserID    int64
	Client    string
	ExpiresAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, client, userAgent, ip string, expiresAt time.Time) (Session, error) {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	var sess Session
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, token_hash, client, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, user_id, client, expires_at`,
		userID, tokenHash, client, userAgent, ip, expiresAt,
	).Scan(&sess.ID, &sess.UserID, &sess.Client, &sess.ExpiresAt)
	return sess, mapErr(err)
}

// SessionUser resolves a token hash to an active (non-expired, non-disabled) user.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte) (Session, User, error) {
	var sess Session
	var u User
	var lastSeen time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.user_id, s.client, s.expires_at, s.last_seen_at,
		       u.id, u.email, u.display_name, u.role, u.password_hash, u.disabled_at, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.disabled_at IS NULL`, tokenHash,
	).Scan(&sess.ID, &sess.UserID, &sess.Client, &sess.ExpiresAt, &lastSeen,
		&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.PasswordHash, &u.DisabledAt, &u.CreatedAt)
	if err != nil {
		return sess, u, mapErr(err)
	}
	if time.Since(lastSeen) > 10*time.Minute {
		_, _ = s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, sess.ID)
	}
	return sess, u, nil
}

func (s *Store) DeleteSession(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// DeleteUserSessions removes all sessions of a user except keepID (pass 0 to remove all).
func (s *Store) DeleteUserSessions(ctx context.Context, userID, keepID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, keepID)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}
