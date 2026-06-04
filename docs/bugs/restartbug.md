# Bug: Machine Restart on Unreachable Host Causes Silent Data Loss

**Severity:** Critical
**Status:** Open
**Date:** 2026-03-11
**Affected machine:** `3ly03xw` (UUID `69e0d67f-e03a-4bb9-97d5-460b0a09a7b3`)

## Summary

When a machine's host becomes unreachable and the user restarts the machine, the placement system silently places it on a different host with a **brand new empty data volume**, destroying all user workspace data, configs, and credentials without warning.

## Timeline (machine `3ly03xw`)

1. Machine was running on **host 92** (GCP `n2-standard-4`, IP `34.61.152.194`)
2. Host 92 went **unreachable** on ~2026-03-07 (last heartbeat `2026-03-07T07:31:18Z`)
3. User restarted the machine on 2026-03-11
4. Placement system detected host 92 status = `unreachable`
5. Took the "place fresh but preserve affinity" code path (lines 305-311 of `runtime.go`)
6. Machine placed on **host 98** (OVH bare-metal, IP `15.204.241.166`)
7. Agent on host 98 had no existing data volume for this machine UUID
8. Agent **created a new 5GB data volume** — all previous data lost

Agent logs confirm the new volume creation:
```
vm.data_volume.create  machine_id=69e0d67f...  size_gb=5
vm.data_volume.created path=/var/lib/ocm/data/69e0d67f...ext4  size_gb=5
```

## Root Cause

`backend/internal/machines/runtime.go` lines 305-311:

```go
} else if prevHost.Status == "unreachable" || prevHost.Status == "error" || prevHost.Status == "draining" {
    // Host may recover — place fresh but preserve affinity for future restart
    slog.Info("machine.start.host_unavailable", ...)
    placement, host, vmIP, err = rs.placement.Reserve(ctx, machine.ID, placementReq)
    ...
}
```

This calls `Reserve()` (fresh placement on any available host) when the previous host is unreachable. The comment says "preserve affinity for future restart" but the machine is already being placed on a new host. The agent on the new host sees no existing data volume and creates a fresh one.

The code **does not**:
- Warn the user that data will be lost
- Block the restart and explain why
- Attempt to migrate the data volume
- Distinguish between "host temporarily down" and "host gone for days"

## Data Volume Architecture

- Each machine has a persistent ext4 data volume at `{DataDir}/{machineID}.ext4` on the host
- Contains `/workspace`, `/home/openclaw`, configs, SSH keys, installed packages
- Survives stop/start **only if the machine restarts on the same host**
- Data volumes are local to the host — there is no replication or migration

## Impact

- All user workspace files (code, repos, artifacts) — **lost**
- All user config (shell history, dotfiles, SSH keys) — **lost**
- All installed packages and tools — **lost**
- No warning given to the user before or after

## Host Status at Time of Bug

| Host | Provider | Status | Last Heartbeat |
|------|----------|--------|----------------|
| 89 | GCP | ready | 2026-03-11 |
| 92 | GCP | **unreachable** | 2026-03-07 (4 days stale) |
| 98 | OVH | ready | 2026-03-11 |

## Old Data Volume

The original data volume likely still exists on GCP host 92's disk at:
```
/var/lib/ocm/data/69e0d67f-e03a-4bb9-97d5-460b0a09a7b3.ext4
```
Recovery may be possible if the GCP VM is started.

## Proposed Fixes

### Immediate (prevent silent data loss)

1. **Block restart when host is unreachable**: Return an error like "Cannot restart: previous host (92) is unreachable. Your data volume is on that host. Options: (a) wait for host recovery, (b) force restart with `--discard-data` to start fresh on a new host."

2. **Add a `force` / `discard-data` flag**: Let users explicitly opt into data loss when they understand the consequences.

### Short-term

3. **Escalate unreachable → terminated**: If a host has been unreachable for >N hours (e.g., 24h), automatically mark it `terminated` so the affinity code breaks cleanly and the user isn't left in limbo.

4. **Surface data loss warning in the dashboard**: When a machine's host is unreachable, show a warning banner explaining the situation and options.

### Long-term

5. **Data volume migration**: Before placing on a new host, copy the data volume from the old host (if reachable) to the new host via SSH/rsync.

6. **Networked storage**: Move data volumes to a shared filesystem (e.g., NFS, GCS FUSE, or Ceph) so they're host-independent.

7. **Backup/snapshot**: Periodic snapshots of data volumes to object storage (GCS) for disaster recovery.

## Files Involved

| File | Role |
|------|------|
| `backend/internal/machines/runtime.go:284-326` | Host affinity and placement decision |
| `backend/internal/orchestrator/firecracker_linux.go:172-188` | Data volume creation (creates new if missing) |
| `backend/internal/orchestrator/firecracker_linux.go:461-501` | Stop cleanup (preserves data volume) |
| `backend/internal/orchestrator/firecracker_linux.go:404-459` | Destroy cleanup (deletes data volume) |
| `backend/internal/fleet/placement.go` | `Reserve()` vs `RecoverAffinity()` logic |
| `scripts/init-openclaw.sh:57-94` | Data volume mount and symlink setup inside VM |

## Reproduction

1. Create a machine on host A
2. Add files to `/workspace/`
3. Stop the machine
4. Mark host A as `unreachable` (or actually make it unreachable)
5. Start the machine — it gets placed on host B with empty data volume
6. All files from step 2 are gone
