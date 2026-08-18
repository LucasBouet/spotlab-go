// Package stats serves the listening-history endpoints: logging a
// qualified play, the "Statistiques" tab aggregates, and the homepage
// recommendations build. Direct port of stats.ts/recommendations.ts/
// track-genre.ts (docs/PLAN.md Phase 7).
package stats

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

const topLimit = 10

// Mount registers the stats routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries, deezer *catalog.DeezerClient, lastFMAPIKey string) {
	lastfm := newLastFMClient(lastFMAPIKey)
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Post("/api/plays", handlePostPlay(queries, deezer, lastfm))
		r.Get("/api/stats", handleGetStats(queries))
		r.Get("/api/recommendations", handleGetRecommendations(queries, deezer))
		r.Get("/api/smart-playlists", handleGetSmartPlaylists(queries, deezer))
		r.Get("/api/smart-playlists/artist/{artistId}", handleGetArtistPlaylists(deezer))
		r.Get("/api/smart-playlists/search", handleGetSearchedPlaylist(deezer))
	})
}

type postPlayRequest struct {
	DeezerTrackID int64  `json:"deezerTrackId"`
	Title         string `json:"title"`
	ArtistName    string `json:"artistName"`
	AlbumTitle    string `json:"albumTitle"`
	AlbumCover    string `json:"albumCover"`
	Duration      int64  `json:"duration"`
}

// validPlayRequest mirrors the POST /api/plays validation in the old
// server: a positive track id, a non-empty title, and a positive duration.
func validPlayRequest(body postPlayRequest) bool {
	return body.DeezerTrackID > 0 && body.Title != "" && body.Duration > 0
}

// handlePostPlay is POST /api/plays: logs one *qualified* play (the client
// only calls this once a track has been heard past its scrobble threshold)
// and kicks off genre resolution in the background — never make the
// player's fire-and-forget beacon wait on the Last.fm/Deezer round-trips.
func handlePostPlay(queries *db.Queries, deezer *catalog.DeezerClient, lastfm *lastFMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		var body postPlayRequest
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if !validPlayRequest(body) {
			apihttp.Error(w, http.StatusBadRequest, "Paramètres invalides.")
			return
		}

		if err := queries.InsertPlayEvent(r.Context(), db.InsertPlayEventParams{
			ID: idgen.New(), UserID: user.ID, DeezerTrackID: body.DeezerTrackID,
			Title: body.Title, ArtistName: body.ArtistName,
			AlbumTitle: body.AlbumTitle, AlbumCover: body.AlbumCover, Duration: body.Duration,
		}); err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}

		// Resolved on a background context (not r.Context(), which is
		// cancelled the moment this handler returns) so the several-second
		// Last.fm/Deezer round-trips never delay the response, exactly like
		// the old server's `void ensureTrackGenre(...).catch(() => {})`.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ensureTrackGenre(ctx, queries, deezer, lastfm, body.DeezerTrackID, body.ArtistName)
			ensureTrackReleaseYear(ctx, queries, deezer, body.DeezerTrackID)
		}()

		apihttp.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleGetStats is GET /api/stats: the settings "Statistiques" tab payload.
func handleGetStats(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		stats, err := getListeningStats(r.Context(), queries, user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		apihttp.JSON(w, http.StatusOK, stats)
	}
}

