// Hand-written (see db.go's package doc for why sqlc isn't used here).

package db

import (
	"context"
	"database/sql"
)

// ------------------------------------------------------------ play_events

type InsertPlayEventParams struct {
	ID            string
	UserID        string
	DeezerTrackID int64
	Title         string
	ArtistName    string
	AlbumTitle    string
	AlbumCover    string
	Duration      int64
}

func (q *Queries) InsertPlayEvent(ctx context.Context, arg InsertPlayEventParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO play_events (id, user_id, deezer_track_id, title, artist_name, album_title, album_cover, duration)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		arg.ID, arg.UserID, arg.DeezerTrackID, arg.Title, arg.ArtistName, arg.AlbumTitle, arg.AlbumCover, arg.Duration,
	)
	return err
}

type PlayTotals struct {
	Count        int64
	TotalSeconds int64
}

// GetPlayTotals is the aggregate half of getListeningStats in stats.ts
// (prisma.playEvent.aggregate) — COALESCE covers a user with zero plays,
// where SUM would otherwise come back NULL rather than 0.
func (q *Queries) GetPlayTotals(ctx context.Context, userID string) (PlayTotals, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(duration), 0) FROM play_events WHERE user_id = ?`, userID)
	var t PlayTotals
	err := row.Scan(&t.Count, &t.TotalSeconds)
	return t, err
}

type TopTrackRow struct {
	Title      string
	ArtistName string
	AlbumCover string
	Count      int64
}

// TopTracks groups by deezer_track_id (not title+artist) so two tracks that
// happen to share a title never merge — same grouping key as stats.ts.
func (q *Queries) TopTracks(ctx context.Context, userID string, limit int) ([]TopTrackRow, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT title, artist_name, album_cover, COUNT(*) AS cnt
		 FROM play_events
		 WHERE user_id = ?
		 GROUP BY deezer_track_id
		 ORDER BY cnt DESC, MAX(created_at) DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TopTrackRow
	for rows.Next() {
		var r TopTrackRow
		if err := rows.Scan(&r.Title, &r.ArtistName, &r.AlbumCover, &r.Count); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

type TopAlbumRow struct {
	AlbumTitle string
	ArtistName string
	AlbumCover string
	Count      int64
}

func (q *Queries) TopAlbums(ctx context.Context, userID string, limit int) ([]TopAlbumRow, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT album_title, artist_name, album_cover, COUNT(*) AS cnt
		 FROM play_events
		 WHERE user_id = ?
		 GROUP BY album_title, artist_name
		 ORDER BY cnt DESC, MAX(created_at) DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TopAlbumRow
	for rows.Next() {
		var r TopAlbumRow
		if err := rows.Scan(&r.AlbumTitle, &r.ArtistName, &r.AlbumCover, &r.Count); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

type TopGenreRow struct {
	GenreName string
	Count     int64
}

func (q *Queries) TopGenres(ctx context.Context, userID string, limit int) ([]TopGenreRow, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT g.genre_name, COUNT(*) AS cnt
		 FROM play_events p
		 JOIN track_genres g ON g.deezer_track_id = p.deezer_track_id
		 WHERE p.user_id = ? AND g.genre_name IS NOT NULL
		 GROUP BY g.genre_name
		 ORDER BY cnt DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TopGenreRow
	for rows.Next() {
		var r TopGenreRow
		if err := rows.Scan(&r.GenreName, &r.Count); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// SeedArtistRow is one of the user's most-played artists in a window, paired
// with one track id of theirs — getSeedArtists in recommendations.ts needs a
// sample track to resolve the artist's Deezer id from later (PlayEvent only
// stores the artist name).
type SeedArtistRow struct {
	ArtistName    string
	SampleTrackID int64
}

// SeedArtists mirrors the groupBy(by: ["artistName"], _max: {deezerTrackId},
// orderBy: {_count: {artistName: "desc"}}) call in recommendations.ts.
// since.Valid = false means "all time" (the `all` window has no lower bound).
func (q *Queries) SeedArtists(ctx context.Context, userID string, since sql.NullTime, limit int) ([]SeedArtistRow, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if since.Valid {
		rows, err = q.db.QueryContext(ctx,
			`SELECT artist_name, MAX(deezer_track_id) AS sample_track_id
			 FROM play_events
			 WHERE user_id = ? AND created_at >= ?
			 GROUP BY artist_name
			 ORDER BY COUNT(*) DESC
			 LIMIT ?`, userID, since.Time, limit)
	} else {
		rows, err = q.db.QueryContext(ctx,
			`SELECT artist_name, MAX(deezer_track_id) AS sample_track_id
			 FROM play_events
			 WHERE user_id = ?
			 GROUP BY artist_name
			 ORDER BY COUNT(*) DESC
			 LIMIT ?`, userID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []SeedArtistRow
	for rows.Next() {
		var r SeedArtistRow
		if err := rows.Scan(&r.ArtistName, &r.SampleTrackID); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// PlayedRow is one distinct track the user has ever played — used to build
// the exclusion sets in getPlayedSets (recommendations.ts) so a recommended
// track/album is never one they've already heard.
type PlayedRow struct {
	DeezerTrackID int64
	AlbumTitle    string
	ArtistName    string
}

func (q *Queries) ListPlayedTracksAndAlbums(ctx context.Context, userID string) ([]PlayedRow, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT deezer_track_id, album_title, artist_name FROM play_events WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PlayedRow
	for rows.Next() {
		var r PlayedRow
		if err := rows.Scan(&r.DeezerTrackID, &r.AlbumTitle, &r.ArtistName); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// ----------------------------------------------------------- track_genres

func (q *Queries) GetTrackGenre(ctx context.Context, deezerTrackID int64) (TrackGenre, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT deezer_track_id, album_id, genre_id, genre_name, source, updated_at
		 FROM track_genres WHERE deezer_track_id = ?`, deezerTrackID)
	var t TrackGenre
	err := row.Scan(&t.DeezerTrackID, &t.AlbumID, &t.GenreID, &t.GenreName, &t.Source, &t.UpdatedAt)
	return t, err
}

