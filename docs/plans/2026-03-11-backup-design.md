# Data Volume Backup & Restore Design

**Date:** 2026-03-11
**Updated:** 2026-03-11 (Phase 1 complete: manual ops + admin migration)
**Branch:** `backups`
**Motivation:** Machine restart on unreachable host causes silent data loss (see `docs/bugs/restartbug.md`)

## Problem

Data volumes are local ext4 files on each host (`/var/lib/ocm/data/{machineID}.ext4`). When a machine's host becomes unreachable and the user restarts, the placement system places it on a different host with a fresh empty data volume — all user data is silently destroyed.

## Approach: Manual-First

Rather than building the full automated backup system at once, we start with the **wiring and manual operations**. Users opt in to backups as a feature toggle, then create/restore/download backups explicitly. Automation (on-stop, daily, placement guard) is layered on in later phases once the plumbing is proven.

**Phase 1 (this PR):** Manual backup, restore, download — end-to-end wiring + admin migration
**Phase 2:** Auto backup on stop, auto-restore on placement, placement guard
**Phase 3:** Daily backups (fsfreeze), admin evacuate, cloning

## Solution Overview

1. **Backups as a user-enabled feature** — toggle on machine, off by default
2. **Manual backup** — user triggers backup via API/CLI/Dashboard (machine must be stopped)
3. **Manual restore** — user restores from a specific backup (machine must be stopped)
4. **Download** — user downloads backup as tar.gz via agent (not Cloud Run)
5. **Encrypted at rest** — AES-256-GCM with per-machine keys

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   Control Plane (Cloud Run)              │
│                                                          │
│  API server ── backup metadata DB (machine_backups)      │
│       │              │                                   │
│       │   ┌──────────┘                                   │
│       ▼   ▼                                              │
│  ┌──────────────────┐                                    │
│  │   GCS Bucket      │                                   │
│  │   /backups/       │                                   │
│  │   {machineID}/    │                                   │
│  │     {timestamp}.ext4.zst.enc                          │
│  └──────────────────┘                                    │
│       ▲         │                                        │
└───────┼─────────┼────────────────────────────────────────┘
        │         │
   upload    download
        │         │
   ┌────┴─────────┴────┐
   │  Host Agent        │
   │  (backup/restore)  │
   │                    │
   │  data/             │
   │  {id}.ext4         │
   └────────────────────┘
```

**Key decision: Agent handles all GCS I/O, not Cloud Run.** The agent has the data volume locally. The control plane orchestrates (tells the agent what to do) and stores metadata. Downloads also go through the agent — it decrypts, decompresses, mounts, and tars the contents. This avoids Cloud Run needing to mount ext4 images (which it can't do as a stateless container).

## Storage Backend

**GCS only.** Rationale:
- Cross-provider restore (GCP→OVH, OVH→GCP) requires a single accessible backend
- Already have GCS client code in the codebase (`cloud.google.com/go/storage`)
- Cost: ~$0.06/machine/month (negligible)

**GCS path:** `gs://openclawmachines/backups/{machineID}/{timestamp}.ext4.zst.enc`

## Encryption & Signing

Backups contain user data. Encrypted at rest, only decryptable for the owning machine.

### Key Hierarchy

```
Platform Master Key (in GCP Secret Manager / env var)
    │
    └─► Machine Backup Key (AES-256, per machine)
            │
            ├─► Encrypt backup data (AES-256-GCM)
            └─► Sign backup (HMAC-SHA256)
```

- **Platform Master Key:** 32-byte key from `BACKUP_MASTER_KEY` env var (sourced from GCP Secret Manager)
- **Machine Backup Key:** 32-byte random key, generated when backups are enabled. Stored in `machines.backup_key`, encrypted with the platform master key.

### Pipeline

```
Backup:  data volume → zstd -3 → AES-256-GCM encrypt → upload to GCS
Restore: download from GCS → verify HMAC → AES-256-GCM decrypt → zstd decompress → data volume
```

