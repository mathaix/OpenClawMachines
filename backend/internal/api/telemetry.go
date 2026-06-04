package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// TelemetryEvent represents a single telemetry event from frontend or worker.
type TelemetryEvent struct {
	Component string         `json:"component"` // "frontend", "worker"
	Event     string         `json:"event"`     // "ws.connect", "api.error", etc.
	Fields    map[string]any `json:"fields"`    // arbitrary structured data
	Timestamp time.Time      `json:"timestamp"`
}

func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	var events []TelemetryEvent
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Rate limit: max 100 events per request
	if len(events) > 100 {
		writeError(w, http.StatusBadRequest, "too many events (max 100)")
		return
	}

	for _, evt := range events {
		// Sanitize: only allow known components
		switch evt.Component {
		case "frontend", "worker":
			// ok
		default:
			continue
		}

		attrs := []any{
			"component", evt.Component,
			"event", evt.Event,
		}
		for k, v := range evt.Fields {
			attrs = append(attrs, k, v)
		}
		if !evt.Timestamp.IsZero() {
			attrs = append(attrs, "client_timestamp", evt.Timestamp)
		}

		slog.Info("telemetry", attrs...)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
