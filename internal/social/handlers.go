package social

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// ActivityFunc computes a user's live presence and now-playing state. Until
// Phase 6's sync engine exists there is no connection/playback state to
// read — main.go passes a stub that always answers the zero value
// (offline, nothing playing). Phase 6 swaps in the real Hub without this
// package changing.
type ActivityFunc func(userID string) FriendActivityDTO

// Mount registers the social routes, all behind requireAuth.
func Mount(r chi.Router, requireAuth func(http.Handler) http.Handler, queries *db.Queries, activity ActivityFunc) {
	r.Group(func(r chi.Router) {
		r.Use(requireAuth)
		r.Get("/api/social", handleSocial(queries, activity))
		r.Get("/api/friends/activity", handleFriendActivity(queries, activity))
		r.Post("/api/social/requests", handleSendRequest(queries))
		r.Post("/api/social/requests/{id}", handleRequestOp(queries))
		r.Delete("/api/social/friends/{id}", handleRemoveFriend(queries))
	})
}

func handleSocial(queries *db.Queries, activity ActivityFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.ListFriendshipsForUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		apihttp.JSON(w, http.StatusOK, buildSocialData(user.ID, rows, activity))
	}
}

func handleFriendActivity(queries *db.Queries, activity ActivityFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		rows, err := queries.ListAcceptedFriendshipsForUser(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}
		updates := make([]FriendActivityUpdateDTO, 0, len(rows))
		for _, row := range rows {
			otherID := row.AddresseeID
			if row.RequesterID != user.ID {
				otherID = row.RequesterID
			}
			updates = append(updates, FriendActivityUpdateDTO{UserID: otherID, Activity: activity(otherID)})
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"activities": updates})
	}
}

func handleSendRequest(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body struct {
			Email string `json:"email"`
		}
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		message, err := SendFriendRequest(r.Context(), queries, user.ID, user.Email, body.Email)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"message": message})
	}
}

func handleRequestOp(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		requestID := chi.URLParam(r, "id")
		var body struct {
			Op string `json:"op"`
		}
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		var err error
		switch body.Op {
		case "accept":
			err = AcceptFriendRequest(r.Context(), queries, user.ID, requestID)
		case "decline":
			err = DeclineFriendRequest(r.Context(), queries, user.ID, requestID)
		case "cancel":
			err = CancelFriendRequest(r.Context(), queries, user.ID, requestID)
		default:
			apihttp.Error(w, http.StatusBadRequest, "Opération inconnue.")
			return
		}
		if err != nil {
			writeServiceError(w, err)
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleRemoveFriend(queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		if err := RemoveFriend(r.Context(), queries, user.ID, chi.URLParam(r, "id")); err != nil {
			writeServiceError(w, err)
			return
		}
		apihttp.JSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidEmail), errors.Is(err, ErrSelfRequest):
		apihttp.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrUserNotFound), errors.Is(err, ErrRequestNotFound), errors.Is(err, ErrFriendNotFound):
		apihttp.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAlreadyFriends), errors.Is(err, ErrRequestAlreadySent):
		apihttp.Error(w, http.StatusConflict, err.Error())
	default:
		apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
	}
}
