package routing

import (
	"context"
	"log/slog"
	"time"
)

type Projector struct {
	reader RouteReader
	kv     KVWriter
}

func NewProjector(reader RouteReader, kv KVWriter) *Projector {
	return &Projector{reader: reader, kv: kv}
}

// SyncOnce reads all running routes from DB and writes them to KV.
func (p *Projector) SyncOnce(ctx context.Context) (int, error) {
	routes, err := p.reader.ListRunningRoutes(ctx)
	if err != nil {
		return 0, err
	}
	synced := 0
	for _, r := range routes {
		if err := p.kv.PutRouteSync(ctx, r.AccountSlug, r.MachineSlug, KVRouteEntry{
			MachineID:    r.MachineID,
			HostHostname: r.HostHostname,
			ProxyToken:   r.ProxyToken,
		}); err != nil {
			slog.Warn("projector.sync.failed", "account", r.AccountSlug, "machine", r.MachineSlug, "error", err)
			continue
		}
		synced++
	}
	return synced, nil
}

// Start runs the projector in a loop until ctx is cancelled.
func (p *Projector) Start(ctx context.Context, interval time.Duration) {
	slog.Info("projector.started", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("projector.stopped")
			return
		case <-ticker.C:
			count, err := p.SyncOnce(ctx)
			if err != nil {
				slog.Error("projector.sync.error", "error", err)
			} else {
				slog.Debug("projector.sync.complete", "routes_synced", count)
			}
		}
	}
}
