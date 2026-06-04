package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// Activity provides a simple API for logging business-level events.
// Events are written fire-and-forget in a background goroutine using
// context.Background() so they survive request cancellation.
type Activity struct {
	store   store.ActivityRepo
	resolve ContextResolver
}

// New creates a new Activity logger. If resolve is nil, names/versions
// will not be enriched (IDs only).
func New(s store.ActivityRepo, resolve ContextResolver) *Activity {
	return &Activity{store: s, resolve: resolve}
}

// LogParams describes a single business-level event.
type LogParams struct {
	Category   string // "machine", "config", "auth", etc.
	Action     string // "machine.started", "config.channel_added"
	Status     string // "success" or "failure"
	ActorType  string // "user", "system", "agent"
	ActorID    *int   // user_id when user, host_id when agent, nil for system
	AccountID  *int
	MachineID  *string
	HostID     *int
	Summary    string         // human-readable
	Detail     map[string]any // structured context (optional)
	DurationMS *int           // elapsed time for the operation
	ErrorCode  *string
	ErrorMsg   *string
}

// Log writes a single activity event. It enriches the event with
// denormalized names/versions from the resolver and writes it in a
// background goroutine. Errors are logged but never returned.
func (a *Activity) Log(ctx context.Context, p LogParams) {
	if a == nil {
		return
	}

	entry := &store.ActivityLog{
		Category:     p.Category,
		Action:       p.Action,
		Status:       p.Status,
		ActorType:    p.ActorType,
		ActorID:      p.ActorID,
		AccountID:    p.AccountID,
		MachineID:    p.MachineID,
		HostID:       p.HostID,
		Summary:      p.Summary,
		StartedAt:    time.Now(),
		DurationMS:   p.DurationMS,
		ErrorCode:    p.ErrorCode,
		ErrorMessage: p.ErrorMsg,
	}

	if p.Detail != nil {
		if d, err := json.Marshal(p.Detail); err == nil {
			entry.Detail = d
		}
	}

	// Enrich with denormalized names and versions from resolver
	if a.resolve != nil {
		a.enrich(ctx, entry)
	}

	// Fire-and-forget on background context to survive request cancellation
	go func() {
		if err := a.store.CreateActivity(context.Background(), entry); err != nil {
			slog.Error("activity.log.failed",
				"action", entry.Action,
				"error", err,
			)
		}
	}()
}

// enrich populates denormalized name/version fields from the resolver.
func (a *Activity) enrich(ctx context.Context, entry *store.ActivityLog) {
	if entry.ActorType == "user" && entry.ActorID != nil {
		name := a.resolve.GetUserName(ctx, *entry.ActorID)
		if name != "" {
			entry.ActorName = &name
		}
	}
	if entry.ActorType == "system" {
		name := "system"
		entry.ActorName = &name
	}

	if entry.AccountID != nil {
		name := a.resolve.GetAccountName(ctx, *entry.AccountID)
		if name != "" {
			entry.AccountName = &name
		}
	}

	if entry.MachineID != nil {
		name := a.resolve.GetMachineName(ctx, *entry.MachineID)
		if name != "" {
			entry.MachineName = &name
		}
	}

	if entry.HostID != nil {
		hctx := a.resolve.GetHostContext(ctx, *entry.HostID)
		if hctx.Name != "" {
			entry.HostName = &hctx.Name
		}
		if hctx.AgentVersion != "" {
			entry.AgentVersion = &hctx.AgentVersion
		}
		if hctx.RootfsVersion != "" {
			entry.RootfsVersion = &hctx.RootfsVersion
		}
		if entry.ActorType == "agent" && entry.ActorID != nil {
			agentName := "agent"
			if hctx.AgentVersion != "" {
				agentName = "agent-" + hctx.AgentVersion
			}
			entry.ActorName = &agentName
		}
	}
}
