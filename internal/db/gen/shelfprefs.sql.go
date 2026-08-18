// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import "context"

// ShelfPref is one row of shelf_prefs — a user's manual pin/position for
// one home-shelf item (a smart playlist or a Blend), keyed by that item's
// own stable string id rather than a foreign key, since neither of those
// is a row in any table of ours (see 0003_pin_order_blends.sql).
type ShelfPref struct {
	UserID   string
	ItemID   string
	Pinned   bool
	Position int64
}

func (q *Queries) ListShelfPrefs(ctx context.Context, userID string) ([]ShelfPref, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT user_id, item_id, pinned, position FROM shelf_prefs WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ShelfPref
	for rows.Next() {
		var p ShelfPref
		if err := rows.Scan(&p.UserID, &p.ItemID, &p.Pinned, &p.Position); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

type UpsertShelfPrefParams struct {
	UserID   string
	ItemID   string
	Pinned   bool
	Position int64
}

func (q *Queries) UpsertShelfPref(ctx context.Context, arg UpsertShelfPrefParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO shelf_prefs (user_id, item_id, pinned, position)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id, item_id) DO UPDATE SET pinned = excluded.pinned, position = excluded.position, updated_at = CURRENT_TIMESTAMP`,
		arg.UserID, arg.ItemID, arg.Pinned, arg.Position,
	)
	return err
}