type InsertTrackGenreParams struct {
	DeezerTrackID int64
	AlbumID       sql.NullInt64
	GenreID       sql.NullInt64
	GenreName     sql.NullString
	Source        string
}

// ON CONFLICT DO NOTHING: the genre cache is global (shared across users),
// so a second concurrent play of the same never-before-seen track racing
// this insert must not error — the first writer wins, exactly like the
// upsert in track-genre.ts (`prisma...create`, but the id is globally
// unique per track so a genuine race is the only way this fires).
func (q *Queries) InsertTrackGenre(ctx context.Context, arg InsertTrackGenreParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO track_genres (deezer_track_id, album_id, genre_id, genre_name, source)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (deezer_track_id) DO NOTHING`,
		arg.DeezerTrackID, arg.AlbumID, arg.GenreID, arg.GenreName, arg.Source,
	)
	return err
}

// UpdateTrackGenreToLastFm upgrades a previously "deezer"-sourced (coarse)
// row once a granular Last.fm tag becomes available — the re-resolution
// path in ensureTrackGenre.
func (q *Queries) UpdateTrackGenreToLastFm(ctx context.Context, deezerTrackID int64, genreName string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE track_genres SET genre_name = ?, genre_id = NULL, source = 'lastfm', updated_at = CURRENT_TIMESTAMP
		 WHERE deezer_track_id = ?`,
		genreName, deezerTrackID,
	)
	return err
}

// ---------------------------------------------------------- recommendations

func (q *Queries) GetRecommendation(ctx context.Context, userID, window string) (Recommendation, error) {
	row := q.db.QueryRowContext(ctx,
		`SELECT id, user_id, window, payload, computed_at FROM recommendations WHERE user_id = ? AND window = ?`,
		userID, window)
	var r Recommendation
	err := row.Scan(&r.ID, &r.UserID, &r.Window, &r.Payload, &r.ComputedAt)
	return r, err
}

type UpsertRecommendationParams struct {
	ID      string
	UserID  string
	Window  string
	Payload string
}

func (q *Queries) UpsertRecommendation(ctx context.Context, arg UpsertRecommendationParams) error {
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO recommendations (id, user_id, window, payload)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id, window) DO UPDATE SET payload = excluded.payload, computed_at = CURRENT_TIMESTAMP`,
		arg.ID, arg.UserID, arg.Window, arg.Payload,
	)
	return err
}
