// Package shelf stores the manual pin/order preferences for the home
// screen's "Vos playlists" shelf — GET/PUT /api/shelf-order. The shelf
// itself (smart playlists + Blends) is assembled client-side from two
// other endpoints (/api/smart-playlists, /api/blends); this package only
// ever sees opaque item id strings, never the items themselves.
package shelf

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// Mount registers the shelf-order routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/shelf-order", handleGet(queries))
		r.Put("/api/shelf-order", handlePut(queries))
	})
}

type shelfOrderDTO struct {
	Positions map[string]int64 `json:"positions"`
	Pinned    []string         `json:"pinned"`
}

func handleGet(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.ListShelfPrefs(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		out := shelfOrderDTO{Positions: map[string]int64{}, Pinned: []string{}}
		for _, row := range rows {
			out.Positions[row.ItemID] = row.Position
			if row.Pinned {
				out.Pinned = append(out.Pinned, row.ItemID)
			}
		}
		apihttp.JSON(w, http.StatusOK, out)
	}
}

// handlePut replaces the caller's whole arrangement in one call — same
// "client always sends its complete current state" contract as
// PUT /api/playlists/order, and for the same reason: simpler than
// incremental move operations, and self-correcting if a previous request
// only partially applied. Every id mentioned by either field (position or
// pin) gets a row; an id pinned without an explicit position defaults to 0.
func handlePut(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		var body shelfOrderDTO
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		pinned := make(map[string]bool, len(body.Pinned))
		for _, id := range body.Pinned {
			pinned[id] = true
		}
		ids := make(map[string]bool, len(body.Positions)+len(body.Pinned))
		for id := range body.Positions {
			ids[id] = true
		}
		for id := range pinned {
			ids[id] = true
		}

		for id := range ids {
			if err := queries.UpsertShelfPref(r.Context(), db.UpsertShelfPrefParams{
				UserID: user.ID, ItemID: id, Pinned: pinned[id], Position: body.Positions[id],
			}); err != nil {
				apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
				return
			}
		}
		apihttp.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
