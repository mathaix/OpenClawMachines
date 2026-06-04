package logging

import (
	"log/slog"
	"os"
	"strings"

	"github.com/mathaix/openclawmachines/backend/pkg/version"
)

// Init sets up the default slog logger with JSON output and common fields.
// Call once at the start of main() with the component name (e.g., "agent", "control-plane").
// All subsequent slog.Info/Error/Warn calls will include component + version fields.
// GCP Cloud Logging auto-parses JSON from stdout.
func Init(component string) {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	logger := slog.New(handler).With(
		slog.String("component", component),
		slog.String("version", version.Version),
		slog.String("commit", version.GitCommit),
	)
	slog.SetDefault(logger)
}
