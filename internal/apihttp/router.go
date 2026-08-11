package apihttp

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter assembles the chi router with the shared middleware stack.
// Each module mounts its own routes onto it from main() — this file only
// owns the skeleton, not any endpoint.
func NewRouter(logger *slog.Logger) chi.Router {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Logging(logger))
	r.Use(Recover(logger))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return r
}
