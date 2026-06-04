# Browser VM: Independent Browser VM Provisioning & Pairing

**Date:** 2026-04-09
**Branch:** `browser`
**Status:** Draft (v2 — addresses Codex review findings)

## Summary

Decouple browser VMs from the main machine lifecycle. Browser VMs become independent, account-scoped resources that users explicitly create, start, stop, and destroy. Machines connect to browser VMs via an explicit pairing action in the Browser tab. The AI agent inside the machine uses the browser VM's headless Chromium via CDP.

**Related design:** `docs/superpowers/specs/2026-04-10-vnc-browser-experience-design.md` covers the interactive VNC layer where users can watch the same browser session and take control for login or other human-only steps. That layer keeps CDP as the automation interface and adds VNC as the human-visible interface.

## Two Browser Paths (Scope Boundary)

This feature covers **browser VMs** — headless Chromium instances for AI agent use.

There is a separate, existing path: **CLI browse** (`ocm machines browse`), which reverse-tunnels the user's local Chrome into the machine via SSH. CLI browse is untouched by this feature — it continues to work independently of browser VMs.

| Path | Who uses it | Chrome location | Connection |
|------|------------|-----------------|------------|
| Browser VM (this feature) | AI agent inside the machine | Headless Chromium on host | Bridge network, CDP port 9222 |
| CLI browse (unchanged) | Human user | User's local Chrome | SSH reverse tunnel into VM |

A browse session (`ocm machines browse`) and a browser VM pairing can coexist on the same machine. The browse session writes its own `browser.cdpUrl` (pointing at `127.0.0.1:9222` via the SSH tunnel) which takes precedence while active. When the browse session ends, the gateway reverts to the browser VM's CDP endpoint if one is paired.

## Requirements

1. Browser VMs are independent resources with their own lifecycle (create, start, stop, destroy)
2. 1:1 pairing with machines for v1 (data model supports many-to-one later)
3. Browser VM must be on the same host as the paired machine
4. Browser VM lifecycle is independent — does not stop when the paired machine stops
5. Fixed sizing: 1 vCPU, 1024 MB RAM
6. Browser VMs are scoped to an account
7. Dedicated "Browser VMs" list in the dashboard
8. Pair/unpair from machine's Browser tab
9. CLI browse (`ocm machines browse`) is unaffected

## Data Model

### New table: `browser_vms`

```sql
CREATE TABLE browser_vms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      INT NOT NULL REFERENCES accounts(id),
    slug            TEXT NOT NULL,             -- 7-char random identifier (same generator as machines)
    name            TEXT NOT NULL DEFAULT '',
    host_id         INT REFERENCES hosts(id),
    vm_ip           TEXT,
    status          TEXT NOT NULL DEFAULT 'stopped',
                    -- stopped | provisioning | running | error
    vcpus           INT NOT NULL DEFAULT 1,
    memory_mb       INT NOT NULL DEFAULT 1024,
    cdp_port        INT NOT NULL DEFAULT 9222,
    rootfs_version  TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(account_id, slug)
);

CREATE INDEX idx_browser_vms_account ON browser_vms(account_id);
CREATE INDEX idx_browser_vms_host ON browser_vms(host_id) WHERE host_id IS NOT NULL;
```

**Slug format:** 7 random alphanumeric characters (e.g., `a3f7k2x`). No prefix in storage. Display can prefix with `browser-` in the UI for clarity.

### Machine pairing column

```sql
ALTER TABLE machines ADD COLUMN browser_vm_id UUID REFERENCES browser_vms(id);
CREATE UNIQUE INDEX idx_machines_browser_vm ON machines(browser_vm_id) WHERE browser_vm_id IS NOT NULL;
```

The unique index enforces 1:1 pairing — a browser VM can only be paired to one machine. This constraint can be dropped later to enable many-to-one.

### Placement

