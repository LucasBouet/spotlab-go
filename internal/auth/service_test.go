package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

func TestRegisterAccountSucceeds(t *testing.T) {
	svc := newTestDB(t)
	user, err := svc.RegisterAccount(context.Background(), "Marie@Example.com", "Marie", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	// Emails are normalized to lowercase, mirroring the old server.
	if user.Email != "marie@example.com" {
		t.Errorf("email = %q, attendu marie@example.com", user.Email)
	}
	if !user.Name.Valid || user.Name.String != "Marie" {
		t.Errorf("name = %+v, attendu Marie", user.Name)
	}
	// The first account in an empty database is auto-promoted to ADMIN —
	// see TestFirstUserBecomesAdminSubsequentUsersDoNot for the full
	// bootstrap-rule coverage.
	if user.Role != "ADMIN" {
		t.Errorf("role = %q, attendu ADMIN (premier compte de la base)", user.Role)
	}
}

func TestFirstUserBecomesAdminSubsequentUsersDoNot(t *testing.T) {
	svc := newTestDB(t)
	first, err := svc.RegisterAccount(context.Background(), "first@example.com", "First", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount (first): %v", err)
	}
	if first.Role != "ADMIN" {
		t.Errorf("role du premier compte = %q, attendu ADMIN", first.Role)
	}

	second, err := svc.RegisterAccount(context.Background(), "second@example.com", "Second", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount (second): %v", err)
	}
	if second.Role != "USER" {
		t.Errorf("role du deuxième compte = %q, attendu USER", second.Role)
	}
}

func TestRegisterAccountAllowsEmptyName(t *testing.T) {
	// A blank name must decode as SQL NULL, not the empty string — this is
	// the exact condition the Android client's UserDto.name = String?
	// exists to survive (docs/PLAN.md pitfall carried over from ROADMAP).
	svc := newTestDB(t)
	user, err := svc.RegisterAccount(context.Background(), "noname@example.com", "  ", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	if user.Name.Valid {
		t.Errorf("name devrait être NULL pour un nom vide/blanc, obtenu %q", user.Name.String)
	}
}

func TestRegisterAccountRejectsInvalidEmail(t *testing.T) {
	svc := newTestDB(t)
	_, err := svc.RegisterAccount(context.Background(), "not-an-email", "X", "correcthorse")
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("err = %v, attendu ErrInvalidEmail", err)
	}
}

func TestRegisterAccountRejectsWeakPassword(t *testing.T) {
	svc := newTestDB(t)
	_, err := svc.RegisterAccount(context.Background(), "x@example.com", "X", "short")
	if !errors.Is(err, ErrWeakPassword) {
		t.Errorf("err = %v, attendu ErrWeakPassword", err)
	}
}

func TestRegisterAccountRejectsDuplicateEmail(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	if _, err := svc.RegisterAccount(ctx, "dup@example.com", "A", "correcthorse"); err != nil {
		t.Fatalf("premier RegisterAccount: %v", err)
	}
	_, err := svc.RegisterAccount(ctx, "dup@example.com", "B", "correcthorse2")
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("err = %v, attendu ErrEmailTaken", err)
	}
}

func TestAuthenticateGivesSameErrorForUnknownEmailAndWrongPassword(t *testing.T) {
	// Distinguishing the two would let an attacker enumerate registered
	// emails through the login endpoint.
	svc := newTestDB(t)
	ctx := context.Background()
	if _, err := svc.RegisterAccount(ctx, "real@example.com", "Real", "correcthorse"); err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}

	_, errUnknown := svc.Authenticate(ctx, "nobody@example.com", "whatever")
	_, errWrongPw := svc.Authenticate(ctx, "real@example.com", "wrongpassword")

	if !errors.Is(errUnknown, ErrInvalidCredentials) {
		t.Errorf("compte inconnu: err = %v, attendu ErrInvalidCredentials", errUnknown)
	}
	if !errors.Is(errWrongPw, ErrInvalidCredentials) {
		t.Errorf("mauvais mot de passe: err = %v, attendu ErrInvalidCredentials", errWrongPw)
	}
}

