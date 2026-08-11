package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestDeezerClient(t *testing.T, handler http.HandlerFunc) *DeezerClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &DeezerClient{http: server.Client(), baseURL: server.URL}
}

func TestFetchTrackPassesEveryFieldThrough(t *testing.T) {
	// The whole point of json.RawMessage passthrough: a field the old
	// server's TS type never named (picture_big-style extras) must still
	// reach the caller untouched.
	const payload = `{"id":42,"title":"Test","some_field_nobody_typed":"still here","nested":{"a":1,"b":[1,2,3]}}`
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/track/42" {
			t.Errorf("path = %q, attendu /track/42", r.URL.Path)
		}
		w.Write([]byte(payload))
	})

	body, ok := client.FetchTrack(context.Background(), "42")
	if !ok {
		t.Fatal("FetchTrack: ok = false")
	}
	if string(body) != payload {
		t.Errorf("body = %s, attendu byte-pour-byte %s", body, payload)
	}
}

func TestFetchTrackRejectsDeezerErrorPayload(t *testing.T) {
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":{"type":"DataException","message":"no data","code":800}}`))
	})

	_, ok := client.FetchTrack(context.Background(), "0")
	if ok {
		t.Error("un payload {\"error\":...} de Deezer doit être traité comme un échec")
	}
}

func TestFetchTrackRejectsNon2xx(t *testing.T) {
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, ok := client.FetchTrack(context.Background(), "1")
	if ok {
		t.Error("un statut non-2xx doit être un échec")
	}
}

func TestFetchDataArrayDefaultsToEmptyWhenFieldAbsent(t *testing.T) {
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total":0}`)) // no "data" key at all
	})

	data, ok := client.fetchDataArray(context.Background(), "/search?q=x")
	if !ok {
		t.Fatal("fetchDataArray: ok = false")
	}
	if string(data) != "[]" {
		t.Errorf("data = %s, attendu []", data)
	}
}

func TestFetchDataArrayPassesElementsThrough(t *testing.T) {
	const payload = `{"data":[{"id":1,"extra_field":"kept"},{"id":2}],"total":2}`
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	})

	data, ok := client.fetchDataArray(context.Background(), "/search?q=x")
	if !ok {
		t.Fatal("fetchDataArray: ok = false")
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, attendu 2", len(decoded))
	}
	if decoded[0]["extra_field"] != "kept" {
		t.Error("un champ non listé dans un type TS étroit doit survivre au passthrough")
	}
}

func TestFetchArtistPageDegradesTopTracksToEmptyOnFailure(t *testing.T) {
	// Only the artist lookup failing is a 404 for the caller; a failed
	// top-tracks call must degrade silently to an empty array instead of
	// failing the whole page — matches `topTracks?.data ?? []`.
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/artist/5":
			w.Write([]byte(`{"id":5,"name":"Artist"}`))
		default: // .../top?limit=25
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	artist, topTracks, ok := client.FetchArtistPage(context.Background(), "5")
	if !ok {
		t.Fatal("FetchArtistPage: ok = false, attendu true (seul l'artiste doit décider)")
	}
	if string(artist) != `{"id":5,"name":"Artist"}` {
		t.Errorf("artist = %s", artist)
	}
	if string(topTracks) != "[]" {
		t.Errorf("topTracks = %s, attendu [] après l'échec de l'appel top-tracks", topTracks)
	}
}

func TestFetchArtistPageFailsWhenArtistItselfFails(t *testing.T) {
	client := newTestDeezerClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, _, ok := client.FetchArtistPage(context.Background(), "999")
	if ok {
		t.Error("un artiste introuvable doit faire échouer toute la page")
	}
}
