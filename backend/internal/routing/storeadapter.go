package routing

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// StoreAdapter bridges store.RouteRepo to the routing.RouteReader interface.
type StoreAdapter struct {
	repo store.RouteRepo
}

// NewStoreAdapter wraps a store.RouteRepo as a RouteReader.
func NewStoreAdapter(repo store.RouteRepo) RouteReader {
	return &StoreAdapter{repo: repo}
}

// ListRunningRoutes fetches running route data from the store and converts it to RouteSnapshots.
func (a *StoreAdapter) ListRunningRoutes(ctx context.Context) ([]RouteSnapshot, error) {
	rows, err := a.repo.ListRunningRoutes(ctx)
	if err != nil {
		return nil, err
	}
	snapshots := make([]RouteSnapshot, len(rows))
	for i, r := range rows {
		snapshots[i] = RouteSnapshot{
			AccountSlug:  r.AccountSlug,
			MachineID:    r.MachineID,
			MachineSlug:  r.MachineSlug,
			HostHostname: r.HostHostname,
			ProxyToken:   r.ProxyToken,
		}
	}
	return snapshots, nil
}
