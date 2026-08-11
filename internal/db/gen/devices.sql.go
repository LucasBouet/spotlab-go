// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
)

type UpsertDeviceParams struct {
	ID       string
	UserID   string
	DeviceID string
	Name     string
	Platform string
}

// UpsertDevice registers a device, or refreshes an already-registered one.
// Only the create path sets Name — a rename made by the user must survive
// every future re-registration (every app launch re-registers).
func (q *Queries) UpsertDevice(ctx context.Context, arg UpsertDeviceParams) (Device, error) {
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO devices (id, user_id, device_id, name, platform, last_seen_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (user_id, device_id)
		DO UPDATE SET platform = excluded.platform, last_seen_at = excluded.last_seen_at
		RETURNING id, user_id, device_id, name, platform, last_seen_at, created_at`,
		arg.ID, arg.UserID, arg.DeviceID, arg.Name, arg.Platform,
	)
	var d Device
	err := row.Scan(&d.ID, &d.UserID, &d.DeviceID, &d.Name, &d.Platform, &d.LastSeenAt, &d.CreatedAt)
	return d, err
}

func (q *Queries) ListDevicesByUser(ctx context.Context, userID string) ([]Device, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT id, user_id, device_id, name, platform, last_seen_at, created_at FROM devices WHERE user_id = ? ORDER BY last_seen_at DESC",
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.DeviceID, &d.Name, &d.Platform, &d.LastSeenAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, d)
	}
	return items, rows.Err()
}

type RenameDeviceParams struct {
	Name     string
	UserID   string
	DeviceID string
}

func (q *Queries) RenameDevice(ctx context.Context, arg RenameDeviceParams) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"UPDATE devices SET name = ? WHERE user_id = ? AND device_id = ?",
		arg.Name, arg.UserID, arg.DeviceID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type DeleteDeviceParams struct {
	UserID   string
	DeviceID string
}

func (q *Queries) DeleteDevice(ctx context.Context, arg DeleteDeviceParams) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"DELETE FROM devices WHERE user_id = ? AND device_id = ?",
		arg.UserID, arg.DeviceID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
