// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
	"encoding/json"
)

const playlistTrackColumns = "id, playlist_id, deezer_track_id, title, artist_name, artist_id, album_title, album_cover, duration, added_at"

func scanPlaylistTrack(row interface{ Scan(...any) error }) (PlaylistTrack, error) {
	var t PlaylistTrack
	err := row.Scan(&t.ID, &t.PlaylistID, &t.DeezerTrackID, &t.Title, &t.ArtistName,
		&t.ArtistID, &t.AlbumTitle, &t.AlbumCover, &t.Duration, &t.AddedAt)
	return t, err
}

// PlaylistWithCovers is ListPlaylistsByUser's row: the playlist plus the
// aggregate/preview fields the old server's single Prisma query with
// `include`/`_count` produced in one round trip.
type PlaylistWithCovers struct {
	Playlist
	TrackCount int64
	Covers     []string
}

// ListPlaylistsByUser returns track count and up to 4 cover previews (most
// recently added first) per playlist, in one round trip — matching the old
// server's single query with `include`.
func (q *Queries) ListPlaylistsByUser(ctx context.Context, userID string) ([]PlaylistWithCovers, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT
			p.id, p.user_id, p.name, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM playlist_tracks pt WHERE pt.playlist_id = p.id) AS track_count,
			(
				SELECT COALESCE(json_group_array(cover), '[]')
				FROM (
					SELECT album_cover AS cover FROM playlist_tracks
					WHERE playlist_id = p.id
					ORDER BY added_at DESC
					LIMIT 4
				)
			) AS covers_json
		FROM playlists p
		WHERE p.user_id = ?
		ORDER BY p.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PlaylistWithCovers
	for rows.Next() {
		var p PlaylistWithCovers
		var coversJSON string
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &p.UpdatedAt,
			&p.TrackCount, &coversJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(coversJSON), &p.Covers); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

type GetPlaylistOwnedParams struct {
	ID     string
	UserID string
}

