# P0 Safety Fixes Design

Date: 2026-03-08
Branch: `thirdparty_provisioning`
Status: Approved

## Problem

A host can be killed from the cloud console and remain `ready` in Postgres indefinitely. The scheduler continues placing machines onto dead hosts. There is no background process to detect this, no bulk cleanup for affected machines, and no graceful shutdown path for VMs when a host is terminated.

All P0 safety fixes from `docs/provisioning_logic.md` are currently missing.

## Scope

This design covers the minimum safety fixes needed before the provider abstraction work can begin. It does not include compare-and-set transitions, operation IDs, placement splits, or multi-factor scheduling — those are deferred to later phases.

## Changes

### 1. Stale-Heartbeat Placement Filter

Add `last_heartbeat > now() - interval '180 seconds'` to the WHERE clause in:

- `PlaceMachineOnHost()` in `backend/internal/store/postgres.go`
- `FindHostWithCapacity()` in `backend/internal/store/postgres.go`

Hosts with NULL or stale heartbeats become invisible to the scheduler immediately.

No migration needed — `last_heartbeat` column already exists.

### 2. Host Status Model

Add `unreachable` and `terminated` to the host status set.

Full set: `provisioning`, `ready`, `draining`, `stopped`, `error`, `unreachable`, `terminated`.

Transitions:

