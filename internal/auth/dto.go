package auth

import (
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// UserDTO mirrors the Android client's UserDto exactly (data/remote/dto/AuthDto.kt).
// `Name` is a pointer so an absent display name serializes as JSON null, not
// "" — the Android field is String? = null and the app's ROADMAP explicitly
// documents this as load-bearing: a non-nullable Kotlin field would crash
// decoding for every account created with no name, on login/register/me/refresh.
type UserDTO struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	Role      string  `json:"role"`
	IsAdmin   bool    `json:"isAdmin"`
	CreatedAt string  `json:"createdAt"`
}

func userDTO(u db.User) UserDTO {
	var name *string
	if u.Name.Valid {
		name = &u.Name.String
	}
	return UserDTO{
		ID:        u.ID,
		Email:     u.Email,
		Name:      name,
		Role:      u.Role,
		IsAdmin:   u.Role == "ADMIN",
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
}

// AuthResponseDTO is the shape of login/register/refresh/activate.
type AuthResponseDTO struct {
	Token     string  `json:"token"`
	ExpiresAt string  `json:"expiresAt"`
	User      UserDTO `json:"user"`
}

func authResponseDTO(session db.Session, user db.User) AuthResponseDTO {
	return AuthResponseDTO{
		Token:     session.ID,
		ExpiresAt: session.ExpiresAt.Format(time.RFC3339),
		User:      userDTO(user),
	}
}

// MeResponseDTO is GET /api/auth/me's body — the boot call.
type MeResponseDTO struct {
	User      UserDTO `json:"user"`
	SiteName  string  `json:"siteName"`
	ExpiresAt string  `json:"expiresAt"`
}

// ServerConfigDTO is GET /api/config's body, the only unauthenticated route
// besides /api/activate.
type ServerConfigDTO struct {
	SiteName            string `json:"siteName"`
	RegistrationEnabled bool   `json:"registrationEnabled"`
}
