# Browser VM Zombie Recovery — Analysis & Outstanding Issues

**Date:** 2026-04-10
**Branch:** `browser`
**Status:** Analysis + implementation notes — documents the shipped fix set and remaining gaps

This document captures the full analysis of the "zombie browser VM" problem:
what was happening, what we fixed, and what Codex review surfaced as
remaining issues. It's a companion to
`2026-04-10-browser-vm-network-architecture.md` and focuses specifically
on the agent-restart lifecycle.

---

## The Original Symptom

When the agent self-updated (polling GCS every 5 minutes for a new
binary), running browser VMs disappeared from the host's `browserVMs`
map, but the control plane DB still thought they were running. The next
pair attempt then failed with:

```
POST /vms/<machine-id>/cdp-target → 404 "vm not found"
```

The VMs became "zombies" — dead on the host, alive in the database,
unreachable from either side.

## Root Cause: systemd Cgroup Cleanup

`ocm-agent.service` was installed with systemd's default
`KillMode=control-group`. From `man systemd.kill`:

> `control-group` (default) — all remaining processes in the control
> group of this unit will be killed on unit stop

When the self-updater triggered `systemctl restart ocm-agent`:

1. systemd sent SIGTERM to the agent's main PID
2. Agent ran its graceful shutdown: saved state, stopped tunnels,
   closed listeners, exited cleanly
3. **systemd then sent SIGKILL to every other process in the agent's
   cgroup**, including every running Firecracker VM
4. New agent started, called `Recover()`, read the state file, checked
   `pidAlive(p.PID)` for each persisted VM — all dead
5. Orphan cleanup removed the state entries, TAPs, and rootfs files

State persistence (`saveBrowserState` / `recoverBrowserVMs`) was
correct and necessary, but insufficient on its own. It protects against
agent *crashes* where the child process happens to survive (e.g., the
Go runtime segfaults but children keep running). It does nothing when
systemd deliberately kills the cgroup.

## The Primary Fix: `KillMode=process`

`KillMode=process` tells systemd to only SIGTERM the main PID and walk
away. Firecracker children are left alive.

### Two rollout paths

1. **New OVH hosts** — `backend/internal/api/enrollment.go` writes the
   systemd unit with `KillMode=process` and `TimeoutStopSec=30` directly
   during enrollment. Hosts enrolled after this branch merges get it
   from day one.

2. **Existing hosts** — `backend/cmd/agent/main.go` calls
   `ensureSystemdKillModeOverride()` on every startup, which writes
   `/etc/systemd/system/ocm-agent.service.d/killmode.conf` and runs
   `systemctl daemon-reload`. Idempotent (skips if the file already has
   the same content). Safe to call every time.

### The bootstrap problem

The **first** self-update after this code ships still kills the VMs —
the current systemd unit is still `KillMode=control-group` when that
update lands. The new agent writes the drop-in and reloads systemd
*after* starting, so it's already too late for that transition.

One sacrificial self-update, zero for all subsequent updates. This is
acceptable if documented.

### Why `Recover()` works after this fix

With `KillMode=process`:

1. Agent gets SIGTERM, runs shutdown, exits
2. systemd stops the service (main PID gone; children untouched)
3. systemd starts the new binary
4. New agent reads `browser-vms.json` and `vms.json`
5. For each VM: `pidAlive(p.PID)` — **true**, Firecracker is still there
6. Calls `firecracker.NewMachine(ctx, Config{SocketPath, VMID})` to
   reattach via the existing API socket
7. Re-registers in the `browserVMs` / `vms` map
8. From the control plane's view, nothing happened

---

## Codex Review Findings

Codex reviewed the fix. No `CRITICAL` issues, but three `IMPORTANT`
ones and three `MINOR` observations. Since the original review, the
browser PID persistence bug and the most dangerous PID-based cleanup
path have been fixed; this section distinguishes what is implemented
today from what still remains.

