package admin

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

func createUser(t *testing.T, queries *dbgen.Queries, email, role string) dbgen.User {
	t.Helper()
	user, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: email, PasswordHash: "x", Role: role,
	})
	if err != nil {
		t.Fatalf("création de l'utilisateur de test: %v", err)
	}
	return user
}