Browser VMs get their own placement table rather than overloading `machine_placements` (which has `machine_id NOT NULL` and many queries that assume machine semantics):

```sql
CREATE TABLE browser_vm_placements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    browser_vm_id   UUID NOT NULL REFERENCES browser_vms(id) ON DELETE CASCADE,
    host_id         INT NOT NULL REFERENCES hosts(id),
    vm_ip           TEXT,
    state           TEXT NOT NULL DEFAULT 'reserved',
    reserved_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at    TIMESTAMPTZ,
    released_at     TIMESTAMPTZ,
    CONSTRAINT browser_vm_placements_state_check CHECK (state IN ('reserved', 'active', 'released'))
);

CREATE UNIQUE INDEX idx_bvp_active_browser_vm
ON browser_vm_placements(browser_vm_id) WHERE released_at IS NULL;

CREATE UNIQUE INDEX idx_bvp_active_host_ip
ON browser_vm_placements(host_id, vm_ip) WHERE released_at IS NULL;

CREATE INDEX idx_bvp_host ON browser_vm_placements(host_id) WHERE released_at IS NULL;
```

### IP Allocation (IPAM)

Browser VMs need IPs from the host's bridge subnet that don't collide with machine IPs.

**Current scheme:** Machine IPs are allocated from `192.168.100.2–192.168.100.154`. Browser companion IPs were derived as `machine_ip + 100` (range `102–254`).

**New scheme:** Browser VMs get independent IPs from the same host bridge subnet. The PlacementService allocates IPs for both machines and browser VMs from a shared pool, preventing collisions. The `idx_bvp_active_host_ip` unique index (combined with `idx_placements_active_host_ip` on machine_placements) enforces no duplicate `(host_id, vm_ip)` pairs at the DB level. The PlacementService queries both tables when finding a free IP.

Browser VMs **keep their IP across stop/start** on the same host (stored in `browser_vms.vm_ip`). A fresh IP is allocated only on first start or when starting on a different host.

## API Endpoints

### Browser VM CRUD

All routes under `/api/accounts/{accountId}/browser-vms`.

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/` | Create a browser VM |
| `GET` | `/` | List account's browser VMs |
| `GET` | `/{browserVmId}` | Get details |
| `POST` | `/{browserVmId}/start` | Start (provision on a host) |
| `POST` | `/{browserVmId}/stop` | Stop (release host, keep record) |
| `DELETE` | `/{browserVmId}` | Destroy (stop + delete) |

#### Create request

```json
{
  "name": "my-browser"    // optional, auto-generated slug always created
}
```

#### Create response

```json
{
  "id": "uuid",
  "slug": "a3f7k2x",
  "name": "my-browser",
  "status": "stopped",
  "vcpus": 1,
  "memory_mb": 1024,
  "created_at": "..."
}
```

#### Start request

```json
{
  "host_id": 5,          // optional — if omitted, picks a host with capacity
  "region": "us-east1"   // optional — prefer hosts in this region
}
```

Starting a browser VM:
1. PlacementService reserves capacity (1 vCPU, 1024 MB) and allocates an IP on the target host
2. Control plane calls the agent on that host to create the browser VM
3. Agent stages browser rootfs (if not cached), boots Firecracker VM
4. Agent reports CDP readiness; control plane marks status `running`

**Note:** A browser VM can be started on any host with capacity. However, it can only be **paired** with a machine on the same host. The frontend's pairing dropdown filters to same-host browser VMs to make this clear.

#### Stop

Stops the Firecracker VM, releases placement (soft — preserves host affinity). Status → `stopped`. The `browser_vms` record persists. If paired to a machine, the machine's browser config is cleared (auto-unpair on stop — see Lifecycle Invariants).

#### Delete

Stops if running, then deletes the record. Auto-unpairs first if paired.

### Machine pairing

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/api/accounts/{accountId}/machines/{machineId}/pair-browser` | Pair |
| `DELETE` | `/api/accounts/{accountId}/machines/{machineId}/pair-browser` | Unpair |

