# Clean Update Flow Design

## Date: 2026-03-23

## Problem

The current agent/rootfs update flow introduces a spurious "restarting" machine status that doesn't reflect a real lifecycle state. It also skips proper backend-side cleanup (capacity release, tunnel/route teardown) because it marks machines as "restarting" and lets the agent kill VMs at the OS level, leaving the backend out of sync.

## Machine Lifecycle (Correct)

```
                              ┌──────────────────────────────────────────────────┐
                              │                   MACHINE LIFECYCLE              │
                              └──────────────────────────────────────────────────┘

    ┌──────────┐
    │  CREATE  │
    └────┬─────┘
         │
         ▼
    ┌──────────┐         ┌────────────────┐         ┌──────────┐
    │ stopped  │────────►│ provisioning   │────────►│ running  │
    └──────────┘  first  │ (seed config,  │ poller  └────┬─────┘
         ▲        start  │  souls, setup) │  OK          │
         │               └───────┬────────┘              │
         │                       │                       │
         │                       │ timeout/              ├──── [USER STOP]
         │                       │ error                 │          │
         │                       ▼                       │          │  StopVM (SIGTERM)
         │               ┌──────────┐                    │          │  release capacity
         │               │  error   │                    │          │  delete tunnel/DNS/KV
         │               └──────────┘                    │          │
         │                                               │          ▼
         │                                               │     ┌──────────┐
         │         ┌─────────────────────────────────────┘     │ stopped  │
         │         │                                           └────┬─────┘
         │         │ [AGENT UPDATE]                                 │
         │         │                                                │
         │         │  Backend (parallel):                           │ [USER START]
         │         │   1. StopVM (SIGTERM)                          │
         │         │   2. Release capacity                          │
         │         │   3. Keep tunnels/DNS/KV                       │
         │         │                                                │
         │         ▼                                                │
         │    ┌──────────┐    heartbeat    ┌──────────┐             │
         │    │ stopped  │───resumes──────►│ starting │◄────────────┘
         │    └──────────┘  auto-restart   │ (no seed │   restart on
         │                                 │  config) │   same host
         │                                 └────┬─────┘
         │                                      │
         │                                      │ poller OK
         │                                      ▼
         │                                 ┌──────────┐
         │                                 │ running  │
         │                                 └──────────┘
         │
         │    ┌──────────────────────────────────────────────┐
         │    │           RESTART — NO CAPACITY              │
         │    │                                              │
         │    │  Home host full or unavailable:              │
         │    │  → ERROR: "migration required"               │
         │    │  → Admin triggers migration workflow:        │
         │    │    1. Backup data from old host              │
         │    │    2. Place on new host (provisioning)       │
         │    │    3. Restore backup                         │
         │    │    4. Start (→ starting → running)           │
         │    └──────────────────────────────────────────────┘


    ┌──────────────────────────────────────────────────────────┐
    │                     DELETE (any state)                    │
    │                                                          │
    │  DestroyVM (hard kill) → delete tunnel/DNS/KV            │
    │  → delete data volume → remove DB record                 │
    └──────────────────────────────────────────────────────────┘


    ┌──────────────────────────────────────────────────────────┐
    │                   STUCK MACHINE RECOVERY                 │
    │                                                          │
    │  provisioning/starting/stopping stuck > 5 min            │
    │  → reconciler sets status to "error"                     │
    │  → releases capacity, cleans up routes                   │
    └──────────────────────────────────────────────────────────┘


    ┌──────────────────────────────────────────────────────────┐
    │                    MIGRATION WORKFLOW                     │
    │                                                          │
    │  running ──[stop]──► stopped                             │
    │                         │                                │
    │                    [start on new host]                    │
    │                         │                                │
    │                         ▼                                │
    │                    provisioning ──► running               │
    │                         │                                │
    │                    [stop for restore]                     │
    │                         │                                │
    │                         ▼                                │
    │                      stopped                             │
    │                         │                                │
    │                    [restore backup]                       │
    │                         │                                │
    │                    [start]                                │
    │                         │                                │
    │                         ▼                                │
    │                      starting ──► running                │
    └──────────────────────────────────────────────────────────┘
```

### Status reference

| Status | Meaning | Seed config? | Tunnels exist? |
|--------|---------|:------------:|:--------------:|
| `stopped` | VM not running, data volume on host | No | Depends on stop type |
| `provisioning` | First boot, setting up from scratch | **Yes** | Creating |
| `starting` | Restart, data volume already has config | No | Already exist (update) or creating (migration) |
| `running` | VM running, gateway healthy | No | Yes |
| `error` | Timeout, crash, or stuck | No | May be stale |

### Valid statuses

| Status | Meaning |
|--------|---------|
| `stopped` | Machine exists, VM not running |
| `provisioning` | First boot — VM created, waiting for gateway to come up |
| `starting` | Restart — VM created on existing host, data volume preserved |
| `running` | VM running, gateway healthy |
| `error` | Something went wrong (timeout, crash, stuck) |

**`restarting` is removed.** It was never a distinct lifecycle phase — it was just "stopped, will be started again soon." The backend tracks restart intent, not the machine status.

### Invariant: seed config only during provisioning

Seed config assembly (`AssembleSeedConfig`) and soul delivery only happen on first boot — when `machine.HostID == nil` (no prior host assignment). On restart, the data volume already has `openclaw.json` and `SOUL.md` from the first boot. Config updates on restart are handled by `pushConfigToRunningMachine` after the VM is running, using the diff-based `buildConfigOps()` to push only changed keys.

