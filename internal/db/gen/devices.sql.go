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

type TouchDeviceLastSeenParams struct {
	UserID   string
	DeviceID string
}

// TouchDeviceLastSeen refreshes last_seen_at for an SSE connection's
// device — a plain UPDATE, never a create. Matching zero rows (the device
// isn't registered) is not an error here, exactly like the old server's
// `prisma.device.update(...).catch(() => {})`: the caller is a
// best-effort side note on every ping, not the source of truth for device
// existence.
func (q *Queries) TouchDeviceLastSeen(ctx context.Context, arg TouchDeviceLastSeenParams) error {
	_, err := q.db.ExecContext(ctx,
		"UPDATE devices SET last_seen_at = CURRENT_TIMESTAMP WHERE user_id = ? AND device_id = ?",
		arg.UserID, arg.DeviceID,
	)
	return err
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

// CountOwnedDevices returns how many *distinct* deviceIDs belong to
// userID. Deliberately not deduplicated on the caller's side first —
// SET_ACTIVE_DEVICES compares this count against the raw request list
// length, so a duplicate id in that list alone makes the counts disagree
// and the whole command gets refused, matching the old server's
// `owned.length !== action.deviceIds.length` exactly.
func (q *Queries) CountOwnedDevices(ctx context.Context, userID string, deviceIDs []string) (int, error) {
	if len(deviceIDs) == 0 {
		return 0, nil
	}
	placeholders := make([]byte, 0, len(deviceIDs)*2)
	args := make([]any, 0, len(deviceIDs)+1)
	args = append(args, userID)
	for i, id := range deviceIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	row := q.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT device_id) FROM devices WHERE user_id = ? AND device_id IN ("+string(placeholders)+")",
		args...,
	)
	var count int
	err := row.Scan(&count)
	return count, err
}