// GetPlaylistOwned is the ownership check shared by every route that
// mutates a playlist or its tracks — a playlist id that exists but belongs
// to someone else must read as "not found", never leak as a permission
// error.
func (q *Queries) GetPlaylistOwned(ctx context.Context, arg GetPlaylistOwnedParams) (Playlist, error) {
	row := q.db.QueryRowContext(ctx,
		"SELECT id, user_id, name, created_at, updated_at FROM playlists WHERE id = ? AND user_id = ?",
		arg.ID, arg.UserID,
	)
	var p Playlist
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (q *Queries) ListPlaylistTracks(ctx context.Context, playlistID string) ([]PlaylistTrack, error) {
	rows, err := q.db.QueryContext(ctx,
		"SELECT "+playlistTrackColumns+" FROM playlist_tracks WHERE playlist_id = ? ORDER BY added_at ASC",
		playlistID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PlaylistTrack
	for rows.Next() {
		t, err := scanPlaylistTrack(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

type CreatePlaylistParams struct {
	ID     string
	UserID string
	Name   string
}

func (q *Queries) CreatePlaylist(ctx context.Context, arg CreatePlaylistParams) (Playlist, error) {
	row := q.db.QueryRowContext(ctx,
		"INSERT INTO playlists (id, user_id, name) VALUES (?, ?, ?) RETURNING id, user_id, name, created_at, updated_at",
		arg.ID, arg.UserID, arg.Name,
	)
	var p Playlist
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

type RenamePlaylistParams struct {
	Name   string
	ID     string
	UserID string
}

// RenamePlaylist returns the number of rows affected — 0 means "not found
// or not owned", which callers turn into a 404 rather than a permission
// error, same reasoning as GetPlaylistOwned.
func (q *Queries) RenamePlaylist(ctx context.Context, arg RenamePlaylistParams) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"UPDATE playlists SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?",
		arg.Name, arg.ID, arg.UserID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type DeletePlaylistParams struct {
	ID     string
	UserID string
}

func (q *Queries) DeletePlaylist(ctx context.Context, arg DeletePlaylistParams) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		"DELETE FROM playlists WHERE id = ? AND user_id = ?",
		arg.ID, arg.UserID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type InsertPlaylistTrackParams struct {
	ID            string
	PlaylistID    string
	DeezerTrackID int64
	Title         string
	ArtistName    string
	ArtistID      sql.NullInt64
	AlbumTitle    string
	AlbumCover    string
	Duration      int64
}

func (q *Queries) InsertPlaylistTrack(ctx context.Context, arg InsertPlaylistTrackParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO playlist_tracks (id, playlist_id, deezer_track_id, title, artist_name, artist_id, album_title, album_cover, duration)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		arg.ID, arg.PlaylistID, arg.DeezerTrackID, arg.Title, arg.ArtistName,
		arg.ArtistID, arg.AlbumTitle, arg.AlbumCover, arg.Duration,
	)
	return err
}

type FindFirstPlaylistTrackByDeezerIDParams struct {
	PlaylistID    string
	DeezerTrackID int64
}

// FindFirstPlaylistTrackByDeezerID picks the earliest-added matching row —
// the closest analogue to the old server's Prisma `findFirst` (which has no
// explicit order, but SQLite returns rows in rowid/insertion order for a
// query with none of its own). sql.ErrNoRows means no match.
func (q *Queries) FindFirstPlaylistTrackByDeezerID(ctx context.Context, arg FindFirstPlaylistTrackByDeezerIDParams) (PlaylistTrack, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT `+playlistTrackColumns+` FROM playlist_tracks
		 WHERE playlist_id = ? AND deezer_track_id = ?
		 ORDER BY added_at ASC, rowid ASC
		 LIMIT 1`,
		arg.PlaylistID, arg.DeezerTrackID,
	)
	return scanPlaylistTrack(row)
}

func (q *Queries) DeletePlaylistTrackByID(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, "DELETE FROM playlist_tracks WHERE id = ?", id)
	return err
}

type DeletePlaylistTrackOwnedParams struct {
	ID     string
	UserID string
}

// DeletePlaylistTrackOwned is scoped by playlist ownership, not by a
// playlist id from the URL — this reproduces the old server's own route
// exactly (it never checks the `:id` path segment against playlistId at
// all, only that the row's playlist belongs to the caller). rowKey is
// globally unique, so this is harmless in practice, but it's a faithful
// port, not a "fix".
func (q *Queries) DeletePlaylistTrackOwned(ctx context.Context, arg DeletePlaylistTrackOwnedParams) (int64, error) {
	result, err := q.db.ExecContext(ctx,
		`DELETE FROM playlist_tracks
		 WHERE playlist_tracks.id = ?
		   AND playlist_tracks.playlist_id IN (SELECT playlists.id FROM playlists WHERE playlists.user_id = ?)`,
		arg.ID, arg.UserID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PlaylistMembership is one row of ListPlaylistMembership.
type PlaylistMembership struct {
	ID       string
	Name     string
	HasTrack bool
}

type ListPlaylistMembershipParams struct {
	DeezerTrackID int64
	UserID        string
}

func (q *Queries) ListPlaylistMembership(ctx context.Context, arg ListPlaylistMembershipParams) ([]PlaylistMembership, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT
			p.id, p.name,
			EXISTS(
				SELECT 1 FROM playlist_tracks pt
				WHERE pt.playlist_id = p.id AND pt.deezer_track_id = ?
			) AS has_track
		FROM playlists p
		WHERE p.user_id = ?
		ORDER BY p.created_at DESC`,
		arg.DeezerTrackID, arg.UserID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PlaylistMembership
	for rows.Next() {
		var m PlaylistMembership
		if err := rows.Scan(&m.ID, &m.Name, &m.HasTrack); err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}
