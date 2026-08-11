package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lucasbouet/spotlab-go/internal/apihttp"
)

// Deps is everything the auth routes need beyond the Service and Activator
// they're built from.
type Deps struct {
	Service             *Service
	Activator           *Activator
	SiteName            string
	RegistrationEnabled bool // always false in practice — see docs/PLAN.md §5
}

// Mount registers every auth + config + activation route, and returns the
// RequireAuth middleware other modules mount their own protected routes
// behind (auth is the one module every other one depends on — see
// docs/PLAN.md §2's build order).
func Mount(r chi.Router, deps Deps) func(http.Handler) http.Handler {
	r.Get("/api/config", handleConfig(deps))
	r.Post("/api/activate", handleActivate(deps))

	r.Post("/api/auth/login", handleLogin(deps))
	r.Post("/api/auth/register", handleRegister(deps))

	r.Group(func(r chi.Router) {
		r.Use(deps.Service.RequireAuth)
		r.Get("/api/auth/me", handleMe(deps))
		r.Post("/api/auth/refresh", handleRefresh(deps))
		r.Post("/api/auth/logout", handleLogout(deps))
	})

	return deps.Service.RequireAuth
}

func handleConfig(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apihttp.JSON(w, http.StatusOK, ServerConfigDTO{
			SiteName:            deps.SiteName,
			RegistrationEnabled: deps.RegistrationEnabled,
		})
	}
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func handleLogin(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body loginBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		user, err := deps.Service.Authenticate(r.Context(), body.Email, body.Password)
		if err != nil {
			// Same message whether the account doesn't exist or the password is
			// wrong — a 401 here never carried an Authorization header, so the
			// Android client reads it as "bad password", not a dead session.
			apihttp.Error(w, http.StatusUnauthorized, ErrInvalidCredentials.Error())
			return
		}

		session, err := deps.Service.CreateSession(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		apihttp.JSON(w, http.StatusOK, authResponseDTO(session, user))
	}
}

type registerBody struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func handleRegister(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !deps.RegistrationEnabled {
			apihttp.Error(w, http.StatusForbidden,
				"Inscription fermée. Utilisez un code d'activation.")
			return
		}

		var body registerBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		user, err := deps.Service.RegisterAccount(r.Context(), body.Email, body.Name, body.Password)
		if err != nil {
			writeRegistrationError(w, err)
			return
		}

		session, err := deps.Service.CreateSession(r.Context(), user.ID)
		if err != nil {
			apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
			return
		}

		apihttp.JSON(w, http.StatusCreated, authResponseDTO(session, user))
	}
}

func handleMe(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		session := SessionFromContext(r.Context())
		apihttp.JSON(w, http.StatusOK, MeResponseDTO{
			User:      userDTO(user),
			SiteName:  deps.SiteName,
			ExpiresAt: session.ExpiresAt.Format(time.RFC3339),
		})
	}
}

func handleRefresh(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		session, user, err := deps.Service.Refresh(r.Context(), token)
		if err != nil {
			apihttp.Error(w, http.StatusUnauthorized, "Session invalide ou expirée.")
			return
		}
		apihttp.JSON(w, http.StatusOK, authResponseDTO(session, user))
	}
}

func handleLogout(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = deps.Service.Logout(r.Context(), bearerToken(r))
		apihttp.NoContent(w)
	}
}

type activateBody struct {
	Code     string `json:"code"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func handleActivate(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body activateBody
		if !apihttp.DecodeJSON(w, r, &body) {
			return
		}

		session, user, err := deps.Activator.Activate(r.Context(), body.Code, body.Email, body.Name, body.Password)
		if err != nil {
			writeActivationError(w, err)
			return
		}

		apihttp.JSON(w, http.StatusCreated, authResponseDTO(session, user))
	}
}

func writeRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidEmail), errors.Is(err, ErrWeakPassword):
		apihttp.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrEmailTaken):
		apihttp.Error(w, http.StatusConflict, err.Error())
	default:
		apihttp.Error(w, http.StatusInternalServerError, "Erreur inattendue.")
	}
}

func writeActivationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidCode), errors.Is(err, ErrExpiredCode), errors.Is(err, ErrCodeAlreadyUsed):
		apihttp.Error(w, http.StatusBadRequest, err.Error())
	default:
		writeRegistrationError(w, err)
	}
}
