package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestLyricsClient(t *testing.T, handler http.HandlerFunc) (*LyricsClient, *int32) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if got := r.Header.Get("User-Agent"); got != lrclibUserAgent {
			t.Errorf("User-Agent = %q, attendu %q", got, lrclibUserAgent)
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &LyricsClient{
		http:    server.Client(),
		baseURL: server.URL,
		cache:   make(map[string]json.RawMessage),
	}, &calls
}

func TestLookupPrefersExactMatch(t *testing.T) {
	client, _ := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/get") {
			w.Write([]byte(`{"id":1,"plainLyrics":"exact match lyrics","extra":"kept"}`))
			return
		}
		w.Write([]byte(`[{"id":2,"plainLyrics":"fuzzy match lyrics"}]`))
	})

	body, found := client.Lookup(context.Background(), "Track", "Artist", "", "")
	if !found {
		t.Fatal("found = false")
	}
	if !strings.Contains(string(body), "exact match lyrics") {
		t.Errorf("body = %s, la correspondance exacte doit gagner sur la recherche floue", body)
	}
	if !strings.Contains(string(body), `"extra":"kept"`) {
		t.Error("un champ lrclib non listé dans LyricsDto doit survivre au passthrough")
	}
}

func TestLookupFallsBackToFirstSearchResult(t *testing.T) {
	client, _ := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/get") {
			w.WriteHeader(http.StatusNotFound) // exact: genuinely not found
			return
		}
		w.Write([]byte(`[{"id":9,"plainLyrics":"fuzzy hit"},{"id":10,"plainLyrics":"second"}]`))
	})

	body, found := client.Lookup(context.Background(), "Track", "Artist", "", "")
	if !found {
		t.Fatal("found = false")
	}
	if !strings.Contains(string(body), "fuzzy hit") {
		t.Errorf("body = %s, attendu le premier résultat de la recherche floue", body)
	}
}

func TestLookupReturns404WhenBothMiss(t *testing.T) {
	client, _ := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, found := client.Lookup(context.Background(), "Nobody", "Nothing", "", "")
	if found {
		t.Error("found = true, attendu false quand les deux recherches échouent")
	}
}

func TestLookupCachesConfirmedNotFound(t *testing.T) {
	client, calls := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	client.Lookup(context.Background(), "Nobody", "Nothing", "", "")
	firstCalls := atomic.LoadInt32(calls)

	client.Lookup(context.Background(), "Nobody", "Nothing", "", "")
	secondCalls := atomic.LoadInt32(calls)

	if secondCalls != firstCalls {
		t.Errorf("un deuxième Lookup identique a rappelé le serveur (%d puis %d appels) : le \"non trouvé\" confirmé doit être mis en cache", firstCalls, secondCalls)
	}
}

func TestLookupCachesAConfirmedFind(t *testing.T) {
	client, calls := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/get") {
			w.Write([]byte(`{"id":1,"plainLyrics":"cached lyrics"}`))
			return
		}
		w.Write([]byte(`[]`))
	})

	client.Lookup(context.Background(), "Track", "Artist", "", "")
	firstCalls := atomic.LoadInt32(calls)

	body, found := client.Lookup(context.Background(), "Track", "Artist", "", "")
	if !found || !strings.Contains(string(body), "cached lyrics") {
		t.Fatalf("deuxième Lookup: found=%v body=%s", found, body)
	}
	if atomic.LoadInt32(calls) != firstCalls {
		t.Error("un résultat trouvé avec succès doit aussi être mis en cache")
	}
}

func TestLookupNeverCachesAnIndeterminateFailure(t *testing.T) {
	// A 500 (or a network/timeout error) is not a real "not found" — the
	// old server only caches when both calls resolved definitely (404 or a
	// real body), never on a transport hiccup. A second Lookup must retry.
	client, calls := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	client.Lookup(context.Background(), "Track", "Artist", "", "")
	firstCalls := atomic.LoadInt32(calls)
	if firstCalls == 0 {
		t.Fatal("le serveur mock n'a jamais été appelé")
	}

	client.Lookup(context.Background(), "Track", "Artist", "", "")
	secondCalls := atomic.LoadInt32(calls)
	if secondCalls == firstCalls {
		t.Error("un échec indéterminé (500) ne doit jamais être mis en cache — le deuxième appel aurait dû réessayer")
	}
}

func TestLookupSendsAlbumAndDurationOnlyToExactEndpoint(t *testing.T) {
	client, _ := newTestLyricsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/get") {
			if r.URL.Query().Get("album_name") != "Album" {
				t.Errorf("/get: album_name = %q, attendu Album", r.URL.Query().Get("album_name"))
			}
			if r.URL.Query().Get("duration") != "200" {
				t.Errorf("/get: duration = %q, attendu 200", r.URL.Query().Get("duration"))
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// /search never carries album/duration, matching the old server.
		if r.URL.Query().Get("album_name") != "" {
			t.Error("/search ne doit jamais recevoir album_name")
		}
		w.WriteHeader(http.StatusNotFound)
	})

	client.Lookup(context.Background(), "Track", "Artist", "Album", "200")
}
