package library

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/db"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
	"github.com/pressly/goose/v3"
)

var silenceGooseOnce sync.Once

// newTestQueries opens a fresh, fully migrated SQLite database in a temp
// directory and returns a query layer plus a real user row to attach
// fixtures to — every library query is scoped by user_id, so tests need a
// real (if minimal) account to exist first.
func newTestQueries(t *testing.T) (*dbgen.Queries, string) {
	t.Helper()
	silenceGooseOnce.Do(func() { goose.SetLogger(goose.NopLogger()) })

	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("ouverture de la base de test: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	queries := dbgen.New(conn)
	user, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: "test@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création de l'utilisateur de test: %v", err)
	}
	return queries, user.ID
}
