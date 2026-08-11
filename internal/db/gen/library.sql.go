// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
)

const likedTrackColumns = "id, user_id, deezer_track_id, title, artist_name, artist_id, album_title, album_cover, duration, created_at"

func scanLikedTrack(row interface{ Scan(...any) error }) (LikedTrack, error) {
	var t LikedTrack
	err := row.Scan(&t.ID, &t.UserID, &t.DeezerTrackID, &t.Title, &t.ArtistName,
		&t.ArtistID, &t.AlbumTitle, &t.AlbumCover, &t.Duration, &t.CreatedAt)
	return t, err
}

func (q *Queries) listLikedTracks(ctx context.Context, orderBy, userID string) ([]LikedTrack, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT "+likedTrackColumns+" FROM liked_tracks WHERE user_id = ? ORDER BY "+orderBy,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []LikedTrack
	for rows.Next() {
		t, err := scanLikedTrack(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

// The three fixed orderings below match SORT_ORDER_BY in the old server
// exactly — a dynamic ORDER BY would mean building SQL from a caller-chosen
// column name, which is a straight path to injection.

func (q *Queries) ListLikedTracksRecent(ctx context.Context, userID string) ([]LikedTrack, error) {
	return q.listLikedTracks(ctx, "created_at DESC", userID)
}

func (q *Queries) ListLikedTracksByTitle(ctx context.Context, userID string) ([]LikedTrack, error) {
	return q.listLikedTracks(ctx, "title ASC", userID)
}

func (q *Queries) ListLikedTracksByArtist(ctx context.Context, userID string) ([]LikedTrack, error) {
	return q.listLikedTracks(ctx, "artist_name ASC", userID)
}

func (q *Queries) ListLikedTrackIDs(ctx context.Context, userID string) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT deezer_track_id FROM liked_tracks WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type IsTrackLikedParams struct {
	UserID        string
	DeezerTrackID int64
}

func (q *Queries) IsTrackLiked(ctx context.Context, arg IsTrackLikedParams) (bool, error) {
	row := q.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM liked_tracks WHERE user_id = ? AND deezer_track_id = ?)",
		arg.UserID, arg.DeezerTrackID,
	)
	var exists bool
	err := row.Scan(&exists)
	return exists, err
}

type UpsertLikedTrackParams struct {
	ID            string
	UserID        string
	DeezerTrackID int64
	Title         string
	ArtistName    string
	ArtistID      sql.NullInt64
	AlbumTitle    string
	AlbumCover    string
	Duration      int64
}

// ON CONFLICT DO NOTHING mirrors the old server's `update: {}` upsert —
// liking an already-liked track is a true no-op, it never refreshes the
// stored metadata.
func (q *Queries) UpsertLikedTrack(ctx context.Context, arg UpsertLikedTrackParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO liked_tracks (id, user_id, deezer_track_id, title, artist_name, artist_id, album_title, album_cover, duration)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, deezer_track_id) DO NOTHING`,
		arg.ID, arg.UserID, arg.DeezerTrackID, arg.Title, arg.ArtistName,
		arg.ArtistID, arg.AlbumTitle, arg.AlbumCover, arg.Duration,
	)
	return err
}

type DeleteLikedTrackParams struct {
	UserID        string
	DeezerTrackID int64
}

func (q *Queries) DeleteLikedTrack(ctx context.Context, arg DeleteLikedTrackParams) error {
	_, err := q.db.ExecContext(ctx,
		"DELETE FROM liked_tracks WHERE user_id = ? AND deezer_track_id = ?",
		arg.UserID, arg.DeezerTrackID,
	)
	return err
}
