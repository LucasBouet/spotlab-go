package auth

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/db"
	"github.com/pressly/goose/v3"
)

var silenceGooseOnce sync.Once

// newTestDB opens a fresh, fully migrated SQLite database in a temp
// directory — real SQLite, real goose migrations, not a mock. Each test
// gets its own file, so tests never interfere with each other.
func newTestDB(t *testing.T) *Service {
	t.Helper()
	// goose logs "OK <migration>" on every Open; useful on a real server's
	// single startup, just noise across dozens of per-test databases.
	silenceGooseOnce.Do(func() { goose.SetLogger(goose.NopLogger()) })

	path := filepath.Join(t.TempDir(), "test.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatalf("ouverture de la base de test: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewService(conn)
}
