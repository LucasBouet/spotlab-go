// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import "context"

func (q *Queries) GetAppSetting(ctx context.Context, key string) (string, error) {
	row := q.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", key)
	var value string
	err := row.Scan(&value)
	return value, err
}

type SetAppSettingParams struct {
	Key   string
	Value string
}

func (q *Queries) SetAppSetting(ctx context.Context, arg SetAppSettingParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO app_settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		arg.Key, arg.Value,
	)
	return err
}

func (q *Queries) ListAppSettings(ctx context.Context) ([]AppSetting, error) {
	rows, err := q.db.QueryContext(ctx, "SELECT key, value, updated_at FROM app_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AppSetting
	for rows.Next() {
		var s AppSetting
		if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

// CountUsers backs the "first user becomes admin" bootstrap rule — see
// registerAccountWith in auth/service.go.
func (q *Queries) CountUsers(ctx context.Context) (int64, error) {
	row := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users")
	var count int64
	err := row.Scan(&count)
	return count, err
}
