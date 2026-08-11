package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// embeddedPublicKeyPEM is baked into the binary at build time. Generate it
// (and the matching private key, which never enters this repo or this
// server) with: go run ./cmd/activate-sign -genkey
//
//go:embed activation_public.pem
var embeddedPublicKeyPEM []byte

var (
	ErrInvalidCode     = errors.New("Code d'activation invalide.")
	ErrExpiredCode     = errors.New("Ce code d'activation a expiré.")
	ErrCodeAlreadyUsed = errors.New("Ce code d'activation a déjà été utilisé.")
)

// activationPayload mirrors cmd/activate-sign's struct — kept as a
// duplicate rather than a shared package, since a signing tool and a
// verifying server importing a common type is more coupling than a six-line
// struct is worth.
type activationPayload struct {
	Sub string `json:"sub"`
	IAT int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Activator verifies activation codes against the embedded (or
// operator-overridden) RSA public key.
type Activator struct {
	service   *Service
	publicKey *rsa.PublicKey
}

// NewActivator parses the public key once at startup — never per request.
// publicKeyPath overrides the embedded key when set (config.ActivationPublicKeyPath),
// letting the key rotate without a rebuild.
func NewActivator(service *Service, publicKeyPath string) (*Activator, error) {
	pemBytes := embeddedPublicKeyPEM
	if publicKeyPath != "" {
		b, err := os.ReadFile(publicKeyPath)
		if err != nil {
			return nil, fmt.Errorf("lecture de %s: %w", publicKeyPath, err)
		}
		pemBytes = b
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("clé publique d'activation illisible (PEM invalide)")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing de la clé publique: %w", err)
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("la clé publique d'activation n'est pas une clé RSA")
	}

	return &Activator{service: service, publicKey: pub}, nil
}

// verify checks the signature and expiry of a raw "payload.signature" code
// (both parts base64url, matching activate-sign's own encoding) and returns
// its payload. Pure — no DB, no side effects, safe to call before opening
// any transaction.
func (a *Activator) verify(code string) (activationPayload, error) {
	parts := strings.SplitN(code, ".", 2)
	if len(parts) != 2 {
		return activationPayload{}, ErrInvalidCode
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return activationPayload{}, ErrInvalidCode
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return activationPayload{}, ErrInvalidCode
	}

	hash := sha256.Sum256(payloadBytes)
	if err := rsa.VerifyPKCS1v15(a.publicKey, crypto.SHA256, hash[:], sig); err != nil {
		return activationPayload{}, ErrInvalidCode
	}

	var payload activationPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return activationPayload{}, ErrInvalidCode
	}
	if payload.Exp != 0 && time.Now().Unix() > payload.Exp {
		return activationPayload{}, ErrExpiredCode
	}
	return payload, nil
}

// codeHash is what activation_uses.code_hash stores — the raw code itself
// is arguably fine to store (it's not a secret once used), but hashing
// keeps the table from being a readable log of every invite ever issued.
func codeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Activate verifies code, and — only on full success — atomically marks it
// used and creates the account it authorizes. A validation failure (bad
// email, weak password, email already taken) rolls back the "used" marker
// along with everything else, so a typo doesn't burn a one-time invitation;
// only a genuinely completed activation, or a genuine replay attempt, ever
// touches activation_uses permanently.
func (a *Activator) Activate(ctx context.Context, code, email, name, password string) (db.Session, db.User, error) {
	payload, err := a.verify(code)
	if err != nil {
		return db.Session{}, db.User{}, err
	}

	tx, err := a.service.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("ouverture de la transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	q := a.service.queries.WithTx(tx)

	rows, err := q.RecordActivationUse(ctx, db.RecordActivationUseParams{
		CodeHash: codeHash(code),
		UserID:   sql.NullString{String: payload.Sub, Valid: payload.Sub != ""},
	})
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("enregistrement du code: %w", err)
	}
	if rows == 0 {
		return db.Session{}, db.User{}, ErrCodeAlreadyUsed
	}

	user, err := registerAccountWith(ctx, q, email, name, password)
	if err != nil {
		return db.Session{}, db.User{}, err
	}

	session, err := createSessionWith(ctx, q, user.ID)
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("création de la session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("validation de la transaction: %w", err)
	}
	return session, user, nil
}
