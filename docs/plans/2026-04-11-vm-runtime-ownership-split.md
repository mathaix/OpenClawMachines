# VM Runtime Ownership Split

**Date:** 2026-04-11  
**Status:** In progress  
**Goal:** Move Firecracker lifecycle ownership out of `ocm-agent` and toward per-VM runtime owners, with per-VM systemd units/scopes as the target end state.

## Why

The current restart-safe design works by preserving Firecracker processes
across `ocm-agent` restart and reattaching from persisted state. That is
good enough for the common case, but it still makes agent restart safety
depend on:

- `KillMode=process`
- correct persisted state on disk
- successful PID / socket / identity verification
- quarantine/reconciliation when recovery is ambiguous

The ownership split changes the model from:

- "agent restarts are safe if recovery succeeds"

to:

- "VMs keep running because they are not owned by the agent process tree"

## Target Architecture

- `ocm-agent` remains the controller:
  - control API
  - metadata server integration
  - proxy / CDP / live view routing
  - heartbeat / reporting
  - update coordination
- Each VM gets an explicit runtime owner:
  - short term: `direct` owner (current behavior, Firecracker launched directly by agent)
  - target: `systemd-unit` owner (Firecracker launched in a per-VM systemd unit/scope)

## Current Implementation Slice

The current code now has both:

- a `direct` runtime owner
- a first `systemd-unit` runtime owner

The `systemd-unit` path is still an incremental rollout, but it is no
longer just a design seam. The orchestrator now:

- selects runtime owner kind from agent config (`VM_RUNTIME_OWNER`)
- launches main VMs and browser VMs through the owner interface
- persists runtime-owner metadata for both main VMs and browser VMs
- uses stable unit-naming helpers for systemd-backed ownership
- routes stop through the owner, so `systemd-unit` stops by unit name
- consults systemd unit state during recovery before trusting persisted PID

That moves ownership decisions out of the inlined orchestration path and
lets recovery treat the unit registry as the first source of truth when
the VM is systemd-owned.

## Checklist

- [x] Add runtime-owner abstraction to the orchestrator
- [x] Keep `direct` owner as the default behavior
- [x] Add `systemd-unit` owner implementation
- [x] Persist `runtime_owner_kind` and `runtime_owner_ref` for main VMs
- [x] Persist `runtime_owner_kind` and `runtime_owner_ref` for browser VMs
- [x] Route main VM create/start through the owner interface
- [x] Route browser VM create/start through the owner interface
- [x] Route stop/destroy through the owner interface
- [x] Make `systemd-unit` stop by unit name
- [x] Use systemd unit state during recovery for systemd-owned VMs
- [x] Infer default unit names when persisted owner refs are missing
- [x] Only trust inferred unit names when the systemd inventory confirms them
- [x] Preserve browser-only recovery when `vms.json` is empty
- [x] Add unit tests for runtime owner selection and systemd recovery behavior
- [x] Add end-to-end integration coverage for the `systemd-unit` path
- [x] Replace `systemctl` shell-outs with `go-systemd/v22/dbus` for state read, stop, and unit listing
- [x] Decouple `systemd-run` invocation from the request context so late cancellation cannot SIGKILL the unit mid-setup
- [x] Replace `systemd-run` start path with `StartTransientUnit` via dbus (phase 2)
- [ ] Decide whether active units without persisted metadata should be auto-imported, quarantined, or remain drift-only
- [x] Switch the rollout default from `direct` to `systemd-unit`
- [ ] Revisit whether `KillMode=process` is still needed once `systemd-unit` is the default

## Persisted Metadata

Both `persistedVM` and `persistedBrowserVM` now carry:

- `runtime_owner_kind`
- `runtime_owner_ref`

Current defaults for new agent launches:

- `runtime_owner_kind = "systemd-unit"`
- `runtime_owner_ref = "<unit-name>"`

Compatibility behavior for older persisted state:

- missing `runtime_owner_kind` is still interpreted as `direct`
- missing `runtime_owner_ref` for `systemd-unit` is inferred from the VM ID

Target values for systemd-backed ownership:

- `runtime_owner_kind = "systemd-unit"`
- `runtime_owner_ref = "<unit-name>"`

Suggested naming:

- main VM: `ocm-vm-<machine-id>.service`
- browser VM: `ocm-browser-vm-<browser-vm-id>.service`

