package library

import (
	"database/sql"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

// Mount registers the library routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/library/tracks", handleListTracks(queries))
		r.Get("/api/library/likes", handleListLikedIDs(queries))
		r.Get("/api/library/likes/{trackId}", handleGetLiked(queries))
		r.Put("/api/library/likes/{trackId}", handlePutLiked(queries))
		r.Delete("/api/library/likes/{trackId}", handleDeleteLiked(queries))
	})
}

var trackIDPattern = regexp.MustCompile(`^\d+$`)

// parseTrackID mirrors the old server's parseTrackId: only a bare positive
// integer string is accepted, matching the URL segment exactly — no sign,
// no leading zeros edge case beyond what the regex already accepts (SQLite
// itself is fine converting "007" to 7, and the old server has the same
// leniency since Number("007") === 7).
func parseTrackID(raw string) (int64, bool) {
	if !trackIDPattern.MatchString(raw) {
		return 0, false
	}
	var id int64
	for _, c := range raw {
		id = id*10 + int64(c-'0')
	}
	return id, true
}

func handleListTracks(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		// `sort` overrides the saved library_sort_order preference for this
		// call only. No settings module exists yet (out of v1 scope — see
		// docs/PLAN.md §8), so the fallback is the same hardcoded "recent"
		// default USER_SETTINGS itself declares; nothing can currently make
		// it anything else.
		var (
			tracks []db.LikedTrack
			err    error
		)
		switch r.URL.Query().Get("sort") {
		case "title":
			tracks, err = queries.ListLikedTracksByTitle(r.Context(), user.ID)
		case "artist":
			tracks, err = queries.ListLikedTracksByArtist(r.Context(), user.ID)
		default:
			tracks, err = queries.ListLikedTracksRecent(r.Context(), user.ID)
		}
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"tracks": likedTrackDTOs(tracks)})
	}
}

func handleListLikedIDs(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		ids, err := queries.ListLikedTrackIDs(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if ids == nil {
			ids = []int64{}
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ids": ids})
	}
}

func handleGetLiked(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		trackID, ok := parseTrackID(chi.URLParam(r, "trackId"))
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}
		liked, err := queries.IsTrackLiked(r.Context(), db.IsTrackLikedParams{
			UserID: user.ID, DeezerTrackID: trackID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"liked": liked})
	}
}

type putLikeBody struct {
	Title      string `json:"title"`
	ArtistName string `json:"artistName"`
	ArtistID   *int64 `json:"artistId"`
	AlbumTitle string `json:"albumTitle"`
	AlbumCover string `json:"albumCover"`
	Duration   int64  `json:"duration"`
}

// handlePutLiked stores a denormalized copy of the track, so the body
// carries the metadata the client already has on screen rather than
// costing a Deezer round-trip here. Idempotent: liking twice is a no-op
// (UpsertLikedTrack's ON CONFLICT DO NOTHING), matching the old server.
func handlePutLiked(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		trackID, ok := parseTrackID(chi.URLParam(r, "trackId"))
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}

		var body putLikeBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		// Deezer occasionally returns delisted/withdrawn tracks (missing
		// cover art or title) from search/album/artist. Storing one would
		// later crash the web client rendering it back (empty image src) —
		// rejected here the same way the old server rejects it.
		if body.Title == "" || body.AlbumCover == "" {
			apihttp.Error(w, http.StatusBadRequest, "Ce titre n'est pas disponible.")
			return
		}
		if trackID <= 0 {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant de titre invalide.")
			return
		}

		artistID := sql.NullInt64{}
		if body.ArtistID != nil {
			artistID = sql.NullInt64{Int64: *body.ArtistID, Valid: true}
		}

		err := queries.UpsertLikedTrack(r.Context(), db.UpsertLikedTrackParams{
			ID:            idgen.New(),
			UserID:        user.ID,
			DeezerTrackID: trackID,
			Title:         body.Title,
			ArtistName:    body.ArtistName,
			ArtistID:      artistID,
			AlbumTitle:    body.AlbumTitle,
			AlbumCover:    body.AlbumCover,
			Duration:      body.Duration,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"liked": true})
	}
}

func handleDeleteLiked(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		trackID, ok := parseTrackID(chi.URLParam(r, "trackId"))
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}
		err := queries.DeleteLikedTrack(r.Context(), db.DeleteLikedTrackParams{
			UserID: user.ID, DeezerTrackID: trackID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"liked": false})
	}
}
