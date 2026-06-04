package routing

import (
	"context"
	"log/slog"
)

type Reconciler struct {
	reader RouteReader
	kv     KVWriter
}

func NewReconciler(reader RouteReader, kv KVWriter) *Reconciler {
	return &Reconciler{reader: reader, kv: kv}
}

// ReconcileOnce compares kvRoutes against DB truth and deletes stale entries.
// kvRoutes keys are "accountSlug:machineSlug".
func (r *Reconciler) ReconcileOnce(ctx context.Context, kvRoutes map[string]KVRouteEntry) (int, error) {
	dbRoutes, err := r.reader.ListRunningRoutes(ctx)
	if err != nil {
		return 0, err
	}

	valid := make(map[string]struct{}, len(dbRoutes))
	for _, route := range dbRoutes {
		valid[route.AccountSlug+":"+route.MachineSlug] = struct{}{}
	}

	stale := 0
	for key := range kvRoutes {
		if _, ok := valid[key]; !ok {
			// Parse "accountSlug:machineSlug"
			for i := range key {
				if key[i] == ':' {
					if err := r.kv.DeleteRouteSync(ctx, key[:i], key[i+1:]); err != nil {
						slog.Warn("reconciler.delete.failed", "key", key, "error", err)
					} else {
						slog.Info("reconciler.deleted_stale", "key", key)
						stale++
					}
					break
				}
			}
		}
	}
	return stale, nil
}