## What the Systemd-backed Owner Does Now

The current `systemd-unit` owner now:

1. Prepare VM host artifacts exactly as today:
   - tap device
   - rootfs copy
   - socket path
   - Firecracker config
2. Start Firecracker under `systemd-run` using a per-VM unit name. The
   `systemd-run` invocation is intentionally not tied to the request
   context, so a late client cancellation cannot SIGKILL the client
   mid-setup and leave a half-started unit.
3. Record the unit name in persisted state.
4. Stop via go-systemd `StopUnitContext` against the per-VM unit name.
   The result channel is buffered size-1 to stay within go-systemd's
   serialized JobRemoved dispatcher semantics, so ctx cancellation
   during the stop job never deadlocks the bus connection.
5. Recover by consulting both:
   - persisted VM metadata
   - systemd unit state (via `GetUnitPropertiesContext` for
     `LoadState`/`ActiveState`/`SubState` and
     `GetUnitTypePropertiesContext(unit, "Service")` for `MainPID`)
6. Reconcile discovered unit inventory against persisted owner refs so stale
   PID-only state is no longer the primary recovery source. The inventory
   is fetched via `ListUnitsByPatternsContext` against
   `ocm-vm-*.service` and `ocm-browser-vm-*.service`.
7. Treat inferred owner refs as authoritative only when the systemd
   inventory confirms them; otherwise recovery leaves the ref blank so
   the VM quarantines with `unit_ref_missing` instead of synthesizing a
   unit name that later fails as `unit_missing`.

What is still incomplete:

- units discovered in systemd without matching persisted metadata are logged
  as drift, not imported as live VMs
- the `systemd-run` shell-out in the start path is still present; replacing
  it with `StartTransientUnit` via dbus is tracked as phase 2 and will
  also eliminate the firecracker-go-sdk `ProcessRunner` race where
  `m.cmd.Wait()` fires when `systemd-run` exits right after handing the
  unit off to PID 1

## Recovery Model After the Split

For `runtime_owner_kind = "systemd-unit"`:

- recovery asks systemd over dbus for the unit `LoadState`, `ActiveState`,
  `SubState`, and `MainPID` — text parsing of `systemctl show` output is
  no longer in the code path
- if the unit is active with a valid `MainPID`, that PID becomes the live
  process identity used for subsequent socket/Firecracker verification
- recovery enumerates matching units via `ListUnitsByPatternsContext` so
  it can reconcile inferred or stale owner refs against the live systemd
  inventory
- if the unit is inactive or missing, recovery no longer trusts the stale
  persisted PID as the primary authority
- a GC'd transient unit (the dominant recovery case for `--collect`
  units) surfaces via dbus as `UnknownObject`; `isUnitNotFoundErr` maps
  that — along with `NoSuchUnit`, `not loaded`, and `not found` — to
  `LoadState="not-found"` so the state machine takes the `unit_missing`
  cleanup branch just like the old `systemctl show` reader did
- PID verification remains defense in depth after unit-state verification
- quarantine still handles unresolved drift such as missing units with
  still-live host artifacts
- units without matching persisted metadata are currently logged as drift
  rather than auto-imported

## Required Follow-up Work

1. Decide whether units discovered without persisted metadata should be
   auto-imported, quarantined as first-class records, or remain log-only drift.
2. Revisit whether `KillMode=process` is still needed once the
   systemd-backed path is the default.

## Non-goals in This Slice

- No new host-local daemon yet.
- No removal of current reattachment/quarantine logic.
- No change to external API behavior.

This slice now provides the first real systemd-backed owner, but it is
still an incremental rollout rather than the final host lifecycle model.

## Phase 2: `StartTransientUnit` Handler Swap

### Why this is worth doing

Today's `systemdUnitRuntimeOwner.start()` launches Firecracker by
shelling out to `systemd-run` and passing the resulting `*exec.Cmd`
to `firecracker.WithProcessRunner`. This mostly works, but it
leaves two issues that only a structural change fixes:

1. **Latent race between `systemd-run` exit and sdk socket wait.**
   `systemd-run` (without `--wait`) is a client that returns as soon
   as the unit is started by PID 1. The sdk's `startVMM` installs a
   goroutine that calls `m.cmd.Wait()` and, when it returns, runs
   the cleanup chain — which includes `os.Remove(socketPath)`.
   In parallel, `m.waitForSocket` polls for the same socket.
   Today we survive because `waitForSocket` almost always wins,
   but the ordering is not guaranteed, and once it loses we see
   a Firecracker "socket missing" failure on a VM that actually
   booted fine.

