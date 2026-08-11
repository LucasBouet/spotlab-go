package stats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
)

// newRecommendationsDeezerStub maps a request path (ignoring query string)
// to the raw JSON body a fake Deezer server answers with — enough surface
// for a full buildPersonal round-trip: resolveArtist, fetchRelated,
// fetchArtistCatalogue (top tracks + albums) for each seed and each
// discovered related artist.
func newRecommendationsDeezerStub(t *testing.T, responses map[string]string, hitCount *int64) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if hitCount != nil {
			atomic.AddInt64(hitCount, 1)
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("appel Deezer inattendu: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}
}

func newDeezerClientForRecTest(server *httptest.Server) *catalog.DeezerClient {
	return catalog.NewDeezerClientForTesting(server.Client(), server.URL)
}

func twoSeedArtistDeezerResponses() map[string]string {
	return map[string]string{
		// Seed resolution: sample track -> artist. getSeedArtists picks
		// MAX(deezer_track_id) per artist as the sample, so this must match
		// whichever track id each artist's play history resolves to.
		"/track/10": `{"id":10,"artist":{"id":100,"name":"Metallica"}}`,
		"/track/2":  `{"id":2,"artist":{"id":200,"name":"Slayer"}}`,
		// Related artists per seed.
		"/artist/100/related": `{"data":[{"id":300,"name":"Related1"}]}`,
		"/artist/200/related": `{"data":[]}`,
		// Catalogue per seed + related artist.
		"/artist/100/top":    `{"data":[{"id":10,"title":"T10","duration":200,"artist":{"id":100,"name":"Metallica"},"album":{"title":"Alb","cover_medium":"c.jpg"}}]}`,
		"/artist/100/albums": `{"data":[{"id":50,"title":"AlbX","cover_medium":"c.jpg"}]}`,
		"/artist/200/top":    `{"data":[{"id":20,"title":"T20","duration":200,"artist":{"id":200,"name":"Slayer"},"album":{"title":"Alb","cover_medium":"c.jpg"}}]}`,
		"/artist/200/albums": `{"data":[{"id":60,"title":"AlbY","cover_medium":"c.jpg"}]}`,
		"/artist/300/top":    `{"data":[{"id":30,"title":"T30","duration":200,"artist":{"id":300,"name":"Related1"},"album":{"title":"Alb","cover_medium":"c.jpg"}}]}`,
		"/artist/300/albums": `{"data":[{"id":70,"title":"AlbZ","cover_medium":"c.jpg"}]}`,
	}
}

func TestGetRecommendationsBuildsPersonalFromSeedsAndExcludesAlreadyPlayed(t *testing.T) {
	queries, userID := newTestQueries(t)
	// Metallica's only play is track 10, which the stub also returns as
	// Metallica's top track — proving the exclusion set drops a track that
	// resurfaces via the Deezer catalogue fetch, not just literal seeds.
	insertPlay(t, queries, userID, 10, "Master of Puppets", "Metallica", "Master of Puppets", "cover.jpg", 300)
	insertPlay(t, queries, userID, 2, "Angel of Death", "Slayer", "Reign in Blood", "cover2.jpg", 250)

	responses := twoSeedArtistDeezerResponses()
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	result, err := getRecommendations(context.Background(), queries, deezer, userID, windowAll, false)
	if err != nil {
		t.Fatalf("getRecommendations: %v", err)
	}

	if result.Window != "all" || result.EffectiveWindow != "all" {
		t.Errorf("Window/EffectiveWindow = %q/%q, attendu all/all", result.Window, result.EffectiveWindow)
	}
	if len(result.BasedOn) != 2 {
		t.Errorf("BasedOn = %v, attendu 2 artistes seed", result.BasedOn)
	}

	for _, track := range result.Tracks {
		if track.ID == 10 {
			t.Error("le morceau déjà écouté (id 10) ne doit pas apparaître dans les recommandations")
		}
	}
	if len(result.Tracks) != 2 { // T20 (Slayer) + T30 (related) — T10 excluded
		t.Errorf("len(Tracks) = %d, attendu 2 (T10 exclu)", len(result.Tracks))
	}
	if len(result.Albums) != 3 {
		t.Errorf("len(Albums) = %d, attendu 3", len(result.Albums))
	}
}

func TestGetRecommendationsCachesWithinTTLAndForceRefreshBypassesIt(t *testing.T) {
	queries, userID := newTestQueries(t)
	insertPlay(t, queries, userID, 10, "Master of Puppets", "Metallica", "Master of Puppets", "cover.jpg", 300)
	insertPlay(t, queries, userID, 2, "Angel of Death", "Slayer", "Reign in Blood", "cover2.jpg", 250)

	var hits int64
	responses := twoSeedArtistDeezerResponses()
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, &hits))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	if _, err := getRecommendations(context.Background(), queries, deezer, userID, windowAll, false); err != nil {
		t.Fatalf("premier appel: %v", err)
	}
	firstHits := atomic.LoadInt64(&hits)
	if firstHits == 0 {
		t.Fatal("le premier appel (cache froid) devait interroger Deezer")
	}

	if _, err := getRecommendations(context.Background(), queries, deezer, userID, windowAll, false); err != nil {
		t.Fatalf("deuxième appel: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != firstHits {
		t.Errorf("deuxième appel (dans le TTL) a fait %d requêtes Deezer supplémentaires, attendu 0 (doit servir le cache)", got-firstHits)
	}

	if _, err := getRecommendations(context.Background(), queries, deezer, userID, windowAll, true); err != nil {
		t.Fatalf("appel refresh=1: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got <= firstHits {
		t.Error("refresh=1 devait contourner le cache et refaire des requêtes Deezer")
	}
}

func TestGetRecommendationsFallsBackToChartsWithNoPlayHistory(t *testing.T) {
	queries, userID := newTestQueries(t)

	responses := map[string]string{
		"/chart": `{"tracks":{"data":[{"id":99,"title":"Chart Track","duration":180,"artist":{"id":9,"name":"Chart Artist"},"album":{"title":"Alb","cover_medium":"c.jpg"}}]},"albums":{"data":[{"id":88,"title":"Chart Album","cover_medium":"c.jpg","artist":{"id":9,"name":"Chart Artist"}}]}}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	result, err := getRecommendations(context.Background(), queries, deezer, userID, windowAll, false)
	if err != nil {
		t.Fatalf("getRecommendations: %v", err)
	}
	if result.EffectiveWindow != "charts" {
		t.Errorf("EffectiveWindow = %q, attendu charts (aucun historique)", result.EffectiveWindow)
	}
	if len(result.Tracks) != 1 || result.Tracks[0].ID != 99 {
		t.Errorf("Tracks = %v, attendu le morceau du chart", result.Tracks)
	}
	if len(result.BasedOn) != 0 {
		t.Errorf("BasedOn = %v, attendu vide en mode charts", result.BasedOn)
	}
}
