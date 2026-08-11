package sync

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// pingInterval mirrors PING_INTERVAL_MS in sync/stream/route.ts.
const pingInterval = 20 * time.Second

// handleStream is GET /api/sync/stream — a direct port of the old
// server's Route Handler, adapted to Go's blocking-handler model instead
// of a ReadableStream with callbacks (the two behave the same from the
// client's side: a long-lived chunked response).
func handleStream(hub *Hub, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		deviceID := r.URL.Query().Get("deviceId")
		if deviceID == "" {
			apihttp.Error(w, http.StatusBadRequest, "deviceId manquant.")
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			apihttp.Error(w, http.StatusInternalServerError, "Le streaming n'est pas pris en charge.")
			return
		}

		// touchLastSeen — best effort, matches `.catch(() => {})`: a
		// device row that doesn't exist yet (or a transient DB hiccup)
		// must never keep the stream from opening.
		go func() {
			_ = queries.TouchDeviceLastSeen(context.Background(), db.TouchDeviceLastSeenParams{
				UserID: user.ID, DeviceID: deviceID,
			})
		}()

		rows, err := queries.ListDevicesByUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		deviceDTOs := DeviceDTOsFromRows(rows)

		connID, out, err := hub.Subscribe(r.Context(), user.ID, deviceID, deviceDTOs)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		defer hub.Unsubscribe(context.Background(), user.ID, connID)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Required: without this, an nginx reverse proxy buffers the
		// response and playback sync appears to hang (see
		// deploy/nginx-snippet.conf).
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		// Bounds the browser/OkHttp auto-reconnect delay, which otherwise
		// defaults to something UA-specific and often several seconds —
		// matters most for a dropped mobile connection coming back.
		if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
			return
		}
		flusher.Flush()

		// Tells every other one of this user's connections that this
		// device just came online — mirrors the old server calling
		// broadcastDevices right after subscribing, independent of this
		// connection's own snapshot frame (which already carries the
		// device list as it stood a moment earlier).
		hub.BroadcastDevices(r.Context(), user.ID, deviceDTOs)
		defer func() {
			if rows, err := queries.ListDevicesByUser(context.Background(), user.ID); err == nil {
				hub.BroadcastDevices(context.Background(), user.ID, DeviceDTOsFromRows(rows))
			}
		}()

		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()

		for {
			select {
			case frame, ok := <-out:
				if !ok {
					return // Hub dropped this connection (buffer overflow)
				}
				if _, err := w.Write(frame); err != nil {
					return
				}
				flusher.Flush()
			case t := <-ticker.C:
				// A real named event, not an SSE `: comment` (EventSource
				// never surfaces those), so the client can treat pings as
				// a liveness signal and force a reconnect when they stop.
				if _, err := w.Write(encodeSSE("ping", t.UnixMilli())); err != nil {
					return
				}
				flusher.Flush()
				go func() {
					_ = queries.TouchDeviceLastSeen(context.Background(), db.TouchDeviceLastSeenParams{
						UserID: user.ID, DeviceID: deviceID,
					})
				}()
			case <-r.Context().Done():
				return
			}
		}
	}
}
