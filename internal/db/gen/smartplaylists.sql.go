// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
	"time"
)

// -------------------------------------------------------- track_release_years

type TrackReleaseYear struct {
	DeezerTrackID int64     `db:"deezer_track_id" json:"deezer_track_id"`
	Year          int64     `db:"year" json:"year"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
}

func (q *Queries) GetTrackReleaseYear(ctx context.Context, deezerTrackID int64) (TrackReleaseYear, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT deezer_track_id, year, updated_at FROM track_release_years WHERE deezer_track_id = ?`, deezerTrackID)
	var t TrackReleaseYear
	err := row.Scan(&t.DeezerTrackID, &t.Year, &t.UpdatedAt)
	return t, err
}

type InsertTrackReleaseYearParams struct {
	DeezerTrackID int64
	Year          int64
}

// ON CONFLICT DO NOTHING: same reasoning as InsertTrackGenre — a global,
// shared-across-users cache, so a race between two concurrent first plays
// of the same track must not error.
func (q *Queries) InsertTrackReleaseYear(ctx context.Context, arg InsertTrackReleaseYearParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO track_release_years (deezer_track_id, year)
		 VALUES (?, ?)
		 ON CONFLICT (deezer_track_id) DO NOTHING`,
		arg.DeezerTrackID, arg.Year,
	)
	return err
}

// TopDecadeRow is one of the user's most-played decades, already bucketed
// (integer division truncates 2013 -> 2010).
type TopDecadeRow struct {
	Decade int64
	Count  int64
}

func (q *Queries) TopDecades(ctx context.Context, userID string, limit int) ([]TopDecadeRow, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT (y.year / 10) * 10 AS decade, COUNT(*) AS cnt
		 FROM play_events p
		 JOIN track_release_years y ON y.deezer_track_id = p.deezer_track_id
		 WHERE p.user_id = ?
		 GROUP BY decade
		 ORDER BY cnt DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TopDecadeRow
	for rows.Next() {
		var r TopDecadeRow
		if err := rows.Scan(&r.Decade, &r.Count); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// DistinctTrackIDsByGenreForUser and DistinctTrackIDsByDecadeForUser return
// just the ids: the smart-playlist build re-fetches each track from Deezer
// (see internal/stats/smartplaylists.go) rather than trusting the
// title/artist/cover snapshotted in play_events at play time, which can go
// stale (renamed track, re-covered album) — same tradeoff the recommendations
// build already makes for every track it surfaces.
func (q *Queries) DistinctTrackIDsByGenreForUser(ctx context.Context, userID, genreName string, limit int) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT p.deezer_track_id
		 FROM play_events p
		 JOIN track_genres g ON g.deezer_track_id = p.deezer_track_id
		 WHERE p.user_id = ? AND g.genre_name = ?
		 GROUP BY p.deezer_track_id
		 ORDER BY COUNT(*) DESC, MAX(p.created_at) DESC
		 LIMIT ?`, userID, genreName, limit)
	if err != nil {
		return nil, err
	}
	return scanTrackIDs(rows)
}

func (q *Queries) DistinctTrackIDsByDecadeForUser(ctx context.Context, userID string, minYear, maxYear int64, limit int) ([]int64, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT p.deezer_track_id
		 FROM play_events p
		 JOIN track_release_years y ON y.deezer_track_id = p.deezer_track_id
		 WHERE p.user_id = ? AND y.year >= ? AND y.year <= ?
		 GROUP BY p.deezer_track_id
		 ORDER BY COUNT(*) DESC, MAX(p.created_at) DESC
		 LIMIT ?`, userID, minYear, maxYear, limit)
	if err != nil {
		return nil, err
	}
	return scanTrackIDs(rows)
}

func scanTrackIDs(rows *sql.Rows) ([]int64, error) {
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

// -------------------------------------------------------- smart_playlists_cache

type SmartPlaylistsCache struct {
	UserID     string    `db:"user_id" json:"user_id"`
	Payload    string    `db:"payload" json:"payload"`
	ComputedAt time.Time `db:"computed_at" json:"computed_at"`
}

func (q *Queries) GetSmartPlaylistsCache(ctx context.Context, userID string) (SmartPlaylistsCache, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT user_id, payload, computed_at FROM smart_playlists_cache WHERE user_id = ?`, userID)
	var c SmartPlaylistsCache
	err := row.Scan(&c.UserID, &c.Payload, &c.ComputedAt)
	return c, err
}

func (q *Queries) UpsertSmartPlaylistsCache(ctx context.Context, userID, payload string) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO smart_playlists_cache (user_id, payload)
		 VALUES (?, ?)
		 ON CONFLICT (user_id) DO UPDATE SET payload = excluded.payload, computed_at = CURRENT_TIMESTAMP`,
		userID, payload,
	)
	return err
}
