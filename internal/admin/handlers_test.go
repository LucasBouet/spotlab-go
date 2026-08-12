package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func newAdminRequest(method, path string, actingAdmin db.User, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(auth.ContextWithUserForTesting(req.Context(), actingAdmin))
}

func TestHandleListUsersReturnsAllOrderedByCreation(t *testing.T) {
	queries := newTestQueries(t)
	first := createUser(t, queries, "a@example.com", "ADMIN")
	second := createUser(t, queries, "b@example.com", "USER")

	req := newAdminRequest(http.MethodGet, "/api/admin/users", first, "")
	w := httptest.NewRecorder()
	handleListUsers(queries)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, attendu 200", w.Code)
	}
	var body ListUsersResponseDTO
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if len(body.Users) != 2 || body.Users[0].ID != first.ID || body.Users[1].ID != second.ID {
		t.Errorf("Users = %+v, attendu [%s, %s] dans l'ordre de création", body.Users, first.ID, second.ID)
	}
}

func TestHandleSetUserRoleRejectsSelfModification(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	r := chi.NewRouter()
	r.Patch("/api/admin/users/{id}", handleSetUserRole(queries))

	req := newAdminRequest(http.MethodPatch, "/api/admin/users/"+adminUser.ID, adminUser, `{"role":"USER"}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, attendu 403 (un admin ne peut pas changer son propre rôle)", w.Code)
	}

	unchanged, err := queries.ListUsers(req.Context())
	if err != nil || unchanged[0].Role != "ADMIN" {
		t.Errorf("le rôle n'aurait pas dû changer: %+v (err=%v)", unchanged, err)
	}
}

func TestHandleSetUserRoleRejectsInvalidRole(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")
	target := createUser(t, queries, "target@example.com", "USER")

	r := chi.NewRouter()
	r.Patch("/api/admin/users/{id}", handleSetUserRole(queries))

	req := newAdminRequest(http.MethodPatch, "/api/admin/users/"+target.ID, adminUser, `{"role":"SUPERUSER"}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, attendu 400", w.Code)
	}
}

