package stats

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// Reuses newRecommendationsDeezerStub/newDeezerClientForRecTest from
// recommendations_integration_test.go (same package, same fake-Deezer
// shape: path -> raw JSON body) rather than duplicating them.

func TestBuildSmartPlaylistsBuildsArtistAllAndRadioFromTopSeed(t *testing.T) {
	queries, userID := newTestQueries(t)
	insertPlay(t, queries, userID, 10, "Master of Puppets", "Metallica", "Master of Puppets", "cover.jpg", 300)
	insertPlay(t, queries, userID, 11, "One", "Metallica", "...And Justice for All", "cover2.jpg", 400)

	responses := map[string]string{
		// Seed resolution: MAX(deezer_track_id) per artist -> track 11.
		"/track/11":         `{"id":11,"artist":{"id":100,"name":"Metallica"}}`,
		"/artist/100/top":   `{"data":[{"id":10,"title":"Master of Puppets","duration":515,"artist":{"id":100,"name":"Metallica"},"album":{"title":"MOP","cover_medium":"c.jpg"}}]}`,
		"/artist/100/radio": `{"data":[{"id":99,"title":"Radio Track","duration":200,"artist":{"id":200,"name":"Similar Band"},"album":{"title":"Alb","cover_medium":"r.jpg"}}]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	result, err := getSmartPlaylists(context.Background(), queries, deezer, userID, false)
	if err != nil {
		t.Fatalf("getSmartPlaylists: %v", err)
	}

	var sawAll, sawRadio bool
	for _, p := range result.Playlists {
		if p.Kind == "artist_all" && p.Title == "100% Metallica" {
			sawAll = true
			if len(p.Tracks) != 1 || p.Tracks[0].ID != 10 {
				t.Errorf("playlist 100%% Metallica.Tracks = %v, attendu [10]", p.Tracks)
			}
		}
		if p.Kind == "artist_radio" && p.Title == "Metallica Radio" {
			sawRadio = true
			if len(p.Tracks) != 1 || p.Tracks[0].ID != 99 {
				t.Errorf("playlist Metallica Radio.Tracks = %v, attendu [99]", p.Tracks)
			}
		}
	}
	if !sawAll {
		t.Error("attendu une playlist \"100% Metallica\"")
	}
	if !sawRadio {
		t.Error("attendu une playlist \"Metallica Radio\"")
	}
}

func TestBuildSmartPlaylistsBuildsGenreAndDecadeFromOwnHistoryOnly(t *testing.T) {
	queries, userID := newTestQueries(t)
	insertPlay(t, queries, userID, 20, "Song A", "Artist A", "Album A", "a.jpg", 200)
	insertPlay(t, queries, userID, 21, "Song B", "Artist B", "Album B", "b.jpg", 200)
	insertTrackGenre(t, queries, 20, "lastfm", "Metalcore")
	if err := queries.InsertTrackReleaseYear(context.Background(), dbgen.InsertTrackReleaseYearParams{
		DeezerTrackID: 21, Year: 2014,
	}); err != nil {
		t.Fatalf("InsertTrackReleaseYear: %v", err)
	}

	responses := map[string]string{
		"/track/20": `{"id":20,"title":"Song A","duration":200,"artist":{"id":300,"name":"Artist A"},"album":{"title":"Album A","cover_medium":"a.jpg"}}`,
		"/track/21": `{"id":21,"title":"Song B","duration":200,"artist":{"id":301,"name":"Artist B"},"album":{"title":"Album B","cover_medium":"b.jpg"}}`,
		// Both tracks' artists also get picked as seed artists (this account's
		// only play history) — stubbed empty so the artist_all/radio pair for
		// each is silently dropped and this test can assert genre/decade alone.
		"/artist/300/top":   `{"data":[]}`,
		"/artist/300/radio": `{"data":[]}`,
		"/artist/301/top":   `{"data":[]}`,
		"/artist/301/radio": `{"data":[]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, nil))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	result, err := getSmartPlaylists(context.Background(), queries, deezer, userID, false)
	if err != nil {
		t.Fatalf("getSmartPlaylists: %v", err)
	}

	var sawGenre, sawDecade bool
	for _, p := range result.Playlists {
		if p.Kind == "genre" && p.Title == "Metalcore" {
			sawGenre = true
			if len(p.Tracks) != 1 || p.Tracks[0].ID != 20 {
				t.Errorf("playlist Metalcore.Tracks = %v, attendu [20]", p.Tracks)
			}
		}
		if p.Kind == "decade" && p.Title == "Années 2010" {
			sawDecade = true
			if len(p.Tracks) != 1 || p.Tracks[0].ID != 21 {
				t.Errorf("playlist Années 2010.Tracks = %v, attendu [21]", p.Tracks)
			}
		}
	}
	if !sawGenre {
		t.Error("attendu une playlist \"Metalcore\"")
	}
	if !sawDecade {
		t.Error("attendu une playlist \"Années 2010\"")
	}
}

func TestGetSmartPlaylistsCachesWithinTTLAndForceRefreshBypassesIt(t *testing.T) {
	queries, userID := newTestQueries(t)
	insertPlay(t, queries, userID, 10, "Master of Puppets", "Metallica", "Master of Puppets", "cover.jpg", 300)

	var hits int64
	responses := map[string]string{
		"/track/10":         `{"id":10,"artist":{"id":100,"name":"Metallica"}}`,
		"/artist/100/top":   `{"data":[{"id":10,"title":"T","duration":200,"artist":{"id":100,"name":"Metallica"},"album":{"title":"A","cover_medium":"c.jpg"}}]}`,
		"/artist/100/radio": `{"data":[]}`,
	}
	server := httptest.NewServer(newRecommendationsDeezerStub(t, responses, &hits))
	defer server.Close()
	deezer := newDeezerClientForRecTest(server)

	if _, err := getSmartPlaylists(context.Background(), queries, deezer, userID, false); err != nil {
		t.Fatalf("premier appel: %v", err)
	}
	firstHits := atomic.LoadInt64(&hits)
	if firstHits == 0 {
		t.Fatal("le premier appel (cache froid) devait interroger Deezer")
	}

	if _, err := getSmartPlaylists(context.Background(), queries, deezer, userID, false); err != nil {
		t.Fatalf("deuxième appel: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != firstHits {
		t.Errorf("deuxième appel (dans le TTL) a fait %d requêtes Deezer supplémentaires, attendu 0", got-firstHits)
	}

	if _, err := getSmartPlaylists(context.Background(), queries, deezer, userID, true); err != nil {
		t.Fatalf("appel refresh=1: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got <= firstHits {
		t.Error("refresh=1 devait contourner le cache et refaire des requêtes Deezer")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Metalcore":        "metalcore",
		"Death Metal":      "death-metal",
		"R&B":              "r-b",
		"  spaced   out  ": "spaced-out",
		"Post-Hardcore":    "post-hardcore",
	}
	for input, want := range cases {
		if got := slugify(input); got != want {
			t.Errorf("slugify(%q) = %q, attendu %q", input, got, want)
		}
	}
}
