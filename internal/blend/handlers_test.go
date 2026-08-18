package blend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func newBlendRequest(method, path string, actingUser dbgen.User, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(auth.ContextWithUserForTesting(req.Context(), actingUser))
}

func newBlendRouter(queries *dbgen.Queries) http.Handler {
	r := chi.NewRouter()
	Mount(r, func(next http.Handler) http.Handler { return next }, queries)
	return r
}

func TestCanonicalPairIsOrderIndependent(t *testing.T) {
	a1, b1 := canonicalPair("x", "y")
	a2, b2 := canonicalPair("y", "x")
	if a1 != a2 || b1 != b2 {
		t.Errorf("canonicalPair(x,y) = (%s,%s), canonicalPair(y,x) = (%s,%s) — doivent être identiques", a1, b1, a2, b2)
	}
}

func TestHandleCreateRejectsNonFriends(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	router := newBlendRouter(queries)

	req := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, attendu 403 (pas encore amis)", rec.Code)
	}
}

func TestHandleCreateIsIdempotentRegardlessOfWhoInitiates(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	acceptFriendship(t, queries, alice.ID, bob.ID)
	router := newBlendRouter(queries)

	first := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("premier appel: status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}
	var firstBlend BlendDTO
	if err := json.Unmarshal(firstRec.Body.Bytes(), &firstBlend); err != nil {
		t.Fatalf("décodage: %v", err)
	}

	// Bob creates "the same" blend from his side — must return the existing
	// one (200, same id), not a duplicate.
	second := newBlendRequest(http.MethodPost, "/api/blends", bob, `{"friendUserId":"`+alice.ID+`"}`)
	secondRec := httptest.NewRecorder()
	router.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusOK {
		t.Errorf("deuxième appel (déjà existant): status = %d, attendu 200", secondRec.Code)
	}
	var secondBlend BlendDTO
	if err := json.Unmarshal(secondRec.Body.Bytes(), &secondBlend); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if secondBlend.ID != firstBlend.ID {
		t.Errorf("ID = %q, attendu le même blend %q qu'à la création", secondBlend.ID, firstBlend.ID)
	}
}

func TestHandleCreateNamesThePartnerNotTheCaller(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	acceptFriendship(t, queries, alice.ID, bob.ID)
	router := newBlendRouter(queries)

	aliceReq := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	aliceRec := httptest.NewRecorder()
	router.ServeHTTP(aliceRec, aliceReq)
	var aliceView BlendDTO
	_ = json.Unmarshal(aliceRec.Body.Bytes(), &aliceView)
	if aliceView.PartnerUserID != bob.ID || !strings.Contains(aliceView.Title, "Bob") {
		t.Errorf("vue d'Alice: PartnerUserID=%q Title=%q, attendu Bob comme partenaire", aliceView.PartnerUserID, aliceView.Title)
	}

	bobReq := newBlendRequest(http.MethodPost, "/api/blends", bob, `{"friendUserId":"`+alice.ID+`"}`)
	bobRec := httptest.NewRecorder()
	router.ServeHTTP(bobRec, bobReq)
	var bobView BlendDTO
	_ = json.Unmarshal(bobRec.Body.Bytes(), &bobView)
	if bobView.PartnerUserID != alice.ID || !strings.Contains(bobView.Title, "Alice") {
		t.Errorf("vue de Bob: PartnerUserID=%q Title=%q, attendu Alice comme partenaire — même ligne, deux points de vue", bobView.PartnerUserID, bobView.Title)
	}
}

func TestHandleListMixesBothMembersLikedTracksWithAttribution(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	acceptFriendship(t, queries, alice.ID, bob.ID)
	likeTrack(t, queries, alice.ID, 1, "Alice Song", "Artist A")
	likeTrack(t, queries, bob.ID, 2, "Bob Song", "Artist B")
	router := newBlendRouter(queries)

	create := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	router.ServeHTTP(httptest.NewRecorder(), create)

	listReq := newBlendRequest(http.MethodGet, "/api/blends", alice, "")
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	var body struct {
		Blends []BlendDTO `json:"blends"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if len(body.Blends) != 1 {
		t.Fatalf("len(Blends) = %d, attendu 1", len(body.Blends))
	}
	tracks := body.Blends[0].Tracks
	if len(tracks) != 2 {
		t.Fatalf("len(Tracks) = %d, attendu 2 (un titre de chaque membre)", len(tracks))
	}
	byID := map[int64]BlendTrackDTO{}
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}
	if byID[1].AddedByUserID != alice.ID || byID[1].AddedByName != "Alice" {
		t.Errorf("titre 1 attribué à %+v, attendu Alice", byID[1])
	}
	if byID[2].AddedByUserID != bob.ID || byID[2].AddedByName != "Bob" {
		t.Errorf("titre 2 attribué à %+v, attendu Bob", byID[2])
	}
}

func TestHandleDeleteAllowsEitherMember(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	acceptFriendship(t, queries, alice.ID, bob.ID)
	router := newBlendRouter(queries)

	create := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, create)
	var created BlendDTO
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)

	// Bob (not the creator) deletes it — either member may end a Blend.
	del := newBlendRequest(http.MethodDelete, "/api/blends/"+created.ID, bob, "")
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusOK {
		t.Errorf("status = %d, attendu 200 (l'autre membre peut aussi supprimer)", delRec.Code)
	}
}

func TestHandleDeleteRejectsNonMember(t *testing.T) {
	queries := newTestQueries(t)
	alice := createUser(t, queries, "alice@example.com", "Alice")
	bob := createUser(t, queries, "bob@example.com", "Bob")
	stranger := createUser(t, queries, "stranger@example.com", "Stranger")
	acceptFriendship(t, queries, alice.ID, bob.ID)
	router := newBlendRouter(queries)

	create := newBlendRequest(http.MethodPost, "/api/blends", alice, `{"friendUserId":"`+bob.ID+`"}`)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, create)
	var created BlendDTO
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)

	del := newBlendRequest(http.MethodDelete, "/api/blends/"+created.ID, stranger, "")
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, del)
	if delRec.Code != http.StatusNotFound {
		t.Errorf("status = %d, attendu 404 (ne doit pas fuiter l'existence à un non-membre)", delRec.Code)
	}
}
