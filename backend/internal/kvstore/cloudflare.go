// Package kvstore provides Cloudflare KV integration for route/account caching.
//
// # Consistency Model
//
// KV is a cache of route data with the PostgreSQL database as the source of truth.
// All writes are synchronous with retry to ensure consistency.
//
// Self-healing: When the Worker calls /api/internal/resolve, the backend queries
// the database and writes fresh data back to KV, reconciling any stale entries.
package kvstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// KVStore wraps the Cloudflare KV REST API for route/account data.
type KVStore struct {
	accountID   string
	namespaceID string
	apiToken    string
	client      *http.Client
}

// New creates a KVStore. Returns nil if required config is missing (KV sync disabled).
func New(accountID, namespaceID, apiToken string) *KVStore {
	if accountID == "" || namespaceID == "" || apiToken == "" {
		return nil
	}
	return &KVStore{
		accountID:   accountID,
		namespaceID: namespaceID,
		apiToken:    apiToken,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// RouteEntry is the value stored under route:{accountSlug}:{machineSlug}.
type RouteEntry struct {
	MachineID    string `json:"machine_id"`
	HostHostname string `json:"host_hostname"`
	ProxyToken   string `json:"proxy_token"`
}

// AccountEntry is the value stored under account:{accountSlug}.
type AccountEntry struct {
	AccountID int   `json:"account_id"`
	UserIDs   []int `json:"user_ids"`
}

// PutRouteSync writes a route entry synchronously with retry.
func (kv *KVStore) PutRouteSync(ctx context.Context, accountSlug, machineSlug string, entry RouteEntry) error {
	key := fmt.Sprintf("route:%s:%s", accountSlug, machineSlug)
	return kv.putWithRetry(key, entry, 3600)
}

// PutRoute writes a route entry with 1h TTL synchronously with retry.
func (kv *KVStore) PutRoute(ctx context.Context, accountSlug, machineSlug string, entry RouteEntry) error {
	key := fmt.Sprintf("route:%s:%s", accountSlug, machineSlug)
	return kv.putWithRetry(key, entry, 3600)
}

// DeleteRouteSync removes a route entry synchronously.
func (kv *KVStore) DeleteRouteSync(ctx context.Context, accountSlug, machineSlug string) error {
	key := fmt.Sprintf("route:%s:%s", accountSlug, machineSlug)
	return kv.delWithRetry(key)
}

// PutAccount writes an account entry with 24h TTL synchronously with retry.
func (kv *KVStore) PutAccount(ctx context.Context, accountSlug string, entry AccountEntry) error {
	key := fmt.Sprintf("account:%s", accountSlug)
	return kv.putWithRetry(key, entry, 86400)
}

// PutRevocation writes a user revocation entry with 24h TTL synchronously.
func (kv *KVStore) PutRevocation(ctx context.Context, userID int) error {
	key := fmt.Sprintf("revoked:%d", userID)
	return kv.putWithRetry(key, map[string]int64{"revoked_at": time.Now().Unix()}, 86400)
}

func (kv *KVStore) baseURL() string {
	return fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s",
		kv.accountID, kv.namespaceID)
}

func (kv *KVStore) putWithRetry(key string, value interface{}, ttl int) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i*500) * time.Millisecond) // exponential backoff
		}
		if err := kv.put(key, value, ttl); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (kv *KVStore) delWithRetry(key string) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i*500) * time.Millisecond)
		}
		if err := kv.del(key); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func (kv *KVStore) put(key string, value interface{}, ttl int) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	u := fmt.Sprintf("%s/values/%s?expiration_ttl=%d", kv.baseURL(), url.PathEscape(key), ttl)
	req, err := http.NewRequest("PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+kv.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := kv.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("KV PUT %s: status %d", key, resp.StatusCode)
	}
	return nil
}

func (kv *KVStore) del(key string) error {
	u := fmt.Sprintf("%s/values/%s", kv.baseURL(), url.PathEscape(key))
	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+kv.apiToken)

	resp, err := kv.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		return fmt.Errorf("KV DELETE %s: status %d", key, resp.StatusCode)
	}
	return nil
}