#### Pair request

```json
{
  "browser_vm_id": "uuid"
}
```

#### Pairing validation

All of these must be true:
- Browser VM belongs to the same account
- Browser VM is on the same host as the machine
- Browser VM status is `running`
- Browser VM is not already paired to another machine (enforced by unique index + `SELECT FOR UPDATE`)
- Machine status is `running`

#### Pair — what happens

All operations run in a single DB transaction with `SELECT ... FOR UPDATE` on both the machine and browser VM rows:

1. Set `machines.browser_vm_id` = browser VM ID
2. Agent call: `AllowVMPair(machine.vm_ip, browser_vm.vm_ip)` — adds bridge firewall rules
3. Config assembly synthesizes the full browser config block (see Config Assembly Changes)
4. Push updated config to the running VM via `pushMachineConfig`
5. Gateway restart to pick up new browser config

If step 2-5 fails after step 1, the DB change is rolled back. The operation is atomic: either the full pair succeeds or nothing changes.

#### Unpair — what happens

1. Agent call: `RemoveVMPair(machine.vm_ip, browser_vm.vm_ip)` — removes firewall rules
2. Clear `machines.browser_vm_id`
3. Config assembly drops browser config block
4. Push updated config + gateway restart
5. Browser VM keeps running (independent lifecycle)

If agent call fails (e.g., host unreachable), DB change still proceeds — the reconciler will clean up stale firewall rules on next heartbeat.

### Agent API

New endpoints on the host agent (called by control plane):

| Method | Path | Action |
|--------|------|--------|
| `POST` | `/browser-vms` | Create a browser VM on this host |
| `DELETE` | `/browser-vms/{id}` | Destroy a browser VM on this host |
| `GET` | `/browser-vms/{id}/health` | CDP health check |
| `POST` | `/browser-vms/{id}/pair` | Add firewall rules for a machine↔browser pair |
| `DELETE` | `/browser-vms/{id}/pair` | Remove firewall rules for a pair |

#### Agent create request

```json
{
  "browser_vm_id": "uuid",
  "vm_ip": "192.168.100.110",
  "rootfs_version": "abc1234-20260409T120000Z"
}
```

The agent:
1. Stages browser rootfs from GCS (if not cached)
2. Reflink-copies base image to per-VM rootfs
3. Creates TAP device, configures network
4. Boots Firecracker with init-browser.sh
5. Waits for CDP port 9222 readiness
6. Returns success

#### Agent pair request

```json
{
  "machine_vm_ip": "192.168.100.10"
}
```

Calls `bridge.AllowVMPair(machineIP, browserIP)` to allow traffic between the two VMs on port 9222.

#### Agent unpair request

Calls `bridge.RemoveVMPair(machineIP, browserIP)` to remove the firewall rules.

## Orchestrator Changes

### Decouple browser VM from main VM

Currently `createBrowserVM()` is called inside `Create()` when `cfg.BrowserVMIP` is set. Changes:

1. Extract `createBrowserVM()` into a standalone method callable via the new agent endpoint
2. Browser VMs get their own entries in the orchestrator's VM map (keyed by browser VM ID)
3. `destroyBrowserVM()` becomes standalone — no longer coupled to main VM destroy
4. Remove `BrowserVMIP` from `VMConfig` and `VMRequest` structs
5. Remove `BrowserVMIP` and `BrowserVMStatus` from `VMInstance` and `VMResponse` structs
6. Keep `stageBrowserRootfs()` unchanged — it already works independently

### VM tracking

The orchestrator currently tracks browser VMs as part of the main VM's state. Change to:

