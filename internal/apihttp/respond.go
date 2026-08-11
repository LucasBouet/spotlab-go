// Package apihttp holds the pieces every route handler shares: the error
// envelope, JSON helpers, and request logging/recovery middleware.
package apihttp

import (
	"encoding/json"
	"net/http"
)

// errorBody is the exact shape every non-2xx response must have — the
// Android client decodes {"error": "..."} and nothing else. No "message",
// no "errors[]": changing this field name breaks every error path at once.
type errorBody struct {
	Error string `json:"error"`
}

// Error writes a French, user-displayable error message with the given
// status. Message text is written in French throughout, mirroring the
// current server and the Android app's own error surface.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, errorBody{Error: message})
}

// JSON writes v as the response body with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// NoContent answers 200 with an empty body — several of the app's mutation
// endpoints (unlike, rename, decline) never decode a response body at all.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
}

// DecodeJSON reads and decodes a JSON request body. On failure it writes a
// 400 itself and returns false, so handlers can just `if !ok { return }`.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		Error(w, http.StatusBadRequest, "Requête invalide.")
		return false
	}
	return true
}
