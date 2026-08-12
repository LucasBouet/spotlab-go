// Package config reads the server's environment into a single struct, read
// once at startup. No external dependency (viper etc.) — at this scale a
// handful of os.Getenv calls with defaults is the whole problem.
package config

import "os"

type Config struct {
	// Port the HTTP server listens on.
	Port string
	// DatabasePath is the SQLite file. The directory is created if missing.
	DatabasePath string
	// StreamCacheDir holds cached/in-progress audio files, one per Deezer track id.
	StreamCacheDir string
	// LastFMAPIKey enables genre resolution via Last.fm; empty falls back to
	// Deezer's coarser per-album genre only.
	LastFMAPIKey string
	// YTDLPPath is the yt-dlp binary. Empty means "look up PATH".
	YTDLPPath string
	// FFmpegPath is the ffmpeg binary, used only for GET /api/download's
	// on-the-fly MP3 transcode. Empty means "look up PATH".
	FFmpegPath string
	// ActivationPublicKeyPath overrides the embedded RSA public key, for
	// rotating it without a rebuild. Empty uses the embedded key.
	ActivationPublicKeyPath string
	// LogLevel is one of debug|info|warn|error.
	LogLevel string
	// SiteName is shown to clients via GET /api/config.
	SiteName string
}

func Load() Config {
	return Config{
		Port:                    getEnv("PORT", "8081"),
		DatabasePath:            getEnv("DATABASE_PATH", "./data/spotlab.db"),
		StreamCacheDir:          getEnv("STREAM_CACHE_DIR", "./cache/songs"),
		LastFMAPIKey:            os.Getenv("LASTFM_API_KEY"),
		YTDLPPath:               getEnv("YTDLP_PATH", "yt-dlp"),
		FFmpegPath:              getEnv("FFMPEG_PATH", "ffmpeg"),
		ActivationPublicKeyPath: os.Getenv("ACTIVATION_PUBLIC_KEY_PATH"),
		LogLevel:                getEnv("LOG_LEVEL", "info"),
		SiteName:                getEnv("SITE_NAME", "Spotlab"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