### IMPORTANT #1 — `KillMode=process` affects all agent children

The override protects everything in the cgroup, not just Firecracker.
If the agent abnormally exits (segfault, OOM), `cloudflared` and any
future subprocesses become orphans. The restarted agent then spawns
duplicates.

**Status:** known tradeoff. `KillMode=process` is the least-bad short
term choice in the current architecture, but it is not the ideal end
state. `KillMode=mixed` is **not** a valid fix here — systemd still
sends the final kill signal to the unit's remaining processes, so
Firecracker would die on restart just like today. The real long-term
fix is to give Firecracker its own ownership boundary (for example
per-VM systemd units/scopes, or a dedicated local VM supervisor) so
`ocm-agent` can restart independently while agent-owned helpers like
`cloudflared` still stop cleanly. Not yet implemented.

**Impact:** Operational. `cloudflared` is idempotent — spawning a
duplicate usually doesn't break anything; the first one tends to exit
once its tunnel connections are closed. Monitor for orphan
`cloudflared` processes until process ownership is properly split.

### IMPORTANT #2 — Weak PID validation during recovery

Both main and browser VM recovery paths use `pidAlive(p.PID)` which
boils down to `kill(pid, 0)`. That only confirms *some* process owns
that PID — not that it's still a Firecracker process, not that it's
still the same Firecracker process we launched. With PID reuse (common
on busy hosts after many restarts), recovery can mis-attach to or kill
an unrelated process.

**Status:** partially fixed.

**Impact:** High blast radius even if occurrence is infrequent. The
risk is not just PID wraparound on a huge 32-bit counter; normal host
process churn can reuse a PID after the original Firecracker exits. If
that happens, we'd mark a random process as "our VM" and eventually
kill it during teardown.

**Implemented now:**

- Recovery verifies `/proc/<pid>/comm == firecracker` before trusting
  the PID.
- Recovery reattaches to the Firecracker socket and verifies the
  reported Firecracker instance ID matches the persisted VM ID before
  accepting the VM into memory.
- Recovery no longer calls `Kill(p.PID)` on the browser VM socket-missing
  path, and the main VM path no longer escalates to destructive cleanup
  after a failed reattach.

**Implemented after the initial fix:**

- Unresolved persisted records are now kept in a quarantine set and
  written back to disk on later `saveState()` / `saveBrowserState()`
  passes instead of disappearing on the next save.
- Quarantined records carry metadata (`quarantined`,
  `quarantine_reason`, `quarantined_at`) so later recovery attempts can
  retry reattachment or operators can inspect what went wrong.

**Still not fixed:**

- Quarantine exists, but there is not yet a richer operator-facing
  surface (API/health endpoint/UI) for inspecting and reconciling
  quarantined records.
- Main VM cleanup still assumes "dead PID" means the persisted entry can
  be safely removed; that is correct for ephemeral host artifacts, but
  it is not yet paired with a richer drift/alert story.

**How it works now:**

- Read `/proc/<pid>/comm` and verify it's `firecracker`
- Open the API socket and query Firecracker instance info to verify the
  VMID matches the persisted state
- Only mark as recoverable if both checks pass
- Never call `Kill(p.PID)` during recovery cleanup unless process
  identity is proven first. If identity cannot be proven, prefer
  non-destructive cleanup (drop state, remove known ephemeral files if
  safe, surface an alert) over killing an unknown PID

### IMPORTANT #3 — Browser recovery + `saveBrowserState` persistence bug

**This is the bug that reproduces the original symptom during the
*second* agent restart**, exactly what the user reported.

**Setup:**

1. Agent v1 starts browser VM A. `machine.PID()` returns PID=123 (valid —
   the SDK launched the process itself). `saveBrowserState` writes
   `PID=123` to `browser-vms.json`.