2. **Architectural leakage between the sdk machine handle and the
   systemd owner ref.** `firecrackerRuntimeOwner.stop` takes both a
   `*firecracker.Machine` and a unit ref. The sdk machine is only
   useful for graceful shutdown via the API socket; the unit ref is
   what actually stops the process. The dual surface exists only
   because the sdk is still the thing "starting" Firecracker, even
   though the real start was done by systemd.

Both problems evaporate if we stop pretending the sdk owns the
Firecracker process lifecycle and instead start Firecracker via dbus,
then hand a disconnected `*firecracker.Machine` to the rest of the
handler chain purely for API configuration.

### Solution shape

Replace the default `StartVMMHandler` in `m.Handlers.FcInit` with a
custom handler that:

1. Calls `dbus.StartTransientUnitContext` with:
   - `name`: our existing `ocm-vm-<id>.service` / `ocm-browser-vm-<id>.service`
   - `mode`: `"replace"`
   - `properties`:
     - `PropExecStart(["firecracker", "--api-sock", socketPath, "--id", vmID, "--no-seccomp"], false)`
     - `PropType("exec")`
     - `Property{Name:"Restart", Value: dbus.MakeVariant("no")}`
     - `Property{Name:"KillMode", Value: dbus.MakeVariant("process")}`
     - `Property{Name:"CollectMode", Value: dbus.MakeVariant("inactive-or-failed")}` (maps to `systemd-run --collect`)
     - `PropDescription("OCM Firecracker VM " + vmID)` (for `journalctl -u`)
2. Waits on the returned result channel for `"done"` (buffered size 1;
   same load-bearing pattern as the existing `StopUnitContext` call).
3. Polls `systemdUnitStateReader(unit)` until `ActiveState == "active"`
   and `MainPID > 0`, or the request ctx expires. Short interval
   (10–25ms), tight timeout (3s — same as sdk default).
4. Polls the Firecracker API socket for readiness (file exists + HTTP
   request returns), replicating the file+HTTP check from
   `m.waitForSocket` but with a local helper instead of calling the
   private sdk method.
5. Uses the caller/request context for the boot handshake (`machine.Start`)
   so dbus start, unit-active polling, and socket readiness all honor the
   control-plane startup deadline; the background-backed machine context is
   still retained only for post-boot lifecycle ownership.
