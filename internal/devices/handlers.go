package devices

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

// OnlineFunc reports whether a device holds a live connection. Until
// Phase 6's sync engine exists there is no connection state to check —
// main.go passes a stub that always answers false; Phase 6 swaps in the
// real Hub without this package changing.
type OnlineFunc func(userID, deviceID string) bool

// BroadcastFunc pushes the updated device roster to a user's connected
// devices. A no-op until Phase 6; without it, a newly registered device
// only reaches already-connected peers on their next unrelated refresh —
// the "new devices don't show up until I reload" bug the old server's own
// comment on this call warns about.
type BroadcastFunc func(userID string)

// Mount registers the device routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries, isOnline OnlineFunc, broadcast BroadcastFunc) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Post("/api/devices/register", handleRegister(queries, isOnline, broadcast))
		r.Get("/api/devices", handleList(queries, isOnline))
		r.Patch("/api/devices/{deviceId}", handleRename(queries, broadcast))
		r.Delete("/api/devices/{deviceId}", handleForget(queries, broadcast))
	})
}

type registerBody struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func handleRegister(queries *db.Queries, isOnline OnlineFunc, broadcast BroadcastFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body registerBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if body.DeviceID == "" || body.Name == "" || body.Platform == "" {
			apihttp.Error(w, http.StatusBadRequest, "Paramètres manquants.")
			return
		}

		device, err := queries.UpsertDevice(r.Context(), db.UpsertDeviceParams{
			ID: idgen.New(), UserID: user.ID, DeviceID: body.DeviceID,
			Name: body.Name, Platform: body.Platform,
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		broadcast(user.ID)
		apihttp.JSON(w, http.StatusOK, map[string]any{
			"device": deviceDTO(device.DeviceID, device.Name, device.Platform, device.LastSeenAt, isOnline(user.ID, device.DeviceID)),
		})
	}
}

func handleList(queries *db.Queries, isOnline OnlineFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.ListDevicesByUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		dtos := make([]DeviceDTO, 0, len(rows))
		for _, d := range rows {
			dtos = append(dtos, deviceDTO(d.DeviceID, d.Name, d.Platform, d.LastSeenAt, isOnline(user.ID, d.DeviceID)))
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"devices": dtos})
	}
}

func handleRename(queries *db.Queries, broadcast BroadcastFunc) http.HandlerFunc {
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
			apihttp.Error(w, http.StatusBadRequest, "Nom invalide.")
			return
		}

		rows, err := queries.RenameDevice(r.Context(), db.RenameDeviceParams{
			Name: name, UserID: user.ID, DeviceID: chi.URLParam(r, "deviceId"),
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Appareil introuvable.")
			return
		}

		broadcast(user.ID)
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleForget(queries *db.Queries, broadcast BroadcastFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.DeleteDevice(r.Context(), db.DeleteDeviceParams{
			UserID: user.ID, DeviceID: chi.URLParam(r, "deviceId"),
		})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		if rows == 0 {
			apihttp.Error(w, http.StatusNotFound, "Appareil introuvable.")
			return
		}

		broadcast(user.ID)
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
