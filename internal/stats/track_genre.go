package stats

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// deezerGenreResult is the coarse fallback: track -> album.id -> album's
// first genre. Mirrors fetchDeezerGenre in track-genre.ts, including its
// odd-looking contract — a non-nil result with an empty GenreName is a
// legitimate outcome (the album resolved but Deezer listed no genre for
// it), still worth caching so the same track doesn't re-trigger two
// Deezer round-trips on every subsequent play.
type deezerGenreResult struct {
	AlbumID   int64
	GenreID   int64
	GenreName string
}

func fetchDeezerGenre(ctx context.Context, deezer *catalog.DeezerClient, deezerTrackID int64) *deezerGenreResult {
	trackRaw, ok := deezer.FetchTrack(ctx, strconv.FormatInt(deezerTrackID, 10))
	if !ok {
		return nil
	}
	var track struct {
		Album struct {
			ID int64 `json:"id"`
		} `json:"album"`
	}
	if err := json.Unmarshal(trackRaw, &track); err != nil || track.Album.ID == 0 {
		return nil
	}

	albumRaw, ok := deezer.FetchAlbum(ctx, strconv.FormatInt(track.Album.ID, 10))
	if !ok {
		return nil
	}
	var album struct {
		Genres struct {
			Data []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		} `json:"genres"`
	}
	if err := json.Unmarshal(albumRaw, &album); err != nil {
		return nil
	}

	result := &deezerGenreResult{AlbumID: track.Album.ID}
	if len(album.Genres.Data) > 0 {
		result.GenreID = album.Genres.Data[0].ID
		result.GenreName = album.Genres.Data[0].Name
	}
	return result
}

// ensureTrackGenre resolves and caches a track's genre (shared across
// users) the first time it's played, preferring Last.fm's granular tags
// and falling back to Deezer's coarse album genre — a direct port of
// ensureTrackGenre in track-genre.ts. Best-effort throughout: any failure
// (network, DB) simply leaves nothing written, so a later play retries;
// callers run this fire-and-forget off the request path (see handlers.go).
func ensureTrackGenre(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, lastfm *lastFMClient, deezerTrackID int64, artistName string) {
	existing, err := queries.GetTrackGenre(ctx, deezerTrackID)
	if err == nil {
		// Already granular, or we have no better source available — leave it.
		if existing.Source.String == "lastfm" || !lastfm.hasKey() {
			return
		}
		if upgraded := lastfm.fetchGenre(ctx, artistName); upgraded != "" {
			_ = queries.UpdateTrackGenreToLastFm(ctx, deezerTrackID, upgraded)
		}
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return
	}

	if genre := lastfm.fetchGenre(ctx, artistName); genre != "" {
		_ = queries.InsertTrackGenre(ctx, db.InsertTrackGenreParams{
			DeezerTrackID: deezerTrackID,
			GenreName:     sql.NullString{String: genre, Valid: true},
			Source:        "lastfm",
		})
		return
	}

	deezerGenre := fetchDeezerGenre(ctx, deezer, deezerTrackID)
	if deezerGenre == nil {
		return // both sources unreachable → retry on a later play
	}
	params := db.InsertTrackGenreParams{
		DeezerTrackID: deezerTrackID,
		AlbumID:       sql.NullInt64{Int64: deezerGenre.AlbumID, Valid: deezerGenre.AlbumID != 0},
		Source:        "deezer",
	}
	if deezerGenre.GenreName != "" {
		params.GenreID = sql.NullInt64{Int64: deezerGenre.GenreID, Valid: true}
		params.GenreName = sql.NullString{String: deezerGenre.GenreName, Valid: true}
	}
	_ = queries.InsertTrackGenre(ctx, params)
}

// ensureTrackReleaseYear resolves and caches a track's release year the
// first time it's played — the decade-playlist equivalent of
// ensureTrackGenre above, global cache, same lazy/best-effort shape.
// Deezer's track object carries release_date directly (confirmed against
// the real API: e.g. `/track/3135556` -> "release_date":"2001-03-12"), so
// this is one Deezer round-trip, no album fetch needed.
func ensureTrackReleaseYear(ctx context.Context, queries *db.Queries, deezer *catalog.DeezerClient, deezerTrackID int64) {
	if _, err := queries.GetTrackReleaseYear(ctx, deezerTrackID); err == nil {
		return // already resolved
	} else if !errors.Is(err, sql.ErrNoRows) {
		return
	}

	raw, ok := deezer.FetchTrack(ctx, strconv.FormatInt(deezerTrackID, 10))
	if !ok {
		return
	}
	var track struct {
		ReleaseDate string `json:"release_date"`
		Album       struct {
			ReleaseDate string `json:"release_date"`
		} `json:"album"`
	}
	if err := json.Unmarshal(raw, &track); err != nil {
		return
	}
	dateStr := track.ReleaseDate
	if dateStr == "" {
		dateStr = track.Album.ReleaseDate
	}
	if len(dateStr) < 4 {
		return
	}
	year, err := strconv.Atoi(dateStr[:4])
	if err != nil || year < 1900 || year > time.Now().Year()+1 {
		return // implausible value — leave unresolved rather than cache garbage
	}

	_ = queries.InsertTrackReleaseYear(ctx, db.InsertTrackReleaseYearParams{
		DeezerTrackID: deezerTrackID, Year: int64(year),
	})
}
