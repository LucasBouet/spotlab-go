// Package social implements friends, friend requests, and presence —
// GET /api/social, GET /api/friends/activity, /api/social/requests/**,
// /api/social/friends/**.
package social

import (
	"database/sql"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// FriendTrackDTO / FriendActivityDTO mirror the Android client's
// FriendTrackDto / FriendActivityDto exactly.
type FriendTrackDTO struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Cover  string `json:"cover"`
}

type FriendActivityDTO struct {
	Online    bool            `json:"online"`
	IsPlaying bool            `json:"isPlaying"`
	Track     *FriendTrackDTO `json:"track"`
}

// FriendDTO mirrors FriendDto. Name is nullable for the same reason as
// UserDto.name — the column allows null and this DTO forwards it
// untouched; a non-nullable field crashes decoding the whole friend list
// for one nameless account. friendshipId and userId are two different ids
// for the same row: DELETE /api/social/friends/{id} wants the first, a jam
// invite wants the second — mixing them up is a 404, not a compile error.
type FriendDTO struct {
	FriendshipID string            `json:"friendshipId"`
	UserID       string            `json:"userId"`
	Name         *string           `json:"name"`
	Email        string            `json:"email"`
	Activity     FriendActivityDTO `json:"activity"`
}

// FriendRequestDTO mirrors FriendRequestDto. It carries the friendship row
// id, never a user id — a pending request cannot be used to invite anyone
// to anything until it's accepted.
type FriendRequestDTO struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Email     string  `json:"email"`
	CreatedAt string  `json:"createdAt"`
}

type SocialDataDTO struct {
	Friends  []FriendDTO        `json:"friends"`
	Incoming []FriendRequestDTO `json:"incoming"`
	Outgoing []FriendRequestDTO `json:"outgoing"`
}

func nullableName(name sql.NullString) *string {
	if !name.Valid {
		return nil
	}
	return &name.String
}

func friendRequestDTO(id string, name sql.NullString, email string, createdAt time.Time) FriendRequestDTO {
	return FriendRequestDTO{
		ID: id, Name: nullableName(name), Email: email,
		CreatedAt: createdAt.Format(time.RFC3339),
	}
}

// FriendActivityUpdateDTO mirrors FriendActivityUpdateDto — one row of
// GET /api/friends/activity, the poll payload.
type FriendActivityUpdateDTO struct {
	UserID   string            `json:"userId"`
	Activity FriendActivityDTO `json:"activity"`
}

// buildSocialData splits every friendship row the user is party to into
// friends/incoming/outgoing, exactly like the old server's getSocialData:
// ACCEPTED becomes a friend; PENDING becomes outgoing if this user sent it,
// incoming otherwise. activity is computed by the caller (it needs the
// injected presence function, which this package doesn't own — see
// handlers.go) and passed in per friend.
func buildSocialData(userID string, rows []db.FriendshipWithUsers, activityFor func(otherUserID string) FriendActivityDTO) SocialDataDTO {
	data := SocialDataDTO{
		Friends:  []FriendDTO{},
		Incoming: []FriendRequestDTO{},
		Outgoing: []FriendRequestDTO{},
	}

	for _, row := range rows {
		isRequester := row.RequesterID == userID
		otherID, otherName, otherEmail := row.AddresseeID, row.AddresseeName, row.AddresseeEmail
		if !isRequester {
			otherID, otherName, otherEmail = row.RequesterID, row.RequesterName, row.RequesterEmail
		}

		switch {
		case row.Status == "ACCEPTED":
			data.Friends = append(data.Friends, FriendDTO{
				FriendshipID: row.ID, UserID: otherID, Name: nullableName(otherName),
				Email: otherEmail, Activity: activityFor(otherID),
			})
		case isRequester:
			data.Outgoing = append(data.Outgoing, friendRequestDTO(row.ID, otherName, otherEmail, row.CreatedAt))
		default:
			data.Incoming = append(data.Incoming, friendRequestDTO(row.ID, otherName, otherEmail, row.CreatedAt))
		}
	}
	return data
}