2. Agent self-updates to v2. Firecracker survives (`KillMode=process`).
3. Agent v2 reads state file, sees `PID=123`, `pidAlive(123)` is true.
   Calls `firecracker.NewMachine(ctx, Config{SocketPath: socket})` to
   reattach. This creates a `*firecracker.Machine` object that is
   **observing** an existing process, not tracking a child it launched.
4. VM A is registered in `browserVMs` with `bvm.machine = <reattached>`.
5. Any next event (new browser VM create, status change, destroy of a
   different VM) triggers `saveBrowserState()`.
6. `saveBrowserState` iterates over `browserVMs` and calls
   `bvm.machine.PID()` for VM A. **The SDK's `Machine.PID()` returns 0
   (or an error) for a reattached Machine** — it has no child PID to
   track, only a socket.
7. The persisted record for VM A is re-saved with `PID=0`.
8. Agent self-updates to v3. Firecracker VM A is still alive.
9. Agent v3 reads state file, sees `PID=0` for VM A, calls
   `pidAlive(0)` → `false`.
10. **Orphan cleanup removes VM A from the state file, removes its
    TAP, deletes its rootfs file.** The Firecracker process is still
    running — it's now a true zombie with no state on the host.

This is why the user saw "works once, breaks on second restart." The
first agent update preserved the VM. The second update zombied it.

**Status:** fixed.

**Implemented fix:**

Mirror the main VM pattern — store PID on `browserRunningVM` at create
time AND at recover time, and use that field in `saveBrowserState`
instead of calling `machine.PID()` every time.

```go
type browserRunningVM struct {
    ID         string
    VMIP       string
    // ... existing fields ...

    // PID of the Firecracker process. Captured at create time (from
    // machine.PID()) or at recover time (from the persisted state file).
    // Never recomputed from machine.PID() because the SDK returns 0 for
    // Machine objects created via NewMachine reattach.
    PID        int

    machine    *firecracker.Machine
    cancel     context.CancelFunc
}
```

In `CreateBrowserVM`, after `machine.Start`, capture and store PID:
```go
pid, err := machine.PID()
if err != nil { /* fail create */ }
bvm.PID = pid
```

In `recoverBrowserVMs`, restore from the persisted record after
PID+identity verification succeeds:
```go
bvm := &browserRunningVM{
    // ...
    PID: p.PID,
    machine: reattached,
}
```

In `saveBrowserState`, use the struct field:
```go
persisted = append(persisted, persistedBrowserVM{
    // ...
    PID: bvm.PID,  // NOT bvm.machine.PID()
})
```

**Tests added:**

- `TestSaveBrowserStateUsesStoredPID`
- `TestRecoverBrowserVMPreservesPIDAcrossSave`

Those unit tests cover the critical double-restart persistence path:
`recover → saveBrowserState → recover` no longer rewrites the PID to 0.

### Implemented Recovery Flow

This is the exact host-side restart flow after the current fix set:

1. Browser VM create starts Firecracker and immediately reads
   `machine.PID()`. Create fails if the PID cannot be obtained.
2. `browserRunningVM.PID` is stored in memory and written to
   `browser-vms.json`.
3. Agent restarts under `KillMode=process`, leaving Firecracker alive.
4. `Recover()` loads `browser-vms.json`.
5. For each persisted browser VM:
   - `pidAlive(p.PID)` must succeed
   - `/proc/<pid>/comm` must equal `firecracker`
   - the Firecracker socket must exist
   - `firecracker.NewMachine(...)` must reattach
   - Firecracker instance info must report the expected VM ID
6. Only then is the VM re-registered in the in-memory `browserVMs` map.
7. Future `saveBrowserState()` calls persist `bvm.PID`, not
   `bvm.machine.PID()`, so a reattached SDK object can no longer zero
   out the saved PID.

If any identity check fails, recovery skips destructive PID-based
cleanup, leaves the VM out of the active in-memory map, and writes the
persisted record back to disk in a quarantined state for later recovery
or operator reconciliation.

