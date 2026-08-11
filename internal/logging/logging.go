// Package logging sets up the process-wide structured logger. JSON on
// stdout: under systemd that's captured by journald as-is, so
// `journalctl -u spotlab-go -f | jq` works with no extra plumbing.
package logging

import (
	"log/slog"
	"os"
)

func New(level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
