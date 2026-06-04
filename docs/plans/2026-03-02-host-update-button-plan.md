# Host Update Button Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add an "Update" button per host in the Admin Hosts table that shows version staleness, gracefully drains VMs, and triggers an agent self-update.

**Architecture:** New agent endpoint `POST /trigger-update` stops VMs and runs self-update. Backend proxies the call from an admin endpoint. Frontend shows version badge + update button. Agent startup cleans up stale TAP devices.

**Tech Stack:** Go (agent + backend), TypeScript/React (frontend), GCS (manifests)

---

### Task 1: Stale TAP cleanup on agent startup

When the agent restarts, orphaned TAP devices from dead VMs can block new VM creation. Add cleanup in the orchestrator startup.

**Files:**
- Modify: `backend/internal/network/bridge_linux.go`
- Modify: `backend/internal/orchestrator/firecracker_linux.go:49-80`
- Test: `backend/internal/network/bridge_linux_test.go` (new)

**Step 1: Add `ListTaps` method to bridge**

Add to `backend/internal/network/bridge_linux.go` after `RemoveTap` (line 70):

```go
// ListTaps returns the names of all TAP devices attached to this bridge.
func (b *Bridge) ListTaps() ([]string, error) {
	out, err := exec.Command("ip", "-o", "link", "show", "master", b.Name, "type", "tun").Output()
	if err != nil {
		return nil, fmt.Errorf("list taps on %s: %w", b.Name, err)
	}
	var taps []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "8: tap69e0d67f-e0: <FLAGS> ..."
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 2 {
			name := strings.TrimSpace(parts[1])
			if strings.HasPrefix(name, "tap") {
				taps = append(taps, name)
			}
		}
	}
	return taps, nil
}
```

**Step 2: Add `cleanupStaleTaps` to orchestrator**

Add to `backend/internal/orchestrator/firecracker_linux.go` after `cleanupOrphanedVM` (line 723):

```go
// cleanupStaleTaps removes TAP devices on the bridge that don't belong to any known VM.
// This handles the case where the agent restarted without persisting VM state
// (e.g., during self-update or crash).
func (o *firecrackerOrchestrator) cleanupStaleTaps() {
	taps, err := o.bridge.ListTaps()
	if err != nil {
		slog.Warn("tap.stale_scan_failed", "error", err)
		return
	}

	// Build set of known TAP names from loaded VMs
	knownTaps := make(map[string]bool)
	o.mu.Lock()
	for _, vm := range o.vms {
		knownTaps[vm.tapDevice] = true
	}
	o.mu.Unlock()

	for _, tap := range taps {
		if knownTaps[tap] {
			continue
		}
		slog.Warn("tap.stale_cleanup", "tap_name", tap)
		if err := o.bridge.RemoveTap(tap); err != nil {
			slog.Warn("tap.stale_cleanup_failed", "tap_name", tap, "error", err)
		}
	}
}
```

**Step 3: Call it from `New()`**

In `backend/internal/orchestrator/firecracker_linux.go`, after `o.loadState()` (line 77), add:

```go
	// Clean up any TAP devices that survived a restart but weren't in persisted state
	o.cleanupStaleTaps()
```

**Step 4: Run tests**

Run: `make test-go`
Expected: PASS (no existing tests break)

**Step 5: Commit**

```bash
git add backend/internal/network/bridge_linux.go backend/internal/orchestrator/firecracker_linux.go
git commit -m "fix: clean up stale TAP devices on agent startup"
```

---

### Task 2: Agent `POST /trigger-update` endpoint

Add the endpoint that stops all VMs and triggers a self-update.

**Files:**
- Modify: `backend/internal/agentapi/server.go:15-37` (Server struct + NewServer)
- Create: `backend/internal/agentapi/handlers_update.go`
- Modify: `backend/internal/selfupdate/updater.go` (add `TriggerUpdate` method)

**Step 1: Add updater to Server struct**

In `backend/internal/agentapi/server.go`, add the import and field:

```go
import (
	// ... existing imports
	"github.com/mathaix/openclawmachines/backend/internal/selfupdate"
)
```

Add field to Server struct (line 23):
```go
	updater      *selfupdate.Updater // nil if self-update not configured
```

Update `NewServer` signature and wiring (line 26):
```go
func NewServer(agentToken string, orch orchestrator.Orchestrator, allowedCIDRs string, proxy *apiproxy.Proxy, metadataAddr string, updater *selfupdate.Updater) *Server {
	return &Server{
		agentToken:   agentToken,
		startTime:    time.Now(),
		orchestrator: orch,
		progress:     NewProgressManager(),
		logMgr:       NewLogManager(1000),
		allowedCIDRs: allowedCIDRs,
		proxy:        proxy,
		metadataAddr: metadataAddr,
		updater:      updater,
	}
}
```

