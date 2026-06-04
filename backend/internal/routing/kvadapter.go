package routing

import (
	"context"

	"github.com/mathaix/openclawmachines/backend/internal/kvstore"
)

// KVAdapter bridges kvstore.KVStore to the routing.KVWriter interface.
type KVAdapter struct {
	kv *kvstore.KVStore
}

// NewKVAdapter wraps a KVStore as a KVWriter. Returns nil if kv is nil.
func NewKVAdapter(kv *kvstore.KVStore) KVWriter {
	if kv == nil {
		return nil
	}
	return &KVAdapter{kv: kv}
}

// PutRouteSync converts a KVRouteEntry to kvstore.RouteEntry and writes it.
func (a *KVAdapter) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry KVRouteEntry) error {
	return a.kv.PutRouteSync(ctx, accountSlug, machineSlug, kvstore.RouteEntry{
		MachineID:    entry.MachineID,
		HostHostname: entry.HostHostname,
		ProxyToken:   entry.ProxyToken,
	})
}

// DeleteRouteSync removes the route entry for the given account+machine slugs.
func (a *KVAdapter) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	return a.kv.DeleteRouteSync(ctx, accountSlug, machineSlug)
}

// PutAccount converts a KVAccountEntry to kvstore.AccountEntry and writes it.
func (a *KVAdapter) PutAccount(ctx context.Context, slug string, entry KVAccountEntry) error {
	return a.kv.PutAccount(ctx, slug, kvstore.AccountEntry{
		AccountID: entry.AccountID,
		UserIDs:   entry.UserIDs,
	})
}
