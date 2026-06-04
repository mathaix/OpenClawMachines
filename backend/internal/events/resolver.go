package events

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ContextResolver resolves entity IDs to denormalized names and versions.
type ContextResolver interface {
	GetUserName(ctx context.Context, userID int) string
	GetAccountName(ctx context.Context, accountID int) string
	GetMachineName(ctx context.Context, machineID string) string
	GetHostContext(ctx context.Context, hostID int) HostContext
}

// HostContext carries denormalized host metadata.
type HostContext struct {
	Name          string
	AgentVersion  string
	RootfsVersion string
}

// StoreResolver resolves names from the database with a TTL cache.
type StoreResolver struct {
	store store.Store

	mu    sync.RWMutex
	cache map[string]cachedEntry
}

type cachedEntry struct {
	value     string
	expiresAt time.Time
}

const cacheTTL = 5 * time.Minute

// NewStoreResolver creates a resolver backed by the given store.
func NewStoreResolver(s store.Store) *StoreResolver {
	return &StoreResolver{
		store: s,
		cache: make(map[string]cachedEntry),
	}
}

func (r *StoreResolver) get(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.value, true
}

func (r *StoreResolver) set(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = cachedEntry{value: value, expiresAt: time.Now().Add(cacheTTL)}
}

func (r *StoreResolver) GetUserName(ctx context.Context, userID int) string {
	key := "user:" + itoa(userID)
	if v, ok := r.get(key); ok {
		return v
	}
	user, err := r.store.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	r.set(key, user.Name)
	return user.Name
}

func (r *StoreResolver) GetAccountName(ctx context.Context, accountID int) string {
	key := "account:" + itoa(accountID)
	if v, ok := r.get(key); ok {
		return v
	}
	account, err := r.store.GetAccount(ctx, accountID)
	if err != nil {
		return ""
	}
	r.set(key, account.Name)
	return account.Name
}

func (r *StoreResolver) GetMachineName(ctx context.Context, machineID string) string {
	key := "machine:" + machineID
	if v, ok := r.get(key); ok {
		return v
	}
	machine, err := r.store.GetMachine(ctx, machineID)
	if err != nil {
		return ""
	}
	r.set(key, machine.Name)
	return machine.Name
}

func (r *StoreResolver) GetHostContext(ctx context.Context, hostID int) HostContext {
	host, err := r.store.GetHost(ctx, hostID)
	if err != nil {
		return HostContext{}
	}
	hc := HostContext{
		Name:          host.VMName,
		RootfsVersion: host.RootfsVersion,
	}
	if host.AgentVersion != nil {
		hc.AgentVersion = *host.AgentVersion
	}
	return hc
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
