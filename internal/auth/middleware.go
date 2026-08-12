package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

type contextKey int

const authContextKey contextKey = iota

type authContext struct {
	session db.Session
	user    db.User
}

// UserFromContext returns the authenticated user. Only valid inside a
// handler wrapped by RequireAuth — every route needing a user is expected
// to be mounted behind it, so this panics on programmer error rather than
// returning a zero value that would silently misbehave.
func UserFromContext(ctx context.Context) db.User {
	return authFromContext(ctx).user
}

// SessionFromContext returns the session the current request authenticated
// with — mainly for /api/auth/me, which reports its real expiresAt.
func SessionFromContext(ctx context.Context) db.Session {
	return authFromContext(ctx).session
}

// ContextWithUserForTesting attaches user the same way RequireAuth would,
// so other packages' tests can exercise a handler that reads
// UserFromContext/SessionFromContext without standing up a full HTTP
// request + real session — mirrors catalog.NewDeezerClientForTesting's
// precedent for test-only exports.
func ContextWithUserForTesting(ctx context.Context, user db.User) context.Context {
	return context.WithValue(ctx, authContextKey, authContext{user: user})
}

func authFromContext(ctx context.Context) authContext {
	auth, ok := ctx.Value(authContextKey).(authContext)
	if !ok {
		panic("auth: appelé hors d'un handler protégé par RequireAuth")
	}
	return auth
}

// RequireAuth reads the Authorization: Bearer <token> header, resolves it
// to a session and user, and rejects with 401 otherwise. The Android client
// only ever uses this header (never the web client's cookie fallback), so
// that's the only source this middleware reads.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			apihttp.Error(w, http.StatusUnauthorized, "Non authentifié.")
			return
		}

		session, user, err := s.ValidateToken(r.Context(), token)
		if err != nil {
			apihttp.Error(w, http.StatusUnauthorized, "Session invalide ou expirée.")
			return
		}

		ctx := context.WithValue(r.Context(), authContextKey, authContext{session: session, user: user})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimPrefix(header, prefix)
}
