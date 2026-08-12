package playlists

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

// Tuning constants ported verbatim from import-deezer-playlist.ts / deezer.ts.
const (
	// lookupChunkSize: SQLite historically caps bound parameters per query
	// around 999, so the "liked" dedup lookup over a whole playlist must
	// batch well under that. Inserts don't need the same treatment here —
	// see importTracksToLiked's doc comment on why a plain per-track loop
	// inside one transaction stands in for Prisma's createMany chunking.
	lookupChunkSize = 400

	maxPlaylistTracks = 10000
	tracksPageSize    = 100
	pageRetryAttempts = 5
	pageRetryDelay    = 1200 * time.Millisecond
	pageRequestDelay  = 150 * time.Millisecond
)

// ImportDestination mirrors ImportDestination in import-deezer-playlist.ts.
type ImportDestination string

const (
	ImportDestinationPlaylist ImportDestination = "playlist"
	ImportDestinationLiked    ImportDestination = "liked"
)

// ImportResult is what the NDJSON handler's final "done" event reports.
type ImportResult struct {
	Destination ImportDestination
	PlaylistID  string // empty when Destination == liked
	TrackCount  int
}

var playlistIDRegex = regexp.MustCompile(`(?i)playlist[/=](\d+)`)

var digitsOnly = regexp.MustCompile(`^\d+$`)

// resolveDeezerPlaylistID mirrors resolveDeezerPlaylistId in deezer.ts: a
// bare id, an id embedded in a Deezer URL, or (for a share link like
// link.deezer.com/...) a HEAD request following redirects to find the
// real URL the id lives in.
func resolveDeezerPlaylistID(ctx context.Context, httpClient *http.Client, input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}
	if digitsOnly.MatchString(trimmed) {
		return trimmed, true
	}
	if m := playlistIDRegex.FindStringSubmatch(trimmed); m != nil {
		return m[1], true
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, trimmed, nil)
	if err != nil {
		return "", false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if m := playlistIDRegex.FindStringSubmatch(resp.Request.URL.String()); m != nil {
		return m[1], true
	}
	return "", false
}

type deezerPlaylistTrackJSON struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Duration int64  `json:"duration"`
	Artist   struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Title       string `json:"title"`
		CoverMedium string `json:"cover_medium"`
	} `json:"album"`
}

// fetchWithRetry is a simplification of fetchDeezerPageWithRetry in
// deezer.ts: the original distinguishes a 429/quota response (retry) from
// any other Deezer error (fail immediately) using the response status and
// error message text, neither of which DeezerClient.fetch surfaces (it
// collapses transport failure / non-2xx / Deezer error payload into one
// bool by design — see deezer.go). At this project's scale — a handful of
// self-hosted users importing playlists occasionally, not a service
// hammering Deezer's rate limit — retrying blindly on any failure is an
// acceptable trade for not having to plumb a second, more detailed fetch
// path through just for this one feature.
func fetchWithRetry(ctx context.Context, fetch func() (json.RawMessage, bool)) (json.RawMessage, bool) {
	for attempt := 0; attempt < pageRetryAttempts; attempt++ {
		if body, ok := fetch(); ok {
			return body, true
		}
		select {
		case <-time.After(pageRetryDelay):
		case <-ctx.Done():
			return nil, false
		}
	}
	return nil, false
}

// fetchDeezerPlaylist mirrors fetchDeezerPlaylist in deezer.ts: fetch the
// playlist's own metadata for its title/track count, then page through the
// dedicated /tracks collection (the playlist object's embedded `tracks` is
// capped at 400 by Deezer with no further cursor).
func fetchDeezerPlaylist(
	ctx context.Context,
	deezer *catalog.DeezerClient,
	id string,
	onProgress func(fetched, total int),
) (title string, tracks []deezerPlaylistTrackJSON, err error) {
	raw, ok := deezer.FetchPlaylist(ctx, id)
	if !ok {
		return "", nil, errors.New("Impossible de récupérer cette playlist Deezer.")
	}
	var playlist struct {
		Title    string `json:"title"`
		NbTracks int    `json:"nb_tracks"`
	}
	if err := json.Unmarshal(raw, &playlist); err != nil {
		return "", nil, errors.New("Impossible de récupérer cette playlist Deezer.")
	}

	total := playlist.NbTracks
	if total > maxPlaylistTracks {
		total = maxPlaylistTracks
	}
	if onProgress != nil {
		onProgress(0, total)
	}

	var nextURL string // empty means "fetch the first page directly"
	for len(tracks) < maxPlaylistTracks {
		select {
		case <-time.After(pageRequestDelay):
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}

		var page json.RawMessage
		var ok bool
		if nextURL == "" {
			page, ok = fetchWithRetry(ctx, func() (json.RawMessage, bool) {
				return deezer.FetchPlaylistTracksPage(ctx, id, tracksPageSize)
			})
		} else {
			url := nextURL
			page, ok = fetchWithRetry(ctx, func() (json.RawMessage, bool) {
				return deezer.FetchURL(ctx, url)
			})
		}
		if !ok {
			return "", nil, errors.New("Erreur de l'API Deezer.")
		}

		var parsed struct {
			Data []deezerPlaylistTrackJSON `json:"data"`
			Next string                    `json:"next"`
		}
		if err := json.Unmarshal(page, &parsed); err != nil {
			return "", nil, errors.New("Erreur de l'API Deezer.")
		}
		tracks = append(tracks, parsed.Data...)
		if onProgress != nil {
			fetched := len(tracks)
			if fetched > total {
				fetched = total
			}
			onProgress(fetched, total)
		}
		if parsed.Next == "" {
			break
		}
		nextURL = parsed.Next
	}

	if len(tracks) > maxPlaylistTracks {
		tracks = tracks[:maxPlaylistTracks]
	}
	return playlist.Title, tracks, nil
}

