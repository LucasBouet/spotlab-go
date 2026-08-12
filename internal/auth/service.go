// Package auth owns accounts, sessions, and the RSA activation gate that
// replaces open self-registration. See docs/PLAN.md §5.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/idgen"
)

// SessionDuration matches the old server exactly (session.ts): 30 days,
// and the Android client's renewal window assumes this value.
const SessionDuration = 30 * 24 * time.Hour

var emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var (
	ErrInvalidCredentials = errors.New("Adresse e-mail ou mot de passe incorrect.")
	ErrEmailTaken         = errors.New("Un compte existe déjà avec cette adresse e-mail.")
	ErrInvalidEmail       = errors.New("Adresse e-mail invalide.")
	ErrWeakPassword       = errors.New("Le mot de passe doit contenir au moins 8 caractères.")
	ErrSessionNotFound    = errors.New("Session invalide.")
)

type Service struct {
	sqlDB   *sql.DB
	queries *db.Queries
}

func NewService(sqlDB *sql.DB) *Service {
	return &Service{sqlDB: sqlDB, queries: db.New(sqlDB)}
}

// appSettingsSiteNameKey and appSettingsRegistrationKey match the old
// server's AppSettingKey values exactly (config/settings.ts) — an admin
// panel that reused the same keys wouldn't need any data migration.
const (
	appSettingsSiteNameKey     = "site_name"
	appSettingsRegistrationKey = "registration_enabled"
)

// SiteName resolves the admin-configurable site name, falling back to
// fallback (the startup config value) when nothing has been set in
// app_settings yet — the table exists from Phase 1 but nothing wrote to it
// until the admin panel did.
func (s *Service) SiteName(ctx context.Context, fallback string) string {
	value, err := s.queries.GetAppSetting(ctx, appSettingsSiteNameKey)
	if err != nil || value == "" {
		return fallback
	}
	return value
}

// RegistrationEnabled resolves the admin-configurable open-registration
// toggle. Unset (the common case — see docs/PLAN.md §5, closed by design)
// falls back to fallback rather than defaulting to open, so a server that
// never touches the admin panel behaves exactly as before this existed.
func (s *Service) RegistrationEnabled(ctx context.Context, fallback bool) bool {
	value, err := s.queries.GetAppSetting(ctx, appSettingsRegistrationKey)
	if err != nil {
		return fallback
	}
	return value == "true"
}

// RegisterAccount creates a new user with a freshly hashed password. Shared
// by /api/auth/register (gated by registration being closed, see handlers.go)
// and /api/activate (bypasses that gate — a valid signature is itself the
// authorization).
func (s *Service) RegisterAccount(ctx context.Context, email, name, password string) (db.User, error) {
	return registerAccountWith(ctx, s.queries, email, name, password)
}

// registerAccountWith is the tx-agnostic core: it takes a *db.Queries so
// /api/activate can run it inside the same transaction as the one-time-use
// check on the activation code (see activation.go). A failed registration
// — bad email, weak password, email already taken — must roll back that
// check too, or a mistyped email would burn an otherwise-valid code.
func registerAccountWith(ctx context.Context, q *db.Queries, email, name, password string) (db.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRegex.MatchString(email) {
		return db.User{}, ErrInvalidEmail
	}
	if len(password) < 8 {
		return db.User{}, ErrWeakPassword
	}

	if _, err := q.GetUserByEmail(ctx, email); err == nil {
		return db.User{}, ErrEmailTaken
	} else if !errors.Is(err, sql.ErrNoRows) {
		return db.User{}, fmt.Errorf("vérification de l'e-mail: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return db.User{}, fmt.Errorf("hachage du mot de passe: %w", err)
	}

	nameValue := sql.NullString{}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		nameValue = sql.NullString{String: trimmed, Valid: true}
	}

	// The old server had no bootstrap rule at all — the first admin was set
	// by hand in the database. On a fresh self-hosted install that's an
	// unnecessary rough edge: whoever activates first (in practice, the
	// person who deployed the server) becomes ADMIN automatically, and
	// every account after that is a plain USER promoted through the admin
	// panel this unblocks in the first place.
	role := "USER"
	if count, err := q.CountUsers(ctx); err == nil && count == 0 {
		role = "ADMIN"
	}

	return q.CreateUser(ctx, db.CreateUserParams{
		ID:           idgen.New(),
		Email:        email,
		Name:         nameValue,
		PasswordHash: hash,
		Role:         role,
	})
}

