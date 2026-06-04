# Host Update Button — Design

## Goal

Let admins see which hosts are running outdated agent/rootfs versions and trigger an update with one click from the Admin Hosts table. The update gracefully drains running VMs, downloads the latest agent binary, and restarts the agent.

## Architecture

Split across three layers: frontend badge + button, backend proxy + manifest reader, agent trigger endpoint.

```
Admin clicks "Update"
    │
    ▼
Frontend: POST /api/admin/hosts/{id}/update-agent
    │
    ▼
Backend: validates host status, proxies to agent via internal IP
    │
    ▼
Agent (port 9090): POST /trigger-update
    → returns 202 immediately
    → background: stop all VMs → download new binary → restart
    │
    ▼
Agent restarts, next heartbeat reports new agent_version
Frontend sees updated version on next 5s poll
```

## Components

### Agent: `POST /trigger-update` endpoint

New endpoint on the control router (port 9090, bearer-token authed).

- Returns `202 Accepted` with `{"status": "updating", "vm_count": N}` immediately.
- Returns `409 Conflict` if an update is already in progress.
- Spawns a background goroutine:
  1. Stops all running VMs gracefully via `orchestrator.DestroyAll()`.
  2. Calls `updater.CheckAndUpdate()` — bypasses idle check and timer.
  3. If update found: downloads, verifies SHA256, replaces binary, restarts via `systemctl restart ocm-agent`.
  4. If already current: logs it, no restart.

### Backend: Two new admin endpoints

**`GET /api/admin/latest-versions`**
- Reads `gs://openclawmachines/agent/manifest.json` and `gs://openclawmachines/rootfs/manifest.json` from GCS.
- Returns latest version info for both artifacts.
- Cached in memory for 60 seconds.

**`POST /api/admin/hosts/{hostId}/update-agent`**
- Validates host exists and status is `ready`.
- Proxies `POST /trigger-update` to the agent via `agentClient` (internal IP:9090).
- Returns the agent's response (202 or 409).

### Frontend: Admin Hosts table changes

**Version badge** per host row:
- Green "Current" badge when `host.agent_version === latest.agent.version`.
- Yellow "Update available" badge when versions differ.
- Gray "Unknown" when `agent_version` is null.

**Update button** in the actions column:
- Enabled when version is stale and host status is `ready`.
- Disabled with tooltip when host is offline, already updating, or current.

**Confirmation dialog:**
- "This will stop N running machine(s) on this host and restart the agent. Continue?"
- Shows even when `machine_count` is 0 (still restarts the agent).

**During update:**
- Button shows spinner.
- Resolves when next heartbeat reports the new version (up to 60s).

## Data: Latest versions fetch

Frontend calls `GET /api/admin/latest-versions` once on Admin page load. Response:

```json
{
  "agent": {
    "version": "2e7f8b3-20260302T195637Z",
    "built_at": "2026-03-02T20:05:48Z"
  },
  "rootfs": {
    "version": "2e7f8b3-20260302T200453Z",
    "openclaw_version": "2026.2.26",
    "built_at": "2026-03-02T20:05:36Z"
  }
}
```

Backend caches this for 60s in memory (simple `sync.Once` + timestamp check).

## Error Handling

| Scenario | Behavior |
|----------|----------|
| Host offline (no heartbeat) | Button disabled, tooltip: "Host unreachable" |
| Host already updating | Agent returns 409, frontend shows toast: "Update already in progress" |
| Agent can't reach GCS | Agent logs error, stays on current version |
| VM stop fails | Agent logs error, aborts update |
| Heartbeat delayed after restart | Frontend shows "Updating..." until heartbeat arrives |

No rollback mechanism — if the new binary fails, the host goes `unreachable` via existing heartbeat monitoring.

## Agent Startup: Stale Resource Cleanup

When the agent restarts (self-update, host reboot, crash), Firecracker VMs are destroyed but their TAP network devices survive. If a machine with the same ID is started again, the TAP name collides (`ioctl(TUNSETIFF): Device or resource busy`).

**Fix:** On agent startup, before accepting any VM create requests, scan for and remove orphaned TAP devices attached to the bridge:

1. List all TAP devices on `ocm-br0` (`ip link show master ocm-br0 type tuntap`)
2. Compare against known VMs (none after fresh restart)
3. Delete any orphaned TAPs (`ip link delete <tap>`)
4. Log each cleanup: `slog.Warn("tap.stale_cleanup", "tap_name", name)`

This runs once in `orchestrator.New()` alongside `stageBaseRootfs`. No persistent state needed — if it's on the bridge and there are no VMs, it's stale.

## Files to modify

### Agent (Go)
- `backend/internal/agentapi/server.go` — add `POST /trigger-update` route + handler
- `backend/internal/selfupdate/updater.go` — expose `CheckAndUpdate()` for direct invocation (already public)
- `backend/internal/orchestrator/firecracker_linux.go` — add `cleanupStaleTaps()` in `New()` startup

### Backend (Go)
- `backend/internal/api/server.go` — add `POST /admin/hosts/{hostId}/update-agent` + `GET /admin/latest-versions`
- `backend/internal/api/gcs_manifest.go` (new) — GCS manifest reader with caching

### Frontend (TypeScript)
- `frontend/src/lib/api.ts` — add `getLatestVersions()` and `updateHostAgent()` API calls
- `frontend/src/pages/admin/AdminHosts.tsx` — version badge, update button, confirmation dialog