```go
type firecrackerOrchestrator struct {
    vms        map[string]*vmInstance      // main VMs, keyed by machine ID
    browserVMs map[string]*browserInstance  // browser VMs, keyed by browser VM ID
    // ...
}

type browserInstance struct {
    ID         string
    VMIP       string
    SocketPath string
    TapDevice  string
    RootfsPath string
    Status     string  // creating | running | ready | error
    CDPPort    int
}
```

### Lifecycle states

**Canonical lifecycle** (DB/API/frontend):

| Status | Meaning |
|--------|---------|
| `stopped` | Record exists, no VM running |
| `provisioning` | Agent is booting the Firecracker VM |
| `running` | VM is up, CDP port is reachable |
| `error` | Boot failed or CDP unreachable (see `error_message`) |

**Agent-internal transient states** (not exposed to API):
- `creating` → maps to `provisioning` in DB
- `cdp_timeout` → maps to `error` in DB with message "CDP port 9222 not reachable"
- `ready` → maps to `running` in DB (CDP confirmed)

The frontend shows four badge colors: stopped (yellow), provisioning (blue), running (green), error (red).

### Recovery

On agent restart, recover browser VMs the same way main VMs are recovered — scan the state directory for browser VM state files, re-register them in the map. Firewall rules for active pairings are re-applied from persisted pair state (browser VM state file includes paired machine IP if any).

## Config Assembly Changes

### Source of truth for browser config

Currently: the `browser` capability row in `machine_capabilities` drives whether the browser config block is emitted. The capability template provides `browser.enabled`, `attachOnly`, `noSandbox`, `headless`.

**New source of truth: `machines.browser_vm_id`.**

When `browser_vm_id IS NOT NULL`:
- Config assembly looks up the browser VM's `vm_ip`
- Synthesizes the full browser config block directly (no capability template needed):
  ```json
  {
    "browser": {
      "enabled": true,
      "cdpUrl": "http://<browser_vm_ip>:9222",
      "attachOnly": true,
      "noSandbox": true,
      "headless": true
    }
  }
  ```

When `browser_vm_id IS NULL`:
- No browser block emitted (unless a CLI browse session is active, which has its own config path)

This replaces the capability-driven browser config entirely. The `resolveBrowserVMIP()` function in `machine_config.go` is removed.

### CLI browse session interaction

The existing browse session flow (`POST /browse-session`) already pushes its own browser config with `cdpUrl: "http://127.0.0.1:9222"` (pointing at the SSH reverse tunnel). This takes precedence while active because it triggers a gateway restart with the tunnel-based config.

When the browse session ends (`DELETE /browse-session/{id}` or janitor cleanup), the cleanup restores the machine's base config — which now comes from browser VM pairing if one exists. No special handling needed: `cleanupBrowseSession()` already calls `pushMachineConfig()` which re-assembles config from current state.

### Pair/unpair flow

- On pair: synthesize browser block, push config, restart gateway
- On unpair: drop browser block, push config, restart gateway
- Same mechanism as existing `pushMachineConfig` — no new config push path needed

## Lifecycle Invariants & Edge Cases

### Pairing breaks when hosts diverge

**Machine migrates to a different host:**
Machine start with affinity recovery or migration may land on a different host than the browser VM. On machine start, if `browser_vm_id` is set, check whether the browser VM is on the same host. If not: **auto-unpair** (`browser_vm_id = NULL`), log warning, emit activity event. The machine starts without browser. The user must re-pair after starting a browser VM on the new host.

**Browser VM stopped while paired:**
On browser VM stop: **auto-unpair**. Clear `machines.browser_vm_id` for any machine paired to this browser VM. Push updated config to the machine (drops browser block). Emit activity event.

**Browser VM deleted while paired:**
Delete auto-unpairs first (same as stop), then deletes the record.

**Browser VM enters error state while paired:**
Leave the pairing in place (the user may want to see the error and restart the browser VM). Config assembly checks browser VM status: if not `running`, the browser block is **not emitted** — the gateway gets no `cdpUrl` and the agent simply has no browser available. When the browser VM recovers to `running`, the next config push restores the browser block. The frontend shows the pairing with a warning badge so the user knows the browser VM needs attention.

