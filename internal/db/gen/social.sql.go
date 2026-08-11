// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
	"time"
)

// FriendshipWithUsers is a friendship row joined with both participants —
// what the old server's `include: { requester: true, addressee: true }`
// produced in one query.
type FriendshipWithUsers struct {
	ID             string
	RequesterID    string
	AddresseeID    string
	Status         string
	CreatedAt      time.Time
	RequesterEmail string
	RequesterName  sql.NullString
	AddresseeEmail string
	AddresseeName  sql.NullString
}

// ListFriendshipsForUser returns every friendship row (any status, either
// direction) the user is party to — the caller splits it into
// friends/incoming/outgoing by status and by which side userID is on,
// exactly like the old server's getSocialData.
func (q *Queries) ListFriendshipsForUser(ctx context.Context, userID string) ([]FriendshipWithUsers, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT
			f.id, f.requester_id, f.addressee_id, f.status, f.created_at,
			requester.email, requester.name,
			addressee.email, addressee.name
		FROM friendships f
		JOIN users requester ON requester.id = f.requester_id
		JOIN users addressee ON addressee.id = f.addressee_id
		WHERE f.requester_id = ? OR f.addressee_id = ?
		ORDER BY f.created_at DESC`,
		userID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []FriendshipWithUsers
	for rows.Next() {
		var f FriendshipWithUsers
		if err := rows.Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt,
			&f.RequesterEmail, &f.RequesterName, &f.AddresseeEmail, &f.AddresseeName); err != nil {
			return nil, err
		}
		items = append(items, f)
	}
	return items, rows.Err()
}

// AcceptedFriendship is one row of ListAcceptedFriendshipsForUser — just
// enough to resolve "the other side", for the presence poll.
type AcceptedFriendship struct {
	RequesterID string
	AddresseeID string
}

func (q *Queries) ListAcceptedFriendshipsForUser(ctx context.Context, userID string) ([]AcceptedFriendship, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT requester_id, addressee_id FROM friendships
		WHERE status = 'ACCEPTED' AND (requester_id = ? OR addressee_id = ?)`,
		userID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []AcceptedFriendship
	for rows.Next() {
		var a AcceptedFriendship
		if err := rows.Scan(&a.RequesterID, &a.AddresseeID); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

type FindFriendshipBetweenParams struct {
	UserA string
	UserB string
}

// FindFriendshipBetween checks both directions — the unique index on
// (requester_id, addressee_id) only guards one order, so a row could exist
// either way. Any status counts (PENDING or ACCEPTED), matching the old
// server's unfiltered findFirst.
func (q *Queries) FindFriendshipBetween(ctx context.Context, arg FindFriendshipBetweenParams) (Friendship, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT id, requester_id, addressee_id, status, created_at, updated_at
		FROM friendships
		WHERE (requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)
		LIMIT 1`,
		arg.UserA, arg.UserB, arg.UserB, arg.UserA,
	)
	var f Friendship
	err := row.Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

func (q *Queries) GetFriendshipByID(ctx context.Context, id string) (Friendship, error) {
	row := q.db.QueryRowContext(ctx,
		"SELECT id, requester_id, addressee_id, status, created_at, updated_at FROM friendships WHERE id = ?",
		id,
	)
	var f Friendship
	err := row.Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

type CreateFriendshipParams struct {
	ID          string
	RequesterID string
	AddresseeID string
}

func (q *Queries) CreateFriendship(ctx context.Context, arg CreateFriendshipParams) error {
	_, err := q.db.ExecContext(ctx,
		"INSERT INTO friendships (id, requester_id, addressee_id) VALUES (?, ?, ?)",
		arg.ID, arg.RequesterID, arg.AddresseeID,
	)
	return err
}

func (q *Queries) AcceptFriendship(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx,
		"UPDATE friendships SET status = 'ACCEPTED', updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		id,
	)
	return err
}

func (q *Queries) DeleteFriendshipByID(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, "DELETE FROM friendships WHERE id = ?", id)
	return err
}
