package sync

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// Mount registers the sync (SSE + command) and jam routes, all behind
// requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, hub *Hub, queries *db.Queries) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/sync/stream", handleStream(hub, queries))
		r.Post("/api/sync/command", handleCommand(hub, queries))
		r.Post("/api/jam", handleJam(hub, queries))
	})
}

func handleCommand(hub *Hub, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body SyncCommandDTO
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if body.DeviceID == "" || body.Action.Type == "" {
			apihttp.Error(w, http.StatusBadRequest, "Requête invalide.")
			return
		}

		if body.Action.Type == "SET_ACTIVE_DEVICES" {
			owned, err := queries.CountOwnedDevices(r.Context(), user.ID, body.Action.DeviceIDs)
			if err != nil {
				apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
				return
			}
			if owned != len(body.Action.DeviceIDs) {
				apihttp.Error(w, http.StatusBadRequest, "Appareil inconnu.")
				return
			}
		}

		state, err := hub.ApplyCommand(r.Context(), user.ID, body.DeviceID, body.Action)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, SyncCommandResultDTO{OK: true, Revision: state.Revision})
	}
}
