package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

func newSearchedPlaylistRouter(deezer *catalog.DeezerClient) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/smart-playlists/search", handleGetSearchedPlaylist(deezer))
	return r
}

// TestBuildSearchedPlaylistPicksFirstPublicMatchWithEnoughTracks is the
// regression this whole endpoint exists to fix: search used to build a
// "playlist" by matching the query against an *artist's* name, so
// "metalcore" could surface a "100% Metalcore" pair built from some
// obscure, unrelated artist who happens to share that name. This searches
// Deezer's own curated playlists instead, and must skip past a private or
// too-small stub to find the real editorial compilation.
func TestBuildSearchedPlaylistPicksFirstPublicMatchWithEnoughTracks(t *testing.T) {
	responses := map[string]string{
		"/search/playlist": `{"data":[
			{"id":1,"title":"Metalcore (private stub)","public":false,"nb_tracks":150,"picture_medium":"p1.jpg"},
			{"id":2,"title":"Metalcore (too small)","public":true,"nb_tracks":3,"picture_medium":"p2.jpg"},
			{"id":3,"title":"Metalcore","public":true,"nb_tracks":150,"picture_medium":"p3.jpg"}
		]}`,
		"/playlist/3/tracks": `{"data":[
			{"id":10,"title":"Song A","duration":200,"artist":{"id":900,"name":"Band A"},"album":{"title":"Alb","cover_medium":"c.jpg"}},
			{"id":11,"title":"Song B","duration":200,"artist":{"id":901,"name":"Band B"},"album":{"title":"Alb2","cover_medium":"c2.jpg"}}
		]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	playlist := buildSearchedPlaylist(context.Background(), deezer, "metalcore")
	if playlist == nil {
		t.Fatal("buildSearchedPlaylist = nil, attendu une playlist")
	}
	if playlist.ID != "search-playlist-3" || playlist.Title != "Metalcore" || playlist.Kind != "playlist_search" {
		t.Errorf("playlist = %+v, attendu id=search-playlist-3 title=Metalcore kind=playlist_search", playlist)
	}
	if len(playlist.Tracks) != 2 {
		t.Errorf("len(Tracks) = %d, attendu 2", len(playlist.Tracks))
	}
	if playlist.Cover != "p3.jpg" {
		t.Errorf("Cover = %q, attendu la pochette de la playlist Deezer", playlist.Cover)
	}
}

func TestBuildSearchedPlaylistReturnsNilWithNoGoodMatch(t *testing.T) {
	responses := map[string]string{
		"/search/playlist": `{"data":[
			{"id":1,"title":"Too small","public":true,"nb_tracks":2,"picture_medium":"p1.jpg"}
		]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	if playlist := buildSearchedPlaylist(context.Background(), deezer, "obscurequery"); playlist != nil {
		t.Errorf("playlist = %+v, attendu nil", playlist)
	}
}

func TestHandleGetSearchedPlaylistRejectsMissingQuery(t *testing.T) {
	router := newSearchedPlaylistRouter(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/smart-playlists/search", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, attendu 400 (requête manquante)", rec.Code)
	}
}

func TestHandleGetSearchedPlaylistReturnsEmptyArrayOnNoMatch(t *testing.T) {
	responses := map[string]string{
		"/search/playlist": `{"data":[]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	router := newSearchedPlaylistRouter(newDeezerClientForRecTest(server))

	req := httptest.NewRequest(http.MethodGet, "/api/smart-playlists/search?q=nothingmatchesthis", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Playlists []SmartPlaylistDTO `json:"playlists"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if len(body.Playlists) != 0 {
		t.Errorf("len(Playlists) = %d, attendu 0", len(body.Playlists))
	}
}
