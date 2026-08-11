package social

import (
	"context"
	"errors"
	"testing"
)

func TestSendFriendRequestCreatesRequest(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")

	message, err := SendFriendRequest(context.Background(), queries, a.ID, a.Email, b.Email)
	if err != nil {
		t.Fatalf("SendFriendRequest: %v", err)
	}
	if message != "Demande envoyée." {
		t.Errorf("message = %q", message)
	}
}

func TestSendFriendRequestRejectsSelf(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")

	_, err := SendFriendRequest(context.Background(), queries, a.ID, a.Email, "A@Example.com")
	if !errors.Is(err, ErrSelfRequest) {
		t.Errorf("err = %v, attendu ErrSelfRequest (comparaison insensible à la casse)", err)
	}
}

func TestSendFriendRequestRejectsUnknownEmail(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")

	_, err := SendFriendRequest(context.Background(), queries, a.ID, a.Email, "nobody@example.com")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, attendu ErrUserNotFound", err)
	}
}

func TestSendFriendRequestRejectsDuplicate(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()

	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("première demande: %v", err)
	}
	_, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email)
	if !errors.Is(err, ErrRequestAlreadySent) {
		t.Errorf("err = %v, attendu ErrRequestAlreadySent", err)
	}
}

func TestSendFriendRequestAcceptsExistingReverseRequestInstead(t *testing.T) {
	// The single most important behavior in this file: if the target
	// already invited us, sending our own request accepts theirs rather
	// than creating a competing reverse row.
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()

	if _, err := SendFriendRequest(ctx, queries, b.ID, b.Email, a.Email); err != nil {
		t.Fatalf("B invite A: %v", err)
	}
	message, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email)
	if err != nil {
		t.Fatalf("A invite B (devrait accepter l'existante): %v", err)
	}
	if message != "Vous êtes maintenant amis." {
		t.Errorf("message = %q, attendu la confirmation d'amitié, pas une nouvelle demande", message)
	}

	rows, err := queries.ListFriendshipsForUser(ctx, a.ID)
	if err != nil {
		t.Fatalf("ListFriendshipsForUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, attendu 1 (pas de ligne concurrente créée)", len(rows))
	}
	if rows[0].Status != "ACCEPTED" {
		t.Errorf("status = %q, attendu ACCEPTED", rows[0].Status)
	}
}

func TestSendFriendRequestRejectsWhenAlreadyFriends(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()

	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande initiale: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)
	if err := queries.AcceptFriendship(ctx, rows[0].ID); err != nil {
		t.Fatalf("AcceptFriendship: %v", err)
	}

	_, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email)
	if !errors.Is(err, ErrAlreadyFriends) {
		t.Errorf("err = %v, attendu ErrAlreadyFriends", err)
	}
}

func TestAcceptFriendRequestOnlyAllowsAddressee(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()
	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)
	requestID := rows[0].ID

	if err := AcceptFriendRequest(ctx, queries, a.ID, requestID); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("l'expéditeur ne doit pas pouvoir accepter sa propre demande: err = %v", err)
	}
	if err := AcceptFriendRequest(ctx, queries, b.ID, requestID); err != nil {
		t.Errorf("le destinataire devrait pouvoir accepter: %v", err)
	}
}

func TestDeclineFriendRequestOnlyAllowsAddressee(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()
	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)
	requestID := rows[0].ID

	if err := DeclineFriendRequest(ctx, queries, a.ID, requestID); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("l'expéditeur ne doit pas pouvoir refuser sa propre demande: err = %v", err)
	}
}

func TestCancelFriendRequestOnlyAllowsSender(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()
	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)
	requestID := rows[0].ID

	if err := CancelFriendRequest(ctx, queries, b.ID, requestID); !errors.Is(err, ErrRequestNotFound) {
		t.Errorf("le destinataire ne doit pas pouvoir annuler la demande: err = %v", err)
	}
	if err := CancelFriendRequest(ctx, queries, a.ID, requestID); err != nil {
		t.Errorf("l'expéditeur devrait pouvoir annuler: %v", err)
	}
}

func TestRemoveFriendAllowsEitherParticipant(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	c := createTestUser(t, queries, "c@example.com", "Carol")
	ctx := context.Background()
	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)
	friendshipID := rows[0].ID
	if err := AcceptFriendRequest(ctx, queries, b.ID, friendshipID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	if err := RemoveFriend(ctx, queries, c.ID, friendshipID); !errors.Is(err, ErrFriendNotFound) {
		t.Errorf("un tiers ne doit jamais pouvoir retirer une amitié qui n'est pas la sienne: err = %v", err)
	}
	if err := RemoveFriend(ctx, queries, a.ID, friendshipID); err != nil {
		t.Errorf("le demandeur devrait pouvoir retirer l'amitié: %v", err)
	}
}

func TestRemoveFriendRejectsStillPending(t *testing.T) {
	queries := newTestQueries(t)
	a := createTestUser(t, queries, "a@example.com", "Alice")
	b := createTestUser(t, queries, "b@example.com", "Bob")
	ctx := context.Background()
	if _, err := SendFriendRequest(ctx, queries, a.ID, a.Email, b.Email); err != nil {
		t.Fatalf("demande: %v", err)
	}
	rows, _ := queries.ListFriendshipsForUser(ctx, a.ID)

	if err := RemoveFriend(ctx, queries, a.ID, rows[0].ID); !errors.Is(err, ErrFriendNotFound) {
		t.Errorf("une demande encore en attente n'est pas une amitié à retirer: err = %v", err)
	}
}
