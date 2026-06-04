# Metadata Server Pull-Based Secrets — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace push-based channel key delivery with pull-on-miss from the backend API so channel tokens resolve reliably via `ocm-secrets`.

**Architecture:** The metadata server's `/v1/secrets` endpoint gains a pull-through cache. On cache miss for a channel secret, it calls a new backend endpoint (`GET /api/agent/machines/{machineID}/secrets`) to fetch decrypted channel credentials, caches them with a 60s TTL, and returns them. The push-based channel key plumbing (`UpdateVMChannelKeys`, `RemoveVMChannelKey`, etc.) is removed.

**Tech Stack:** Go, HTTP, in-memory TTL cache

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `backend/internal/api/agent_auth.go` | Modify | Add `handleAgentAuthGetSecrets` handler |
| `backend/internal/api/server.go` | Modify | Register new route |
| `backend/internal/metadata/metadata.go` | Modify | Add `SecretCache` struct, `SecretFetcher` interface, TTL const |
| `backend/internal/metadata/server_linux.go` | Modify | Pull-on-miss logic in `handleSecrets` |
| `backend/internal/metadata/server_linux_test.go` | Modify | Add pull-cache tests |
| `backend/internal/api/channel_config.go` | Modify | Remove push/remove calls, simplify connect/disconnect |
| `backend/internal/api/machine_config.go` | Modify | Remove channel key push from `pushCredentialsToVM` |
| `backend/internal/agentclient/client.go` | Modify | Remove `UpdateVMChannelKeys`, `RemoveVMChannelKey` |
| `backend/internal/agentapi/handlers.go` | Modify | Remove `handleUpdateVMChannelKeys`, `handleRemoveVMChannelKey` |
| `backend/internal/agentapi/server.go` | Modify | Remove channel-keys routes |
| `backend/internal/agentapi/server_test.go` | Modify | Remove mock methods |
| `backend/internal/orchestrator/orchestrator.go` | Modify | Remove `UpdateChannelKeys`, `RemoveChannelKey` from interfaces |
| `backend/internal/orchestrator/firecracker_linux.go` | Modify | Remove implementations |
| `backend/internal/orchestrator/firecracker_stub.go` | Modify | Remove stubs |
| `backend/internal/metadata/metadata.go` | Modify | Remove `UpdateMachineChannelKeys`, `RemoveMachineChannelKey` |

---

### Task 1: Add backend endpoint for channel secrets

**Files:**
- Modify: `backend/internal/api/agent_auth.go` (add handler at end of file)
- Modify: `backend/internal/api/server.go:508-518` (add route)

- [ ] **Step 1: Add the handler to `agent_auth.go`**

Append to end of `backend/internal/api/agent_auth.go`:

```go
// handleAgentAuthGetSecrets returns decrypted channel credentials for a machine,
// keyed by the secret IDs that ocm-secrets requests (e.g. "channel-telegram-botToken").
// Called by the metadata server on cache miss to resolve exec secret refs.
func (s *Server) handleAgentAuthGetSecrets(w http.ResponseWriter, r *http.Request) {
	_, ok := s.authenticateAgentToken(w, r)
	if !ok {
		return
	}

	machineID := chi.URLParam(r, "machineID")

	if s.secretKey == "" {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}

	creds, err := s.store.ListMachineCredentialsWithValues(r.Context(), machineID)
	if err != nil {
		slog.Warn("agent_auth.get_secrets.list_failed", "machine_id", machineID, "error", err)
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}

	secrets := make(map[string]string)
	for _, cred := range creds {
		fieldName, ok := configassembly.ChannelTokenFieldName[cred.Provider]
		if !ok {
			continue // not a channel credential
		}
		val, err := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
		if err != nil {
			slog.Warn("agent_auth.get_secrets.decrypt_failed", "machine_id", machineID, "provider", cred.Provider, "error", err)
			continue
		}
		secrets[fmt.Sprintf("channel-%s-%s", cred.Provider, fieldName)] = val
	}

	writeJSON(w, http.StatusOK, secrets)
}
```

Note: This requires adding imports for `configassembly` and `crypto` to `agent_auth.go`. Add:
```go
"github.com/mathaix/openclawmachines/backend/internal/configassembly"
"github.com/mathaix/openclawmachines/backend/pkg/crypto"
```

