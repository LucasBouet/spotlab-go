package admin

import (
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// AdminUserDTO mirrors listUsers' row shape in the old server's
// Admin/service.ts — a plain account summary, no session/password data.
type AdminUserDTO struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	Role      string  `json:"role"`
	CreatedAt string  `json:"createdAt"`
}

func adminUserDTO(u db.User) AdminUserDTO {
	var name *string
	if u.Name.Valid {
		name = &u.Name.String
	}
	return AdminUserDTO{
		ID: u.ID, Email: u.Email, Name: name, Role: u.Role,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
}

func adminUserDTOs(users []db.User) []AdminUserDTO {
	out := make([]AdminUserDTO, len(users))
	for i, u := range users {
		out[i] = adminUserDTO(u)
	}
	return out
}

type ListUsersResponseDTO struct {
	Users []AdminUserDTO `json:"users"`
}

// AppSettingDefinitionDTO mirrors AppSettingDefinition in the old server's
// config/settings.ts — sent alongside the current values so a generic
// settings form can render itself without hardcoding labels client-side.
type AppSettingDefinitionDTO struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Type        string `json:"type"` // "string" | "boolean"
	Default     string `json:"default"`
}

// appSettingDefinitions is the closed set of admin-editable settings.
// registration_enabled defaults to "false" here — not "true" as the old
// server had it — matching this server's closed-by-default posture
// (docs/PLAN.md §5); an admin can still flip it on from this same panel.
var appSettingDefinitions = []AppSettingDefinitionDTO{
	{
		Key: "site_name", Label: "Nom de l'application",
		Description: "Affiché dans l'app et l'écran de connexion.",
		Type:        "string", Default: "Spotlab",
	},
	{
		Key: "registration_enabled", Label: "Inscriptions ouvertes",
		Description: "Autorise la création de comptes sans code d'activation.",
		Type:        "boolean", Default: "false",
	},
}

type SettingsResponseDTO struct {
	Settings    map[string]string         `json:"settings"`
	Definitions []AppSettingDefinitionDTO `json:"definitions"`
}
