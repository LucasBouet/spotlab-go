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
	"github.com/lucasbouet/spotlab-go/internal/auth"
	"github.com/lucasbouet/spotlab-go/internal/catalog"
	"github.com/lucasbouet/spotlab-go/internal/config"
	"github.com/lucasbouet/spotlab-go/internal/db"
	dbgen "github.com/lucasbouet/spotlab-go/internal/db/gen"
	"github.com/lucasbouet/spotlab-go/internal/devices"
	"github.com/lucasbouet/spotlab-go/internal/library"
	"github.com/lucasbouet/spotlab-go/internal/logging"
	"github.com/lucasbouet/spotlab-go/internal/playlists"
	"github.com/lucasbouet/spotlab-go/internal/social"
	"github.com/lucasbouet/spotlab-go/internal/stats"
	"github.com/lucasbouet/spotlab-go/internal/stream"
	"github.com/lucasbouet/spotlab-go/internal/sync"
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

	authService := auth.NewService(conn)
	activator, err := auth.NewActivator(authService, cfg.ActivationPublicKeyPath)
	if err != nil {
		logger.Error("activation RSA", "error", err)
		os.Exit(1)
	}

	router := apihttp.NewRouter(logger)
	requireAuth := auth.Mount(router, auth.Deps{
		Service:   authService,
		Activator: activator,
		SiteName:  cfg.SiteName,
		// Toujours fermé : l'activation par clé RSA est le seul point
		// d'entrée pour de nouvelles personnes (docs/PLAN.md §5).
		RegistrationEnabled: false,
	})

	deezerClient := catalog.NewDeezerClient()
	catalog.Mount(router, requireAuth, deezerClient, catalog.NewLyricsClient())

	queries := dbgen.New(conn)
	library.Mount(router, requireAuth, queries)
	playlists.Mount(router, requireAuth, queries)
	stats.Mount(router, requireAuth, queries, deezerClient, cfg.LastFMAPIKey)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Le Hub possède tout l'état de lecture/jam en mémoire — une seule
	// goroutine, tout le reste (appareils, amis) lui délègue présence et
	// diffusion par fermetures plutôt que par dépendance directe, pour
	// suivre la même séparation que le paquet sync lui-même impose entre
	// l'acteur et l'E/S DB (docs/PLAN.md §3.1).
	hub := sync.NewHub(logger)
	go hub.Run(ctx)

	isOnline := func(userID, deviceID string) bool { return hub.IsOnline(context.Background(), userID, deviceID) }
	broadcastDevices := func(userID string) {
		rows, err := queries.ListDevicesByUser(context.Background(), userID)
		if err != nil {
			return
		}
		hub.BroadcastDevices(context.Background(), userID, sync.DeviceDTOsFromRows(rows))
	}
	devices.Mount(router, requireAuth, queries, isOnline, broadcastDevices)

	activity := func(userID string) social.FriendActivityDTO {
		a := hub.Activity(context.Background(), userID)
		dto := social.FriendActivityDTO{Online: a.Online, IsPlaying: a.IsPlaying}
		if a.Track != nil {
			dto.Track = &social.FriendTrackDTO{Title: a.Track.Title, Artist: a.Track.Artist, Cover: a.Track.Cover}
		}
		return dto
	}
	social.Mount(router, requireAuth, queries, activity)

	sync.Mount(router, requireAuth, hub, queries)

	// The audio pipeline's downloads run on the server's own lifetime
	// context, not any single request's — see manager.go's doc comment.
	streamManager := stream.NewManager(ctx, cfg.StreamCacheDir, cfg.YTDLPPath, deezerClient)
	stream.Mount(router, requireAuth, streamManager)

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

	<-ctx.Done()

	logger.Info("arrêt en cours")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("arrêt forcé", "error", err)
	}
}
