package playlists

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

type importBody struct {
	Link        string `json:"link"`
	Destination string `json:"destination"`
	Name        string `json:"name"`
}

// handleImport is POST /api/playlists/import — mirrors
// src/app/api/playlists/import/route.ts: an NDJSON stream of `progress`
// events (as Deezer pages come in) followed by exactly one `done` or
// `error` event. Chosen over a single JSON response because a full
// playlist import is dozens of throttled Deezer round-trips (docs/PLAN.md
// scope note) — without progress, a large playlist would look hung.
func handleImport(sqlDB *sql.DB, queries *db.Queries, deezer *catalog.DeezerClient, httpClient *http.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())

		var body importBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		destination := ImportDestinationPlaylist
		if body.Destination == "liked" {
			destination = ImportDestinationLiked
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			apihttp.Error(w, http.StatusInternalServerError, "Le streaming n'est pas pris en charge.")
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		send := func(event map[string]any) {
			line, err := json.Marshal(event)
			if err != nil {
				return
			}
			_, _ = w.Write(append(line, '\n'))
			flusher.Flush()
		}

		result, err := importDeezerPlaylistForUser(
			r.Context(), sqlDB, queries, deezer, httpClient,
			user.ID, body.Link, destination, body.Name,
			func(fetched, total int) {
				send(map[string]any{"type": "progress", "fetched": fetched, "total": total})
			},
		)
		if err != nil {
			send(map[string]any{"type": "error", "message": err.Error()})
			return
		}

		done := map[string]any{
			"type":        "done",
			"destination": string(result.Destination),
			"trackCount":  result.TrackCount,
		}
		if result.PlaylistID != "" {
			done["playlistId"] = result.PlaylistID
		}
		send(done)
	}
}

// newImportHTTPClient backs resolveDeezerPlaylistID's HEAD-follow-redirect
// probe for share links (e.g. link.deezer.com/...) that don't embed the
// playlist id directly in the URL.
func newImportHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}
