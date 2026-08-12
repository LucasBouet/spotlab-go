package playlists

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

var trackIDPattern = regexp.MustCompile(`^\d+$`)

// parsePositiveTrackID mirrors the old server's exact check for
// ?trackId= — a bare positive integer string, rejecting an absent param
// the same way it rejects a malformed one (the regex fails to match "").
func parsePositiveTrackID(raw string) (int64, bool) {
	if !trackIDPattern.MatchString(raw) {
		return 0, false
	}
	var id int64
	for _, c := range raw {
		id = id*10 + int64(c-'0')
	}
	return id, true
}

// Mount registers every playlist route, all behind requireAuth. sqlDB and
// deezer are only needed for the Deezer playlist import — every other
// route only ever touches queries.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, sqlDB *sql.DB, queries *db.Queries, deezer *catalog.DeezerClient) {
	importClient := newImportHTTPClient()
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/playlists", handleList(queries))
		r.Post("/api/playlists", handleCreate(queries))
		r.Get("/api/playlists/membership", handleMembership(queries))
		r.Post("/api/playlists/import", handleImport(sqlDB, queries, deezer, importClient))
		r.Get("/api/playlists/{id}", handleDetail(queries))
		r.Patch("/api/playlists/{id}", handleRename(queries))
		r.Delete("/api/playlists/{id}", handleDelete(queries))
		r.Post("/api/playlists/{id}/tracks", handleAddTrack(queries))
		r.Delete("/api/playlists/{id}/tracks/{rowKey}", handleRemoveTrack(queries))
	})
}

func handleList(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.ListPlaylistsByUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"playlists": playlistSummaryDTOs(rows)})
	}
}

func handleCreate(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body struct {
			Name string `json:"name"`
		}
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			apihttp.Error(w, http.StatusBadRequest, "Le nom de la playlist est requis.")
			return
		}

		playlist, err := queries.CreatePlaylist(r.Context(), db.CreatePlaylistParams{
			ID: idgen.New(), UserID: user.ID, Name: name,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusCreated, map[string]any{"playlist": playlistRefDTO(playlist)})
	}
}

func handleDetail(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		playlistID := chi.URLParam(r, "id")

		playlist, err := queries.GetPlaylistOwned(r.Context(), db.GetPlaylistOwnedParams{
			ID: playlistID, UserID: user.ID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.Error(w, http.StatusNotFound, "Playlist introuvable.")
			return
		}
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		tracks, err := queries.ListPlaylistTracks(r.Context(), playlistID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, playlistDetailDTO(playlist, tracks))
	}
}

func handleRename(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body struct {
			Name string `json:"name"`
		}
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			apihttp.Error(w, http.StatusBadRequest, "Le nom de la playlist est requis.")
			return
		}

		rows, err := queries.RenamePlaylist(r.Context(), db.RenamePlaylistParams{
			Name: name, ID: chi.URLParam(r, "id"), UserID: user.ID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Playlist introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleDelete(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.DeletePlaylist(r.Context(), db.DeletePlaylistParams{
			ID: chi.URLParam(r, "id"), UserID: user.ID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Playlist introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

type trackInputBody struct {
	DeezerTrackID int64  `json:"deezerTrackId"`
	Title         string `json:"title"`
	ArtistName    string `json:"artistName"`
	ArtistID      *int64 `json:"artistId"`
	AlbumTitle    string `json:"albumTitle"`
	AlbumCover    string `json:"albumCover"`
	Duration      int64  `json:"duration"`
	Toggle        *bool  `json:"toggle"`
}

// handleAddTrack adds a track to a playlist, or — with `"toggle": true` in
// the body — removes it if already present instead. Both paths check
// ownership first so a playlist id that exists but belongs to someone else
// answers 404, never a permission error that would confirm its existence.
func handleAddTrack(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		playlistID := chi.URLParam(r, "id")

		var body trackInputBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if body.DeezerTrackID <= 0 || body.Title == "" {
			apihttp.Error(w, http.StatusBadRequest, "Paramètres manquants.")
			return
		}

		if _, err := queries.GetPlaylistOwned(r.Context(), db.GetPlaylistOwnedParams{
			ID: playlistID, UserID: user.ID,
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				apihttp.Error(w, http.StatusNotFound, "Playlist introuvable.")
				return
			}
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		artistID := sql.NullInt64{}
		if body.ArtistID != nil {
			artistID = sql.NullInt64{Int64: *body.ArtistID, Valid: true}
		}

		if body.Toggle != nil && *body.Toggle {
			existing, err := queries.FindFirstPlaylistTrackByDeezerID(r.Context(), db.FindFirstPlaylistTrackByDeezerIDParams{
				PlaylistID: playlistID, DeezerTrackID: body.DeezerTrackID,
			})
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if err := queries.InsertPlaylistTrack(r.Context(), db.InsertPlaylistTrackParams{
					ID: idgen.New(), PlaylistID: playlistID, DeezerTrackID: body.DeezerTrackID,
					Title: body.Title, ArtistName: body.ArtistName, ArtistID: artistID,
					AlbumTitle: body.AlbumTitle, AlbumCover: body.AlbumCover, Duration: body.Duration,
				}); err != nil {
					apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
					return
				}
				apihttp.JSON(w, http.StatusOK, map[string]any{"added": true})
			case err != nil:
				apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			default:
				// Removes only this one occurrence — a playlist may hold
				// the same track more than once, and toggle-off only ever
				// takes back the last toggle-on, matching the old server's
				// findFirst-then-delete exactly.
				if err := queries.DeletePlaylistTrackByID(r.Context(), existing.ID); err != nil {
					apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
					return
				}
				apihttp.JSON(w, http.StatusOK, map[string]any{"added": false})
			}
			return
		}

		if err := queries.InsertPlaylistTrack(r.Context(), db.InsertPlaylistTrackParams{
			ID: idgen.New(), PlaylistID: playlistID, DeezerTrackID: body.DeezerTrackID,
			Title: body.Title, ArtistName: body.ArtistName, ArtistID: artistID,
			AlbumTitle: body.AlbumTitle, AlbumCover: body.AlbumCover, Duration: body.Duration,
		}); err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusCreated, map[string]any{"added": true})
	}
}

// handleRemoveTrack is scoped by playlist ownership, not by the `{id}` path
// segment at all — see DeletePlaylistTrackOwned's doc comment, this is a
// faithful port of a real quirk in the old server, not an oversight here.
func handleRemoveTrack(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.DeletePlaylistTrackOwned(r.Context(), db.DeletePlaylistTrackOwnedParams{
			ID: chi.URLParam(r, "rowKey"), UserID: user.ID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Titre introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleMembership(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		raw := r.URL.Query().Get("trackId")
		trackID, ok := parsePositiveTrackID(raw)
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Paramètre trackId invalide.")
			return
		}

		rows, err := queries.ListPlaylistMembership(r.Context(), db.ListPlaylistMembershipParams{
			DeezerTrackID: trackID, UserID: user.ID,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"playlists": playlistMembershipDTOs(rows)})
	}
}