**Host failure / agent restart:**
On agent restart, recovery re-registers browser VMs and re-applies firewall rules for known pairings. If a browser VM didn't survive (Firecracker process gone), the agent reports its status as `error`. The reconciler (heartbeat) reports this to the control plane, which auto-unpairs.

### Concurrent operations

**Two machines race to pair the same browser VM:**
`SELECT ... FOR UPDATE` on the browser VM row within the pairing transaction. The unique index on `machines(browser_vm_id)` is the final backstop — second transaction gets a constraint violation → 409 Conflict.

**Pair succeeds in DB but config push fails:**
Transaction rolls back (pair and config push are in the same operation). If the agent call (firewall rules) succeeded before the DB rollback, the reconciler removes stale rules on next heartbeat.

**Stop called on a paired browser VM:**
Allowed. Auto-unpairs (see above), then stops.

**Delete called on a paired browser VM:**
Allowed. Auto-unpairs, then stops, then deletes.

## Frontend

### New page: Browser VMs list

Route: `/accounts/{accountId}/browser-vms`

Dashboard sidebar gets a new "Browser VMs" entry (alongside Machines).

Features:
- List all browser VMs with status, host, paired machine name
- "New Browser VM" button → creates one (status: stopped)
- Start/stop/delete actions per row
- Status badges: stopped (yellow), provisioning (blue), running (green), error (red)

### Updated: Machine Browser tab

Replace the current toggle-based UI with a pairing interface:

**Unpaired state:**
- Dropdown showing available browser VMs (running, same host, same account, not already paired)
- "Pair" button
- Link to create a new browser VM if none available
- Note: if machine is stopped, show "Start machine to pair a browser VM"

**Paired state:**
- Shows paired browser VM slug, status, CDP endpoint
- "Unpair" button

### Removed

- Remove the existing browser capability toggle (the old `enableMachineCapability("browser", "managed")` flow)
- Remove `BrowserTab.tsx`'s current toggle UI — replace entirely with the pairing UI

## Artifact Pipeline

No changes needed. The browser rootfs build/upload pipeline is already complete and independent:

- `make build-browser-rootfs` → builds Alpine + Chromium ext4
- `make upload-browser-rootfs` → compresses, uploads to GCS `browser-rootfs/` prefix
- Agent fetches via `BROWSER_ROOTFS_GCS_MANIFEST` env var
- `stageBrowserRootfs()` handles download, caching, decompression

## Migration Path — Subsystem Checklist

### 1. Database

- [ ] New migration: `browser_vms` table
- [ ] New migration: `browser_vm_placements` table
- [ ] New migration: `machines.browser_vm_id` column
- [ ] No changes to `machine_placements` (browser VMs get their own table)

### 2. Store layer (`backend/internal/store/`)

- [ ] New: `BrowserVM` struct and CRUD methods (Create, Get, List, Update, Delete)
- [ ] New: `BrowserVMPlacement` methods (Reserve, Activate, Release)
- [ ] New: `PairBrowserVM(machineID, browserVMID)`, `UnpairBrowserVM(machineID)`
- [ ] Update: IP allocation queries to check both `machine_placements` and `browser_vm_placements`
- [ ] Remove: browser capability checks from machine start path

### 3. API layer (`backend/internal/api/`)

- [ ] New: browser VM CRUD handlers + routes
- [ ] New: pair/unpair handlers on machine routes
- [ ] Update: `machine_config.go` — remove `resolveBrowserVMIP()`, replace with `browser_vm_id` lookup
- [ ] Update: config push/preview to use browser VM pairing instead of capability
- [ ] Keep: browse session endpoints unchanged (`browse_session.go`)

### 4. RuntimeService (`backend/internal/machines/runtime.go`)

