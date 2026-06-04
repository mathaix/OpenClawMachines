package tunnel

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// MachineChecker checks whether a machine exists and is active (running/provisioning).
type MachineChecker interface {
	IsMachineActive(ctx context.Context, slug string) (bool, error)
}

// StartReaper runs a background goroutine that periodically removes orphaned VM tunnels.
// An orphaned tunnel is one with the "ocm-vm-" prefix that has no corresponding active machine.
func StartReaper(ctx context.Context, mgr *Manager, checker MachineChecker, interval time.Duration, dataPlaneDomain string) {
	if mgr == nil {
		return
	}
	dataPlaneDomain = strings.Trim(strings.TrimSpace(dataPlaneDomain), ".")
	if dataPlaneDomain == "" {
		slog.Error("tunnel.reaper.domain_missing", "error", "DATA_PLANE_DOMAIN is required for orphan tunnel cleanup")
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("tunnel.reaper.stopped")
				return
			case <-ticker.C:
				reapWithDomain(ctx, mgr, checker, dataPlaneDomain)
			}
		}
	}()
	slog.Info("tunnel.reaper.started", "interval", interval)
}

func reapWithDomain(ctx context.Context, mgr *Manager, checker MachineChecker, dataPlaneDomain string) {
	dataPlaneDomain = strings.Trim(strings.TrimSpace(dataPlaneDomain), ".")
	if dataPlaneDomain == "" {
		slog.Error("tunnel.reaper.domain_missing", "error", "DATA_PLANE_DOMAIN is required for orphan tunnel cleanup")
		return
	}

	tunnels, err := mgr.ListTunnels(ctx)
	if err != nil {
		slog.Error("tunnel.reaper.list_failed", "error", err)
		return
	}

	var orphaned, active, skipped int
	for _, t := range tunnels {
		if !strings.HasPrefix(t.Name, "ocm-vm-") {
			skipped++
			continue
		}

		// Extract machine slug from tunnel name "ocm-vm-{slug}"
		slug := strings.TrimPrefix(t.Name, "ocm-vm-")

		isActive, err := checker.IsMachineActive(ctx, slug)
		if err != nil {
			slog.Warn("tunnel.reaper.check_failed", "tunnel_name", t.Name, "slug", slug, "error", err)
			continue
		}

		if isActive {
			active++
			continue
		}

		// Orphaned tunnel — delete it (both HTTP and SSH hostnames)
		hostname := "m-" + slug + "." + dataPlaneDomain
		sshHostname := "ssh-" + slug + "." + dataPlaneDomain
		if err := mgr.DeleteTunnelAndDNS(ctx, t.ID, hostname, sshHostname); err != nil {
			slog.Error("tunnel.reaper.delete_failed", "tunnel_id", t.ID, "tunnel_name", t.Name, "error", err)
			continue
		}
		orphaned++
		slog.Info("tunnel.reaper.deleted_orphan", "tunnel_id", t.ID, "tunnel_name", t.Name)
	}

	if orphaned > 0 || active > 0 {
		slog.Info("tunnel.reaper.sweep_complete", "orphaned", orphaned, "active", active, "skipped", skipped)
	}
}
