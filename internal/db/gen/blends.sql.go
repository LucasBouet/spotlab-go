// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
	"time"
)

// Blend is one pairing of two friends. user_a_id/user_b_id are always
// stored in canonical (lexicographically smaller id first) order — see
// internal/blend/canonicalPair — so the UNIQUE(user_a_id, user_b_id)
// constraint and FindBlendByUsers both work regardless of who created it.
type Blend struct {
	ID        string    `db:"id" json:"id"`
	UserAID   string    `db:"user_a_id" json:"user_a_id"`
	UserBID   string    `db:"user_b_id" json:"user_b_id"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// BlendWithUsers is a Blend joined with both members — one round trip
// instead of two extra user lookups per blend, same reasoning as
// social.sql.go's FriendshipWithUsers.
type BlendWithUsers struct {
	Blend
	UserAName  sql.NullString
	UserAEmail string
	UserBName  sql.NullString
	UserBEmail string
}

const blendWithUsersColumns = `
	b.id, b.user_a_id, b.user_b_id, b.created_at,
	ua.name, ua.email, ub.name, ub.email
`

func scanBlendWithUsers(row interface{ Scan(...any) error }) (BlendWithUsers, error) {
	var b BlendWithUsers
	err := row.Scan(&b.ID, &b.UserAID, &b.UserBID, &b.CreatedAt,
		&b.UserAName, &b.UserAEmail, &b.UserBName, &b.UserBEmail)
	return b, err
}

const blendWithUsersJoin = `
	FROM blends b
	JOIN users ua ON ua.id = b.user_a_id
	JOIN users ub ON ub.id = b.user_b_id
`

// FindBlendByUsers expects userA/userB already in canonical order — unlike
// FindFriendshipBetween, this never needs to check both directions, since
// every row is written in that order to begin with.
func (q *Queries) FindBlendByUsers(ctx context.Context, userA, userB string) (BlendWithUsers, error) {
	row := q.db.QueryRowContext(ctx,
		"SELECT "+blendWithUsersColumns+blendWithUsersJoin+"WHERE b.user_a_id = ? AND b.user_b_id = ?",
		userA, userB,
	)
	return scanBlendWithUsers(row)
}

type CreateBlendParams struct {
	ID      string
	UserAID string
	UserBID string
}

func (q *Queries) CreateBlend(ctx context.Context, arg CreateBlendParams) (BlendWithUsers, error) {
	if _, err := q.db.ExecContext(ctx,
		"INSERT INTO blends (id, user_a_id, user_b_id) VALUES (?, ?, ?)",
		arg.ID, arg.UserAID, arg.UserBID,
	); err != nil {
		return BlendWithUsers{}, err
	}
	return q.FindBlendByUsers(ctx, arg.UserAID, arg.UserBID)
}

// ListBlendsForUser returns every blend userID is a member of, newest
// first — order barely matters (the client re-sorts via shelf_prefs
// anyway) but a stable one keeps a freshly-created blend from jumping
// around before the user has ever set an explicit position.
func (q *Queries) ListBlendsForUser(ctx context.Context, userID string) ([]BlendWithUsers, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT "+blendWithUsersColumns+blendWithUsersJoin+
			"WHERE b.user_a_id = ? OR b.user_b_id = ? ORDER BY b.created_at DESC",
		userID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []BlendWithUsers
	for rows.Next() {
		b, err := scanBlendWithUsers(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	return items, rows.Err()
}

// DeleteBlendOwned only succeeds if userID is one of the two members —
// RowsAffected 0 covers both "no such blend" and "not yours", same
// not-found-not-forbidden convention as playlists' ownership checks.
func (q *Queries) DeleteBlendOwned(ctx context.Context, id, userID string) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"DELETE FROM blends WHERE id = ? AND (user_a_id = ? OR user_b_id = ?)",
		id, userID, userID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ------------------------------------------------------------- blend_cache

type BlendCache struct {
	BlendID         string
	Payload         string
	ComputedForDate string
	ComputedAt      time.Time
}

func (q *Queries) GetBlendCache(ctx context.Context, blendID string) (BlendCache, error) {
	row := q.db.QueryRowContext(ctx,
		"SELECT blend_id, payload, computed_for_date, computed_at FROM blend_cache WHERE blend_id = ?", blendID)
	var c BlendCache
	err := row.Scan(&c.BlendID, &c.Payload, &c.ComputedForDate, &c.ComputedAt)
	return c, err
}

type UpsertBlendCacheParams struct {
	BlendID         string
	Payload         string
	ComputedForDate string
}

func (q *Queries) UpsertBlendCache(ctx context.Context, arg UpsertBlendCacheParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO blend_cache (blend_id, payload, computed_for_date)
		 VALUES (?, ?, ?)
		 ON CONFLICT (blend_id) DO UPDATE SET payload = excluded.payload, computed_for_date = excluded.computed_for_date, computed_at = CURRENT_TIMESTAMP`,
		arg.BlendID, arg.Payload, arg.ComputedForDate,
	)
	return err
}