- [ ] **Step 2: Register the route in `server.go`**

In `backend/internal/api/server.go`, after line 518 (`r.Post("/api/agent/machines/{machineID}/refresh-credential", ...)`), add:

```go
r.Get("/api/agent/machines/{machineID}/secrets", srv.handleAgentAuthGetSecrets)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd backend && go build ./...`
Expected: clean build

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/agent_auth.go backend/internal/api/server.go
git commit -m "feat: add agent-auth endpoint for channel secrets pull"
```

---

### Task 2: Add pull-through cache to metadata server

**Files:**
- Modify: `backend/internal/metadata/metadata.go` (add types + TTL const)
- Modify: `backend/internal/metadata/server_linux.go` (pull-on-miss in `handleSecrets`)
- Modify: `backend/internal/metadata/server_linux_test.go` (add tests)

- [ ] **Step 1: Write failing tests in `server_linux_test.go`**

Append to `backend/internal/metadata/server_linux_test.go`:

```go
func TestHandleSecrets_PullOnCacheMiss(t *testing.T) {
	// Register machine with NO channel keys — simulates the race condition.
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-pull",
		Secrets:   map[string]string{"OPIK_API_KEY": "opik-key"},
	})

	// Configure a mock fetcher that returns the telegram token.
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		return map[string]string{
			"channel-telegram-botToken": "pulled-tg-token",
		}, nil
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var secrets map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &secrets); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if secrets["OPIK_API_KEY"] != "opik-key" {
		t.Errorf("OPIK_API_KEY = %q, want opik-key", secrets["OPIK_API_KEY"])
	}
	if secrets["channel-telegram-botToken"] != "pulled-tg-token" {
		t.Errorf("channel-telegram-botToken = %q, want pulled-tg-token", secrets["channel-telegram-botToken"])
	}
}

func TestHandleSecrets_PullCacheTTL(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-ttl",
	})

	callCount := 0
	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		callCount++
		return map[string]string{"channel-telegram-botToken": "token-v" + fmt.Sprintf("%d", callCount)}, nil
	})

	// First call: cache miss, fetches from backend.
	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)
	if callCount != 1 {
		t.Fatalf("expected 1 fetch call, got %d", callCount)
	}

	// Second call: cache hit, no fetch.
	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 1 {
		t.Fatalf("expected still 1 fetch call (cached), got %d", callCount)
	}

	// Expire the cache by backdating fetchedAt.
	s.mu.Lock()
	cfg := s.configs[testVMIP]
	cfg.SecretCache.FetchedAt = time.Now().Add(-2 * SecretCacheTTL)
	s.configs[testVMIP] = cfg
	s.mu.Unlock()

	// Third call: cache expired, re-fetches.
	w = httptest.NewRecorder()
	s.handleSecrets(w, httptest.NewRequest("GET", "/v1/secrets", nil))
	if callCount != 2 {
		t.Fatalf("expected 2 fetch calls after expiry, got %d", callCount)
	}

	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)
	if secrets["channel-telegram-botToken"] != "token-v2" {
		t.Errorf("expected refreshed token, got %q", secrets["channel-telegram-botToken"])
	}
}