Compress first, then encrypt (encrypted data doesn't compress well). Existing `backend/pkg/crypto/` already has AES-256-GCM helpers — extend for streaming.

## Feature Toggle

Backups are **opt-in per machine**. New field on machines:

```sql
ALTER TABLE machines ADD COLUMN backups_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE machines ADD COLUMN backup_key BYTEA;  -- encrypted with platform master key, set when enabled
```

**Enable flow:**
1. User toggles "Backups" on in Dashboard or `ocm machines backups enable`
2. Control plane generates a 32-byte backup key, encrypts with master key, stores in DB
3. Machine now accepts backup/restore operations

**Disable flow:**
1. User toggles off — existing backups remain in GCS until retention expires
2. No new backups can be created
3. Re-enabling reuses the same key (backups stay decryptable)

## Phase 1: Manual Operations (This PR)

### What We Build

| Operation | API | CLI | UI | Who Runs It |
|-----------|-----|-----|-----|-------------|
| Enable/disable backups | `PUT /machines/{id}` | `ocm machines backups enable/disable` | Toggle on machine detail | User |
| Create backup | `POST /machines/{id}/backups` | `ocm machines backups create` | Button on Backups tab | User (machine stopped) |
| List backups | `GET /machines/{id}/backups` | `ocm machines backups list` | Backups tab | User |
| Download backup | `GET /machines/{id}/backups/{id}/download` | `ocm machines backups download` | Download button | User |
| Restore from backup | `POST /machines/{id}/backups/{id}/restore` | `ocm machines backups restore` | Restore button | User (machine stopped) |
| Delete backup | `DELETE /machines/{id}/backups/{id}` | `ocm machines backups delete` | Delete button | User |

### API Flow: Create Backup

```
User → POST /accounts/{acct}/machines/{id}/backups
     → Control plane validates: machine stopped, backups enabled
     → Control plane calls agent: POST /vms/{machineID}/backup
       → Agent compresses data volume (zstd -3)
       → Agent encrypts (AES-256-GCM with machine backup key)
       → Agent uploads to GCS
       → Agent returns: {gcs_path, size_bytes, compressed_bytes, checksum, hmac, nonce}
     → Control plane stores metadata in machine_backups table
     → Returns: {id, timestamp, size, trigger: "manual"}
```

### API Flow: Restore

```
User → POST /accounts/{acct}/machines/{id}/backups/{backupId}/restore
     → Control plane validates: machine stopped, backup exists
     → Control plane looks up backup metadata (gcs_path, nonce, hmac)
     → Control plane calls agent: POST /vms/{machineID}/restore {gcs_path, backup_key, nonce, expected_hmac}
       → Agent downloads from GCS
       → Agent verifies HMAC
       → Agent decrypts
       → Agent decompresses to data volume path (replaces existing)
       → Agent returns success
     → Control plane returns: {status: "restored"}
```

### API Flow: Download

```
User → GET /accounts/{acct}/machines/{id}/backups/{backupId}/download?format=tar.gz
     → Control plane validates ownership
     → Control plane proxies to agent: GET /vms/{machineID}/backup-download/{backupId}
       → Agent downloads from GCS → decrypt → decompress → mount ext4 read-only → tar → stream
       → OR (format=ext4): Agent downloads → decrypt → decompress → stream raw
     → User receives: {machine-name}-{timestamp}.tar.gz
```

**Why agent, not Cloud Run?** Cloud Run is stateless — can't mount ext4 images. The agent has the host filesystem and can do `mount -o loop,ro`. For download of a backup that's not on the current host, the control plane picks any available agent to do the work.

### Agent API: New Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/vms/{machineID}/backup` | Create backup: compress → encrypt → upload to GCS |
| `POST` | `/vms/{machineID}/restore` | Restore: download from GCS → decrypt → decompress → write volume |
| `GET` | `/vms/{machineID}/backup-download/{backupId}` | Stream decrypted backup as tar.gz or ext4 |

These are called by the control plane, not directly by users. The control plane passes the backup key and GCS path.

## Database Schema

```sql
-- Migration 032: machine backups

ALTER TABLE machines ADD COLUMN backups_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE machines ADD COLUMN backup_key BYTEA;

CREATE TABLE machine_backups (
    id               SERIAL PRIMARY KEY,
    machine_id       UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    timestamp        TIMESTAMPTZ NOT NULL,
    gcs_path         TEXT NOT NULL,
    size_bytes       BIGINT NOT NULL,
    compressed_bytes BIGINT NOT NULL,
    checksum_sha256  TEXT NOT NULL,
    hmac_sha256      TEXT NOT NULL,
    nonce            BYTEA NOT NULL,
    trigger          TEXT NOT NULL,      -- 'manual' for Phase 1
    host_id          INT REFERENCES hosts(id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_machine_backups_machine_id ON machine_backups(machine_id);
CREATE INDEX idx_machine_backups_latest ON machine_backups(machine_id, timestamp DESC);
```

## Configuration

| Env Var | Default | Where | Purpose |
|---------|---------|-------|---------|
| `BACKUP_GCS_BUCKET` | `openclawmachines` | Agent | GCS bucket |
| `BACKUP_GCS_PREFIX` | `backups` | Agent | Path prefix in bucket |
| `BACKUP_MAX_VOLUME_GB` | `10` | Agent | Skip if volume exceeds this |
| `GCS_SERVICE_ACCOUNT_KEY` | _(none)_ | Agent | GCS auth for non-GCP hosts |
| `BACKUP_MASTER_KEY` | _(required)_ | Control plane + Agent | Envelope encryption master key |

## CLI Commands

```bash
# Enable backups for a machine
ocm machines backups enable [MACHINE_NAME]

# Disable backups
ocm machines backups disable [MACHINE_NAME]

# Create backup (machine must be stopped)
ocm machines backups create [MACHINE_NAME]

# List backups
ocm machines backups list [MACHINE_NAME]

# Download latest backup
ocm machines backups download [MACHINE_NAME]

# Download specific backup
ocm machines backups download [MACHINE_NAME] --id 46

# Download as raw ext4
ocm machines backups download [MACHINE_NAME] --format ext4

# Restore from specific backup (machine must be stopped)
ocm machines backups restore [MACHINE_NAME] --id 46
```

## Dashboard UI

### Machine Detail — Backups Tab

```
┌───────────────────────────────────────────────────────┐
│  Overview │ Credentials │ Config │ Logs │ [Backups]   │
│  ─────────────────────────────────────────────────── │
│                                                       │
│  Backups: [Enabled ✓]              [+ Create Backup]  │
│                                                       │
│  Backup History                                       │
│  ┌────────────────────────────────────────────────┐   │
│  │  #3  Mar 11 08:15  512 MB  manual  [↓] [↺] [✕]│   │
│  │  #2  Mar 10 22:30  508 MB  manual  [↓] [↺] [✕]│   │
│  │  #1  Mar 10 14:00  505 MB  manual  [↓] [↺] [✕]│   │
│  └────────────────────────────────────────────────┘   │
│                                                       │
│  [↓] Download  [↺] Restore  [✕] Delete               │
└───────────────────────────────────────────────────────┘
```

### Machine Card — Backup Indicator

Small line on MachineCard: "Backups: enabled (last: 2h ago)" or "Backups: disabled"

## Retention

- **3 backups retained** per machine (Phase 1: manual deletion only)
- Agent enforces: after successful upload, delete oldest if > 3
- Phase 2: GCS lifecycle rules as safety net

## Implementation Order

1. **`backend/internal/backup/crypto.go`** — key generation, AES-256-GCM encrypt/decrypt streams, HMAC
2. **`backend/internal/backup/store.go`** — BackupStore interface (Upload, Download, Delete, List)
3. **`backend/internal/backup/gcs.go`** — GCS implementation
4. **`backend/migrations/032_machine_backups.sql`** — schema
5. **`backend/internal/store/`** — BackupRecord type, CRUD queries, Machine.BackupKey + BackupsEnabled fields
6. **`backend/internal/config/config.go`** — backup env vars in AgentConfig
7. **Agent endpoints** — `handleBackupVM`, `handleRestoreVM`, `handleBackupDownload` in agentapi
8. **Control plane endpoints** — backup CRUD in `machine_backups.go`, enable/disable in `handleUpdateMachine`
9. **CLI** — `cli/internal/commands/machines_backups.go`
10. **Dashboard** — `BackupsTab.tsx`, enable toggle, MachineCard indicator

## Files to Create/Modify

### New Files

| File | Purpose |
|------|---------|
| `backend/internal/backup/crypto.go` | AES-256-GCM streaming encrypt/decrypt, HMAC, key gen |
| `backend/internal/backup/crypto_test.go` | Round-trip tests |
| `backend/internal/backup/store.go` | BackupStore interface |
| `backend/internal/backup/gcs.go` | GCS implementation |
| `backend/internal/backup/gcs_test.go` | Tests |
| `backend/internal/api/machine_backups.go` | Control plane backup CRUD endpoints |
| `backend/migrations/032_machine_backups.sql` | Schema |
| `cli/internal/commands/machines_backups.go` | CLI subcommands |
| `frontend/src/components/BackupsTab.tsx` | Dashboard backup tab |

### Modified Files

| File | Change |
|------|--------|
| `backend/internal/agentapi/handlers.go` | Add backup, restore, download handlers |
| `backend/internal/agentapi/server.go` | Add BackupStore dependency, register routes |
| `backend/internal/api/server.go` | Register backup routes, add backup toggle to updateMachine |
| `backend/internal/store/store.go` | BackupRecord type, Machine.BackupsEnabled + BackupKey fields |
| `backend/internal/store/postgres.go` | machine_backups CRUD queries |
| `backend/internal/config/config.go` | Backup config in AgentConfig |
| `backend/cmd/agent/main.go` | Init BackupStore |
| `backend/internal/agentclient/client.go` | BackupVM, RestoreVM, BackupDownload methods |
| `cli/internal/commands/machines.go` | Register backupsCmd |
| `cli/internal/api/types.go` | Backup type |
| `frontend/src/pages/MachineView.tsx` | Add Backups tab |
| `frontend/src/components/MachineCard.tsx` | Backup status indicator |
| `frontend/src/lib/api.ts` | Backup API functions |

## What Phase 1 Does NOT Include

These are deferred to later phases. The design exists in this doc's appendix but we don't build them yet:

- Auto backup on stop
- Auto restore on placement (ensureDataVolume hook)
- Placement guard in runtime.go
- Daily backups (fsfreeze)
- Admin evacuate API
- Machine cloning
- Agent shutdown backups

## Admin Migration (Implemented — originally Phase 3)

Admin migration was pulled forward into Phase 1 because it was needed for host maintenance.

**Endpoint:** `POST /api/admin/machines/migrate`

**Request:**
```json
{
  "machine_id": "uuid",
  "target_host_id": 5,
  "force": false
}
```

**Flow:**
1. Acquire `"migrate"` operation lock (prevents concurrent lifecycle actions)
2. Stop VM on source host via direct `agentClient.StopVM()` (not `RuntimeService.Stop()` — avoids `ReleaseSoft` side effects)
3. Create backup on source host (encrypted, persisted to DB with `trigger=migration`); skipped with `force=true`
4. Destroy VM on source host (best-effort — agent's `Stop()` removes VM from orchestrator map, so `Destroy()` may not find it)
5. Release source host: decrement capacity counters (only for `running`/`provisioning`/`error` status), clear host assignment
6. Start on target host using `StartOptions.TargetHostID` → `PlaceMachineOnSpecificHost` (atomic placement)
7. If backup was created: wait for running → stop → restore → start again (fully automatic)
8. Return `migrated_and_restored` with `backup_id` (or `migrated` if no backup)

**⚠️ Known bug:** The final `Start()` in step 7 spawns a background poller that can race with the running VM and orphan it. See "Migration Auto-Restore Poller Race" section above.

**Key implementation details:**
- **Targeted placement:** `PlaceMachineOnSpecificHost` locks specific host with `FOR UPDATE`, checks capacity, allocates next VM IP (192.168.100.10-250), assigns machine — all in one transaction
- **`StartOptions` variadic pattern:** `Start(ctx, accountID, machine, opts ...StartOptions)` preserves backward compatibility
- **Status-dependent release:** Running/provisioning/error machines have allocated capacity counters; stopped machines had counters already freed by previous `Stop()`
- **Orphan cleanup:** After StopVM, the data volume remains on source host disk. The agent's orphan cleanup process handles it.

**Files:**
- `backend/internal/api/admin_migrate.go` — endpoint + `createMigrationBackup` helper
- `backend/internal/store/postgres.go` — `PlaceMachineOnSpecificHost`
- `backend/internal/fleet/placement.go` — `TargetHostID` in `PlacementRequest`
- `backend/internal/machines/runtime.go` — `StartOptions` struct, targeted placement branch

## Known Bug: Migration Auto-Restore Poller Race (2026-03-13)

**Status:** Fixed — `SkipPoll` option added to `StartOptions` (commit on `backups` branch)
**Affected machine:** PatrickAgent (`eab70766`)
**Severity:** CRITICAL — migration appears to succeed but VM becomes orphaned

### Root Cause

The migration auto-restore flow calls `Start()` at its final step (line 245, `admin_migrate.go`) to boot the machine with restored data. `Start()` returns immediately after calling `CreateVM` on the agent and spawns a **background `pollVMStatus` goroutine** to track the VM until it reaches `running`. Meanwhile, the migration HTTP handler returns `200 migrated_and_restored` to the caller.

The problem: **the poller goroutine outlives the HTTP handler** and continues polling the agent for VM status. On host 92, the VM booted and reached `running` in ~5 seconds (`vm.gateway.ready` at 01:28:32). But the poller's first poll at 01:28:13 got a 404 — likely a timing gap where the agent's `CreateVM` background goroutine hadn't registered the VM in its in-memory map yet. After 5 consecutive 404s, the poller called `ReleaseHard`, which:

1. Decremented host 92's capacity counters
2. Cleared `host_id` and `vm_ip` from the machine record
3. Set machine status to `error` → then `stopped`

This left the Firecracker process **actually running on host 92** (PID 9282) but completely invisible to the control plane — an orphaned VM.

### Cascade Failure

Subsequent `Start()` attempts failed because the orphaned Firecracker still held the TAP device:

```
vm.create_failed: create tap tapeab70766-10: ioctl(TUNSETIFF): Device or resource busy
```

The agent returned 201 from `CreateVM` (accepted into background), but the background goroutine failed immediately on TAP creation. The VM was never added to the agent's map, so `GetVM` returned 404, the poller gave up after 5 attempts, and `ReleaseHard` ran again. This repeated for each retry.

### Timeline

```
01:27:08  migrate operation starts
01:27:12  backup created (trigger=migration), start on target host 92
01:27:23  VM reaches running on host 92 (first boot for migration)
01:27:26  auto-restore: stop for restore
01:27:29  stopped for restore
01:28:05  restore succeeds (backup_id=2)
01:28:06  Start() called — creates VM + spawns poller goroutine
01:28:10  migration handler returns 200 (admin.migrate.complete)
01:28:27  agent: VM boot completes, gateway ready, machine_ready
01:28:13  poller: GetVM → 404 (attempt 1)  ← VM was booting, not yet in agent map
01:28:16  poller: GetVM → 404 (attempt 2)
01:28:22  poller: GetVM → 404 (attempt 5) → ReleaseHard → host_id cleared
01:28:27  VM is actually running but DB says stopped, no host
───────── subsequent retries all fail on stale TAP device ─────────
01:29:08  Start retry: CreateVM → TAP busy → 404 x5 → ReleaseHard
01:29:48  Start retry: CreateVM → TAP busy → 404 x5 → ReleaseHard
```

### Fix Options

1. **Don't poll after migration restart** — The migration handler already verified the VM reached `running` in step 6 (`waitForMachineStatus`). The final `Start()` after restore should skip the poller and let heartbeats handle status. This requires a `SkipPoll` option in `StartOptions`.

2. **Wait for poller in migration handler** — Instead of returning immediately after `Start()`, the migration handler waits for the VM to reach `running` via its own `waitForMachineStatus`. But the poller goroutine still runs in parallel and could race.

3. **Fix the agent's CreateVM to register synchronously** — The 404 happens because `CreateVM` returns 201 before the VM is in the agent's map. If the agent registered the VM in the map before returning 201, the poller would find it.

**Recommended fix:** Option 1 (`SkipPoll`) + Option 3 (agent registers synchronously). Option 1 avoids the race entirely for migrations. Option 3 prevents the same class of bug in any `Start()` path.

### Immediate Recovery

The orphaned Firecracker on host 92 needs manual cleanup:
```bash
# On host 92:
kill 9282 9305  # main + browser Firecracker processes
ip link del tapeab70766-10
ip link del btapeab70766-1
```

Then PatrickAgent can be started normally via the API.

## Codex Review Findings (addressed)

Issues found by OpenAI Codex review of the full design:

| Finding | Severity | Resolution |
|---------|----------|------------|
| Async backup races with fast restart | CRITICAL | **Deferred** — Phase 1 is manual only, no race possible |
| `terminated` hosts bypass guard | CRITICAL | **Deferred** — placement guard is Phase 2 |
| Restore architecture inconsistent (agent vs control plane) | IMPORTANT | **Resolved** — agent is single source of truth for GCS I/O, control plane stores metadata only |
| Shutdown path bypasses handler backup | IMPORTANT | **Deferred** — auto backup is Phase 2 |
| Cloud Run can't mount ext4 for download | IMPORTANT | **Resolved** — agent handles download, not Cloud Run |
| Migrate needs SetAffinity() | IMPORTANT | **Resolved** — admin migrate implemented with targeted placement (`PlaceMachineOnSpecificHost`) |
| Agent→CP callback is new subsystem | IMPORTANT | **Simplified** — Phase 1 is synchronous: CP calls agent, agent returns result, CP stores metadata |

---

## Appendix: Full Vision (Phases 2-3)

The scenarios and automation design from the original document are preserved here for reference. They will be implemented once Phase 1 proves the wiring works.

### Phase 2: Automation
- On-stop backup (async, with freshness tracking to prevent Codex's race condition)
- Auto-restore in `ensureDataVolume()` (agent checks control plane for backup metadata)
- Placement guard in `runtime.go` covering `unreachable/error/draining/terminated`
- Backup freshness: guard checks `backup.timestamp > machine.stopped_at` not just `HasBackup`

### Phase 3: Advanced
- Daily backups (requires agent→VM exec channel for fsfreeze)
- ~~Admin migrate API~~ — **Implemented in Phase 1** (see above)
- Admin evacuate API (`POST /admin/hosts/{id}/evacuate`)
- Machine cloning with re-encryption
- Agent shutdown backups with completion waiting

### Scenario Summary (unchanged)

| # | Scenario | Phase |
|---|----------|-------|
| 1 | Stop/restart, no capacity | 2 (auto backup + restore) |
| 2 | Admin migrate | ~~3~~ **1 (implemented)** |
| 3 | Software upgrade backup | 2 |
| 4 | Resize (bigger box) | 2 (auto restore) |
| 5 | Host crash | 2 (placement guard) |
| 6 | Host evacuation | 3 |
| 7 | Machine clone | 3 |
| 8 | Cross-provider migration | 3 |
| 9 | Manual backup | **1 (this PR)** |
| 10 | Disaster recovery | 2 (placement guard) |