// Authenticate verifies credentials for login. Deliberately returns the
// same error for "no such account" and "wrong password" — distinguishing
// them lets an attacker enumerate registered emails.
func (s *Service) Authenticate(ctx context.Context, email, password string) (db.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.User{}, ErrInvalidCredentials
		}
		return db.User{}, fmt.Errorf("recherche de l'utilisateur: %w", err)
	}

	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		return db.User{}, ErrInvalidCredentials
	}
	return user, nil
}

// newToken is a 64-hex-char random string — the token IS the session's
// primary key, no separate signing, matching the old server exactly
// (session.ts: randomBytes(32).toString("hex")).
func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// CreateSession issues a fresh 30-day session for userID.
func (s *Service) CreateSession(ctx context.Context, userID string) (db.Session, error) {
	return createSessionWith(ctx, s.queries, userID)
}

func createSessionWith(ctx context.Context, q *db.Queries, userID string) (db.Session, error) {
	token, err := newToken()
	if err != nil {
		return db.Session{}, fmt.Errorf("génération du jeton: %w", err)
	}
	return q.CreateSession(ctx, db.CreateSessionParams{
		ID:        token,
		UserID:    userID,
		ExpiresAt: time.Now().Add(SessionDuration),
	})
}

// ValidateToken resolves a bearer token to its session and user, lazily
// deleting the session row if it has expired — same semantics as the old
// server (session.ts: expired sessions are cleaned up on next lookup, not
// by a separate scheduled job). The session is returned (not just the
// user) so /api/auth/me can report its real expiresAt, which the Android
// client's renewal check reads on every boot.
func (s *Service) ValidateToken(ctx context.Context, token string) (db.Session, db.User, error) {
	row, err := s.queries.GetSessionWithUser(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Session{}, db.User{}, ErrSessionNotFound
		}
		return db.Session{}, db.User{}, fmt.Errorf("recherche de la session: %w", err)
	}
	if time.Now().After(row.Session.ExpiresAt) {
		_ = s.queries.DeleteSession(ctx, token)
		return db.Session{}, db.User{}, ErrSessionNotFound
	}
	return row.Session, row.User, nil
}

// Refresh atomically revokes oldToken and issues a new session for the same
// user — no grace period. If the response carrying the new token is lost in
// transit, the old one is already dead: reconnection is required. This
// matches the old server's refresh route exactly (docs/PLAN.md, ROADMAP
// pitfall carried over from the Android client's own notes).
func (s *Service) Refresh(ctx context.Context, oldToken string) (db.Session, db.User, error) {
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("ouverture de la transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	q := s.queries.WithTx(tx)

	row, err := q.GetSessionWithUser(ctx, oldToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Session{}, db.User{}, ErrSessionNotFound
		}
		return db.Session{}, db.User{}, fmt.Errorf("recherche de la session: %w", err)
	}
	if time.Now().After(row.Session.ExpiresAt) {
		return db.Session{}, db.User{}, ErrSessionNotFound
	}

	if err := q.DeleteSession(ctx, oldToken); err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("révocation de l'ancien jeton: %w", err)
	}

	token, err := newToken()
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("génération du jeton: %w", err)
	}
	session, err := q.CreateSession(ctx, db.CreateSessionParams{
		ID:        token,
		UserID:    row.User.ID,
		ExpiresAt: time.Now().Add(SessionDuration),
	})
	if err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("création du nouveau jeton: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return db.Session{}, db.User{}, fmt.Errorf("validation de la transaction: %w", err)
	}
	return session, row.User, nil
}

// Logout revokes a token. Always succeeds even if the token was already
// gone — logging out twice isn't an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.queries.DeleteSession(ctx, token)
}