func TestHandleSecrets_PullFetchError(t *testing.T) {
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-err",
		Secrets:   map[string]string{"platform-key": "val"},
	})

	s.SecretFetcher = SecretFetcherFunc(func(machineID string) (map[string]string, error) {
		return nil, fmt.Errorf("backend unreachable")
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	// Should still return platform secrets even if pull fails.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var secrets map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &secrets)
	if secrets["platform-key"] != "val" {
		t.Errorf("platform-key = %q, want val", secrets["platform-key"])
	}
}

func TestHandleSecrets_NoFetcherFallsBack(t *testing.T) {
	// No SecretFetcher set — should behave like before (no pull).
	s := testServerWithMachine(MachineConfig{
		MachineID: "m-nofetcher",
		Secrets:   map[string]string{"key": "val"},
	})

	req := httptest.NewRequest("GET", "/v1/secrets", nil)
	w := httptest.NewRecorder()
	s.handleSecrets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
```

Note: add `"fmt"` and `"time"` to the test file imports if not already present.

- [ ] **Step 2: Run tests — they should fail**

Run: `cd backend && go test ./internal/metadata/ -run TestHandleSecrets_Pull -v`
Expected: FAIL — `SecretFetcher`, `SecretFetcherFunc`, `SecretCacheTTL`, `SecretCache` not defined

- [ ] **Step 3: Add types to `metadata.go`**

Add after the `CredentialEntry` struct (after line 36):

```go
// SecretCacheTTL is how long pulled secrets are cached before re-fetching.
const SecretCacheTTL = 60 * time.Second

// SecretFetcher pulls secrets from the backend API on cache miss.
type SecretFetcher interface {
	FetchSecrets(machineID string) (map[string]string, error)
}

// SecretFetcherFunc adapts a plain function to the SecretFetcher interface.
type SecretFetcherFunc func(machineID string) (map[string]string, error)

func (f SecretFetcherFunc) FetchSecrets(machineID string) (map[string]string, error) {
	return f(machineID)
}

// SecretCacheEntry holds pulled secrets with a timestamp for TTL expiry.
type SecretCacheEntry struct {
	Secrets   map[string]string
	FetchedAt time.Time
}
```

Add `SecretCache` field to `MachineConfig` (after the `Souls` field, line 59):

```go
SecretCache  SecretCacheEntry `json:"-"` // pull-through cache for channel secrets
```

Add `SecretFetcher` field to `Server` (after `AgentVersion`, line 26):

```go
SecretFetcher SecretFetcher // pulls secrets from backend on cache miss
```

- [ ] **Step 4: Implement pull-on-miss in `handleSecrets` in `server_linux.go`**

Replace the `handleSecrets` function (lines 88-113) with:

```go
func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.configFromRequest(w, r)
	if !ok {
		return
	}

	// Merge platform secrets and channel credential tokens into a single map.
	merged := make(map[string]string, len(cfg.Secrets)+len(cfg.ChannelKeys))
	for k, v := range cfg.Secrets {
		merged[k] = v
	}
	for provider, entry := range cfg.ChannelKeys {
		fieldName, ok := configassembly.ChannelTokenFieldName[provider]
		if !ok {
			continue
		}
		merged[fmt.Sprintf("channel-%s-%s", provider, fieldName)] = entry.Value
	}

	// Pull-through cache: if we have a fetcher, check if the cache has
	// additional secrets (e.g., channel tokens not present at RegisterMachine time).
	if s.SecretFetcher != nil {
		sourceIP := extractSourceIP(r.RemoteAddr)
		cached := s.getPulledSecrets(sourceIP, cfg.MachineID)
		for k, v := range cached {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		slog.Error("metadata.encode.failed", "path", r.URL.Path, "error", err)
	}
}
```

Add the `getPulledSecrets` helper to `server_linux.go` (after `handleSecrets`):

```go
// getPulledSecrets returns cached secrets, fetching from the backend if the
// cache is empty or stale. Results are stored in the MachineConfig for reuse.
func (s *Server) getPulledSecrets(vmIP, machineID string) map[string]string {
	s.mu.RLock()
	cfg, ok := s.configs[vmIP]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	// Return cached if fresh.
	if cfg.SecretCache.Secrets != nil && time.Since(cfg.SecretCache.FetchedAt) < SecretCacheTTL {
		return cfg.SecretCache.Secrets
	}

	// Fetch from backend.
	secrets, err := s.SecretFetcher.FetchSecrets(machineID)
	if err != nil {
		slog.Warn("metadata.secrets.pull_failed", "machine_id", machineID, "error", err)
		// Return stale cache if available.
		if cfg.SecretCache.Secrets != nil {
			return cfg.SecretCache.Secrets
		}
		return nil
	}

	// Update cache.
	s.mu.Lock()
	cfg, ok = s.configs[vmIP]
	if ok {
		cfg.SecretCache = SecretCacheEntry{
			Secrets:   secrets,
			FetchedAt: time.Now(),
		}
		s.configs[vmIP] = cfg
	}
	s.mu.Unlock()

	slog.Info("metadata.secrets.pulled", "machine_id", machineID, "secret_count", len(secrets))
	return secrets
}
```

- [ ] **Step 5: Run tests — they should pass**

Run: `cd backend && go test ./internal/metadata/ -run TestHandleSecrets -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/metadata/
git commit -m "feat: pull-through cache for channel secrets in metadata server"
```

---

### Task 3: Wire SecretFetcher in agent startup

**Files:**
- Modify: `backend/cmd/agent/main.go` or wherever the metadata server is constructed and wired

- [ ] **Step 1: Find where metadata server is created in agent startup**

Run: `grep -rn "metadata.New\|metadata.DefaultServer\|SetMetadataRegistrar" backend/cmd/agent/`

- [ ] **Step 2: Create an HTTP-based SecretFetcher and assign it to the metadata server**

After the metadata server is created and has `BackendURL` and `AgentToken` set, add:

```go
// Wire pull-through secret fetcher so channel tokens resolve on cache miss.
if metaSrv.BackendURL != "" && metaSrv.AgentToken != "" {
	metaSrv.SecretFetcher = &httpSecretFetcher{
		backendURL: metaSrv.BackendURL,
		agentToken: metaSrv.AgentToken,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}
```

Create the `httpSecretFetcher` implementation. This can go in a new file `backend/internal/metadata/fetcher.go`:

```go
package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// httpSecretFetcher fetches secrets from the backend API.
type httpSecretFetcher struct {
	backendURL string
	agentToken string
	client     *http.Client
}

func (f *httpSecretFetcher) FetchSecrets(machineID string) (map[string]string, error) {
	url := fmt.Sprintf("%s/api/agent/machines/%s/secrets", f.backendURL, machineID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.agentToken)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch secrets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("backend returned HTTP %d", resp.StatusCode)
	}

	var secrets map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&secrets); err != nil {
		return nil, fmt.Errorf("decode secrets: %w", err)
	}
	return secrets, nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd backend && go build ./...`
Expected: clean build

- [ ] **Step 4: Commit**

```bash
git add backend/internal/metadata/fetcher.go backend/cmd/agent/
git commit -m "feat: wire HTTP secret fetcher in agent startup"
```

---

### Task 4: Remove push-based channel key plumbing

**Files:**
- Modify: `backend/internal/api/channel_config.go` (simplify connect/disconnect)
- Modify: `backend/internal/api/machine_config.go:651-671` (remove channel key push)
- Modify: `backend/internal/agentclient/client.go` (remove `UpdateVMChannelKeys`, `RemoveVMChannelKey`)
- Modify: `backend/internal/agentapi/handlers.go` (remove handlers)
- Modify: `backend/internal/agentapi/server.go:97-98` (remove routes)
- Modify: `backend/internal/agentapi/server_test.go:127-133` (remove mock methods)
- Modify: `backend/internal/orchestrator/orchestrator.go` (remove from interfaces)
- Modify: `backend/internal/orchestrator/firecracker_linux.go:615-641` (remove impls)
- Modify: `backend/internal/orchestrator/firecracker_stub.go:52-58` (remove stubs)
- Modify: `backend/internal/metadata/metadata.go:277-308` (remove `UpdateMachineChannelKeys`, `RemoveMachineChannelKey`)

- [ ] **Step 1: Simplify `handleChannelConnect` in `channel_config.go`**

Replace lines 120-137 (the live update block) with:

```go
	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.connect.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			// Restart gateway — channel changes require restart, hot-reload ignores them.
			// The metadata server will pull the channel token from the backend on demand.
			if err := s.restartGateway(r.Context(), machine); err != nil {
				slog.Warn("channel.connect.restart_gateway_failed", "machine_id", machineID, "error", err)
			}
			liveUpdate = "sent"
		}
	}
