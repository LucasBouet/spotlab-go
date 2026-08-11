package social

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

type testUser struct {
	ID    string
	Email string
}

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

func createTestUser(t *testing.T, queries *dbgen.Queries, email, name string) testUser {
	t.Helper()
	nameValue := sql.NullString{}
	if name != "" {
		nameValue = sql.NullString{String: name, Valid: true}
	}
	user, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: email, Name: nameValue, PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création de l'utilisateur %s: %v", email, err)
	}
	return testUser{ID: user.ID, Email: user.Email}
}