func TestHandleSetUserRole404ForUnknownUser(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	r := chi.NewRouter()
	r.Patch("/api/admin/users/{id}", handleSetUserRole(queries))

	req := newAdminRequest(http.MethodPatch, "/api/admin/users/does-not-exist", adminUser, `{"role":"ADMIN"}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, attendu 404", w.Code)
	}
}

func TestHandleSetUserRolePromotesTarget(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")
	target := createUser(t, queries, "target@example.com", "USER")

	r := chi.NewRouter()
	r.Patch("/api/admin/users/{id}", handleSetUserRole(queries))

	req := newAdminRequest(http.MethodPatch, "/api/admin/users/"+target.ID, adminUser, `{"role":"ADMIN"}`)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, attendu 200", w.Code)
	}
	users, err := queries.ListUsers(req.Context())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if u.ID == target.ID && u.Role != "ADMIN" {
			t.Errorf("role = %q, attendu ADMIN après promotion", u.Role)
		}
	}
}

func TestHandleDeleteUserRejectsSelfDeletion(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	r := chi.NewRouter()
	r.Delete("/api/admin/users/{id}", handleDeleteUser(queries))

	req := newAdminRequest(http.MethodDelete, "/api/admin/users/"+adminUser.ID, adminUser, "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, attendu 403", w.Code)
	}
}

func TestHandleDeleteUser404ForUnknownUser(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	r := chi.NewRouter()
	r.Delete("/api/admin/users/{id}", handleDeleteUser(queries))

	req := newAdminRequest(http.MethodDelete, "/api/admin/users/does-not-exist", adminUser, "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, attendu 404", w.Code)
	}
}

func TestHandleDeleteUserRemovesTarget(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")
	target := createUser(t, queries, "target@example.com", "USER")

	r := chi.NewRouter()
	r.Delete("/api/admin/users/{id}", handleDeleteUser(queries))

	req := newAdminRequest(http.MethodDelete, "/api/admin/users/"+target.ID, adminUser, "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, attendu 200", w.Code)
	}
	users, err := queries.ListUsers(req.Context())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0].ID != adminUser.ID {
		t.Errorf("users = %+v, attendu seulement l'admin restant", users)
	}
}

func TestHandleGetSettingsReturnsDefaultsWhenUnset(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	req := newAdminRequest(http.MethodGet, "/api/admin/settings", adminUser, "")
	w := httptest.NewRecorder()
	handleGetSettings(queries)(w, req)

	var body SettingsResponseDTO
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if body.Settings["site_name"] != "Spotlab" {
		t.Errorf("site_name = %q, attendu le défaut Spotlab", body.Settings["site_name"])
	}
	if body.Settings["registration_enabled"] != "false" {
		t.Errorf("registration_enabled = %q, attendu le défaut false", body.Settings["registration_enabled"])
	}
	if len(body.Definitions) != 2 {
		t.Errorf("len(Definitions) = %d, attendu 2", len(body.Definitions))
	}
}

func TestHandleUpdateSettingsPartialUpdate(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	req := newAdminRequest(http.MethodPatch, "/api/admin/settings", adminUser, `{"site_name":"Ma Radio"}`)
	w := httptest.NewRecorder()
	handleUpdateSettings(queries)(w, req)

	var body SettingsResponseDTO
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if body.Settings["site_name"] != "Ma Radio" {
		t.Errorf("site_name = %q, attendu Ma Radio", body.Settings["site_name"])
	}
	// registration_enabled wasn't in the request body — must be left alone.
	if body.Settings["registration_enabled"] != "false" {
		t.Errorf("registration_enabled = %q, attendu inchangé (false)", body.Settings["registration_enabled"])
	}
}

func TestHandleUpdateSettingsAcceptsRealBooleanForRegistrationEnabled(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	req := newAdminRequest(http.MethodPatch, "/api/admin/settings", adminUser, `{"registration_enabled":true}`)
	w := httptest.NewRecorder()
	handleUpdateSettings(queries)(w, req)

	var body SettingsResponseDTO
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if body.Settings["registration_enabled"] != "true" {
		t.Errorf("registration_enabled = %q, attendu true", body.Settings["registration_enabled"])
	}
}

func TestHandleUpdateSettingsIgnoresBlankStringValue(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")
	_ = queries.SetAppSetting(context.Background(), db.SetAppSettingParams{Key: "site_name", Value: "Existing"})

	r := newAdminRequest(http.MethodPatch, "/api/admin/settings", adminUser, `{"site_name":"   "}`)
	w := httptest.NewRecorder()
	handleUpdateSettings(queries)(w, r)

	var body SettingsResponseDTO
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("décodage: %v", err)
	}
	if body.Settings["site_name"] != "Existing" {
		t.Errorf("site_name = %q, attendu inchangé (Existing) — une valeur vide/blanche doit être ignorée", body.Settings["site_name"])
	}
}

func TestRequireAdminRejectsNonAdmin(t *testing.T) {
	queries := newTestQueries(t)
	plainUser := createUser(t, queries, "user@example.com", "USER")

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := newAdminRequest(http.MethodGet, "/api/admin/users", plainUser, "")
	w := httptest.NewRecorder()
	requireAdmin(next).ServeHTTP(w, req)

	if called {
		t.Error("un utilisateur non-admin n'aurait jamais dû atteindre le handler protégé")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, attendu 403", w.Code)
	}
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	queries := newTestQueries(t)
	adminUser := createUser(t, queries, "admin@example.com", "ADMIN")

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })

	req := newAdminRequest(http.MethodGet, "/api/admin/users", adminUser, "")
	w := httptest.NewRecorder()
	requireAdmin(next).ServeHTTP(w, req)

	if !called {
		t.Error("un admin aurait dû atteindre le handler protégé")
	}
}