## Agent Update Flow (Redesigned)

### Current flow (broken)

1. Mark machines `"restarting"` (no backend-side cleanup)
2. Tell agent to update (agent kills VMs at OS level)
3. Heartbeat resumes → restart "restarting" machines
4. Backend state is stale: capacity still allocated, tunnels/routes still exist

### New flow

```
Backend                          Agent (on host)
───────                          ───────────────
1. GET running machines on host
2. For each machine (parallel):
   - POST /vms/{id}/stop        → Firecracker Shutdown()
     (SIGTERM → gateway)           (10s timeout, then StopVMM)
   - Release host capacity
   - Update DB status → "stopped"
   (Skip tunnel/KV/DNS cleanup)
3. Store machine IDs in host
   status_message as JSON
4. Mark host "updating"
5. POST /trigger-update          → Self-update + restart
                                   (no VMs to drain)
6. ...agent restarts...
7.                               ← Heartbeat resumes
8. Read machine IDs from
   host status_message
9. Mark host "ready"
10. Start each machine
    (parallel, background)
```

### Lightweight stop for updates

During an update, the machine is coming back on the same host with the same slug. Tunnels, DNS records, and KV routes don't need to change. The lightweight stop:

- **Does**: Call agent StopVM (SIGTERM to gateway), release host capacity, set status to "stopped"
- **Skips**: Tunnel deletion, DNS record deletion, KV route deletion

This avoids unnecessary Cloudflare API calls and reduces restart latency. The tunnel/DNS/KV entries remain valid because the machine returns to the same host.

Full tunnel cleanup happens only when:
- User explicitly stops a machine (may not restart)
- Machine is deleted
- Machine migrates to a different host

### Tracking restart intent

The backend stores the list of machine IDs that were running before the update in the host's `status_message` field as JSON when marking the host as `"updating"`:

```json
{"pending_restarts": ["machine-id-1", "machine-id-2"]}
```

When the heartbeat handler detects the host transitioning from `"updating"` → `"ready"`, it reads this list and starts each machine. This survives backend restarts (Cloud Run redeployments) because it's persisted in the DB.

After restart is complete, the `status_message` is cleared.

### Parallel stop

Machines are stopped in parallel using goroutines with a WaitGroup. Each stop is independent — one machine failing to stop doesn't block others. Errors are collected and logged. If a machine fails to stop, it's excluded from the restart list (left in its current state for manual recovery).

### Stuck machine detector update

`FindStuckMachines` currently checks `'provisioning'` and `'stopping'`. With the removal of `"restarting"`, add `'starting'` to the query so machines stuck in the starting phase are also auto-recovered.

## Capacity Failure on Restart

When a stopped machine's home host has no capacity (or is unreachable/error/draining), the machine cannot simply be placed on a different host — its data volume lives on the original host. Starting without data would boot an unconfigured gateway with no workspace, no config, no souls.

### Current behavior (broken)

`RecoverAffinity` falls through to `Reserve()` on a different host. `isRestart` is still `true` (machine had `HostID`), so seed config is skipped. The machine boots empty on the new host.

### Correct behavior

If the home host cannot accept the machine, the start should trigger a **migration flow** — back up data from the old host, place on a new host, restore data, then start. This is the same migration workflow used for admin-initiated migrations (`admin_migrate_workflow.go`).

The `RecoverAffinity` fallback to fresh `Reserve()` should be replaced with an error that signals "needs migration." The `Start()` caller can then either:
- Return an error to the user: "Machine's home host has no capacity — migration required"
- Auto-trigger migration (future enhancement)

For now, returning an error is the safe choice. The admin can then initiate a migration manually.

## Changes Required

### Backend

| File | Change |
|------|--------|
| `api/server.go` | Rewrite `handleTriggerHostUpdate`: parallel stop, store restart list in `status_message`, trigger update after VMs are stopped |
| `api/server.go` | Update `restartMachinesAfterUpdate`: read machine IDs from `status_message` instead of querying for "restarting" status |
| `api/server.go` | Heartbeat handler: clear `status_message` after restart |
| `machines/runtime.go` | Add `StopForUpdate()` or `StopOptions{KeepRoutes: bool}` — lightweight stop that skips tunnel/KV/DNS cleanup |
| `machines/runtime.go` | Keep existing fix: set status to `"starting"` (not `"provisioning"`) on restart |
| `machines/runtime.go` | Remove `RecoverAffinity` fallback to `Reserve()` — return error if home host can't accept machine (needs migration) |
| `fleet/placement.go` | `RecoverAffinity` returns a distinct error when home host is viable but has no capacity, instead of silently falling through to fresh placement |
| `store/postgres.go` | Update `FindStuckMachines` to include `'starting'` status |

### Frontend

| File | Change |
|------|--------|
| `lib/types.ts` | Remove `"restarting"` from Machine status union type |
| `components/MachineCard.tsx` | Remove `restarting` from badge/dot maps |
| `pages/MachineView.tsx` | Remove `"restarting"` from `isBooting` check |
| `pages/MachineWorkspace.tsx` | Remove `"restarting"` from status checks |
| `pages/GatewayDashboard.tsx` | Remove `"restarting"` from status checks |
| `pages/admin/AdminMachines.tsx` | Remove `"restarting"` from filter/badge/option |

### Agent (no changes)

The agent's `handleTriggerUpdate` still works — it just won't have any VMs to drain since the backend already stopped them. The `Shutdown()` call becomes a no-op (no VMs in the list).
