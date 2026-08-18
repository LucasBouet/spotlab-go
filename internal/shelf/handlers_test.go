package shelf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lucasbouet/spotlab-go/internal/auth"
	"github.com/lucasbouet/spotlab-go/internal/db"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
	"github.com/pressly/goose/v3"
)

var silenceGooseOnce sync.Once

func newTestQueries(t *testing.T) (*dbgen.Queries, dbgen.User) {
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
	return queries, user
}

func newShelfRequest(method, path string, actingUser dbgen.User, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(auth.ContextWithUserForTesting(req.Context(), actingUser))
}

func TestShelfOrderRoundTripsPositionsAndPinned(t *testing.T) {
	queries, user := newTestQueries(t)

	putReq := newShelfRequest(http.MethodPut, "/api/shelf-order", user,
		`{"positions":{"artist-all-1":0,"genre-metalcore":1},"pinned":["genre-metalcore"]}`)
	putRec := httptest.NewRecorder()
	handlePut(queries)(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	getReq := newShelfRequest(http.MethodGet, "/api/shelf-order", user, "")
	getRec := httptest.NewRecorder()
	handleGet(queries)(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getRec.Code)
	}

	var got shelfOrderDTO
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("décodage réponse: %v", err)
	}
	if got.Positions["artist-all-1"] != 0 || got.Positions["genre-metalcore"] != 1 {
		t.Errorf("Positions = %v, attendu artist-all-1:0, genre-metalcore:1", got.Positions)
	}
	if len(got.Pinned) != 1 || got.Pinned[0] != "genre-metalcore" {
		t.Errorf("Pinned = %v, attendu [genre-metalcore]", got.Pinned)
	}
}

func TestShelfOrderIsScopedPerUser(t *testing.T) {
	queries, userA := newTestQueries(t)
	userB, err := queries.CreateUser(context.Background(), dbgen.CreateUserParams{
		ID: idgen.New(), Email: "b@example.com", PasswordHash: "x", Role: "USER",
	})
	if err != nil {
		t.Fatalf("création du second utilisateur: %v", err)
	}

	putReq := newShelfRequest(http.MethodPut, "/api/shelf-order", userA, `{"positions":{"x":0},"pinned":["x"]}`)
	handlePut(queries)(httptest.NewRecorder(), putReq)

	getReq := newShelfRequest(http.MethodGet, "/api/shelf-order", userB, "")
	getRec := httptest.NewRecorder()
	handleGet(queries)(getRec, getReq)

	var got shelfOrderDTO
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("décodage réponse: %v", err)
	}
	if len(got.Positions) != 0 || len(got.Pinned) != 0 {
		t.Errorf("les préférences d'un autre compte ne doivent jamais apparaître: %+v", got)
	}
}

func TestShelfOrderPutIsIdempotentOverwrite(t *testing.T) {
	queries, user := newTestQueries(t)

	first := newShelfRequest(http.MethodPut, "/api/shelf-order", user, `{"positions":{"a":0},"pinned":["a"]}`)
	handlePut(queries)(httptest.NewRecorder(), first)

	// Second call unpins "a" and moves it — the whole arrangement is
	// replaced, not merged: no stray "pinned" survives from the first call.
	second := newShelfRequest(http.MethodPut, "/api/shelf-order", user, `{"positions":{"a":5},"pinned":[]}`)
	handlePut(queries)(httptest.NewRecorder(), second)

	getReq := newShelfRequest(http.MethodGet, "/api/shelf-order", user, "")
	getRec := httptest.NewRecorder()
	handleGet(queries)(getRec, getReq)
	var got shelfOrderDTO
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("décodage réponse: %v", err)
	}
	if got.Positions["a"] != 5 {
		t.Errorf("Positions[a] = %d, attendu 5", got.Positions["a"])
	}
	if len(got.Pinned) != 0 {
		t.Errorf("Pinned = %v, attendu vide (désépinglé par le second appel)", got.Pinned)
	}
}
