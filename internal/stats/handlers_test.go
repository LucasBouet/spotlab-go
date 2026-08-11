package stats

import (
	"context"
	"testing"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

func TestValidPlayRequestRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		body postPlayRequest
		want bool
	}{
		{"valid", postPlayRequest{DeezerTrackID: 1, Title: "T", Duration: 200}, true},
		{"zero track id", postPlayRequest{DeezerTrackID: 0, Title: "T", Duration: 200}, false},
		{"negative track id", postPlayRequest{DeezerTrackID: -1, Title: "T", Duration: 200}, false},
		{"empty title", postPlayRequest{DeezerTrackID: 1, Title: "", Duration: 200}, false},
		{"zero duration", postPlayRequest{DeezerTrackID: 1, Title: "T", Duration: 0}, false},
		{"negative duration", postPlayRequest{DeezerTrackID: 1, Title: "T", Duration: -5}, false},
	}
	for _, c := range cases {
		if got := validPlayRequest(c.body); got != c.want {
			t.Errorf("%s: validPlayRequest = %v, attendu %v", c.name, got, c.want)
		}
	}
}

func TestGetListeningStatsOnEmptyHistoryReturnsZeroedNonNilSlices(t *testing.T) {
	queries, userID := newTestQueries(t)
	stats, err := getListeningStats(context.Background(), queries, userID)
	if err != nil {
		t.Fatalf("getListeningStats: %v", err)
	}
	if stats.TotalPlays != 0 || stats.TotalSeconds != 0 {
		t.Errorf("totaux = %+v, attendu zéro", stats)
	}
	if stats.TopTracks == nil || stats.TopAlbums == nil || stats.TopGenres == nil {
		t.Error("les listes top doivent être des slices vides non-nil, jamais null en JSON")
	}
}

func TestGetListeningStatsAggregatesTotalsAndRanksByPlayCount(t *testing.T) {
	queries, userID := newTestQueries(t)

	// Track 1 played 3x (200s each), track 2 played once (180s).
	insertPlay(t, queries, userID, 1, "Song A", "Artist X", "Album A", "cover-a.jpg", 200)
	insertPlay(t, queries, userID, 1, "Song A", "Artist X", "Album A", "cover-a.jpg", 200)
	insertPlay(t, queries, userID, 1, "Song A", "Artist X", "Album A", "cover-a.jpg", 200)
	insertPlay(t, queries, userID, 2, "Song B", "Artist Y", "Album B", "cover-b.jpg", 180)

	stats, err := getListeningStats(context.Background(), queries, userID)
	if err != nil {
		t.Fatalf("getListeningStats: %v", err)
	}

	if stats.TotalPlays != 4 {
		t.Errorf("TotalPlays = %d, attendu 4", stats.TotalPlays)
	}
	if stats.TotalSeconds != 200*3+180 {
		t.Errorf("TotalSeconds = %d, attendu %d", stats.TotalSeconds, 200*3+180)
	}
	if len(stats.TopTracks) != 2 {
		t.Fatalf("len(TopTracks) = %d, attendu 2", len(stats.TopTracks))
	}
	if stats.TopTracks[0].Label != "Song A" || stats.TopTracks[0].Count != 3 {
		t.Errorf("TopTracks[0] = %+v, attendu Song A x3 en tête", stats.TopTracks[0])
	}
	if stats.TopTracks[1].Label != "Song B" || stats.TopTracks[1].Count != 1 {
		t.Errorf("TopTracks[1] = %+v, attendu Song B x1", stats.TopTracks[1])
	}

	if len(stats.TopAlbums) != 2 || stats.TopAlbums[0].Label != "Album A" {
		t.Errorf("TopAlbums = %+v, attendu Album A en tête", stats.TopAlbums)
	}
}

func TestGetListeningStatsScopesToRequestingUserOnly(t *testing.T) {
	queries, userA := newTestQueries(t)
	userB, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: "other@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du deuxième utilisateur: %v", err)
	}

	insertPlay(t, queries, userA, 1, "A's Song", "Artist", "Album", "cover.jpg", 200)
	insertPlay(t, queries, userB.ID, 2, "B's Song", "Artist", "Album", "cover.jpg", 200)

	stats, err := getListeningStats(context.Background(), queries, userA)
	if err != nil {
		t.Fatalf("getListeningStats: %v", err)
	}
	if stats.TotalPlays != 1 {
		t.Errorf("TotalPlays = %d, attendu 1 — les écoutes de l'autre utilisateur ne doivent pas compter", stats.TotalPlays)
	}
	if len(stats.TopTracks) != 1 || stats.TopTracks[0].Label != "A's Song" {
		t.Errorf("TopTracks = %+v, attendu seulement A's Song", stats.TopTracks)
	}
}