```

- [ ] **Step 2: Simplify `handleChannelDisconnect` in `channel_config.go`**

Replace lines 182-198 (the live update block) with:

```go
	liveUpdate := "not_running"
	if machine.Status == "running" && machine.HostID != nil && s.agentClient != nil {
		if err := s.pushChannelOps(r.Context(), machine, ops); err != nil {
			slog.Error("channel.disconnect.push_failed", "machine_id", machineID, "channel", channelID, "error", err)
			liveUpdate = "failed"
		} else {
			// Restart gateway — channel changes require restart.
			if err := s.restartGateway(r.Context(), machine); err != nil {
				slog.Warn("channel.disconnect.restart_gateway_failed", "machine_id", machineID, "error", err)
			}
			liveUpdate = "sent"
		}
	}
```

- [ ] **Step 3: Remove `pushChannelKeysToVM` and `removeChannelKeyFromVM` from `channel_config.go`**

Delete the functions at lines 498-517:
- `func (s *Server) pushChannelKeysToVM(...)` (lines 498-508)
- `func (s *Server) removeChannelKeyFromVM(...)` (lines 510-517)

- [ ] **Step 4: Remove channel key push from `pushCredentialsToVM` in `machine_config.go`**

Delete lines 651-671 (the "Also push channel credential tokens" block):

```go
	// Also push channel credential tokens so exec secret refs can resolve.
	channelKeys := make(map[string]metadata.CredentialEntry)
	...
	if len(channelKeys) > 0 {
		if err := s.agentClient.UpdateVMChannelKeys(ctx, host, machine.ID, channelKeys); err != nil {
			return fmt.Errorf("push channel keys: %w", err)
		}
	}
