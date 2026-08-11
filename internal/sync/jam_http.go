package sync

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/auth"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// JamOpDTO mirrors the Android client's JamOpDto — the single request
// shape for every jam operation, selected by Op.
type JamOpDTO struct {
	Op           string  `json:"op"`
	FriendUserID *string `json:"friendUserId,omitempty"`
	JamID        *string `json:"jamId,omitempty"`
	DeviceID     *string `json:"deviceId,omitempty"`
}

type JamOpResultDTO struct {
	OK    bool    `json:"ok"`
	JamID *string `json:"jamId,omitempty"`
}

// displayName mirrors the old server's displayName helper used throughout
// the jam route: a trimmed name if there is one, the email otherwise.
func displayName(name sql.NullString, email string) string {
	if name.Valid {
		if trimmed := strings.TrimSpace(name.String); trimmed != "" {
			return trimmed
		}
	}
	return email
}

// handleJam is POST /api/jam — the single entry point for every jam
// membership operation, selected by body.op. Playback commands keep
// flowing through /api/sync/command; once a user is a jam member the sync
// layer already routes those to the shared state (applyJamCommandLocked).
func handleJam(hub *Hub, queries *db.Queries) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserFromContext(r.Context())
		var body JamOpDTO
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}
		me := displayName(user.Name, user.Email)

		switch body.Op {
		case "invite":
			handleJamInvite(w, r, hub, queries, user.ID, me, body)
		case "accept":
			handleJamAccept(w, r, hub, user.ID, me, body)
		case "decline":
			handleJamDecline(w, r, hub, user.ID, body)
		case "leave":
			hub.LeaveJam(r.Context(), user.ID)
			apihttp.JSON(w, http.StatusOK, JamOpResultDTO{OK: true})
		case "stop":
			handleJamStop(w, r, hub, user.ID)
		default:
			apihttp.Error(w, http.StatusBadRequest, "Opération inconnue.")
		}
	}
}

func handleJamInvite(w http.ResponseWriter, r *http.Request, hub *Hub, queries *db.Queries, userID, myName string, body JamOpDTO) {
	if body.FriendUserID == nil || *body.FriendUserID == "" || body.DeviceID == nil || *body.DeviceID == "" {
		apihttp.Error(w, http.StatusBadRequest, "Requête invalide.")
		return
	}
	friendID := *body.FriendUserID
	if friendID == userID {
		apihttp.Error(w, http.StatusBadRequest, "Vous ne pouvez pas vous inviter vous-même.")
		return
	}

	// Only accepted friends can be invited (either direction of the row).
	friendship, err := queries.FindFriendshipBetween(r.Context(), db.FindFriendshipBetweenParams{UserA: userID, UserB: friendID})
	if errors.Is(err, sql.ErrNoRows) || friendship.Status != "ACCEPTED" {
		apihttp.Error(w, http.StatusForbidden, "Cet utilisateur n'est pas dans vos amis.")
		return
	} else if err != nil {
		apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
		return
	}

	friend, err := queries.GetUserByID(r.Context(), friendID)
	if errors.Is(err, sql.ErrNoRows) {
		apihttp.Error(w, http.StatusNotFound, "Utilisateur introuvable.")
		return
	} else if err != nil {
		apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
		return
	}

	jamID, err := hub.InviteToJam(r.Context(), userID, myName, *body.DeviceID, friend.ID, displayName(friend.Name, friend.Email))
	if err != nil {
		apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
		return
	}
	apihttp.JSON(w, http.StatusOK, JamOpResultDTO{OK: true, JamID: &jamID})
}

func handleJamAccept(w http.ResponseWriter, r *http.Request, hub *Hub, userID, myName string, body JamOpDTO) {
	if body.JamID == nil || *body.JamID == "" || body.DeviceID == nil || *body.DeviceID == "" {
		apihttp.Error(w, http.StatusBadRequest, "Requête invalide.")
		return
	}
	if err := hub.AcceptJamInvite(r.Context(), *body.JamID, userID, myName, *body.DeviceID); err != nil {
		apihttp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	apihttp.JSON(w, http.StatusOK, JamOpResultDTO{OK: true})
}

func handleJamDecline(w http.ResponseWriter, r *http.Request, hub *Hub, userID string, body JamOpDTO) {
	if body.JamID == nil || *body.JamID == "" {
		apihttp.Error(w, http.StatusBadRequest, "Requête invalide.")
		return
	}
	hub.DeclineJamInvite(r.Context(), *body.JamID, userID)
	apihttp.JSON(w, http.StatusOK, JamOpResultDTO{OK: true})
}

func handleJamStop(w http.ResponseWriter, r *http.Request, hub *Hub, userID string) {
	if err := hub.StopJam(r.Context(), userID); err != nil {
		apihttp.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	apihttp.JSON(w, http.StatusOK, JamOpResultDTO{OK: true})
}
