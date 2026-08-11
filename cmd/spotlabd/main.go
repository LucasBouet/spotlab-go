// Command spotlabd is the Spotlab server: a single process owning the
// SQLite database, the in-memory playback/jam state, and every HTTP route
// the Android app talks to. See docs/PLAN.md in this repo (copied from the
// approved migration plan) for the phase-by-phase design.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lucasbouet/spotlab-go/internal/apihttp"
	"github.com/lucasbouet/spotlab-go/internal/config"
	"github.com/lucasbouet/spotlab-go/internal/db"
	"github.com/lucasbouet/spotlab-go/internal/logging"
)

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)

	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o755); err != nil {
		logger.Error("création du dossier de la base", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.StreamCacheDir, 0o755); err != nil {
		logger.Error("création du dossier de cache audio", "error", err)
		os.Exit(1)
	}

	conn, err := db.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("ouverture de la base", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	router := apihttp.NewRouter(logger)
	// Modules mount their own routes here as each phase lands, e.g.:
	//   auth.Mount(router, authDeps)
	//   catalog.Mount(router, catalogDeps)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	go func() {
		logger.Info("démarrage", "port", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serveur arrêté en erreur", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	logger.Info("arrêt en cours")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("arrêt forcé", "error", err)
	}
}
