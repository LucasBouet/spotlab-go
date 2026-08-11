package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// signTestCode reproduces cmd/activate-sign's exact encoding — duplicated
// rather than imported (main packages aren't importable), and small enough
// that keeping it in sync by inspection is fine.
func signTestCode(t *testing.T, priv *rsa.PrivateKey, sub string, exp int64) string {
	t.Helper()
	payload := activationPayload{Sub: sub, IAT: time.Now().Unix(), Exp: exp}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encodage du payload: %v", err)
	}
	hash := sha256.Sum256(payloadJSON)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(payloadJSON) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
}

// newTestActivator generates a fresh RSA keypair, writes its public half to
// a temp file, and builds an Activator that verifies against it via the
// ACTIVATION_PUBLIC_KEY_PATH override path — so tests never touch the real
// embedded key used in production.
func newTestActivator(t *testing.T, svc *Service) (*Activator, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("génération de la clé de test: %v", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("encodage de la clé publique: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})

	path := filepath.Join(t.TempDir(), "test_public.pem")
	if err := os.WriteFile(path, pubPEM, 0o644); err != nil {
		t.Fatalf("écriture de la clé publique de test: %v", err)
	}

	activator, err := NewActivator(svc, path)
	if err != nil {
		t.Fatalf("NewActivator: %v", err)
	}
	return activator, priv
}

func TestActivateSucceedsWithValidCode(t *testing.T) {
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	code := signTestCode(t, priv, "friend-1", 0)

	session, user, err := activator.Activate(context.Background(), code, "friend@example.com", "Friend", "correcthorse")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if user.Email != "friend@example.com" {
		t.Errorf("email = %q", user.Email)
	}
	if len(session.ID) != 64 {
		t.Errorf("longueur du token = %d, attendu 64", len(session.ID))
	}
}

func TestActivateRejectsReplay(t *testing.T) {
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	code := signTestCode(t, priv, "friend-1", 0)
	ctx := context.Background()

	if _, _, err := activator.Activate(ctx, code, "friend@example.com", "Friend", "correcthorse"); err != nil {
		t.Fatalf("première activation: %v", err)
	}

	_, _, err := activator.Activate(ctx, code, "someone-else@example.com", "Other", "correcthorse2")
	if !errors.Is(err, ErrCodeAlreadyUsed) {
		t.Errorf("err = %v, attendu ErrCodeAlreadyUsed sur le rejeu", err)
	}
}

func TestActivateRejectsTamperedSignature(t *testing.T) {
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	code := signTestCode(t, priv, "friend-1", 0)
	tampered := code[:len(code)-4] + "AAAA" // corrupt the tail of the signature

	_, _, err := activator.Activate(context.Background(), tampered, "friend@example.com", "Friend", "correcthorse")
	if !errors.Is(err, ErrInvalidCode) {
		t.Errorf("err = %v, attendu ErrInvalidCode pour une signature altérée", err)
	}
}

func TestActivateRejectsCodeFromUnrelatedKeypair(t *testing.T) {
	svc := newTestDB(t)
	activator, _ := newTestActivator(t, svc) // activator trusts THIS keypair's public half

	otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("génération de la clé étrangère: %v", err)
	}
	forged := signTestCode(t, otherPriv, "attacker", 0)

	_, _, err = activator.Activate(context.Background(), forged, "attacker@example.com", "Attacker", "correcthorse")
	if !errors.Is(err, ErrInvalidCode) {
		t.Errorf("err = %v, attendu ErrInvalidCode pour une clé non reconnue", err)
	}
}

func TestActivateRejectsExpiredCode(t *testing.T) {
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	code := signTestCode(t, priv, "friend-1", time.Now().Add(-time.Hour).Unix())

	_, _, err := activator.Activate(context.Background(), code, "friend@example.com", "Friend", "correcthorse")
	if !errors.Is(err, ErrExpiredCode) {
		t.Errorf("err = %v, attendu ErrExpiredCode", err)
	}
}

func TestActivateAcceptsZeroExpiryAsNeverExpiring(t *testing.T) {
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	// exp=0 with iat set far in the past would look "expired" under a naive
	// `now > exp` check that doesn't special-case zero.
	code := signTestCode(t, priv, "friend-1", 0)

	if _, _, err := activator.Activate(context.Background(), code, "friend@example.com", "Friend", "correcthorse"); err != nil {
		t.Errorf("Activate: %v, un exp=0 ne doit jamais être traité comme expiré", err)
	}
}

func TestActivateRollsBackOnValidationFailure(t *testing.T) {
	// The subtle correctness property: a code rejected for a reason that
	// has nothing to do with the code itself (email taken, weak password)
	// must NOT be burned — only a genuinely completed activation, or a
	// genuine replay, should ever make activation_uses stick.
	svc := newTestDB(t)
	activator, priv := newTestActivator(t, svc)
	ctx := context.Background()

	if _, err := svc.RegisterAccount(ctx, "taken@example.com", "Existing", "correcthorse"); err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}

	code := signTestCode(t, priv, "friend-1", 0)

	if _, _, err := activator.Activate(ctx, code, "taken@example.com", "Friend", "correcthorse"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("première tentative: err = %v, attendu ErrEmailTaken", err)
	}

	// Same code, valid email this time — must still succeed.
	if _, _, err := activator.Activate(ctx, code, "not-taken@example.com", "Friend", "correcthorse"); err != nil {
		t.Fatalf("le code n'aurait pas dû être brûlé par l'échec de validation précédent: %v", err)
	}

	// Now it really is used up.
	if _, _, err := activator.Activate(ctx, code, "yet-another@example.com", "X", "correcthorse"); !errors.Is(err, ErrCodeAlreadyUsed) {
		t.Errorf("err = %v, attendu ErrCodeAlreadyUsed après une activation réussie", err)
	}
}

func TestActivateRejectsMalformedCode(t *testing.T) {
	svc := newTestDB(t)
	activator, _ := newTestActivator(t, svc)

	for _, code := range []string{"", "no-dot-here", "onlyone.part.toomany", "not-base64!.also-not-base64!"} {
		_, _, err := activator.Activate(context.Background(), code, "x@example.com", "X", "correcthorse")
		if !errors.Is(err, ErrInvalidCode) {
			t.Errorf("code %q: err = %v, attendu ErrInvalidCode", code, err)
		}
	}
}
