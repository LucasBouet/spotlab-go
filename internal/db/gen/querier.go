// Querier documents every query method on *Queries in one place. See db.go's
// package doc for why this is hand-maintained rather than sqlc-generated.
package db

import (
	"context"
)

type Querier interface {
	CreateSession(ctx context.Context, arg CreateSessionParams) (Session, error)
	CreateUser(ctx context.Context, arg CreateUserParams) (User, error)
	DeleteSession(ctx context.Context, id string) error
	GetSessionWithUser(ctx context.Context, id string) (GetSessionWithUserRow, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	GetUserByID(ctx context.Context, id string) (User, error)
	// The PRIMARY KEY on code_hash is the whole replay guard: a second attempt
	// with the same code hits a UNIQUE conflict here and updates zero rows,
	// atomically, no separate check-then-insert race.
	RecordActivationUse(ctx context.Context, arg RecordActivationUseParams) (int64, error)
	UpdateUserPasswordHash(ctx context.Context, arg UpdateUserPasswordHashParams) error
}

var _ Querier = (*Queries)(nil)
