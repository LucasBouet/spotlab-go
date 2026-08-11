// Originally sqlc-generated from activation.sql (Phase 1, verified working
// before sqlc was dropped — see db.go's package doc).

package db

import (
	"context"
	"database/sql"
)

const recordActivationUse = `-- name: RecordActivationUse :execrows
INSERT OR IGNORE INTO activation_uses (code_hash, user_id) VALUES (?, ?)
`

type RecordActivationUseParams struct {
	CodeHash string         `db:"code_hash" json:"code_hash"`
	UserID   sql.NullString `db:"user_id" json:"user_id"`
}

// The PRIMARY KEY on code_hash is the whole replay guard: a second attempt
// with the same code hits a UNIQUE conflict here and updates zero rows,
// atomically, no separate check-then-insert race.
func (q *Queries) RecordActivationUse(ctx context.Context, arg RecordActivationUseParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, recordActivationUse, arg.CodeHash, arg.UserID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