- [ ] Remove: browser capability detection in `start()` (lines ~396-417)
- [ ] Remove: `browserVMIP` from VM request assembly
- [ ] Add: auto-unpair check on machine start if browser VM is on different host
- [ ] Update: `pushConfigToRunningMachine()` to include browser block from pairing
- [ ] Update: machine upgrade path — same auto-unpair logic

### 5. Agent API (`backend/internal/agentapi/`)

- [ ] New: `handleCreateBrowserVM`, `handleDestroyBrowserVM`, `handleBrowserVMHealth`
- [ ] New: `handlePairBrowserVM`, `handleUnpairBrowserVM` (firewall rules)
- [ ] Remove: `BrowserVMIP` from `VMRequest` and `VMResponse`
- [ ] Update: `handleVMDiagnostics()` — browser VM diagnostics now use standalone lookup
- [ ] Update: health proxy — browser VM status from orchestrator's `browserVMs` map

### 6. Orchestrator (`backend/internal/orchestrator/`)

- [ ] Extract: `createBrowserVM()` → standalone `CreateBrowserVM(cfg BrowserVMConfig)`
- [ ] Extract: `destroyBrowserVM()` → standalone `DestroyBrowserVM(id string)`
- [ ] New: `browserVMs` map on `firecrackerOrchestrator`
- [ ] New: `browserInstance` struct
- [ ] New: `ListBrowserVMs()`, `GetBrowserVM(id)`
- [ ] Remove: `BrowserVMIP` from `VMConfig`
- [ ] Remove: `BrowserVMIP`, `BrowserVMStatus` from `VMInstance`
- [ ] Update: `Recover()` to also recover browser VMs from state dir
- [ ] Add: pair/unpair firewall rule methods

### 7. Network (`backend/internal/network/`)

- [ ] Keep: `BrowserVMIP()` function (may still be useful for IP range validation)
- [ ] Keep: `AllowVMPair()` / `RemoveVMPair()` — now called from pair/unpair endpoints

### 8. Metadata (`backend/internal/metadata/`)

- [ ] Remove: browser VM targeting fields that assumed per-machine companion model
- [ ] Update: agent client to use new browser VM endpoints

### 9. Config assembly (`backend/internal/configassembly/`)

- [ ] Update: `Assemble()` to check `browser_vm_id` instead of browser capability
- [ ] Synthesize full browser block from pairing (no capability template)
- [ ] Keep: browse session config path unchanged

### 10. Frontend (`frontend/`)

- [ ] New: `BrowserVMsPage.tsx` — list, create, start/stop/delete
- [ ] New: API client methods for browser VM endpoints
- [ ] Rewrite: `BrowserTab.tsx` — pairing UI replaces capability toggle
- [ ] Update: sidebar navigation — add "Browser VMs" entry
- [ ] Remove: old capability enable/disable for "browser"

### 11. Tests

- [ ] Remove: `TestBrowserVM()` and `TestBrowserVM_NoBrowserRootfs()` from integration tests (companion model)
- [ ] New: integration tests for standalone browser VM lifecycle
- [ ] New: integration tests for pairing/unpairing + firewall rules
- [ ] New: API tests for all browser VM endpoints
- [ ] New: store tests for browser VM CRUD and placement
- [ ] Update: runtime tests — remove browser capability mocks

## Testing

- **Unit tests:** Store CRUD for browser_vms, pairing validation (same account, same host, 1:1 constraint, race conditions)
- **Integration tests:** Full lifecycle — create browser VM, start, pair to machine, verify CDP reachable from machine, unpair, stop, delete
- **Edge case tests:** Auto-unpair on stop, auto-unpair on host divergence, concurrent pair attempts, browse session + pairing coexistence
- **API tests:** All new endpoints, error cases (pair wrong host, pair already-paired, pair stopped browser VM, etc.)
- **Frontend tests:** Browser VM list, pairing UI states, dropdown filtering
