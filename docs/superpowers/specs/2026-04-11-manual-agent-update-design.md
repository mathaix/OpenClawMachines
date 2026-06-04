# Manual Agent Update — Design

**Date:** 2026-04-11
**Status:** Draft
**Author:** Mathew (via Claude)

## Summary

Remove the 5-minute agent self-update polling loop. All agent updates after first boot are triggered manually by an admin clicking the existing "Update" button on the AdminHosts page.

The startup self-update check is kept — fresh hosts still pull the latest agent at first boot so provisioning a new host does not require an extra manual step.

## Motivation

Today the agent polls GCS every `AGENT_UPDATE_INTERVAL` (default 300s). When a new binary is published, every host picks it up within 5 minutes and restarts via `systemctl`. **That restart kills every running Firecracker VM on the host.**

Consequences:

- An accidental `make upload-agent` triggers a fleet-wide VM kill within 5 minutes, with no staging or canary.
- The operator has no window to validate the new binary on one host before it hits the rest of the fleet.
- The behavior is invisible from the UI — an admin watching AdminHosts cannot tell whether a version bump is "about to happen" or "already happening on another host."

The manual-update pathway (button → control-plane API → agent `POST /trigger-update` → `CheckAndUpdate` → `systemctl` restart) already exists end-to-end and is used when the admin clicks the amber "Update" button that appears on stale hosts. This spec simply removes the automatic path so the manual button becomes the only post-boot trigger.

## Goals

1. Agents do not auto-update after their first boot.
2. Admins retain the existing one-click per-host update flow, unchanged.
3. Fresh hosts still pull the latest agent at first boot (no regression in provisioning UX).
4. No dead code, dead config env vars, or dead test fixtures left behind.

## Non-goals

- Bulk "update all hosts" button.
- Agent version drift dashboard beyond what AdminHosts already shows.
- Changing the VM-drain behavior during update. The existing drain remains.
- Any change to rootfs or OpenClaw artifact delivery — this is strictly about the agent binary.

## Current State (as of 2026-04-11)

**Agent boot path — `backend/cmd/agent/main.go`:**

- Line 74-93: startup self-update check. `selfupdate.New(..., cfg.AgentUpdateInterval)` → `updater.CheckAndUpdate(startupCtx)`. If updated, returns and systemd restarts the process.
- Line 272-275: periodic self-update goroutine. `go updater.Run(ctx)` — this is the loop being removed.

**Updater — `backend/internal/selfupdate/updater.go`:**

- `Updater.Run(ctx)` (lines ~232-270): ticker-driven loop that calls `CheckAndUpdate` every `u.interval`, with a consecutive-failure backoff (`maxConsecutiveFailures`, `u.failures`).
- `Updater.CheckAndUpdate(ctx)`: the actual compare-download-replace-restart logic. Called by startup, by the manual trigger handler, and today by `Run`.

**Config — `backend/internal/config/config.go`:**

- `AgentUpdateInterval int` field, read from `AGENT_UPDATE_INTERVAL` env var, default 300 seconds.

**Manual trigger (already in place, not changing):**

- Frontend: `AdminHosts.tsx` → `handleTriggerUpdate` → `triggerHostUpdate(hostId)` → `POST /admin/hosts/{id}/trigger-update`.
- Control plane: `handleTriggerHostUpdate` in `backend/internal/api/admin_hosts.go` → `agentClient.TriggerUpdate(ctx, host)`.
- Agent: `handleTriggerUpdate` in `backend/internal/agentapi/handlers_update.go` → `s.updater.CheckAndUpdate(ctx)`.
- `updateInProgress` atomic guard prevents concurrent triggers.

## Changes

### 1. Delete the polling goroutine

`backend/cmd/agent/main.go:272-275`:

```go
// DELETE
if cfg.AgentGCSManifest != "" && updater != nil {
    go updater.Run(ctx)
}
```

The surrounding comment at line 272 ("8c. Start periodic self-update check") is also removed.

### 2. Delete `Updater.Run` and its private failure-counter machinery

`backend/internal/selfupdate/updater.go`:

- Remove `func (u *Updater) Run(ctx context.Context)` (lines ~232-270).
- Remove `u.failures` field and all writes to it.
- Remove `maxConsecutiveFailures` constant and the interval-based backoff block.
- Verify `CheckAndUpdate` has no remaining reads of the deleted fields.

Rationale: `failures` and the backoff exist only to prevent `Run`'s ticker from hot-looping on a persistent manifest error. `CheckAndUpdate` on its own is always driven by a single-shot caller (startup, manual trigger), so a single-call failure surfaces to that caller and there is nothing to back off.

### 3. Simplify `selfupdate.New` signature

`backend/internal/selfupdate/updater.go`:

Before:
```go
func New(ctx context.Context, manifestURL string, interval time.Duration) (*Updater, error)
```

After:
```go
func New(ctx context.Context, manifestURL string) (*Updater, error)
```

