package social

import (
	"database/sql"
	"testing"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestBuildSocialDataSplitsByStatusAndDirection(t *testing.T) {
	rows := []db.FriendshipWithUsers{
		{
			ID: "f1", RequesterID: "me", AddresseeID: "friend1", Status: "ACCEPTED",
			CreatedAt: time.Unix(0, 0), RequesterEmail: "me@x.com", RequesterName: sql.NullString{},
			AddresseeEmail: "friend1@x.com", AddresseeName: sql.NullString{String: "Friend One", Valid: true},
		},
		{
			// I sent this one — must land in Outgoing.
			ID: "f2", RequesterID: "me", AddresseeID: "pending1", Status: "PENDING",
			CreatedAt: time.Unix(0, 0), RequesterEmail: "me@x.com",
			AddresseeEmail: "pending1@x.com", AddresseeName: sql.NullString{String: "Pending One", Valid: true},
		},
		{
			// Someone else sent this one — must land in Incoming.
			ID: "f3", RequesterID: "pending2", AddresseeID: "me", Status: "PENDING",
			CreatedAt: time.Unix(0, 0), RequesterEmail: "pending2@x.com",
			RequesterName: sql.NullString{String: "Pending Two", Valid: true}, AddresseeEmail: "me@x.com",
		},
	}

	data := buildSocialData("me", rows, func(otherUserID string) FriendActivityDTO {
		return FriendActivityDTO{}
	})

	if len(data.Friends) != 1 || data.Friends[0].UserID != "friend1" {
		t.Errorf("Friends = %+v", data.Friends)
	}
	if len(data.Outgoing) != 1 || data.Outgoing[0].ID != "f2" {
		t.Errorf("Outgoing = %+v", data.Outgoing)
	}
	if len(data.Incoming) != 1 || data.Incoming[0].ID != "f3" {
		t.Errorf("Incoming = %+v", data.Incoming)
	}
}

func TestBuildSocialDataResolvesTheOtherSideRegardlessOfDirection(t *testing.T) {
	// When "me" is the addressee of an accepted friendship, the friend's
	// identity must come from the requester side, not from "me" again.
	rows := []db.FriendshipWithUsers{
		{
			ID: "f1", RequesterID: "them", AddresseeID: "me", Status: "ACCEPTED",
			CreatedAt: time.Unix(0, 0), RequesterEmail: "them@x.com",
			RequesterName: sql.NullString{String: "Them", Valid: true}, AddresseeEmail: "me@x.com",
		},
	}
	data := buildSocialData("me", rows, func(string) FriendActivityDTO { return FriendActivityDTO{} })
	if len(data.Friends) != 1 || data.Friends[0].UserID != "them" {
		t.Fatalf("Friends = %+v", data.Friends)
	}
	if data.Friends[0].Name == nil || *data.Friends[0].Name != "Them" {
		t.Errorf("Name = %v, attendu Them", data.Friends[0].Name)
	}
}

func TestBuildSocialDataNeverReturnsNilSlices(t *testing.T) {
	data := buildSocialData("me", nil, func(string) FriendActivityDTO { return FriendActivityDTO{} })
	if data.Friends == nil || data.Incoming == nil || data.Outgoing == nil {
		t.Errorf("un ou plusieurs champs sont nil, attendu des slices vides : %+v", data)
	}
}

func TestBuildSocialDataOmitsNameWhenNull(t *testing.T) {
	rows := []db.FriendshipWithUsers{
		{
			ID: "f1", RequesterID: "me", AddresseeID: "nameless", Status: "ACCEPTED",
			CreatedAt: time.Unix(0, 0), RequesterEmail: "me@x.com",
			AddresseeEmail: "nameless@x.com", AddresseeName: sql.NullString{Valid: false},
		},
	}
	data := buildSocialData("me", rows, func(string) FriendActivityDTO { return FriendActivityDTO{} })
	if data.Friends[0].Name != nil {
		t.Errorf("Name = %v, attendu nil pour un compte sans nom", *data.Friends[0].Name)
	}
}

func TestBuildSocialDataCallsActivityOnlyForFriends(t *testing.T) {
	calls := 0
	rows := []db.FriendshipWithUsers{
		{ID: "f1", RequesterID: "me", AddresseeID: "friend1", Status: "ACCEPTED", CreatedAt: time.Unix(0, 0)},
		{ID: "f2", RequesterID: "me", AddresseeID: "pending1", Status: "PENDING", CreatedAt: time.Unix(0, 0)},
	}
	buildSocialData("me", rows, func(string) FriendActivityDTO {
		calls++
		return FriendActivityDTO{}
	})
	if calls != 1 {
		t.Errorf("activity() appelée %d fois, attendu 1 (seulement pour les amis acceptés)", calls)
	}
}
