// Package stats serves the listening-history endpoints: logging a
// qualified play, the "Statistiques" tab aggregates, and the homepage
// recommendations build. Direct port of stats.ts/recommendations.ts/
// track-genre.ts (docs/PLAN.md Phase 7).
package stats

import (
	"context"
	"net/http"
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