// importDeezerPlaylistForUser mirrors importDeezerPlaylistForUser in
// import-deezer-playlist.ts.
func importDeezerPlaylistForUser(
	ctx context.Context,
	sqlDB *sql.DB,
	queries *db.Queries,
	deezer *catalog.DeezerClient,
	httpClient *http.Client,
	userID, link string,
	destination ImportDestination,
	customName string,
	onProgress func(fetched, total int),
) (ImportResult, error) {
	trimmedLink := strings.TrimSpace(link)
	if trimmedLink == "" {
		return ImportResult{}, errors.New("Le lien de la playlist Deezer est requis.")
	}

	playlistID, ok := resolveDeezerPlaylistID(ctx, httpClient, trimmedLink)
	if !ok {
		return ImportResult{}, errors.New("Ce lien de playlist Deezer est invalide.")
	}

	title, tracks, err := fetchDeezerPlaylist(ctx, deezer, playlistID, onProgress)
	if err != nil {
		return ImportResult{}, err
	}

	// Deezer occasionally lists withdrawn/delisted tracks (negative ids,
	// missing cover art) inside otherwise valid playlists — they can't be
	// streamed anyway, so skip them instead of failing the whole import.
	validTracks := make([]deezerPlaylistTrackJSON, 0, len(tracks))
	for _, t := range tracks {
		if t.ID > 0 && t.Title != "" && t.Album.CoverMedium != "" {
			validTracks = append(validTracks, t)
		}
	}
	if len(validTracks) == 0 {
		return ImportResult{}, errors.New("Cette playlist Deezer est vide.")
	}

	if destination == ImportDestinationLiked {
		count, err := importTracksToLiked(ctx, sqlDB, queries, userID, validTracks)
		if err != nil {
			return ImportResult{}, err
		}
		return ImportResult{Destination: ImportDestinationLiked, TrackCount: count}, nil
	}

	name := strings.TrimSpace(customName)
	if name == "" {
		name = strings.TrimSpace(title)
	}
	if name == "" {
		name = "Playlist importée"
	}

	playlist, err := createPlaylistWithTracks(ctx, sqlDB, queries, userID, name, validTracks)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{Destination: ImportDestinationPlaylist, PlaylistID: playlist, TrackCount: len(validTracks)}, nil
}

func chunkInt64(items []int64, size int) [][]int64 {
	var chunks [][]int64
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		chunks = append(chunks, items[i:end])
	}
	return chunks
}

func artistIDOrNull(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}

// importTracksToLiked dedupes against the user's existing likes (in
// lookupChunkSize batches) and inserts the rest inside one transaction —
// SQLite writes are cheap locally, so one Go-level loop per track inside a
// single BEGIN/COMMIT stands in for Prisma's createMany batching without
// needing to hand-build a multi-row INSERT.
func importTracksToLiked(ctx context.Context, sqlDB *sql.DB, queries *db.Queries, userID string, tracks []deezerPlaylistTrackJSON) (int, error) {
	trackIDs := make([]int64, len(tracks))
	for i, t := range tracks {
		trackIDs[i] = t.ID
	}

	existing := make(map[int64]bool, len(trackIDs))
	for _, idsChunk := range chunkInt64(trackIDs, lookupChunkSize) {
		found, err := queries.ListLikedTrackIDsAmong(ctx, userID, idsChunk)
		if err != nil {
			return 0, err
		}
		for id := range found {
			existing[id] = true
		}
	}

	var newTracks []deezerPlaylistTrackJSON
	for _, t := range tracks {
		if !existing[t.ID] {
			newTracks = append(newTracks, t)
		}
	}

	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	txQueries := queries.WithTx(tx)

	for _, t := range newTracks {
		if err := txQueries.UpsertLikedTrack(ctx, db.UpsertLikedTrackParams{
			ID: idgen.New(), UserID: userID, DeezerTrackID: t.ID, Title: t.Title,
			ArtistName: t.Artist.Name, ArtistID: artistIDOrNull(t.Artist.ID),
			AlbumTitle: t.Album.Title, AlbumCover: t.Album.CoverMedium, Duration: t.Duration,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(newTracks), nil
}

// createPlaylistWithTracks creates a brand-new playlist and inserts every
// track — no dedup needed, playlist_tracks allows duplicates by design
// (see 0001_init.sql).
func createPlaylistWithTracks(ctx context.Context, sqlDB *sql.DB, queries *db.Queries, userID, name string, tracks []deezerPlaylistTrackJSON) (string, error) {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	txQueries := queries.WithTx(tx)

	playlist, err := txQueries.CreatePlaylist(ctx, db.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: name})
	if err != nil {
		return "", err
	}

	for _, t := range tracks {
		if err := txQueries.InsertPlaylistTrack(ctx, db.InsertPlaylistTrackParams{
			ID: idgen.New(), PlaylistID: playlist.ID, DeezerTrackID: t.ID, Title: t.Title,
			ArtistName: t.Artist.Name, ArtistID: artistIDOrNull(t.Artist.ID),
			AlbumTitle: t.Album.Title, AlbumCover: t.Album.CoverMedium, Duration: t.Duration,
		}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return playlist.ID, nil
}
