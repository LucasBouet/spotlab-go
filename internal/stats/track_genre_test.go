package stats

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func newTestLastFMServer(t *testing.T, tagsJSON string) *lastFMClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tagsJSON))
	}))
	t.Cleanup(server.Close)
	return &lastFMClient{http: server.Client(), apiKey: "test-key", baseURL: server.URL}
}

func newNoKeyLastFMClient() *lastFMClient {
	return &lastFMClient{http: http.DefaultClient, apiKey: "", baseURL: lastFMAPIBase}
}

func newTestDeezerServer(t *testing.T, handler http.HandlerFunc) *catalog.DeezerClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return catalog.NewDeezerClientForTesting(server.Client(), server.URL)
}

func insertTrackGenre(t *testing.T, queries *dbgen.Queries, deezerTrackID int64, source, genreName string) {
	t.Helper()
	if err := queries.InsertTrackGenre(context.Background(), dbgen.InsertTrackGenreParams{
		DeezerTrackID: deezerTrackID,
		GenreName:     sql.NullString{String: genreName, Valid: true},
		Source:        source,
	}); err != nil {
		t.Fatalf("InsertTrackGenre: %v", err)
	}
}

func TestEnsureTrackGenrePrefersLastFmOverDeezer(t *testing.T) {
	queries, _ := newTestQueries(t)
	lastfm := newTestLastFMServer(t, `{"toptags":{"tag":[{"name":"Deathcore"},{"name":"seen live"}]}}`)
	// A Deezer server that would answer, but must never be called: proves
	// the lastfm-first branch short-circuits before touching Deezer at all.
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Deezer ne devait pas être appelé : %s", r.URL.Path)
	})

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.Source.String != "lastfm" || row.GenreName.String != "Deathcore" {
		t.Errorf("row = %+v, attendu source=lastfm genre=Deathcore", row)
	}
}

func TestEnsureTrackGenreFallsBackToDeezerWhenNoLastFmTagMatches(t *testing.T) {
	queries, _ := newTestQueries(t)
	lastfm := newTestLastFMServer(t, `{"toptags":{"tag":[{"name":"seen live"},{"name":"favorites"}]}}`)
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/track/42":
			w.Write([]byte(`{"id":42,"album":{"id":7}}`))
		case "/album/7":
			w.Write([]byte(`{"id":7,"genres":{"data":[{"id":100,"name":"Rock"}]}}`))
		default:
			t.Errorf("chemin Deezer inattendu: %s", r.URL.Path)
		}
	})

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.Source.String != "deezer" || row.GenreName.String != "Rock" || row.AlbumID.Int64 != 7 {
		t.Errorf("row = %+v, attendu source=deezer genre=Rock albumId=7", row)
	}
}

func TestEnsureTrackGenreFallsBackToDeezerWhenNoLastFmKeyConfigured(t *testing.T) {
	queries, _ := newTestQueries(t)
	lastfm := newNoKeyLastFMClient()
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/track/42":
			w.Write([]byte(`{"id":42,"album":{"id":7}}`))
		case "/album/7":
			w.Write([]byte(`{"id":7,"genres":{"data":[{"id":100,"name":"Jazz"}]}}`))
		}
	})

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.Source.String != "deezer" || row.GenreName.String != "Jazz" {
		t.Errorf("row = %+v, attendu source=deezer genre=Jazz", row)
	}
}

func TestEnsureTrackGenreLeavesLastFmSourcedRowAlone(t *testing.T) {
	queries, _ := newTestQueries(t)
	insertTrackGenre(t, queries, 42, "lastfm", "Deathcore")

	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Deezer ne devait pas être appelé pour une ligne déjà lastfm : %s", r.URL.Path)
	})
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"toptags":{"tag":[]}}`))
	}))
	defer server.Close()
	lastfm := &lastFMClient{http: server.Client(), apiKey: "test-key", baseURL: server.URL}

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	if called {
		t.Error("Last.fm ne devait pas être appelé : la ligne existante est déjà source=lastfm")
	}

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.GenreName.String != "Deathcore" {
		t.Errorf("GenreName = %q, attendu inchangé Deathcore", row.GenreName.String)
	}
}

func TestEnsureTrackGenreUpgradesDeezerSourcedRowOnceLastFmAvailable(t *testing.T) {
	queries, _ := newTestQueries(t)
	insertTrackGenre(t, queries, 42, "deezer", "Rock")

	lastfm := newTestLastFMServer(t, `{"toptags":{"tag":[{"name":"Deathcore"}]}}`)
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Deezer ne devait pas être appelé pendant une mise à niveau depuis lastfm: %s", r.URL.Path)
	})

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.Source.String != "lastfm" || row.GenreName.String != "Deathcore" {
		t.Errorf("row = %+v, attendu mise à niveau vers source=lastfm genre=Deathcore", row)
	}
	if row.GenreID.Valid {
		t.Error("GenreID doit être remis à NULL lors de la mise à niveau vers lastfm")
	}
}

func TestEnsureTrackGenreLeavesRowUntouchedWhenNoUpgradeTagMatches(t *testing.T) {
	queries, _ := newTestQueries(t)
	insertTrackGenre(t, queries, 42, "deezer", "Rock")

	lastfm := newTestLastFMServer(t, `{"toptags":{"tag":[{"name":"seen live"}]}}`)
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Deezer ne devait pas être appelé : %s", r.URL.Path)
	})

	ensureTrackGenre(context.Background(), queries, deezer, lastfm, 42, "Some Artist")

	row, err := queries.GetTrackGenre(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetTrackGenre: %v", err)
	}
	if row.Source.String != "deezer" || row.GenreName.String != "Rock" {
		t.Errorf("row = %+v, attendu inchangé (aucun tag reconnu pour la mise à niveau)", row)
	}
}

func TestFetchDeezerGenreReturnsNilWhenTrackHasNoAlbum(t *testing.T) {
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":42}`))
	})
	if got := fetchDeezerGenre(context.Background(), deezer, 42); got != nil {
		t.Errorf("fetchDeezerGenre = %+v, attendu nil (pas d'album)", got)
	}
}

func TestFetchDeezerGenreReturnsAlbumWithoutGenreWhenNoneListed(t *testing.T) {
	deezer := newTestDeezerServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/track/42":
			w.Write([]byte(`{"id":42,"album":{"id":7}}`))
		case "/album/7":
			w.Write([]byte(`{"id":7,"genres":{"data":[]}}`))
		}
	})
	got := fetchDeezerGenre(context.Background(), deezer, 42)
	if got == nil {
		t.Fatal("fetchDeezerGenre = nil, attendu un résultat avec AlbumID renseigné")
	}
	if got.AlbumID != 7 || got.GenreName != "" {
		t.Errorf("got = %+v, attendu AlbumID=7 GenreName vide", got)
	}
}