**Step 2: Add route to control router**

In `backend/internal/agentapi/server.go`, inside the authenticated group (after line 64 — `r.Delete("/vms/{machineID}", ...)`), add:

```go
		r.Post("/trigger-update", s.handleTriggerUpdate)
```

**Step 3: Create handler file**

Create `backend/internal/agentapi/handlers_update.go`:

```go
package agentapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

var updateInProgress atomic.Bool

func (s *Server) handleTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "unavailable",
			"message": "self-update not configured",
		})
		return
	}

	if !updateInProgress.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"status":  "already_updating",
			"message": "an update is already in progress",
		})
		return
	}

	// Count running VMs for the response
	vms, _ := s.orchestrator.List(r.Context())
	vmCount := len(vms)

	slog.Info("trigger_update.accepted", "vm_count", vmCount)

	// Return 202 immediately, do the work in background
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":   "updating",
		"vm_count": vmCount,
	})

	go func() {
		defer updateInProgress.Store(false)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Step 1: Gracefully stop all VMs
		if vmCount > 0 {
			slog.Info("trigger_update.draining", "vm_count", vmCount)
			if err := s.orchestrator.Shutdown(ctx); err != nil {
				slog.Error("trigger_update.drain_failed", "error", err)
				return
			}
			slog.Info("trigger_update.drained")
		}

		// Step 2: Run self-update check
		slog.Info("trigger_update.checking")
		updated, err := s.updater.CheckAndUpdate(ctx)
		if err != nil {
			slog.Error("trigger_update.failed", "error", err)
			return
		}
		if !updated {
			slog.Info("trigger_update.already_current")
		}
		// If updated, the agent will restart via systemctl — this goroutine will be killed
	}()
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
```

**Step 4: Update main.go wiring**

In `backend/cmd/agent/main.go` line 189, update the `NewServer` call to pass the updater:

```go
	srv := agentapi.NewServer(cfg.AgentToken, orch, cfg.ControlAllowedCIDRs, proxy, metadataAddr, updater)
```

Note: `updater` may be nil if `cfg.AgentGCSManifest` is empty — the handler checks for this.

**Step 5: Run tests**

Run: `make test-go`
Expected: PASS. Fix any compile errors from the `NewServer` signature change (update test files calling `NewServer`).

Check `backend/internal/agentapi/server_test.go` — it likely calls `NewServer` and will need the extra `nil` param.

**Step 6: Commit**

```bash
git add backend/internal/agentapi/ backend/cmd/agent/main.go backend/internal/selfupdate/
git commit -m "feat(agent): add POST /trigger-update endpoint"
```

---

### Task 3: Backend admin endpoint + agent client method

Add the backend endpoint that proxies the trigger-update call to the agent.

**Files:**
- Modify: `backend/internal/agentclient/client.go` (add `TriggerUpdate`)
- Modify: `backend/internal/api/server.go` (add route + handler)

**Step 1: Add `TriggerUpdate` to agent client**

In `backend/internal/agentclient/client.go`, after `RefreshRootfs` (line ~355), add:

```go
// TriggerUpdate tells the agent to stop all VMs and self-update.
func (c *Client) TriggerUpdate(ctx context.Context, host *store.Host) error {
	url := c.agentURL(host) + "/trigger-update"

	resp, err := c.doRequest(ctx, "POST", url, nil)
	if err != nil {
		return fmt.Errorf("trigger update on agent: %w", err)
	}
	defer resp.Body.Close()

	// 202 = accepted, 409 = already updating — both are "success" from backend perspective
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("trigger update: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
```

**Step 2: Add route**

In `backend/internal/api/server.go`, inside the admin route group (after `r.Post("/hosts/{hostId}/refresh-rootfs", ...)` around line 248), add:

```go
	r.Post("/hosts/{hostId}/trigger-update", srv.handleTriggerHostUpdate)
```

**Step 3: Add handler**

In `backend/internal/api/server.go`, after `handleRefreshRootfs` (around line 1585), add:

```go
func (s *Server) handleTriggerHostUpdate(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseIntParam(r, "hostId")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}

	host, err := s.store.GetHost(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	if host.Status != "ready" {
		writeError(w, http.StatusConflict, "host is not ready (status: "+host.Status+")")
		return
	}

	slog.Info("admin.trigger_update", "host_id", hostID, "host_name", host.VMName)

	if err := s.agentClient.TriggerUpdate(r.Context(), host); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "updating",
		"message": "update triggered on " + host.VMName,
	})
}
```