func getListeningStats(ctx context.Context, queries *db.Queries, userID string) (ListeningStatsDTO, error) {
	totals, err := queries.GetPlayTotals(ctx, userID)
	if err != nil {
		return ListeningStatsDTO{}, err
	}
	topTracks, err := queries.TopTracks(ctx, userID, topLimit)
	if err != nil {
		return ListeningStatsDTO{}, err
	}
	topAlbums, err := queries.TopAlbums(ctx, userID, topLimit)
	if err != nil {
		return ListeningStatsDTO{}, err
	}
	topGenres, err := queries.TopGenres(ctx, userID, topLimit)
	if err != nil {
		return ListeningStatsDTO{}, err
	}

	stats := ListeningStatsDTO{
		TotalPlays:   totals.Count,
		TotalSeconds: totals.TotalSeconds,
		TopTracks:    make([]StatEntryDTO, len(topTracks)),
		TopAlbums:    make([]StatEntryDTO, len(topAlbums)),
		TopGenres:    make([]StatEntryDTO, len(topGenres)),
	}
	for i, t := range topTracks {
		stats.TopTracks[i] = StatEntryDTO{Label: t.Title, Sublabel: t.ArtistName, Cover: t.AlbumCover, Count: t.Count}
	}
	for i, a := range topAlbums {
		stats.TopAlbums[i] = StatEntryDTO{Label: a.AlbumTitle, Sublabel: a.ArtistName, Cover: a.AlbumCover, Count: a.Count}
	}
	for i, g := range topGenres {
		stats.TopGenres[i] = StatEntryDTO{Label: g.GenreName, Count: g.Count}
	}
	return stats, nil
}

// handleGetRecommendations is GET /api/recommendations?window=day|week|all
// (add &refresh=1 to bypass the 6h cache).
func handleGetRecommendations(queries *db.Queries, deezer *catalog.DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		window, ok := parseWindow(r.URL.Query().Get("window"))
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Période invalide.")
			return
		}
		forceRefresh := r.URL.Query().Get("refresh") == "1"

		result, err := getRecommendations(r.Context(), queries, deezer, user.ID, window, forceRefresh)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		apihttp.JSON(w, http.StatusOK, result)
	}
}

// handleGetSmartPlaylists is GET /api/smart-playlists (add &refresh=1 to
// bypass the 24h cache) — the "100% <Artist>", "<Artist> Radio", genre and
// decade playlists shown on the home screen.
func handleGetSmartPlaylists(queries *db.Queries, deezer *catalog.DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		forceRefresh := r.URL.Query().Get("refresh") == "1"

		result, err := getSmartPlaylists(r.Context(), queries, deezer, user.ID, forceRefresh)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		apihttp.JSON(w, http.StatusOK, result)
	}
}

// handleGetArtistPlaylists is GET /api/smart-playlists/artist/{artistId}
// ?name=<artist name> — the on-demand counterpart to the artist half of
// GET /api/smart-playlists: search (any artist Deezer resolves, not just
// this account's own top-listened ones) calls this to build the "100% X" /
// "X Radio" pair for whichever artist the user just searched, so a custom
// playlist search isn't limited to what already happens to be on the home
// shelf. [name] comes from the client rather than a second Deezer lookup —
// it already has it from the artist search result that led here, and using
// exactly what the user saw avoids a redundant round trip.
func handleGetArtistPlaylists(deezer *catalog.DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		artistID, err := strconv.ParseInt(chi.URLParam(r, "artistId"), 10, 64)
		if err != nil || artistID <= 0 {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant d'artiste invalide.")
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			apihttp.Error(w, http.StatusBadRequest, "Nom d'artiste requis.")
			return
		}

		playlists := buildArtistPlaylists(r.Context(), deezer, artistRef{ID: artistID, Name: name})
		if playlists == nil {
			playlists = []SmartPlaylistDTO{}
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"playlists": playlists})
	}
}

// handleGetSearchedPlaylist is GET /api/smart-playlists/search?q=<query> —
// the genre/mood/style counterpart to handleGetArtistPlaylists above: a
// search query that isn't (or isn't only) an artist name still deserves a
// real playlist result, via Deezer's own curated playlist search rather
// than an artist-name coincidence. See buildSearchedPlaylist for why this
// exists. Zero matches is a valid answer (empty array, 200), not an error —
// the client merges this into the same search results an artist-name match
// already produces, and most queries won't hit both.
func handleGetSearchedPlaylist(deezer *catalog.DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			apihttp.Error(w, http.StatusBadRequest, "Requête manquante.")
			return
		}

		var playlists []SmartPlaylistDTO
		if playlist := buildSearchedPlaylist(r.Context(), deezer, query); playlist != nil {
			playlists = append(playlists, *playlist)
		}
		if playlists == nil {
			playlists = []SmartPlaylistDTO{}
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"playlists": playlists})
	}
}
