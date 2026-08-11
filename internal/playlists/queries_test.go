package playlists

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

func addTrack(t *testing.T, queries *dbgen.Queries, playlistID string, deezerTrackID int64, cover string) string {
	t.Helper()
	rowKey := idgen.New()
	if err := queries.InsertPlaylistTrack(context.Background(), dbgen.InsertPlaylistTrackParams{
		ID: rowKey, PlaylistID: playlistID, DeezerTrackID: deezerTrackID,
		Title: "T", ArtistName: "A", AlbumTitle: "Alb", AlbumCover: cover, Duration: 200,
	}); err != nil {
		t.Fatalf("InsertPlaylistTrack: %v", err)
	}
	return rowKey
}

func TestGetPlaylistOwnedScopesByUser(t *testing.T) {
	queries, userA := newTestQueries(t)
	ctx := context.Background()
	userB, err := queries.CreateUser(ctx, dbgen.CreateUserParams{
		ID: idgen.New(), Email: "b@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du second utilisateur: %v", err)
	}
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userA, Name: "Mine"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}

	if _, err := queries.GetPlaylistOwned(ctx, dbgen.GetPlaylistOwnedParams{ID: playlist.ID, UserID: userA}); err != nil {
		t.Errorf("le propriétaire devrait pouvoir accéder à sa playlist: %v", err)
	}
	if _, err := queries.GetPlaylistOwned(ctx, dbgen.GetPlaylistOwnedParams{ID: playlist.ID, UserID: userB.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("un autre utilisateur doit obtenir sql.ErrNoRows, pas %v — la playlist ne doit pas fuiter son existence", err)
	}
}

func TestListPlaylistsByUserAggregatesCountAndCovers(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "P"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}

	for i := range 6 {
		addTrack(t, queries, playlist.ID, int64(i), "cover"+string(rune('A'+i))+".jpg")
	}

	rows, err := queries.ListPlaylistsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListPlaylistsByUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, attendu 1", len(rows))
	}
	if rows[0].TrackCount != 6 {
		t.Errorf("TrackCount = %d, attendu 6 (tous les titres comptent, même hors de l'aperçu)", rows[0].TrackCount)
	}
	if len(rows[0].Covers) != 4 {
		t.Errorf("len(Covers) = %d, attendu 4 (aperçu plafonné)", len(rows[0].Covers))
	}
}

func TestListPlaylistsByUserCoversIsEmptyNotNullForEmptyPlaylist(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	if _, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "Empty"}); err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}

	rows, err := queries.ListPlaylistsByUser(ctx, userID)
	if err != nil {
		t.Fatalf("ListPlaylistsByUser: %v", err)
	}
	if rows[0].Covers == nil {
		t.Error("Covers = nil, attendu un slice vide non-nil (json_group_array sur zéro ligne doit décoder proprement)")
	}
	if rows[0].TrackCount != 0 {
		t.Errorf("TrackCount = %d, attendu 0", rows[0].TrackCount)
	}
}

func TestFindFirstPlaylistTrackByDeezerIDPicksEarliestAdded(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "P"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}

	first := addTrack(t, queries, playlist.ID, 111, "a.jpg")
	addTrack(t, queries, playlist.ID, 111, "a.jpg") // duplicate, same Deezer track

	found, err := queries.FindFirstPlaylistTrackByDeezerID(ctx, dbgen.FindFirstPlaylistTrackByDeezerIDParams{
		PlaylistID: playlist.ID, DeezerTrackID: 111,
	})
	if err != nil {
		t.Fatalf("FindFirstPlaylistTrackByDeezerID: %v", err)
	}
	if found.ID != first {
		t.Errorf("rowKey trouvé = %s, attendu le premier ajouté %s — le toggle doit toujours retirer le même exemplaire en premier", found.ID, first)
	}
}

func TestFindFirstPlaylistTrackByDeezerIDNoRowsWhenAbsent(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "P"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	_, err = queries.FindFirstPlaylistTrackByDeezerID(ctx, dbgen.FindFirstPlaylistTrackByDeezerIDParams{
		PlaylistID: playlist.ID, DeezerTrackID: 999,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("err = %v, attendu sql.ErrNoRows", err)
	}
}

func TestDeletePlaylistTrackOwnedIgnoresPlaylistIDEntirely(t *testing.T) {
	// Faithful port of a real quirk: the old server's DELETE
	// .../tracks/{rowKey} scopes only by playlist ownership, never checks
	// the playlist id in the URL against the row's actual playlist.
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "P"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	rowKey := addTrack(t, queries, playlist.ID, 1, "a.jpg")

	rows, err := queries.DeletePlaylistTrackOwned(ctx, dbgen.DeletePlaylistTrackOwnedParams{ID: rowKey, UserID: userID})
	if err != nil {
		t.Fatalf("DeletePlaylistTrackOwned: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, attendu 1", rows)
	}
}

func TestDeletePlaylistTrackOwnedRejectsWrongOwner(t *testing.T) {
	queries, userA := newTestQueries(t)
	ctx := context.Background()
	userB, err := queries.CreateUser(ctx, dbgen.CreateUserParams{
		ID: idgen.New(), Email: "b@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du second utilisateur: %v", err)
	}
	playlist, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userA, Name: "P"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	rowKey := addTrack(t, queries, playlist.ID, 1, "a.jpg")

	rows, err := queries.DeletePlaylistTrackOwned(ctx, dbgen.DeletePlaylistTrackOwnedParams{ID: rowKey, UserID: userB.ID})
	if err != nil {
		t.Fatalf("DeletePlaylistTrackOwned: %v", err)
	}
	if rows != 0 {
		t.Error("un utilisateur ne doit jamais pouvoir retirer un titre de la playlist de quelqu'un d'autre")
	}
}

func TestListPlaylistMembership(t *testing.T) {
	queries, userID := newTestQueries(t)
	ctx := context.Background()
	withTrack, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "Has it"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	without, err := queries.CreatePlaylist(ctx, dbgen.CreatePlaylistParams{ID: idgen.New(), UserID: userID, Name: "Without"})
	if err != nil {
		t.Fatalf("CreatePlaylist: %v", err)
	}
	addTrack(t, queries, withTrack.ID, 111, "a.jpg")

	rows, err := queries.ListPlaylistMembership(ctx, dbgen.ListPlaylistMembershipParams{DeezerTrackID: 111, UserID: userID})
	if err != nil {
		t.Fatalf("ListPlaylistMembership: %v", err)
	}
	byID := map[string]bool{}
	for _, r := range rows {
		byID[r.ID] = r.HasTrack
	}
	if !byID[withTrack.ID] {
		t.Error("la playlist qui contient le titre doit avoir hasTrack=true")
	}
	if byID[without.ID] {
		t.Error("la playlist qui ne contient pas le titre doit avoir hasTrack=false")
	}
}
