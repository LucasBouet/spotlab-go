package social

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var (
	ErrInvalidEmail       = errors.New("Adresse e-mail invalide.")
	ErrSelfRequest        = errors.New("Vous ne pouvez pas vous ajouter vous-même.")
	ErrUserNotFound       = errors.New("Aucun utilisateur avec cette adresse e-mail.")
	ErrAlreadyFriends     = errors.New("Vous êtes déjà amis.")
	ErrRequestAlreadySent = errors.New("Demande déjà envoyée.")
	ErrRequestNotFound    = errors.New("Demande introuvable.")
	ErrFriendNotFound     = errors.New("Ami introuvable.")
)

// SendFriendRequest mirrors sendFriendRequestFrom exactly, including its
// most useful quirk: if the target already sent *us* a pending request,
// this accepts that one instead of creating a competing reverse row.
func SendFriendRequest(ctx context.Context, queries *db.Queries, userID, userEmail, rawEmail string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(rawEmail))
	if email == "" || !emailRegex.MatchString(email) {
		return "", ErrInvalidEmail
	}
	if email == strings.ToLower(userEmail) {
		return "", ErrSelfRequest
	}

	target, err := queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUserNotFound
		}
		return "", fmt.Errorf("recherche du destinataire: %w", err)
	}

	existing, err := queries.FindFriendshipBetween(ctx, db.FindFriendshipBetweenParams{UserA: userID, UserB: target.ID})
	if err == nil {
		switch {
		case existing.Status == "ACCEPTED":
			return "", ErrAlreadyFriends
		case existing.RequesterID == userID:
			return "", ErrRequestAlreadySent
		default:
			// The target already invited us — accept it instead of
			// creating a competing reverse row.
			if err := queries.AcceptFriendship(ctx, existing.ID); err != nil {
				return "", fmt.Errorf("acceptation de la demande existante: %w", err)
			}
			return "Vous êtes maintenant amis.", nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("recherche d'une amitié existante: %w", err)
	}

	if err := queries.CreateFriendship(ctx, db.CreateFriendshipParams{
		ID: idgen.New(), RequesterID: userID, AddresseeID: target.ID,
	}); err != nil {
		return "", fmt.Errorf("création de la demande: %w", err)
	}
	return "Demande envoyée.", nil
}

// AcceptFriendRequest: only the addressee of a still-pending request may
// accept it.
func AcceptFriendRequest(ctx context.Context, queries *db.Queries, userID, requestID string) error {
	row, err := queries.GetFriendshipByID(ctx, requestID)
	if err != nil || row.AddresseeID != userID || row.Status != "PENDING" {
		return ErrRequestNotFound
	}
	return queries.AcceptFriendship(ctx, requestID)
}

// DeclineFriendRequest: only the addressee of a still-pending request may
// decline it.
func DeclineFriendRequest(ctx context.Context, queries *db.Queries, userID, requestID string) error {
	row, err := queries.GetFriendshipByID(ctx, requestID)
	if err != nil || row.AddresseeID != userID || row.Status != "PENDING" {
		return ErrRequestNotFound
	}
	return queries.DeleteFriendshipByID(ctx, requestID)
}

// CancelFriendRequest: only the sender of a still-pending request may
// cancel it.
func CancelFriendRequest(ctx context.Context, queries *db.Queries, userID, requestID string) error {
	row, err := queries.GetFriendshipByID(ctx, requestID)
	if err != nil || row.RequesterID != userID || row.Status != "PENDING" {
		return ErrRequestNotFound
	}
	return queries.DeleteFriendshipByID(ctx, requestID)
}

// RemoveFriend: either participant of an accepted friendship may remove it.
func RemoveFriend(ctx context.Context, queries *db.Queries, userID, friendshipID string) error {
	row, err := queries.GetFriendshipByID(ctx, friendshipID)
	if err != nil || row.Status != "ACCEPTED" || (row.RequesterID != userID && row.AddresseeID != userID) {
		return ErrFriendNotFound
	}
	return queries.DeleteFriendshipByID(ctx, friendshipID)
}
