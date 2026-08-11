package library

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

func likeTrack(t *testing.T, queries *dbgen.Queries, userID string, trackID int64, title string) {
	t.Helper()
	if err := queries.UpsertLikedTrack(context.Background(), dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: trackID, Title: title,
		ArtistName: "Artist", AlbumTitle: "Album", AlbumCover: "cover.jpg", Duration: 200,
	}); err != nil {
		t.Fatalf("UpsertLikedTrack(%d): %v", trackID, err)
	}
}

func TestUpsertLikedTrackIsANoOpOnConflict(t *testing.T) {
	// The old server's upsert has `update: {}` — liking an already-liked
	// track must never refresh the stored metadata. This is the single
	// most important behavior in this file, since a naive translation
	// (INSERT ... ON CONFLICT DO UPDATE) would silently change it.
	queries, userID := newTestQueries(t)
	ctx := context.Background()

	if err := queries.UpsertLikedTrack(ctx, dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: 1, Title: "Original",
		ArtistName: "A", AlbumTitle: "Alb", AlbumCover: "cover.jpg", Duration: 200,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := queries.UpsertLikedTrack(ctx, dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: 1, Title: "Changed",
		ArtistName: "B", AlbumTitle: "Different", AlbumCover: "other.jpg", Duration: 999,
	}); err != nil {
		t.Fatalf("second upsert (should be a no-op, not an error): %v", err)
	}

	tracks, err := queries.ListLikedTracksRecent(ctx, userID)
	if err != nil {
		t.Fatalf("ListLikedTracksRecent: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("len(tracks) = %d, attendu 1 (conflit, pas doublon)", len(tracks))
	}
	if tracks[0].Title != "Original" {
		t.Errorf("title = %q, attendu Original — les métadonnées ne doivent jamais être rafraîchies par un second like", tracks[0].Title)
	}
}

func TestListLikedTracksRecentOrdersNewestFirst(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	likeTrack(t, queries, userID, 1, "First")
	time.Sleep(1100 * time.Millisecond) // created_at has 1s resolution
	likeTrack(t, queries, userID, 2, "Second")

	tracks, err := queries.ListLikedTracksRecent(ctx, userID)
	if err != nil {
		t.Fatalf("ListLikedTracksRecent: %v", err)
	}
	if len(tracks) != 2 || tracks[0].Title != "Second" || tracks[1].Title != "First" {
		t.Fatalf("ordre = %v, attendu [Second First]", titlesOf(tracks))
	}
}

func TestListLikedTracksByTitleOrdersAlphabetically(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	likeTrack(t, queries, userID, 1, "Zebra")
	likeTrack(t, queries, userID, 2, "Apple")

	tracks, err := queries.ListLikedTracksByTitle(ctx, userID)
	if err != nil {
		t.Fatalf("ListLikedTracksByTitle: %v", err)
	}
	if len(tracks) != 2 || tracks[0].Title != "Apple" || tracks[1].Title != "Zebra" {
		t.Fatalf("ordre = %v, attendu [Apple Zebra]", titlesOf(tracks))
	}
}

func titlesOf(tracks []dbgen.LikedTrack) []string {
	out := make([]string, len(tracks))
	for i, t := range tracks {
		out[i] = t.Title
	}
	return out
}

func TestLikedTracksAreScopedPerUser(t *testing.T) {
	queries, userA := newTestQueries(t)
	ctx := context.Background()
	userB, err := queries.CreateUser(ctx, dbgen.CreateUserParams{
		ID: idgen.New(), Email: "b@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du second utilisateur: %v", err)
	}

	likeTrack(t, queries, userA, 1, "A's track")
	likeTrack(t, queries, userB.ID, 2, "B's track")

	tracksA, err := queries.ListLikedTracksRecent(ctx, userA)
	if err != nil {
		t.Fatalf("ListLikedTracksRecent(A): %v", err)
	}
	if len(tracksA) != 1 || tracksA[0].Title != "A's track" {
		t.Fatalf("bibliothèque de A = %v, ne doit contenir que ses propres titres", titlesOf(tracksA))
	}
}

func TestIsTrackLiked(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	likeTrack(t, queries, userID, 42, "Liked")

	liked, err := queries.IsTrackLiked(ctx, dbgen.IsTrackLikedParams{UserID: userID, DeezerTrackID: 42})
	if err != nil || !liked {
		t.Errorf("IsTrackLiked(42) = %v, %v — attendu true", liked, err)
	}
	notLiked, err := queries.IsTrackLiked(ctx, dbgen.IsTrackLikedParams{UserID: userID, DeezerTrackID: 999})
	if err != nil || notLiked {
		t.Errorf("IsTrackLiked(999) = %v, %v — attendu false", notLiked, err)
	}
}

func TestDeleteLikedTrackIsIdempotent(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	likeTrack(t, queries, userID, 1, "X")

	del := func() {
		if err := queries.DeleteLikedTrack(ctx, dbgen.DeleteLikedTrackParams{UserID: userID, DeezerTrackID: 1}); err != nil {
			t.Fatalf("DeleteLikedTrack: %v", err)
		}
	}
	del()
	del() // deleting an absent row must not error

	ids, err := queries.ListLikedTrackIDs(ctx, userID)
	if err != nil {
		t.Fatalf("ListLikedTrackIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, attendu []", ids)
	}
}

func TestArtistIDSurvivesNullRoundtrip(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()

	// With an artist id.
	if err := queries.UpsertLikedTrack(ctx, dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: 1, Title: "Has artist",
		ArtistName: "A", ArtistID: sql.NullInt64{Int64: 99, Valid: true},
		AlbumTitle: "Alb", AlbumCover: "c.jpg", Duration: 1,
	}); err != nil {
		t.Fatalf("upsert avec artistId: %v", err)
	}
	// Without one — rows saved before the server stored artist ids.
	if err := queries.UpsertLikedTrack(ctx, dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: 2, Title: "No artist",
		ArtistName: "B", AlbumTitle: "Alb", AlbumCover: "c.jpg", Duration: 1,
	}); err != nil {
		t.Fatalf("upsert sans artistId: %v", err)
	}

	tracks, err := queries.ListLikedTracksByTitle(ctx, userID)
	if err != nil {
		t.Fatalf("ListLikedTracksByTitle: %v", err)
	}
	byTitle := map[string]dbgen.LikedTrack{}
	for _, tr := range tracks {
		byTitle[tr.Title] = tr
	}
	if !byTitle["Has artist"].ArtistID.Valid || byTitle["Has artist"].ArtistID.Int64 != 99 {
		t.Errorf("ArtistID = %+v, attendu {99 true}", byTitle["Has artist"].ArtistID)
	}
	if byTitle["No artist"].ArtistID.Valid {
		t.Errorf("ArtistID = %+v, attendu invalide (NULL)", byTitle["No artist"].ArtistID)
	}
}