6. Appends an error-path cleanup func to `m.cleanupFuncs` that calls
   `systemdUnitStopper(unit)` and `os.Remove(socketPath)`. These only
   run on handler-chain failure (via the sdk's `defer` in `Start`),
   not on success.
7. Returns `nil` on success. The rest of the handler chain
   (`CreateLogFiles`, `BootstrapLogging`, `CreateMachine`,
   `CreateBootSource`, `AttachDrives`, `CreateNetworkInterfaces`,
   `AddVsocks`, `ConfigMmds`) runs as today, against the API socket.

The handler is registered via `m.Handlers.FcInit.Swap(Handler{Name:
firecracker.StartVMMHandlerName, Fn: ...})` between `NewMachine` and
`Start`. The sdk already tolerates `m.cmd` being populated but not
started: `PID()` errors cleanly, `stopVMM()` returns nil because
`m.cmd.Process == nil`, and `setupSignals` is only invoked from
inside the default `startVMM` (which we're skipping).

### What the `start()` function changes to

Rough shape (actual code will tighten this):

```go
func (s systemdUnitRuntimeOwner) start(ctx context.Context, vmID string, browser bool, socketPath string, fcCfg firecracker.Config, stdout, stderr io.Writer) (*firecracker.Machine, int, context.CancelFunc, error) {
    if err := systemdAvailable(); err != nil {
        return nil, 0, nil, err
    }
    unit := s.ownerRef(vmID, browser)

    vmCtx, vmCancel := context.WithCancel(context.Background())
    machine, err := firecracker.NewMachine(vmCtx, fcCfg) // no WithProcessRunner
    if err != nil {
        vmCancel()
        return nil, 0, nil, fmt.Errorf("create firecracker machine: %w", err)
    }

    customStart := firecracker.Handler{
        Name: firecracker.StartVMMHandlerName,
        Fn: func(handlerCtx context.Context, m *firecracker.Machine) error {
            return startFirecrackerUnit(handlerCtx, m, unit, socketPath, vmID)
        },
    }
    machine.Handlers.FcInit = machine.Handlers.FcInit.Swap(customStart)

    if err := machine.Start(vmCtx); err != nil {
        vmCancel()
        // Cleanup was already unwound by the sdk's defer; nothing to do.
        return nil, 0, nil, fmt.Errorf("start firecracker via systemd-unit: %w", err)
    }

    state, err := systemdUnitStateReader(unit)
    if err != nil || state.MainPID <= 0 {
        vmCancel()
        _ = systemdUnitStopper(unit)
        return nil, 0, nil, fmt.Errorf("post-start state read for %s: %w", unit, err)
    }
    return machine, state.MainPID, vmCancel, nil
}
```

Where `startFirecrackerUnit` does steps 1–5 above and appends the
error-path cleanup to `m.cleanupFuncs`.

### What the `stop()` function changes to

Today:

```go
func (s systemdUnitRuntimeOwner) stop(ctx, ownerRef, machine, cancel, graceTimeout) error {
    // stops unit via dbus, then StopVMM as fallback
}
```

Under Phase 2, `machine.StopVMM()` is a no-op (m.cmd was never
started), so we can drop the fallback and the parameter. However,
for compatibility with existing persisted state from Phase 1 where
`stop` is called with a live sdk machine, we should keep the
parameter but stop using it. The architecturally correct cleanup
is to remove `*firecracker.Machine` from the `stop` signature
entirely — that's a separate, mechanical change we can land in a
follow-up commit to keep Phase 2 focused.

An alternative worth considering: use the sdk machine's
`Shutdown(ctx)` for graceful shutdown first (sends ctrl+alt+del
via the API, which firecracker handles cleanly) and fall back to
`StopUnit` only if graceful shutdown fails. This mirrors the
current `direct` path and is probably the kinder stop. Adding it
doesn't make the signature worse and improves the stop story.

### Files that change

| File | Change |
|---|---|
| `backend/internal/orchestrator/runtime_owner_linux.go` | Replace `systemdUnitRuntimeOwner.start` body. Add `startFirecrackerUnit` handler and `waitForFirecrackerSocket` helper. Add `systemdStartTransientUnit` seam var for testability. Drop `exec.LookPath("systemd-run")` check (dbus handles its own availability). |
| `backend/internal/orchestrator/runtime_owner_linux_test.go` | Add table-driven test for the new `isSocketReady` helper (file missing → not ready, file exists but HTTP fails → not ready, both → ready). Integration coverage for the full start path stays in `backend/internal/integration/recovery_test.go`. |
| `backend/internal/integration/recovery_test.go` | Existing tests `TestRecovery_AgentRestartPreservesRunningVM_SystemdOwner` and `TestRecovery_BrowserVMSurvivesDoubleAgentRestart_SystemdOwner` already exercise the end-to-end path and will validate Phase 2 on a real systemd host without modification. |
| `docs/plans/2026-04-11-vm-runtime-ownership-split.md` | Flip the `Replace systemd-run start path with StartTransientUnit via dbus (phase 2)` checklist entry to `[x]` once landed. |

### Test strategy

- **Unit**: the parts that can be unit-tested cleanly are the
  property list construction, the socket-readiness helper, and the
  seam wiring. These get table-driven tests. The `systemdStartTransientUnit`
  var gets stubbed for a test that confirms the handler calls it with
  the expected unit name and properties.
- **Integration** (requires real systemd host): the existing two
  `SystemdOwner` recovery tests are the load-bearing coverage. They
  create a real VM, shut the agent down, start a new agent, and
  verify recovery reattaches the same PID and unit ref. These will
  catch any regression in the start → recover → destroy round trip.
- **Race-specific**: the motivating race (`cmd.Wait` → socket
  removal winning over `waitForSocket`) is not reproducible
  deterministically, but it's structurally gone once we stop using
  `WithProcessRunner` at all. Document the removal in the commit
  message; rely on integration tests for regression protection.

### Risks and mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| Handler chain downstream assumes `m.cmd.Process != nil` for some side effect we didn't audit | Low | The audit above traced `m.cmd` references in `machine.go`, handlers.go, and the command_builder. Only `PID()`, `stopVMM()`, and `setupSignals` touch it; all are safe with `cmd.Process == nil`. Verified against sdk v1.0.0. |
| `StartTransientUnitContext` reports `"done"` before the API socket exists | Certain | This is expected — we poll for socket readiness after the job signal. Same model as today. |
| `ExecStart` path resolution differs from `systemd-run`'s default `$PATH` lookup | Low | Pass the absolute path to `firecracker` if it's not in `/usr/bin`. Current systemd-run relies on `$PATH`; we should match that by doing an `exec.LookPath("firecracker")` once at handler-registration time and using the resolved absolute path in `ExecStart`. |
| Missing `CollectMode` causes stopped units to stick around until `systemctl reset-failed` | Certain if omitted | Set `CollectMode=inactive-or-failed` explicitly in the property list. The existing `--collect` semantics must be preserved or `isUnitNotFoundErr`'s UnknownObject path stops firing and recovery regresses. |
| Unit-level `KillMode=process` drop-in interacts differently with a dbus-created unit than with `systemd-run --property KillMode=process` | Low | They should be identical — both set the `KillMode` unit property. Verify on first deploy to a real host. |
| Integration tests require a systemd host to run; unit tests can't catch sdk-level regressions | Medium | Add a short README note in `backend/internal/integration/README.md` (if one exists, else in `docs/`) that the `_SystemdOwner` tests MUST be run on the claude-swarm host before merging. Gate on CI label. |
| `*firecracker.Machine` handle still in the `stop` signature is an unresolved wart | Low | Tracked as a follow-up commit after Phase 2 lands; not in scope. Commit message should note this. |

### Commit plan

One commit, scoped narrowly:

1. `runtime_owner_linux.go` — add `startFirecrackerUnit` handler,
   `waitForFirecrackerSocket` helper, `systemdStartTransientUnit`
   seam var, rewrite `systemdUnitRuntimeOwner.start` body. Drop
   `exec.LookPath("systemd-run")` availability check; add dbus
   connection availability check at handler-registration time.
2. `runtime_owner_linux_test.go` — add socket-readiness unit test
   and a "start calls dbus with expected properties" test using a
   stubbed `systemdStartTransientUnit`.
3. Commit message: name the race, name the architectural wart, and
   point at the integration tests that should be run on a systemd
   host before rollout.

Follow-up commit (separate, after Phase 2 lands clean): drop
`*firecracker.Machine` from `firecrackerRuntimeOwner.stop` signature.

### Phase 2 Checklist

- [x] Add `systemdStartTransientUnit` seam var defaulting to `(*dbus.Conn).StartTransientUnitContext`
- [x] Write `startFirecrackerUnit` handler function (StartTransientUnit → job wait → unit-active poll → socket poll → cleanup-func append)
- [x] Write `waitForFirecrackerSocket` helper (file stat + HTTP probe loop with ctx)
- [x] Resolve `firecracker` binary path once at module init via `exec.LookPath`
- [x] Build the property list with `PropExecStart`, `PropType("exec")`, `Restart=no`, `KillMode=process`, `CollectMode=inactive-or-failed`, `PropDescription`
- [x] Swap the handler into `machine.Handlers.FcInit` via `Swap(Handler{Name: firecracker.StartVMMHandlerName, ...})`
- [x] Rewrite `systemdUnitRuntimeOwner.start` to skip `WithProcessRunner`
- [x] Drop `exec.LookPath("systemd-run")` and `exec.LookPath("systemctl")` pre-checks; replace with a single dbus connection probe
- [x] Add unit test for socket readiness helper
- [x] Add unit test that stubs `systemdStartTransientUnit` and asserts the properties and unit name
- [x] Run `go build ./...`, `go test ./internal/orchestrator/`, `go vet`
- [ ] Run `TestRecovery_AgentRestartPreservesRunningVM_SystemdOwner` and `TestRecovery_BrowserVMSurvivesDoubleAgentRestart_SystemdOwner` on the claude-swarm host
- [x] Mark the phase-2 checklist line in the main checklist as `[x]`
- [ ] Separate follow-up commit: drop `*firecracker.Machine` from `firecrackerRuntimeOwner.stop` signature
