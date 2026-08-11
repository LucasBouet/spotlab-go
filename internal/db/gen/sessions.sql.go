// Originally sqlc-generated from sessions.sql (Phase 1, verified working
// before sqlc was dropped — see db.go's package doc).

package db

import (
	"context"
	"time"
)

const createSession = `-- name: CreateSession :one
INSERT INTO sessions (id, user_id, expires_at)
VALUES (?, ?, ?)
RETURNING id, user_id, expires_at, created_at
`

type CreateSessionParams struct {
	ID        string    `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	ExpiresAt time.Time `db:"expires_at" json:"expires_at"`
}

func (q *Queries) CreateSession(ctx context.Context, arg CreateSessionParams) (Session, error) {
	row := q.db.QueryRowContext(ctx, createSession, arg.ID, arg.UserID, arg.ExpiresAt)
	var i Session
	err := row.Scan(
		&i.ID,
		&i.UserID,
		&i.ExpiresAt,
		&i.CreatedAt,
	)
	return i, err
}

const deleteSession = `-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?
`

func (q *Queries) DeleteSession(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, deleteSession, id)
	return err
}

const getSessionWithUser = `-- name: GetSessionWithUser :one
SELECT sessions.id, sessions.user_id, sessions.expires_at, sessions.created_at, users.id, users.email, users.name, users.password_hash, users.role, users.created_at, users.updated_at
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.id = ?
`

type GetSessionWithUserRow struct {
	Session Session `db:"session" json:"session"`
	User    User    `db:"user" json:"user"`
}

func (q *Queries) GetSessionWithUser(ctx context.Context, id string) (GetSessionWithUserRow, error) {
	row := q.db.QueryRowContext(ctx, getSessionWithUser, id)
	var i GetSessionWithUserRow
	err := row.Scan(
		&i.Session.ID,
		&i.Session.UserID,
		&i.Session.ExpiresAt,
		&i.Session.CreatedAt,
		&i.User.ID,
		&i.User.Email,
		&i.User.Name,
		&i.User.PasswordHash,
		&i.User.Role,
		&i.User.CreatedAt,
		&i.User.UpdatedAt,
	)
	return i, err
}
