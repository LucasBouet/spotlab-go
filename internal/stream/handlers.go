package stream

import (
	"net/http"
	"os"
	"os/exec"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
)

var trackIDPattern = regexp.MustCompile(`^\d+$`)

// Mount registers the audio routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, manager *Manager) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/stream/{id}", handleStream(manager))
		r.Post("/api/prefetch/{id}", handlePrefetch(manager))
		r.Get("/api/download/{id}", handleDownload(manager))
	})
}

// handleStream is GET /api/stream/{id} — the one endpoint that returns
// audio. Mirrors src/app/api/stream/[id]/route.ts: a cached track gets
// full Range support (200/206, Accept-Ranges: bytes); an uncached one
// streams bytes out of yt-dlp as they arrive (200, Accept-Ranges: none,
// not seekable until the next request hits the cached path).
func handleStream(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !trackIDPattern.MatchString(id) {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}

		result, err := manager.OpenStream(id)
		if err != nil {
			apihttp.Error(w, http.StatusBadGateway, streamErrorMessage(err))
			return
		}

		if result.Cached {
			serveCachedFile(w, r, result.FilePath)
			return
		}
		defer result.Reader.Close()
		serveLiveStream(w, result)
	}
}

func serveCachedFile(w http.ResponseWriter, r *http.Request, filePath string) {
	f, err := os.Open(filePath)
	if err != nil {
		apihttp.Error(w, http.StatusNotFound, "Fichier introuvable.")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		apihttp.Error(w, http.StatusNotFound, "Fichier introuvable.")
		return
	}
	w.Header().Set("Content-Type", contentTypeForPath(filePath))
	w.Header().Set("Cache-Control", "no-store")
	// http.ServeContent gives full Range/206/If-Range/Accept-Ranges
	// handling for free — the Go equivalent of the ~25 lines of manual
	// Range-header parsing in the old server's route handler.
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func serveLiveStream(w http.ResponseWriter, result StreamResult) {
	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	for {
		n, err := result.Reader.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return // client disconnected
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			// io.EOF (download finished) or the download failing mid-tail —
			// either way nothing more will come; the old server has the
			// same limitation once headers are already sent.
			return
		}
	}
}

// handlePrefetch is POST /api/prefetch/{id}: forces a track to be
// downloaded and cached, resolving only once caching completes — mirrors
// src/app/api/prefetch/[id]/route.ts.
func handlePrefetch(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !trackIDPattern.MatchString(id) {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}
		if _, err := manager.ResolveFile(id); err != nil {
			apihttp.Error(w, http.StatusBadGateway, streamErrorMessage(err))
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]bool{"success": true})
	}
}

// handleDownload is GET /api/download/{id} — "save to device": the same
// cached container yt-dlp gave us (webm/m4a/whatever), transcoded to MP3
// 192k on the fly for maximum player compatibility. Mirrors
// src/app/api/download/[id]/route.ts. Unlike the yt-dlp pipeline, this
// transcode has exactly one consumer (this response), so it's fine — and
// correct — to tie the ffmpeg process to r.Context(): a client that
// disconnects should kill it, not leave it running for nobody.
func handleDownload(manager *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if !trackIDPattern.MatchString(id) {
			apihttp.Error(w, http.StatusBadRequest, "Identifiant invalide.")
			return
		}

		filePath, err := manager.ResolveFile(id)
		if err != nil {
			apihttp.Error(w, http.StatusBadGateway, streamErrorMessage(err))
			return
		}

		ffmpegPath := manager.ffmpegPath
		if ffmpegPath == "" {
			ffmpegPath = "ffmpeg"
		}
		cmd := exec.CommandContext(r.Context(), ffmpegPath,
			"-i", filePath,
			"-vn",
			"-acodec", "libmp3lame",
			"-b:a", "192k",
			"-f", "mp3",
			"pipe:1",
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		if err := cmd.Start(); err != nil {
			apihttp.Error(w, http.StatusBadGateway, "Le transcodage du titre a échoué.")
			return
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 64*1024)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					break // client disconnected — r.Context() cancellation stops ffmpeg below
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr != nil {
				break
			}
		}
		_ = cmd.Wait()
	}
}

func streamErrorMessage(err error) string {
	if err == nil {
		return "Le téléchargement du titre a échoué."
	}
	return err.Error()
}
