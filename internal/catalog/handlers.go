package catalog

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
)

const (
	defaultSearchLimit = 24
	maxSearchLimit     = 100
)

var deezerSearchEndpoints = map[string]string{
	"track":  "/search",
	"album":  "/search/album",
	"artist": "/search/artist",
}

// Mount registers the catalog and lyrics routes, all behind requireAuth —
// every one of these requires a session, matching the old server (each
// route calls getCurrentUser()/authenticate() itself).
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, deezer *DeezerClient, lyrics *LyricsClient) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/search", handleSearch(deezer))
		r.Get("/api/track/{id}", handleTrack(deezer))
		r.Get("/api/album/{id}", handleAlbum(deezer))
		r.Get("/api/artist/{id}", handleArtist(deezer))
		r.Get("/api/lyrics", handleLyrics(lyrics))
	})
}

func handleSearch(deezer *DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		searchType := r.URL.Query().Get("type")

		// Checked before the type, exactly like the old server: an empty
		// query short-circuits to an empty result regardless of whether
		// `type` would otherwise be valid.
		if query == "" {
			writeRawEnvelope(w, "data", json.RawMessage("[]"))
			return
		}

		endpoint, ok := deezerSearchEndpoints[searchType]
		if !ok {
			apihttp.Error(w, http.StatusBadRequest, "Type de recherche invalide.")
			return
		}

		limit := clampPositiveInt(r.URL.Query().Get("limit"), defaultSearchLimit, maxSearchLimit)
		path := endpoint + "?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
		if index := parsePositiveInt(r.URL.Query().Get("index")); index > 0 {
			path += "&index=" + strconv.Itoa(index)
		}

		data, ok := deezer.fetchDataArray(r.Context(), path)
		if !ok {
			apihttp.Error(w, http.StatusBadGateway, "Le service de recherche est indisponible.")
			return
		}
		writeRawEnvelope(w, "data", data)
	}
}

func handleTrack(deezer *DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := deezer.FetchTrack(r.Context(), chi.URLParam(r, "id"))
		if !ok {
			apihttp.Error(w, http.StatusNotFound, "Titre introuvable.")
			return
		}
		writeRaw(w, body)
	}
}

func handleAlbum(deezer *DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := deezer.FetchAlbum(r.Context(), chi.URLParam(r, "id"))
		if !ok {
			apihttp.Error(w, http.StatusNotFound, "Album introuvable.")
			return
		}
		writeRaw(w, body)
	}
}

func handleArtist(deezer *DeezerClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		artist, topTracks, ok := deezer.FetchArtistPage(r.Context(), chi.URLParam(r, "id"))
		if !ok {
			apihttp.Error(w, http.StatusNotFound, "Artiste introuvable.")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"artist":`))
		w.Write(artist)
		w.Write([]byte(`,"topTracks":`))
		w.Write(topTracks)
		w.Write([]byte(`}`))
	}
}

func handleLyrics(lyrics *LyricsClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		track := strings.TrimSpace(r.URL.Query().Get("track"))
		artist := strings.TrimSpace(r.URL.Query().Get("artist"))
		album := strings.TrimSpace(r.URL.Query().Get("album"))
		duration := strings.TrimSpace(r.URL.Query().Get("duration"))

		if track == "" || artist == "" {
			apihttp.Error(w, http.StatusBadRequest, "Paramètres manquants.")
			return
		}

		body, found := lyrics.Lookup(r.Context(), track, artist, album, duration)
		if !found {
			apihttp.Error(w, http.StatusNotFound, "Paroles introuvables.")
			return
		}
		writeRaw(w, body)
	}
}

func clampPositiveInt(raw string, fallback, max int) int {
	n := parsePositiveInt(raw)
	if n <= 0 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

func parsePositiveInt(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// writeRaw forwards a Deezer/lrclib JSON body byte-for-byte — see the
// DeezerClient doc comment for why this matters over decoding into a
// narrow Go struct and re-marshaling.
func writeRaw(w http.ResponseWriter, body json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func writeRawEnvelope(w http.ResponseWriter, key string, body json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"` + key + `":`))
	w.Write(body)
	w.Write([]byte(`}`))
}