- `ready` → `unreachable`: reconciler detects heartbeat stale >180s
- `unreachable` → `ready`: heartbeat resumes (auto-recovery in heartbeat handler)
- `unreachable` → `terminated`: GCP API confirms instance is gone (404)
- `unreachable` → `error`: GCP API call fails (can't confirm either way, needs operator)

Migration: update status comment in schema (no CHECK constraint exists today — status is a plain TEXT column).

### 3. Host Reconciler

New package: `backend/internal/reconciler/host.go`

Background goroutine started from `cmd/server/main.go`, running every 60 seconds.

Each tick:

1. Find hosts WHERE `status = 'ready'` AND (`last_heartbeat IS NULL` OR `last_heartbeat < now() - 180s`)
2. Mark each as `unreachable`, emit `HostEvent`
3. For already-unreachable hosts, call GCP `compute.Instances.Get()`:
   - If 404: transition to `terminated`
   - If API error: transition to `error` with message
4. For newly terminated hosts, run machine cleanup (Section 4)

Dependencies:

- `store.Store` (DB queries)
- GCP Compute client (instance existence check)
- `kvstore.KVStore` (route cleanup)
- `tunnel.Manager` (tunnel cleanup)

The reconciler:

- Listens on server context cancellation for graceful shutdown
- Skips hosts already in `error` status (those are handled by admin/provisioner paths)
- Logs every transition with structured slog: `host.reconciler.unreachable`, `host.reconciler.terminated`, `host.reconciler.recovered`

### 4. Machine Cleanup on Host Death

When the reconciler transitions a host to `terminated`:

1. New store method: `MarkMachinesOnHostError(ctx, hostID, message)` — bulk UPDATE for machines with status in (`running`, `provisioning`, `starting`) on that host. Returns affected machine IDs.
2. Per affected machine:
   - Delete KV route via `DeleteRouteSync`
   - Delete per-machine Cloudflare tunnel via `DeleteTunnelAndDNS`
   - Emit a `MachineEvent` with type `machine.host_lost`
3. Do NOT clear `host_id` or `vm_ip` — preserve for debugging and recovery context.
4. Stopped machines on a dead host are left as-is (no active runtime to clean up).

### 5. Dead-Host Restart Affinity

In `startMachineInternal` (`backend/internal/api/server.go`), before attempting `ReAllocateHostCapacity`:

1. Check the machine's current host status
2. If host status is `unreachable`, `terminated`, or `error`:
   - Skip re-allocation
   - Clear `host_id` and `vm_ip` on the machine (affinity is broken)
   - Fall through to fresh placement via `PlaceMachine`

User just hits "start" and it works on a different host.

### 6. Heartbeat Auto-Recovery

In `handleAgentHeartbeat` (`backend/internal/api/server.go`):

After `UpdateHostHeartbeat`, if the host's current status is `unreachable`:

1. Update host status to `ready`
2. Emit a `HostEvent` with type `host.reconciler.recovered`
3. Log at Info level

This handles the case where a host was temporarily partitioned but is now reachable again.

### 7. Graceful VM Shutdown on Agent SIGTERM

In `backend/cmd/agent/main.go`, replace the current shutdown sequence with:

1. Agent receives SIGTERM
2. Send "shutting down" notification to control plane (new endpoint: `POST /api/agent/shutdown-notify`)
3. For each VM: attempt `Stop()` with a per-VM timeout of 10 seconds using `context.WithTimeout(context.Background(), 10*time.Second)` — NOT the cancelled main context
4. If `Stop()` times out, fall back to `Destroy()`
5. Clean up resources (cloudflared, servers, etc.)
6. Exit

Control plane on receiving shutdown notification:

- Mark the host as `draining`
- Log the event

This gives machines a chance to flush in-guest state and lets the control plane react immediately rather than waiting 180s for heartbeat staleness.

### 8. Provisioning Compensating Cleanup

In `ProvisionHost()` (`backend/internal/provisioner/provisioner.go`):

Make `failHost` phase-aware. After GCE instance creation, failures include the phase in the status message:

- `"provisioning failed at phase: health_check (instance still running: {vmName} in {zone})"`

Emit a `HostEvent` with type `provisioning_failed_instance_orphaned`.

Do NOT auto-destroy the instance — preserve for forensic investigation. The event gives operators a clear signal to clean up.

## Known Limitations

### Poller race (deferred to Phase 2)

The async `pollVMStatus` goroutine in `server.go` can overwrite the reconciler's `error` status. This is a known limitation until operation IDs are added. The reconciler's error message `host lost` is distinguishable from poller errors, so operators can identify the root cause.

### Tunnel reaper overlap (benign)

The existing `tunnel.StartReaper()` runs every 10 minutes and may attempt to clean up tunnels that the reconciler already deleted. Both paths are idempotent — this is not a problem.

## Files Changed

| File | Change |
|------|--------|
| `backend/internal/store/postgres.go` | Heartbeat filter in placement queries, new `MarkMachinesOnHostError`, `ListStaleHosts`, `ListUnreachableHosts` |
| `backend/internal/store/store.go` | New interface methods |
| `backend/internal/reconciler/host.go` | **New file** — host reconciler |
| `backend/internal/api/server.go` | Heartbeat auto-recovery, dead-host restart skip, shutdown-notify endpoint |
| `backend/cmd/server/main.go` | Wire reconciler startup |
| `backend/cmd/agent/main.go` | Graceful VM shutdown, shutdown notification |
| `backend/internal/orchestrator/firecracker_linux.go` | `GracefulShutdown()` method using Stop-then-Destroy |
| `backend/internal/provisioner/provisioner.go` | Phase-aware failHost, orphan instance events |
| `backend/migrations/0XX_host_status_unreachable.sql` | Add status values documentation |

## Test Plan

### Unit tests (new)

- Stale host excluded from `PlaceMachineOnHost`
- Stale host excluded from `FindHostWithCapacity`
- `MarkMachinesOnHostError` bulk updates correctly, returns affected IDs
- `ListStaleHosts` returns correct hosts based on threshold
- Reconciler transitions: `ready` → `unreachable` → `terminated`
- Reconciler skips hosts already in `error`
- Heartbeat recovery: `unreachable` → `ready`
- Restart skips dead host affinity and falls through to fresh placement
- Graceful shutdown: Stop called before Destroy, timeout triggers fallback
- Shutdown notification marks host `draining`
- Heartbeat handler tests (normal path, recovery path)

### Existing tests (must still pass)

- `make test-go` — all backend unit tests
- `make test-gateway-e2e` — gateway E2E tests
- Scheduler tests in `backend/internal/scheduler/scheduler_test.go`