```

- [ ] **Step 5: Remove `UpdateVMChannelKeys` and `RemoveVMChannelKey` from `agentclient/client.go`**

Delete lines 479-518 (both functions).

- [ ] **Step 6: Remove agent API handlers and routes**

In `backend/internal/agentapi/handlers.go`, delete `handleUpdateVMChannelKeys` (lines 550-568) and `handleRemoveVMChannelKey` (lines 570-584).

In `backend/internal/agentapi/server.go`, delete lines 97-98:
```go
r.Patch("/vms/{machineID}/channel-keys", s.handleUpdateVMChannelKeys)
r.Delete("/vms/{machineID}/channel-keys/{provider}", s.handleRemoveVMChannelKey)
```

- [ ] **Step 7: Remove from orchestrator interface and implementations**

In `backend/internal/orchestrator/orchestrator.go`:
- Remove `UpdateChannelKeys` (line 40) and `RemoveChannelKey` (line 43) from `Orchestrator` interface
- Remove `UpdateMachineChannelKeys` (line 69) and `RemoveMachineChannelKey` (line 70) from `MetadataRegistrar` interface

In `backend/internal/orchestrator/firecracker_linux.go`:
- Delete `UpdateChannelKeys` function (lines 615-627)
- Delete `RemoveChannelKey` function (lines 629-641)

In `backend/internal/orchestrator/firecracker_stub.go`:
- Delete `UpdateChannelKeys` stub (lines 52-54)
- Delete `RemoveChannelKey` stub (lines 56-58)

- [ ] **Step 8: Remove from metadata server**

In `backend/internal/metadata/metadata.go`:
- Delete `UpdateMachineChannelKeys` (lines 277-294)
- Delete `RemoveMachineChannelKey` (lines 296-308)

- [ ] **Step 9: Remove mock methods from `agentapi/server_test.go`**

Delete lines 127-133:
```go
func (m *mockOrchestrator) UpdateChannelKeys(...) error { return nil }
func (m *mockOrchestrator) RemoveChannelKey(...) error { return nil }
```

- [ ] **Step 10: Verify it compiles**

Run: `cd backend && go build ./...`
Expected: clean build. If there are unused import errors (e.g. `configassembly` no longer needed in `machine_config.go`), fix them.

- [ ] **Step 11: Run all tests**

Run: `cd backend && go test ./...`
Expected: ALL PASS

- [ ] **Step 12: Commit**

```bash
git add -A
git commit -m "refactor: remove push-based channel key plumbing, metadata server pulls on demand"
```

---

### Task 5: Run gateway e2e tests and verify

**Files:** None (verification only)

- [ ] **Step 1: Run gateway e2e tests**

Run: `make test-gateway-e2e`
Expected: PASS (~12s)

- [ ] **Step 2: Run full Go test suite**

Run: `make test-go`
Expected: PASS

- [ ] **Step 3: Commit if any fixes were needed**

---

### Task 6: Deploy and verify

- [ ] **Step 1: Deploy backend**

Run: `make deploy-backend`

- [ ] **Step 2: Upload agent**

Run: `make upload-agent`

- [ ] **Step 3: Wait for agent self-update**

Wait ~5 minutes for the agent to pick up the new binary (check heartbeat version in logs).

- [ ] **Step 4: Test: create new machine, connect Telegram, verify gateway starts**

The gateway should now resolve `channel-telegram-botToken` via the pull-through cache on the metadata server.
