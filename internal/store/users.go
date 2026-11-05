package store

import (
	"context"
	"time"
)

const (
	RoleMember = "member"
	RoleAdmin  = "admin"
)

type User struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	Role         string     `json:"role"`
	PasswordHash string     `json:"-"`
	DisabledAt   *time.Time `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

const userCols = `id, email, display_name, role, password_hash, disabled_at, created_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.PasswordHash, &u.DisabledAt, &u.CreatedAt)
	return u, mapErr(err)
}

func (s *Store) CreateUser(ctx context.Context, email, displayName, role, passwordHash string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (email, display_name, role, password_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userCols, email, displayName, role, passwordHash))
}

func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email = $1`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) SetPassword(ctx context.Context, userID int64, hash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetRole(ctx context.Context, userID int64, role string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, userID, role)
	return err
}

func (s *Store) ListUsers(ctx context.Context, limit, offset int) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