### MINOR #1 — Bootstrap edge cases

The first-self-update sacrifice is mostly handled because the agent
installs the override *before* the self-update check runs. Remaining
edge cases:

- `systemctl daemon-reload` fails. The override file exists on disk
  but systemd doesn't know about it. Next restart still uses the old
  `KillMode`.
- The agent is not actually managed by systemd (e.g., run under
  supervisord, Docker, or a bare init script).

**Proposed fix:** log a loud warning in both cases. Add a diagnostic
endpoint that reports the effective `KillMode` via
`systemctl show ocm-agent -p KillMode`, so a dashboard or healthcheck
can alert on drift.

### MINOR #2 — `TimeoutStopSec=30` is fine

The agent's shutdown path is fast: shutdown notify, save state, stop
cloudflared, shutdown proxy, close listeners. Comfortably under 30s.

The bigger observation: `orchestrator.Drain()` at
`firecracker_linux.go:862` doesn't actually do any graceful VM work
today — it just saves state. If we ever add per-VM graceful shutdown
(e.g., flush data volume, let Neko drain WebRTC peers), we'd need to
revisit the timeout.

### MINOR #3 — Writing to `/etc/systemd/system/` at startup

No injection risk: fixed path, fixed contents, no user input. The
concern is operational — the agent mutates persistent host init config
at runtime, which is surprising behavior. Worth documenting and making
it explicit (not hidden in `main.go`).

**Proposed mitigation:** add an environment variable
`OCM_SKIP_SYSTEMD_OVERRIDE=1` that short-circuits
`ensureSystemdKillModeOverride()`. Lets operators opt out if they
manage the unit externally (e.g., via Ansible).

---

## Summary Table

| # | Area | Severity | Status |
|---|---|---|---|
| 1 | Broad `KillMode=process` affects all children | IMPORTANT | Known tradeoff — needs per-VM runtime ownership split later |
| 2 | Recovery trusts `pidAlive` without verifying Firecracker | IMPORTANT | Partially fixed — verifies `/proc/<pid>/comm` + Firecracker VM ID before reattach; quarantine now preserves unresolved records but operator surfacing is still thin |
| 3 | **Browser recovery re-saves with PID=0, next restart zombies the VM** | IMPORTANT | **Fixed — browser state now persists a stable PID field across reattach** |
| 4 | `daemon-reload` failure / non-systemd edge cases | MINOR | Log loudly; add diagnostic |
| 5 | `TimeoutStopSec=30` is enough for now | MINOR | Revisit if Drain() grows graceful VM work |
| 6 | Runtime mutation of host init config is surprising | MINOR | Add opt-out env var + documentation |

---

## Next Steps

1. **Keep the new browser PID persistence path** — `browserRunningVM.PID`
   is now the source of truth and must not regress back to
   `machine.PID()` on save.
2. **Extend recovery verification coverage** — add integration coverage
   for the browser double-restart path, not just unit tests.
3. **Finish #2** — keep the current `/proc/<pid>/comm` + Firecracker VM
   ID verification, then expose quarantined records through diagnostics
   / health surfaces so operators can see and resolve them explicitly.
4. **Document #1 and #4** in the network architecture doc — they're
   known tradeoffs that operators should be aware of.
5. **Plan the long-term ownership split** — move Firecracker out of
   `ocm-agent`'s process tree via per-VM systemd units/scopes or a
   dedicated local supervisor. That is the durable fix for
   non-disruptive agent restarts.
6. **Revisit #5/#6** only if they become a problem in practice.

## Related Documents

- `docs/superpowers/specs/2026-04-10-browser-vm-network-architecture.md`
  — full topology and data flows
- `docs/superpowers/specs/2026-04-09-browser-vm-design.md` — original
  design spec
- `docs/superpowers/plans/2026-04-09-browser-vm.md` — implementation
  plan
- `CLAUDE.md` §"Agent Self-Update" — agent upgrade safety rules