func TestAuthenticateSucceedsWithCorrectCredentials(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	created, err := svc.RegisterAccount(ctx, "real@example.com", "Real", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	user, err := svc.Authenticate(ctx, "real@example.com", "correcthorse")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != created.ID {
		t.Errorf("user.ID = %q, attendu %q", user.ID, created.ID)
	}
}

func TestSessionLifecycle(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	user, err := svc.RegisterAccount(ctx, "session@example.com", "S", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}

	session, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(session.ID) != 64 {
		t.Errorf("longueur du token = %d, attendu 64 (32 octets en hexadécimal)", len(session.ID))
	}

	_, resolved, err := svc.ValidateToken(ctx, session.ID)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if resolved.ID != user.ID {
		t.Errorf("resolved.ID = %q, attendu %q", resolved.ID, user.ID)
	}

	if err := svc.Logout(ctx, session.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, err := svc.ValidateToken(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("après logout: err = %v, attendu ErrSessionNotFound", err)
	}
}

func TestValidateTokenRejectsUnknownToken(t *testing.T) {
	svc := newTestDB(t)
	_, _, err := svc.ValidateToken(context.Background(), "0000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, attendu ErrSessionNotFound", err)
	}
}

func TestValidateTokenRejectsAndCleansUpExpiredSession(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	user, err := svc.RegisterAccount(ctx, "expired@example.com", "E", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}

	// Manufacture an already-expired session directly — CreateSession only
	// ever produces future expiries, so the lazy-cleanup path has to be
	// exercised by hand.
	const expiredToken = "expiredtoken0000000000000000000000000000000000000000000000000"
	if _, err := svc.queries.CreateSession(ctx, db.CreateSessionParams{
		ID:        expiredToken,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("création manuelle de la session expirée: %v", err)
	}

	if _, _, err := svc.ValidateToken(ctx, expiredToken); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, attendu ErrSessionNotFound pour une session expirée", err)
	}

	// The lazy delete must have actually run — a second lookup should hit
	// "no such row" through the exact same code path, not linger forever.
	var count int
	row := svc.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id = ?", expiredToken)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("vérification du nettoyage: %v", err)
	}
	if count != 0 {
		t.Error("la session expirée aurait dû être supprimée lors de sa lecture")
	}
}

func TestRefreshIsAtomicAndHasNoGracePeriod(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	user, err := svc.RegisterAccount(ctx, "refresh@example.com", "R", "correcthorse")
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	oldSession, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	newSession, _, err := svc.Refresh(ctx, oldSession.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if newSession.ID == oldSession.ID {
		t.Error("le renouvellement doit produire un nouveau jeton, pas réutiliser l'ancien")
	}

	// The old token must be dead the instant refresh is processed — no
	// grace period, matching the old server exactly.
	if _, _, err := svc.ValidateToken(ctx, oldSession.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("ancien jeton: err = %v, attendu ErrSessionNotFound immédiatement après le renouvellement", err)
	}
	if _, _, err := svc.ValidateToken(ctx, newSession.ID); err != nil {
		t.Errorf("nouveau jeton devrait être valide: %v", err)
	}
}

func TestSiteNameFallsBackWhenUnset(t *testing.T) {
	svc := newTestDB(t)
	if got := svc.SiteName(context.Background(), "Fallback"); got != "Fallback" {
		t.Errorf("SiteName = %q, attendu le repli Fallback", got)
	}
}

func TestSiteNameReflectsAdminOverride(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	if err := svc.queries.SetAppSetting(ctx, db.SetAppSettingParams{Key: "site_name", Value: "Ma Radio"}); err != nil {
		t.Fatalf("SetAppSetting: %v", err)
	}
	if got := svc.SiteName(ctx, "Fallback"); got != "Ma Radio" {
		t.Errorf("SiteName = %q, attendu Ma Radio", got)
	}
}

func TestRegistrationEnabledFallsBackWhenUnset(t *testing.T) {
	svc := newTestDB(t)
	if got := svc.RegistrationEnabled(context.Background(), false); got {
		t.Error("RegistrationEnabled devrait retomber sur le repli (false) tant que rien n'est en base")
	}
	if got := svc.RegistrationEnabled(context.Background(), true); !got {
		t.Error("RegistrationEnabled devrait retomber sur le repli (true) tant que rien n'est en base")
	}
}

func TestRegistrationEnabledReflectsAdminOverride(t *testing.T) {
	svc := newTestDB(t)
	ctx := context.Background()
	if err := svc.queries.SetAppSetting(ctx, db.SetAppSettingParams{Key: "registration_enabled", Value: "true"}); err != nil {
		t.Fatalf("SetAppSetting: %v", err)
	}
	if got := svc.RegistrationEnabled(ctx, false); !got {
		t.Error("RegistrationEnabled devrait refléter la valeur true écrite en base, malgré un repli à false")
	}
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	svc := newTestDB(t)
	_, _, err := svc.Refresh(context.Background(), "0000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, attendu ErrSessionNotFound", err)
	}
}
