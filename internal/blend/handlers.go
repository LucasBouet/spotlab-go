package blend

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

const idPrefix = "blend-"

// Mount registers the Blend routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Post("/api/blends", handleCreate(queries))
		r.Get("/api/blends", handleList(queries))
		r.Delete("/api/blends/{id}", handleDelete(queries))
	})
}

// canonicalPair orders two user ids consistently regardless of who's
// asking — see blends.sql.go's Blend doc comment for why.
func canonicalPair(a, b string) (string, string) {
	if a < b {
		return a, b
	}
	return b, a
}

type createRequest struct {
	FriendUserID string `json:"friendUserId"`
}

// handleCreate is POST /api/blends {"friendUserId"}: creates the blend if
// it doesn't exist yet, or just returns the existing one — idempotent from
// the client's point of view, so "create a Blend with X" never has to
// check first.
func handleCreate(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		var body createRequest
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if body.FriendUserID == "" || body.FriendUserID == user.ID {
			apihttp.Error(w, http.StatusBadRequest, "Ami invalide.")
			return
		}

		friendship, err := queries.FindFriendshipBetween(r.Context(), db.FindFriendshipBetweenParams{
			UserA: user.ID, UserB: body.FriendUserID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			apihttp.Error(w, http.StatusForbidden, "Vous devez être amis pour créer un Blend.")
			return
		}
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if friendship.Status != "ACCEPTED" {
			apihttp.Error(w, http.StatusForbidden, "Vous devez être amis pour créer un Blend.")
			return
		}

		userA, userB := canonicalPair(user.ID, body.FriendUserID)
		status := http.StatusOK
		row, err := queries.FindBlendByUsers(r.Context(), userA, userB)
		if errors.Is(err, sql.ErrNoRows) {
			row, err = queries.CreateBlend(r.Context(), db.CreateBlendParams{ID: idgen.New(), UserAID: userA, UserBID: userB})
			status = http.StatusCreated
		}
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		dto, err := buildBlendDTO(r.Context(), queries, row, user.ID, false)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, status, dto)
	}
}

// handleList is GET /api/blends (add &refresh=1 to force today's mix to
// rebuild) — every Blend the caller is a member of, full track list
// included, same "everything up front, no separate detail fetch" shape as
// GET /api/smart-playlists.
func handleList(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		forceRefresh := r.URL.Query().Get("refresh") == "1"

		rows, err := queries.ListBlendsForUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		out := make([]BlendDTO, 0, len(rows))
		for _, row := range rows {
			dto, err := buildBlendDTO(r.Context(), queries, row, user.ID, forceRefresh)
			if err != nil {
				apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
				return
			}
			out = append(out, dto)
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"blends": out})
	}
}

// handleDelete is DELETE /api/blends/{id} — either member can end a Blend,
// not just whoever created it.
func handleDelete(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		id := strings.TrimPrefix(chi.URLParam(r, "id"), idPrefix)

		rows, err := queries.DeleteBlendOwned(r.Context(), id, user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Blend introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
