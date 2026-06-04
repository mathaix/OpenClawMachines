# Design: Browser Config Injection for `ocm machines browse`

**Status:** Approved approach — ready for implementation
**Date:** 2026-04-03
**Reviewed by:** Codex (GPT-5.3), Claude Opus 4.6

## Problem

When a user runs `ocm machines browse`, the CLI:
1. Launches local Chrome with remote debugging enabled
2. Creates a reverse SSH tunnel so port 9222 inside the VM reaches local Chrome
3. Registers a CDP target with the backend (host-side proxy routing)

**What's missing:** The gateway *inside* the VM doesn't know a browser is available. The `browser` section of `openclaw.json` is absent for machines that don't have the `browser` capability enabled in the DB. Without it, the gateway won't drive browser automation — the tunnel and CDP proxy exist, but nothing uses them.

### How it works today (companion browser VM path)

For machines with `browser` capability enabled in the DB, `configassembly.AssembleConfig` injects the `browser` config at boot:
```json
{"browser": {"enabled": true, "cdpUrl": "http://<bridge_ip>:9222", "attachOnly": true, "headless": true}}
```
This targets a dedicated headless Chrome Firecracker VM. The browse case is different: `cdpUrl` must be `http://127.0.0.1:9222` (the SSH tunnel endpoint), and `headless` must be `false`.

## Chosen Approach: Browse Session API with Direct ConfigBatch

A new `browse-session` endpoint that orchestrates both CDP target registration and gateway browser config in a single operation, using the existing `ConfigBatch` transport to push `openclaw config set/unset` ops directly — without modifying DB capability state or going through the full config assembly pipeline.

### Why not simpler alternatives?

| Approach | Why not |
|----------|---------|
| **SSH + jq file mutation** (current uncommitted code) | Fragile: race conditions, extra SSH round-trips, no ungraceful-exit cleanup, hardcoded paths |
| **Toggle DB `browser` capability + existing config push** | Assembly produces wrong config (bridge IP, headless=true). Cleanup hard-deletes the capability row, which would wipe permanent browser config if it existed. |
| **Overload `cdp-target` endpoint** | Mixes concerns (routing vs config), RBAC problem (`cdp-target` is outside owner/admin block), surprises other consumers |
| **Gateway auto-discovery polling** | Security risk (any process on :9222 triggers browser mode), polling overhead in every VM |

### API Design

#### Start browse session

```
POST /api/accounts/:accountId/machines/:id/browse-session
Body: { "cdp_target": "127.0.0.1:9222" }
Response: { "session_id": "bs_xxx", "expires_at": "..." }
```

Backend:
1. Validates machine is running, owned by account
2. Checks no active browse session exists for this machine (single lease)
3. Calls `agentClient.SetCDPTarget()` — remaps host-side CDP proxy (existing)
4. Calls `agentClient.ConfigBatch()` with:
   ```
   [{op: "set", path: "browser", value: '{"enabled":true,"cdpUrl":"http://127.0.0.1:9222","attachOnly":true,"noSandbox":true,"headless":false}', strictJSON: true}]
   ```
5. Stores browse session record: `{session_id, machine_id, created_at, expires_at, last_heartbeat}`
6. Returns session ID

#### Heartbeat

```
PUT /api/accounts/:accountId/machines/:id/browse-session/:sessionId/heartbeat
Response: { "expires_at": "..." }
```

CLI sends every ~60s. Backend extends `expires_at` by lease duration (e.g., 3 minutes).

#### Stop browse session

```
DELETE /api/accounts/:accountId/machines/:id/browse-session/:sessionId
Response: { "status": "ok" }
```

Backend:
1. Calls `agentClient.ConfigBatch()` with `[{op: "unset", path: "browser"}]`
2. Calls `agentClient.ResetCDPTarget()`
3. Deletes session record

Idempotent — safe to call if session already expired.

#### Janitor (ungraceful exit cleanup)

A background goroutine in the backend (or a periodic DB query) checks for expired sessions every ~60s:
```sql
SELECT * FROM browse_sessions WHERE expires_at < NOW()
```

For each expired session:
1. Same cleanup as DELETE: ConfigBatch unset + ResetCDPTarget
2. Delete session record
3. Log the cleanup

### CLI Changes

Replace in `machines_browse.go`:
- Remove: `runSSHCommand` helper, SSH+jq inject/cleanup, fresh-cert-for-cleanup logic
- Add: `POST /browse-session` after tunnel is established
- Add: heartbeat goroutine in the wait loop
- Add: `DELETE /browse-session/:id` in cleanup path

```go
// After CDP readiness confirmed and SSH tunnel established:
session, err := client.Post(browseSessionPath, browseSessionBody)
// ... store sessionID

heartbeat := time.NewTicker(60 * time.Second)
defer heartbeat.Stop()

for {
    select {
    case sig := <-sigCh:
        // cleanup
    case err := <-sshDone:
        // cleanup
    case <-heartbeat.C:
        client.Put(heartbeatPath, nil)
    }
}

// Cleanup: just one API call
client.Delete(browseSessionPath + "/" + sessionID)
```

### DB Schema

```sql
CREATE TABLE browse_sessions (
    id          TEXT PRIMARY KEY DEFAULT 'bs_' || gen_random_uuid(),
    machine_id  TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    account_id  INTEGER NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id)  -- single active session per machine
);
```

### Backend Changes

| File | Change |
|------|--------|
| `backend/internal/api/server.go` | Register browse-session routes |
| `backend/internal/api/browse_session.go` | New file: handlers for start/heartbeat/stop |
| `backend/internal/store/postgres.go` | CRUD for browse_sessions table |
| `backend/internal/store/store.go` | Interface additions |
| `backend/internal/api/server.go` | Start janitor goroutine |

### What stays the same

- Chrome launch, CDP readiness polling, SSH tunnel setup — unchanged in CLI
- `SetCDPTarget` / `ResetCDPTarget` agent API — unchanged, called by browse-session handler
- `ConfigBatch` / `openclaw config set/unset` — unchanged, used as transport
- Gateway hot-reload behavior — unchanged, triggers on `openclaw.json` write

### Edge Cases

| Case | Behavior |
|------|----------|
| CLI killed with `kill -9` | Heartbeats stop → session expires → janitor cleans up within ~3 min |
| Laptop sleeps | Same as kill -9 |
| Machine stopped while browsing | `ON DELETE CASCADE` cleans session record; VM teardown handles the rest |
| User already has browser capability (companion VM) | Browse session is separate — it overrides `browser` config while active, janitor restores by unsetting. Companion VM config returns on next config push or restart. |
| Two CLI instances try to browse same machine | Second POST gets 409 Conflict (UNIQUE constraint on machine_id) |
| Network blip causes missed heartbeat | Lease is 3 min, heartbeat is 60s — tolerates 2 missed beats |

## Removed: Previous Approaches

The following were evaluated and rejected (see git history for full analysis):
- Option A: SSH + jq file injection (current uncommitted code)
- Option B: Backend API via DB capability toggle + full config assembly
- Option C: Extend cdp-target endpoint
- Option D: Gateway auto-discovery polling
- Option E: Agent-side config injection
