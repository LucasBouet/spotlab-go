package stats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

func newArtistPlaylistsRouter(deezer *catalog.DeezerClient) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/smart-playlists/artist/{artistId}", handleGetArtistPlaylists(deezer))
	return r
}

// TestHandleGetArtistPlaylistsBuildsPairForAnySearchedArtist is the
// regression this endpoint exists to fix: search used to only ever surface
// "100% X"/"X Radio" for artists that already happened to be one of the
// account's top-3 seeds on the home shelf. Any artist Deezer resolves must
// work, using the name the client already has from its own search hit.
func TestHandleGetArtistPlaylistsBuildsPairForAnySearchedArtist(t *testing.T) {
	responses := map[string]string{
		"/artist/999/top":   `{"data":[{"id":10,"title":"Some Song","duration":200,"artist":{"id":999,"name":"Obscure Band"},"album":{"title":"Alb","cover_medium":"c.jpg"}}]}`,
		"/artist/999/radio": `{"data":[{"id":11,"title":"Similar Song","duration":200,"artist":{"id":998,"name":"Another Band"},"album":{"title":"Alb2","cover_medium":"c2.jpg"}}]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)
	router := newArtistPlaylistsRouter(deezer)

	req := httptest.NewRequest(http.MethodGet, "/api/smart-playlists/artist/999?name=Obscure+Band", nil)
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
	if len(body.Playlists) != 2 {
		t.Fatalf("len(Playlists) = %d, attendu 2 (100%% + Radio)", len(body.Playlists))
	}
	var sawAll, sawRadio bool
	for _, p := range body.Playlists {
		if p.Kind == "artist_all" && p.Title == "100% Obscure Band" {
			sawAll = true
		}
		if p.Kind == "artist_radio" && p.Title == "Obscure Band Radio" {
			sawRadio = true
		}
	}
	if !sawAll || !sawRadio {
		t.Errorf("playlists = %+v, attendu 100%% Obscure Band + Obscure Band Radio", body.Playlists)
	}
}

func TestHandleGetArtistPlaylistsRejectsMissingName(t *testing.T) {
	server := httptest.NewServer(newRecommendationsDeezerStub(t, map[string]string{}, nil))
	defer server.Close()
	router := newArtistPlaylistsRouter(newDeezerClientForRecTest(server))

	req := httptest.NewRequest(http.MethodGet, "/api/smart-playlists/artist/999", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, attendu 400 (nom manquant)", rec.Code)
	}
}

func TestHandleGetArtistPlaylistsRejectsInvalidID(t *testing.T) {
	router := newArtistPlaylistsRouter(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/smart-playlists/artist/not-a-number?name=X", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, attendu 400 (id invalide)", rec.Code)
	}
}