**Step 4: Run tests**

Run: `make test-go`
Expected: PASS

**Step 5: Commit**

```bash
git add backend/internal/agentclient/client.go backend/internal/api/server.go
git commit -m "feat(backend): add POST /admin/hosts/{id}/trigger-update endpoint"
```

---

### Task 4: Backend latest-versions endpoint (GCS manifest reader)

Expose the latest agent + rootfs versions from GCS manifests so the frontend can compare.

**Files:**
- Create: `backend/internal/api/gcs_manifest.go`
- Modify: `backend/internal/api/server.go` (add route)

**Step 1: Create the manifest reader with caching**

Create `backend/internal/api/gcs_manifest.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"github.com/mathaix/openclawmachines/backend/internal/selfupdate"
)

type latestVersionsCache struct {
	mu        sync.Mutex
	result    *LatestVersionsResponse
	fetchedAt time.Time
	ttl       time.Duration
}

type LatestVersionsResponse struct {
	Agent  *ManifestInfo `json:"agent"`
	Rootfs *ManifestInfo `json:"rootfs"`
}

type ManifestInfo struct {
	Version        string `json:"version"`
	BuiltAt        string `json:"built_at,omitempty"`
	OpenClawVersion string `json:"openclaw_version,omitempty"`
}

var versionsCache = &latestVersionsCache{ttl: 60 * time.Second}

func (s *Server) handleLatestVersions(w http.ResponseWriter, r *http.Request) {
	versionsCache.mu.Lock()
	if versionsCache.result != nil && time.Since(versionsCache.fetchedAt) < versionsCache.ttl {
		cached := versionsCache.result
		versionsCache.mu.Unlock()
		writeJSON(w, http.StatusOK, cached)
		return
	}
	versionsCache.mu.Unlock()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	client, err := storage.NewClient(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create GCS client: "+err.Error())
		return
	}
	defer client.Close()

	result := &LatestVersionsResponse{}

	// Fetch agent manifest
	agentManifest, err := selfupdate.FetchManifest(ctx, client, "gs://openclawmachines/agent/manifest.json")
	if err != nil {
		slog.Warn("latest_versions.agent_manifest_failed", "error", err)
	} else {
		result.Agent = &ManifestInfo{
			Version: agentManifest.Version,
			BuiltAt: agentManifest.BuiltAt,
		}
	}

	// Fetch rootfs manifest
	rootfsManifest, err := selfupdate.FetchManifest(ctx, client, "gs://openclawmachines/rootfs/manifest.json")
	if err != nil {
		slog.Warn("latest_versions.rootfs_manifest_failed", "error", err)
	} else {
		result.Rootfs = &ManifestInfo{
			Version:        rootfsManifest.Version,
			BuiltAt:        rootfsManifest.BuiltAt,
			OpenClawVersion: rootfsManifest.OpenClawVersion,
		}
	}

	// Cache the result
	versionsCache.mu.Lock()
	versionsCache.result = result
	versionsCache.fetchedAt = time.Now()
	versionsCache.mu.Unlock()

	writeJSON(w, http.StatusOK, result)
}
```

Note: Check if `selfupdate.Manifest` has `BuiltAt` and `OpenClawVersion` fields. If not, add them to the struct in `backend/internal/selfupdate/manifest.go`. The rootfs manifest JSON includes `"built_at"` and `"openclaw_version"` — make sure the Go struct has matching `json` tags.

**Step 2: Add route**

In `backend/internal/api/server.go`, inside the admin route group, add:

```go
	r.Get("/latest-versions", srv.handleLatestVersions)
```

**Step 3: Check manifest struct**

Read `backend/internal/selfupdate/manifest.go` and ensure the `Manifest` struct includes:
- `BuiltAt string` with `json:"built_at"`
- `OpenClawVersion string` with `json:"openclaw_version"`

If missing, add them — they're present in the rootfs manifest JSON but may not be in the Go struct.

**Step 4: Run tests**

Run: `make test-go`
Expected: PASS

**Step 5: Commit**

```bash
git add backend/internal/api/gcs_manifest.go backend/internal/api/server.go backend/internal/selfupdate/manifest.go
git commit -m "feat(backend): add GET /admin/latest-versions endpoint"
```

---

### Task 5: Frontend — API calls + version badge + update button

