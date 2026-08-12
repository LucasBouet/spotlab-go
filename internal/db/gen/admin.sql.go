// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import "context"

// ListUsers backs the admin panel's user list — every account, oldest
// first, matching listUsers in the old server's Admin/service.ts.
func (q *Queries) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT id, email, name, password_hash, role, created_at, updated_at
		 FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

type SetUserRoleParams struct {
	ID   string
	Role string
}

// SetUserRole returns the number of rows affected — 0 means "no such
// user," which the caller turns into a 404 rather than a false success.
func (q *Queries) SetUserRole(ctx context.Context, arg SetUserRoleParams) (int64, error) {
	result, err := q.db.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", arg.Role, arg.ID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (q *Queries) DeleteUser(ctx context.Context, id string) (int64, error) {
	result, err := q.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
