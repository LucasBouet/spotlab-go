package blend

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/db"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
	"github.com/pressly/goose/v3"
)

var silenceGooseOnce sync.Once

func newTestQueries(t *testing.T) *dbgen.Queries {
	t.Helper()
	silenceGooseOnce.Do(func() { goose.SetLogger(goose.NopLogger()) })

	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("ouverture de la base de test: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return dbgen.New(conn)
}

func createUser(t *testing.T, queries *dbgen.Queries, email, name string) dbgen.User {
	t.Helper()
	nameParam := sql.NullString{}
	if name != "" {
		nameParam = sql.NullString{String: name, Valid: true}
	}
	user, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: email, Name: nameParam, PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création de l'utilisateur %s: %v", email, err)
	}
	return user
}

func likeTrack(t *testing.T, queries *dbgen.Queries, userID string, deezerTrackID int64, title, artist string) {
	t.Helper()
	if err := queries.UpsertLikedTrack(context.Background(), dbgen.UpsertLikedTrackParams{
		ID: idgen.New(), UserID: userID, DeezerTrackID: deezerTrackID,
		Title: title, ArtistName: artist, AlbumTitle: "Album " + title, AlbumCover: "cover-" + title + ".jpg", Duration: 200,
	}); err != nil {
		t.Fatalf("UpsertLikedTrack: %v", err)
	}
}

func acceptFriendship(t *testing.T, queries *dbgen.Queries, requesterID, addresseeID string) {
	t.Helper()
	id := idgen.New()
	if err := queries.CreateFriendship(context.Background(), dbgen.CreateFriendshipParams{
		ID: id, RequesterID: requesterID, AddresseeID: addresseeID,
	}); err != nil {
		t.Fatalf("CreateFriendship: %v", err)
	}
	if err := queries.AcceptFriendship(context.Background(), id); err != nil {
		t.Fatalf("AcceptFriendship: %v", err)
	}
}
