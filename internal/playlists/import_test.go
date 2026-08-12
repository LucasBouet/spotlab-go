package playlists

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/catalog"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestResolveDeezerPlaylistIDAcceptsBareDigits(t *testing.T) {
	id, ok := resolveDeezerPlaylistID(context.Background(), http.DefaultClient, "  123456  ")
	if !ok || id != "123456" {
		t.Errorf("id/ok = %q/%v, attendu 123456/true", id, ok)
	}
}

func TestResolveDeezerPlaylistIDExtractsFromURL(t *testing.T) {
	cases := map[string]string{
		"https://www.deezer.com/fr/playlist/908622995": "908622995",
		"https://www.deezer.com/playlist/908622995":    "908622995",
		"deezer://playlist=908622995":                  "908622995",
	}
	for input, want := range cases {
		id, ok := resolveDeezerPlaylistID(context.Background(), http.DefaultClient, input)
		if !ok || id != want {
			t.Errorf("resolveDeezerPlaylistID(%q) = %q/%v, attendu %q/true", input, id, ok, want)
		}
	}
}

func TestResolveDeezerPlaylistIDFollowsRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	// target.URL doesn't itself contain "playlist/<id>", so wrap it behind
	// a redirect to a URL that does — simulating a link.deezer.com share
	// link that only reveals the real playlist URL after redirecting.
	shortener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/playlist/42", http.StatusFound)
	}))
	defer shortener.Close()

	id, ok := resolveDeezerPlaylistID(context.Background(), http.DefaultClient, shortener.URL)
	if !ok || id != "42" {
		t.Errorf("id/ok = %q/%v, attendu 42/true", id, ok)
	}
}

func TestResolveDeezerPlaylistIDRejectsEmptyAndUnrelatedInput(t *testing.T) {
	for _, input := range []string{"", "   ", "not a url at all"} {
		if _, ok := resolveDeezerPlaylistID(context.Background(), http.DefaultClient, input); ok {
			t.Errorf("resolveDeezerPlaylistID(%q) = true, attendu false", input)
		}
	}
}

