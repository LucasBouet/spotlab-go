// Package admin is the account/settings management surface: promote or
// remove a user, and edit the app_settings an operator would otherwise
// have to hand-edit in SQLite. Restricted to accounts with role=ADMIN —
// see docs/PLAN.md's admin panel scope decision (originally dropped from
// v1, restored on request).
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// Mount registers every /api/admin route, behind requireAuth and then
// requireAdmin — a non-admin authenticated user gets a 403, not a 404, so
// the client can tell "you're not allowed" from "this doesn't exist."
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Use(requireAdmin)
		r.Get("/api/admin/users", handleListUsers(queries))
		r.Patch("/api/admin/users/{id}", handleSetUserRole(queries))
		r.Delete("/api/admin/users/{id}", handleDeleteUser(queries))
		r.Get("/api/admin/settings", handleGetSettings(queries))
		r.Patch("/api/admin/settings", handleUpdateSettings(queries))
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.UserFromContext(r.Context()).Role != "ADMIN" {
			apihttp.Error(w, http.StatusForbidden, "Accès réservé aux administrateurs.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleListUsers(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := queries.ListUsers(r.Context())
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		apihttp.JSON(w, http.StatusOK, ListUsersResponseDTO{Users: adminUserDTOs(users)})
	}
}

type setRoleBody struct {
	Role string `json:"role"`
}

// handleSetUserRole is PATCH /api/admin/users/{id}. An admin may not
// change their own role — the same guard the old server's admin/users
// page enforced, so the last admin can't lock everyone (including
// themselves) out.
func handleSetUserRole(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actingAdmin := auth.UserFromContext(r.Context())
		targetID := chi.URLParam(r, "id")
		if targetID == actingAdmin.ID {
			apihttp.Error(w, http.StatusForbidden, "Vous ne pouvez pas modifier votre propre rôle.")
			return
		}

		var body setRoleBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		if body.Role != "ADMIN" && body.Role != "USER" {
			apihttp.Error(w, http.StatusBadRequest, "Rôle invalide.")
			return
		}

		affected, err := queries.SetUserRole(r.Context(), db.SetUserRoleParams{ID: targetID, Role: body.Role})
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		if affected == 0 {
			apihttp.Error(w, http.StatusNotFound, "Utilisateur introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleDeleteUser is DELETE /api/admin/users/{id}. An admin may not
// delete their own account, for the same reason they may not demote
// themselves.
func handleDeleteUser(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actingAdmin := auth.UserFromContext(r.Context())
		targetID := chi.URLParam(r, "id")
		if targetID == actingAdmin.ID {
			apihttp.Error(w, http.StatusForbidden, "Vous ne pouvez pas supprimer votre propre compte.")
			return
		}

		affected, err := queries.DeleteUser(r.Context(), targetID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur serveur.")
			return
		}
		if affected == 0 {
			apihttp.Error(w, http.StatusNotFound, "Utilisateur introuvable.")
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// settingsSnapshot resolves every defined setting to its current value —
// whatever's in app_settings, or the definition's own default when unset.
func settingsSnapshot(ctx context.Context, queries *db.Queries) map[string]string {
	snapshot := make(map[string]string, len(appSettingDefinitions))
	for _, def := range appSettingDefinitions {
		value, err := queries.GetAppSetting(ctx, def.Key)
		if err != nil || value == "" {
			value = def.Default
		}
		snapshot[def.Key] = value
	}
	return snapshot
}

func handleGetSettings(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apihttp.JSON(w, http.StatusOK, SettingsResponseDTO{
			Settings:    settingsSnapshot(r.Context(), queries),
			Definitions: appSettingDefinitions,
		})
	}
}

// handleUpdateSettings is PATCH /api/admin/settings — a partial update:
// absent keys are left alone, matching updateAppSettingsWith in the old
// server. Booleans accept a real JSON boolean or the strings "true"/"on"
// (tolerating a web-form-shaped client, though only Android sends this
// today); strings are trimmed and a blank value is ignored rather than
// clearing the setting.
func handleUpdateSettings(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		for _, def := range appSettingDefinitions {
			raw, present := body[def.Key]
			if !present {
				continue
			}
			value, ok := parseSettingValue(def, raw)
			if !ok {
				continue
			}
			_ = queries.SetAppSetting(r.Context(), db.SetAppSettingParams{Key: def.Key, Value: value})
		}

		apihttp.JSON(w, http.StatusOK, SettingsResponseDTO{
			Settings:    settingsSnapshot(r.Context(), queries),
			Definitions: appSettingDefinitions,
		})
	}
}

func parseSettingValue(def AppSettingDefinitionDTO, raw json.RawMessage) (string, bool) {
	if def.Type == "boolean" {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			return strconv.FormatBool(b), true
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return strconv.FormatBool(s == "on" || s == "true"), true
		}
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}