Remove the `interval` parameter and the `u.interval` field. Update the single caller at `backend/cmd/agent/main.go:78`. `backend/internal/selfupdate/updater_test.go` constructs `Updater` via a struct literal and does not set `interval` or `failures`, so no test edit is needed from the signature change.

### 4. Delete `AgentUpdateInterval` config

`backend/internal/config/config.go`:

- Remove the `AgentUpdateInterval int` field (line 187).
- Remove the `getEnvInt("AGENT_UPDATE_INTERVAL", 300)` read (line 235).
- Grep the tree for any other reference to `AgentUpdateInterval` or `AGENT_UPDATE_INTERVAL` and remove. Deploy scripts, systemd unit files, `.env.example`, and rootfs cloud-init are all candidates.

### 5. Update CLAUDE.md

The **Artifact Delivery Chain** section currently says:

> **Agent self-update polls every 5 min.** When a new agent binary is detected, it restarts via systemd — **this kills all running VMs on that host.** Only upload a new agent (`make upload-agent`) when agent code actually changed.

Rewrite to:

> **Agent updates are manual.** After `make upload-agent`, hosts stay on their current version until an admin clicks the "Update" button on the AdminHosts page (one host at a time). The update restarts the agent via systemd — **this kills all running VMs on that host.** Fresh hosts still pull the latest agent at first boot; only steady-state updates require the manual click.

Also update the **Common Fixes** row for "Agent not updating":

> Click "Update" on the host row in the AdminHosts page. Warning: kills running VMs on that host. Upload with `make upload-agent` first if the GCS manifest is stale.

### 6. (If present) Scrub deploy / provisioning scripts

Grep for `AGENT_UPDATE_INTERVAL` in `scripts/`, `Makefile`, and any systemd unit files. Remove any references. If the env var is being injected via `gcloud run deploy` or similar, that injection also goes.

## Non-changes

| Component | Reason |
|---|---|
| `main.go:83` startup `CheckAndUpdate` | Fresh hosts must pull the latest at first boot. |
| `handleTriggerUpdate` (agent) | Already correct, already wired to the button. |
| `handleTriggerHostUpdate` (control plane) | Already correct. |
| `agentClient.TriggerUpdate` | Already correct. |
| `AdminHosts.tsx` UI | Already has the button, confirm dialog, version badges, and stale detection. |
| `updateInProgress` atomic guard | Still needed for manual trigger re-entry. |
| VM drain during update | Still correct behavior. |

## Testing

1. `cd backend && go test ./internal/selfupdate/...` — must pass after the `Run` deletion and `New` signature change.
2. `cd backend && go test ./internal/config/...` — must pass after `AgentUpdateInterval` removal.
3. `cd backend && go build ./...` — must compile. This catches any caller I missed.
4. `make test-go` — full Go suite regression check.
5. `grep -rn "AgentUpdateInterval\|AGENT_UPDATE_INTERVAL\|updater\.Run\|Updater.*Run\b" backend scripts Makefile` — must return zero matches after the change.
6. Manual smoke test (post-deploy):
   - `make upload-agent` a no-op change.
   - Wait 10 minutes. Confirm no host has auto-updated (check `make show-agent-manifest` vs heartbeat-reported versions in AdminHosts).
   - Click "Update" on one host. Confirm the button shows "Updating…", the host restarts, and the new version appears in the badge.
   - Confirm the other host is still on the old version.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Admin forgets to click update and hosts drift behind latest | Medium | Visible in AdminHosts — stale version triggers the amber "Update" button and a "Update available" status label. |
| A host reboots (GCE live-migration, kernel panic) and silently jumps to whatever is in GCS at that moment | Low | Accepted per option 1 of brainstorming. Reboot is rare in practice, and the alternative (option 2) degrades first-boot UX for every new host to protect against a rare case. |
| Admin clicks Update while another update is still in flight on the same host | Handled | `updateInProgress` atomic guard in `handleTriggerUpdate` returns 409 Conflict on the second click. |
| Hidden code path somewhere calls `Updater.Run` or reads `AgentUpdateInterval` | Low | Step 6 (global grep) and step 3 (`go build ./...`) catch this at compile time. |

## Rollout

This is a code-only backend change. No rootfs rebuild, no migration. Ship order:

1. Merge PR to `main`.
2. `make deploy-backend` — deploys the new control plane (no-op for this feature; nothing in the control plane changed).
3. `make upload-agent` — pushes the new agent binary with the polling loop removed. **The currently-running agents will pick this up via their existing polling loop within 5 minutes** — this is the last time auto-update will fire. After the fleet self-updates to this binary, polling is gone forever.
4. Verify post-rollout by clicking Update on each host to confirm the manual path still works, then waiting 10 minutes to confirm nothing auto-updates further.

Note on step 3: the old agents self-update one last time to install the new no-polling agent. This is intentional and is the only way to transition without a maintenance window. After this one final auto-update, all future updates are manual.