func trackJSON(id int64, title, artist, cover string) string {
	return `{"id":` + itoa(id) + `,"title":"` + title + `","duration":200,"artist":{"id":1,"name":"` + artist + `"},"album":{"title":"Alb","cover_medium":"` + cover + `"}}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestFetchDeezerPlaylistPaginatesAndReportsProgress(t *testing.T) {
	var progressCalls [][2]int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/playlist/1":
			w.Write([]byte(`{"title":"My Mix","nb_tracks":2}`))
		case r.URL.Path == "/playlist/1/tracks":
			w.Write([]byte(`{"data":[` + trackJSON(1, "T1", "A1", "c1.jpg") + `],"next":"http://` + r.Host + `/next-page"}`))
		case r.URL.Path == "/next-page":
			w.Write([]byte(`{"data":[` + trackJSON(2, "T2", "A2", "c2.jpg") + `]}`))
		default:
			t.Errorf("chemin inattendu: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	// The first page's "next" is a full absolute URL back to this same
	// test server (built from r.Host at request time) — proves
	// fetchDeezerPlaylist follows Deezer's own cursor via FetchURL rather
	// than re-deriving the next page's URL itself.
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)

	title, tracks, err := fetchDeezerPlaylist(context.Background(), deezer, "1", func(fetched, total int) {
		progressCalls = append(progressCalls, [2]int{fetched, total})
	})
	if err != nil {
		t.Fatalf("fetchDeezerPlaylist: %v", err)
	}
	if title != "My Mix" {
		t.Errorf("title = %q, attendu My Mix", title)
	}
	if len(tracks) != 2 || tracks[0].ID != 1 || tracks[1].ID != 2 {
		t.Errorf("tracks = %+v, attendu deux titres id 1 et 2", tracks)
	}
	if len(progressCalls) < 2 || progressCalls[0] != [2]int{0, 2} {
		t.Errorf("progressCalls = %v, attendu un premier appel (0, 2)", progressCalls)
	}
	if last := progressCalls[len(progressCalls)-1]; last != [2]int{2, 2} {
		t.Errorf("dernier appel de progression = %v, attendu (2, 2)", last)
	}
}

func TestFetchDeezerPlaylistFailsWhenPlaylistNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)

	_, _, err := fetchDeezerPlaylist(context.Background(), deezer, "999", nil)
	if err == nil {
		t.Fatal("attendu une erreur pour une playlist introuvable")
	}
}

func newSingleTrackPlaylistServer(t *testing.T) *catalog.DeezerClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist/1":
			w.Write([]byte(`{"title":"My Mix","nb_tracks":1}`))
		case "/playlist/1/tracks":
			w.Write([]byte(`{"data":[` + trackJSON(10, "Song", "Artist", "cover.jpg") + `]}`))
		}
	}))
	t.Cleanup(server.Close)
	return catalog.NewDeezerClientForTesting(server.Client(), server.URL)
}

func TestImportDeezerPlaylistForUserToNewPlaylist(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	deezer := newSingleTrackPlaylistServer(t)

	result, err := importDeezerPlaylistForUser(
		context.Background(), conn, queries, deezer, http.DefaultClient,
		userID, "1", ImportDestinationPlaylist, "",
		nil,
	)
	if err != nil {
		t.Fatalf("importDeezerPlaylistForUser: %v", err)
	}
	if result.Destination != ImportDestinationPlaylist || result.TrackCount != 1 || result.PlaylistID == "" {
		t.Errorf("result = %+v, attendu playlist/1/id-non-vide", result)
	}

	tracks, err := queries.ListPlaylistTracks(context.Background(), result.PlaylistID)
	if err != nil || len(tracks) != 1 || tracks[0].DeezerTrackID != 10 {
		t.Errorf("ListPlaylistTracks = %+v (err=%v), attendu un titre id 10", tracks, err)
	}
}

func TestImportDeezerPlaylistForUserUsesDeezerTitleWhenNoCustomName(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	deezer := newSingleTrackPlaylistServer(t)

	result, err := importDeezerPlaylistForUser(
		context.Background(), conn, queries, deezer, http.DefaultClient,
		userID, "1", ImportDestinationPlaylist, "",
		nil,
	)
	if err != nil {
		t.Fatalf("importDeezerPlaylistForUser: %v", err)
	}
	playlist, err := queries.GetPlaylistOwned(context.Background(), db.GetPlaylistOwnedParams{ID: result.PlaylistID, UserID: userID})
	if err != nil || playlist.Name != "My Mix" {
		t.Errorf("Name = %q (err=%v), attendu My Mix (titre Deezer)", playlist.Name, err)
	}
}

func TestImportDeezerPlaylistForUserToLikedSkipsAlreadyLiked(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist/1":
			w.Write([]byte(`{"title":"My Mix","nb_tracks":2}`))
		case "/playlist/1/tracks":
			w.Write([]byte(`{"data":[` +
				trackJSON(10, "Already Liked", "A", "c1.jpg") + `,` +
				trackJSON(20, "New One", "B", "c2.jpg") + `]}`))
		}
	}))
	defer server.Close()
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)

	if err := queries.UpsertLikedTrack(context.Background(), db.UpsertLikedTrackParams{
		ID: "existing", UserID: userID, DeezerTrackID: 10, Title: "Already Liked",
		ArtistName: "A", AlbumTitle: "Alb", AlbumCover: "c1.jpg", Duration: 200,
	}); err != nil {
		t.Fatalf("setup UpsertLikedTrack: %v", err)
	}

	result, err := importDeezerPlaylistForUser(
		context.Background(), conn, queries, deezer, http.DefaultClient,
		userID, "1", ImportDestinationLiked, "",
		nil,
	)
	if err != nil {
		t.Fatalf("importDeezerPlaylistForUser: %v", err)
	}
	if result.TrackCount != 1 {
		t.Errorf("TrackCount = %d, attendu 1 (le titre déjà aimé doit être exclu du compte)", result.TrackCount)
	}

	ids, err := queries.ListLikedTrackIDs(context.Background(), userID)
	if err != nil {
		t.Fatalf("ListLikedTrackIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("len(ids) = %d, attendu 2 (1 préexistant + 1 nouveau)", len(ids))
	}
}

func TestImportDeezerPlaylistForUserFiltersInvalidTracks(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlist/1":
			w.Write([]byte(`{"title":"My Mix","nb_tracks":3}`))
		case "/playlist/1/tracks":
			w.Write([]byte(`{"data":[` +
				trackJSON(10, "Valid", "A", "cover.jpg") + `,` +
				`{"id":-5,"title":"Withdrawn","duration":200,"artist":{"name":"A"},"album":{"title":"Alb","cover_medium":"cover.jpg"}},` +
				`{"id":30,"title":"No Cover","duration":200,"artist":{"name":"A"},"album":{"title":"Alb","cover_medium":""}}` +
				`]}`))
		}
	}))
	defer server.Close()
	deezer := catalog.NewDeezerClientForTesting(server.Client(), server.URL)

	result, err := importDeezerPlaylistForUser(
		context.Background(), conn, queries, deezer, http.DefaultClient,
		userID, "1", ImportDestinationPlaylist, "Curated",
		nil,
	)
	if err != nil {
		t.Fatalf("importDeezerPlaylistForUser: %v", err)
	}
	if result.TrackCount != 1 {
		t.Errorf("TrackCount = %d, attendu 1 (id négatif et sans cover exclus)", result.TrackCount)
	}
}

func TestImportDeezerPlaylistForUserRejectsEmptyLink(t *testing.T) {
	conn, queries, userID := newTestDB(t)
	_, err := importDeezerPlaylistForUser(
		context.Background(), conn, queries, nil, http.DefaultClient,
		userID, "   ", ImportDestinationPlaylist, "",
		nil,
	)
	if err == nil {
		t.Fatal("attendu une erreur pour un lien vide")
	}
}