Add the update UI to the Admin Hosts table.

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/pages/admin/AdminHosts.tsx`

**Step 1: Add API functions**

In `frontend/src/lib/api.ts`, after `refreshHostRootfs`, add:

```typescript
export const triggerHostUpdate = (hostId: number) =>
  request<{ status: string; message: string }>(`/admin/hosts/${hostId}/trigger-update`, { method: "POST" });

export interface LatestVersions {
  agent?: { version: string; built_at?: string };
  rootfs?: { version: string; openclaw_version?: string; built_at?: string };
}

export const getLatestVersions = () =>
  request<LatestVersions>("/admin/latest-versions");
```

**Step 2: Add state and fetch latest versions**

In `frontend/src/pages/admin/AdminHosts.tsx`, add imports and state:

```typescript
import { listHosts, destroyHost, refreshHostRootfs, triggerHostUpdate, getLatestVersions, type LatestVersions } from "../../lib/api";
```

Add state variables alongside existing ones:
```typescript
const [updatingId, setUpdatingId] = useState<number | null>(null);
const [latestVersions, setLatestVersions] = useState<LatestVersions | null>(null);
```

Fetch latest versions on mount (inside existing `useEffect` or add a new one):
```typescript
useEffect(() => {
  getLatestVersions().then(setLatestVersions).catch(() => {});
}, []);
```

**Step 3: Add update handler**

After `handleRefreshRootfs`, add:

```typescript
const handleTriggerUpdate = async (host: Host) => {
  const vmCount = host.machine_count || 0;
  const msg = vmCount > 0
    ? `This will stop ${vmCount} running machine(s) on ${host.vm_name} and restart the agent. Continue?`
    : `This will restart the agent on ${host.vm_name}. Continue?`;
  if (!confirm(msg)) return;

  setUpdatingId(host.id);
  setError(null);
  try {
    await triggerHostUpdate(host.id);
    toast({ title: "Update triggered", description: `${host.vm_name} is updating...` });
  } catch (err) {
    const msg = err instanceof Error ? err.message : "Failed to trigger update";
    setError(msg);
    toast({ title: "Update failed", description: msg, variant: "error" });
  } finally {
    setUpdatingId(null);
  }
};
```

**Step 4: Add version badge helper**

Add a helper function in the component:

```typescript
const isStale = (host: Host) =>
  latestVersions?.agent?.version && host.agent_version && host.agent_version !== latestVersions.agent.version;
```

**Step 5: Add badge + button to table row**

In the table row, after the `agent_version` display (or in the actions cell alongside Refresh/Destroy), add the version badge:

```tsx
{/* Version status badge */}
{latestVersions?.agent?.version && (
  <span className={`ml-2 inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${
    !host.agent_version
      ? "bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400"
      : isStale(host)
      ? "bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400"
      : "bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400"
  }`}>
    {!host.agent_version ? "Unknown" : isStale(host) ? "Update available" : "Current"}
  </span>
)}
```

Add the Update button in the actions cell (after the Refresh button):

```tsx
{isStale(host) && (
  <button
    onClick={() => handleTriggerUpdate(host)}
    disabled={updatingId === host.id || host.status !== "ready"}
    className="inline-flex items-center gap-1 text-amber-600 dark:text-amber-400 hover:text-amber-800 dark:hover:text-amber-300 text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed"
    title="Stop VMs and update agent"
  >
    {updatingId === host.id && (
      <svg className="animate-spin h-3 w-3" viewBox="0 0 24 24" fill="none">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
      </svg>
    )}
    {updatingId === host.id ? "Updating..." : "Update"}
  </button>
)}
```

**Step 6: Run typecheck**

Run: `make typecheck`
Expected: PASS

**Step 7: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/pages/admin/AdminHosts.tsx
git commit -m "feat(admin): version badge and update button in hosts table"
```

---

### Task 6: Deploy and verify

**Step 1: Deploy backend**

```bash
make deploy-backend
```

**Step 2: Deploy frontend**

```bash
make deploy-frontend
```

**Step 3: Upload agent**

```bash
make upload-agent
```

The agent needs the new `/trigger-update` endpoint. Wait for self-update (~5 min).

**Step 4: Verify**

1. Open Admin panel → Hosts tab
2. Confirm version badge shows "Current" or "Update available"
3. If stale, click Update → confirm dialog → agent updates

**Step 5: Commit docs update**

```bash
# Update docs/CurrentFeature.md with the new feature
git add docs/CurrentFeature.md
git commit -m "docs: add host update button to current feature"
```
