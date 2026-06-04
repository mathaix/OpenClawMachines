# Manual Agent Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Remove the agent's 5-minute self-update polling loop so all post-boot agent updates require an explicit admin click on the existing AdminHosts "Update" button.

**Architecture:** Pure deletion refactor in the agent binary. The manual-update pathway (AdminHosts button → control-plane `POST /admin/hosts/{id}/trigger-update` → agent `POST /trigger-update` → `CheckAndUpdate` → `systemctl restart`) is already wired end-to-end and is not modified. The startup self-update check is preserved so fresh hosts pull the latest binary at first boot. Everything downstream of that — the periodic goroutine, the failure-counter backoff logic that existed only to rate-limit that goroutine, the `AGENT_UPDATE_INTERVAL` env var, and the `intervalSeconds` parameter on `selfupdate.New` — gets removed.

**Tech Stack:** Go 1.25 (backend agent), `backend/internal/selfupdate`, `backend/internal/config`, `backend/cmd/agent`. No frontend or DB changes.

**Reference spec:** `docs/superpowers/specs/2026-04-11-manual-agent-update-design.md`

---

## File Map

| Action | File | Responsibility |
|---|---|---|
| Modify | `backend/internal/selfupdate/updater.go` | Delete `Run`, `defaultInterval`, `maxConsecutiveFailures`, `interval` field, `failures` field, and simplify `New` signature. Remove `"time"` import if no longer used. |
| Modify | `backend/cmd/agent/main.go` | Drop `cfg.AgentUpdateInterval` arg from `selfupdate.New(...)` call at line 78. Delete the `go updater.Run(ctx)` block at lines 272-275 (comment + guard + call). |
| Modify | `backend/internal/config/config.go` | Delete `AgentUpdateInterval` struct field (line 187) and the `getEnvInt("AGENT_UPDATE_INTERVAL", 300)` assignment (line 235). |
| Modify | `CLAUDE.md` | Rewrite the "Agent self-update polls every 5 min" paragraph in the Artifact Delivery Chain section. Rewrite the "Agent not updating" row in the Common Fixes table. |
| No change | `backend/internal/selfupdate/updater_test.go` | Tests use `&Updater{...}` struct literal with only `logger`, `fetchManifest`, `downloadReader` fields — none of the removed fields are referenced, so this file compiles unchanged. |
| No change | `backend/internal/agentapi/handlers_update.go` | `handleTriggerUpdate` calls `s.updater.CheckAndUpdate(ctx)` which stays. |
| No change | `backend/internal/api/admin_hosts.go` | `handleTriggerHostUpdate` is the control-plane entry point for the button — unchanged. |
| No change | `frontend/src/pages/admin/AdminHosts.tsx` | Button, confirm dialog, stale detection already in place. |

## Pre-flight — verify current state

Before starting, confirm the codebase matches what the plan expects. If any of these diverge, stop and re-verify the plan.

- [x] **Step 0.1: Confirm the polling goroutine is at main.go:272-275**

Run: `sed -n '270,278p' backend/cmd/agent/main.go`

Expected output includes:
```
	// 8c. Start periodic self-update check
	if cfg.AgentGCSManifest != "" && updater != nil {
		go updater.Run(ctx)
	}
```

- [x] **Step 0.2: Confirm Run() is the only non-test caller of Updater.Run**

Run: `grep -rn "updater\.Run\|selfupdate\.Updater.*Run" backend --include='*.go' | grep -v _test.go`

Expected: exactly one match — `backend/cmd/agent/main.go:274:		go updater.Run(ctx)`.

- [x] **Step 0.3: Confirm AgentUpdateInterval has no callers outside the 3 known sites**

Run: `grep -rn "AgentUpdateInterval\|AGENT_UPDATE_INTERVAL" backend scripts Makefile rootfs 2>/dev/null`

Expected: exactly 3 matches — `config.go:187`, `config.go:235`, `main.go:78`. Zero matches in `scripts/`, `Makefile`, or `rootfs/`.

- [x] **Step 0.4: Confirm updater_test.go does not reference removed fields**

Run: `grep -n "interval\b\|failures\b\|maxConsec\|\.Run(" backend/internal/selfupdate/updater_test.go`

Expected: zero matches.

---

## Task 1: Simplify selfupdate package

**Files:**
- Modify: `backend/internal/selfupdate/updater.go`

Single atomic edit that removes the polling loop, the failure-counter backoff machinery, the `intervalSeconds` parameter on `New`, and the now-unused `"time"` import. All of these must land together because they're interlocking: deleting `Run` orphans `failures`/`maxConsecutiveFailures`/`interval`, which orphans the `intervalSeconds` parameter, which orphans `defaultInterval`, which is the last use of `"time"` in this file.

- [x] **Step 1.1: Run existing tests to establish baseline (should pass)**

Run: `cd backend && go test ./internal/selfupdate/...`

Expected: `ok  	github.com/mathaix/openclawmachines/backend/internal/selfupdate	...` — all tests pass.

If this fails, stop. The baseline must be green before removing code.

- [x] **Step 1.2: Delete the `defaultInterval` and `maxConsecutiveFailures` constants**

Open `backend/internal/selfupdate/updater.go`. Find this block near line 25:

```go
const (
	// defaultInterval is the default polling interval for self-update checks.
	defaultInterval = 5 * time.Minute

	// maxDownloadSize is the maximum allowed download size (256 MB).
	maxDownloadSize = 256 << 20

	// maxConsecutiveFailures is the threshold before extended backoff kicks in.
	maxConsecutiveFailures = 5
)
```

Replace with:

```go
const (
	// maxDownloadSize is the maximum allowed download size (256 MB).
	maxDownloadSize = 256 << 20
)
```

Rationale: `defaultInterval` was only used by `New` to initialize `interval`, and `interval` was only used by `Run`. `maxConsecutiveFailures` was only used by `Run`'s backoff. Both go.

- [x] **Step 1.3: Remove the `interval` and `failures` fields from `Updater` struct**

Find this struct near line 75:

```go
// Updater polls GCS for new agent versions and applies updates.
type Updater struct {
	client      *storage.Client
	manifestURI string
	interval    time.Duration
	logger      *slog.Logger
	failures    int // consecutive update failures

	// mu serializes CheckAndUpdate across the periodic loop and the
	// manual trigger-update HTTP endpoint. Without this, concurrent
	// callers can clobber each other's temp files and race on restart.
	mu sync.Mutex
```

Replace with:

```go
// Updater checks GCS for new agent versions and applies updates when
// triggered by startup or by the admin "Update" button.
type Updater struct {
	client      *storage.Client
	manifestURI string
	logger      *slog.Logger

	// mu serializes CheckAndUpdate across the startup check and the
	// manual trigger-update HTTP endpoint. Without this, concurrent
	// callers can clobber each other's temp files and race on restart.
	mu sync.Mutex
```

Both the doc comment (was "polls GCS") and the mu comment (was "periodic loop and the manual trigger-update") are updated so future readers aren't misled about how this type is used.

- [x] **Step 1.4: Change `New` signature — drop the `intervalSeconds` parameter**

Find this function near line 97:

```go
// New creates an Updater that polls the given GCS manifest URI.
// intervalSeconds <= 0 uses the default 5-minute interval.
func New(ctx context.Context, manifestURI string, intervalSeconds int) (*Updater, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	interval := defaultInterval
	if intervalSeconds > 0 {
		interval = time.Duration(intervalSeconds) * time.Second
	}

	u := &Updater{
		client:      client,
		manifestURI: manifestURI,
		interval:    interval,
		logger:      slog.Default(),
	}
```

Replace with:

```go
// New creates an Updater that reads the given GCS manifest URI on demand.
// The caller drives updates via CheckAndUpdate (startup check or admin trigger);
// this type no longer runs a background poll.
func New(ctx context.Context, manifestURI string) (*Updater, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	u := &Updater{
		client:      client,
		manifestURI: manifestURI,
		logger:      slog.Default(),
	}
```

- [x] **Step 1.5: Delete the `Run` method in its entirety**

Find this method near line 232 (after `CheckAndUpdate`, before `downloadVerifyAndInstall`):

```go
// Run starts a periodic update check loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated, err := u.CheckAndUpdate(ctx)
			if err != nil {
				u.failures++
				u.logger.Error("selfupdate.error", "error", err, "consecutive_failures", u.failures)

				// Back off on persistent failures: after maxConsecutiveFailures,
				// sleep an extra interval per failure to avoid hot-looping.
				if u.failures >= maxConsecutiveFailures {
					backoff := time.Duration(u.failures) * u.interval
					if backoff > 30*time.Minute {
						backoff = 30 * time.Minute
					}
					u.logger.Warn("selfupdate.backoff", "sleep", backoff, "failures", u.failures)
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			u.failures = 0 // reset on success (including "already_current")
			if updated {
				return // systemctl restart will re-launch us
			}
		}
	}
}
```

Delete the entire function — leading comment through closing brace. The next function (`downloadVerifyAndInstall`) now sits directly after `CheckAndUpdate`.

- [x] **Step 1.6: Remove the `"time"` import**

At the top of the file, find the import block and delete the `"time"` line.

Verify no stray `time.` references remain:

Run: `grep -n "\btime\." backend/internal/selfupdate/updater.go`

Expected: zero matches. If any remain, something was missed — stop and audit.

- [x] **Step 1.7: Build the selfupdate package**

Run: `cd backend && go build ./internal/selfupdate/...`

Expected: no output (success).

If this fails with "undefined: time" or similar, the `"time"` import was removed prematurely or a reference was missed. If it fails with "undefined: interval" or "undefined: failures", a usage outside the deleted regions was missed.

- [x] **Step 1.8: Run selfupdate tests**

Run: `cd backend && go test ./internal/selfupdate/... -v 2>&1 | tail -40`

Expected: all existing tests pass. The test file constructs `Updater` via struct literal using only `logger`, `fetchManifest`, `downloadReader` (verified in step 0.4), so no test code needs to change.

- [x] **Step 1.9: Commit**

```bash
git add backend/internal/selfupdate/updater.go
git commit -m "$(cat <<'EOF'
selfupdate: remove periodic Run loop and failure-counter backoff

Updater.Run, the consecutive-failures backoff, the interval field, and
the defaultInterval constant existed only to drive the 5-minute polling
goroutine in the agent. The agent now relies exclusively on the startup
check and the manual /trigger-update endpoint, so this machinery is
dead weight.

- Delete Updater.Run
- Delete u.failures and maxConsecutiveFailures
- Delete u.interval and defaultInterval
- Drop intervalSeconds parameter from New
- Remove now-unused time import

Existing CheckAndUpdate tests are unchanged — the struct-literal test
setup does not reference any removed field.
EOF
)"
```

## Task 2: Update agent main to stop calling the polling loop

**Files:**
- Modify: `backend/cmd/agent/main.go`

The agent binary is the only consumer of `selfupdate.Run` and `selfupdate.New`'s `intervalSeconds` parameter. After Task 1, `main.go` no longer compiles until these call sites are fixed. That is intentional — the compiler is enforcing atomic cross-file consistency.

- [x] **Step 2.1: Confirm main.go does not build after Task 1**

Run: `cd backend && go build ./cmd/agent/... 2>&1`

Expected: compile errors about `updater.Run` undefined and/or `selfupdate.New` argument count mismatch. **If this builds cleanly, something is wrong — either Task 1 did not actually remove the code, or main.go was already edited.** Stop and investigate.

- [x] **Step 2.2: Update the `selfupdate.New` call at line 78**

Find:

```go
		updater, initErr = selfupdate.New(context.Background(), cfg.AgentGCSManifest, cfg.AgentUpdateInterval)
```

Replace with:

```go
		updater, initErr = selfupdate.New(context.Background(), cfg.AgentGCSManifest)
```

- [x] **Step 2.3: Delete the periodic self-update block at lines 272-275**

Find:

```go
	// 8c. Start periodic self-update check
	if cfg.AgentGCSManifest != "" && updater != nil {
		go updater.Run(ctx)
	}

```

Delete all five lines (comment, guard, go statement, closing brace, trailing blank line).

Verify the following block (heartbeat comment + goroutine) now sits directly after the comment block for step 8b (health probes). The numbering "8c" is gone — that's fine; the remaining comments don't need renumbering because they're narrative, not semantic identifiers.

- [x] **Step 2.4: Build the agent binary**

Run: `cd backend && go build ./cmd/agent/...`

Expected: no output (success).

- [x] **Step 2.5: Run the full backend build to confirm no other package breaks**

Run: `cd backend && go build ./...`

Expected: no output (success).

- [x] **Step 2.6: Run the agent's package tests**

Run: `cd backend && go test ./cmd/agent/...`

Expected: pass (or "no test files" — `cmd/agent` may not ship tests).

- [x] **Step 2.7: Commit**

```bash
git add backend/cmd/agent/main.go
git commit -m "$(cat <<'EOF'
agent: stop running the self-update polling goroutine

Drop the interval argument from selfupdate.New and delete the
go updater.Run(ctx) block. The startup CheckAndUpdate at line 83
remains — fresh hosts still pull the latest agent at first boot.
All post-boot updates now require an admin click on the AdminHosts
"Update" button, which already routes through the existing
/trigger-update handler unchanged.
EOF
)"
```

## Task 3: Delete AgentUpdateInterval from config

**Files:**
- Modify: `backend/internal/config/config.go`

After Task 2, `cfg.AgentUpdateInterval` has no readers. Leaving the field and env-var read around is dead config surface that misleads operators and invites accidental re-use. Delete it.

- [x] **Step 3.1: Confirm AgentUpdateInterval has no readers**

Run: `grep -rn "AgentUpdateInterval" backend --include='*.go'`

Expected: exactly two matches — the struct field definition at `config.go:187` and the assignment at `config.go:235`. Zero readers.

If a reader shows up, something in Task 2 was skipped. Stop.

- [x] **Step 3.2: Delete the struct field**

Find near line 185:

```go
	// Agent self-update (empty = disabled)
	AgentGCSManifest    string // AGENT_GCS_MANIFEST - GCS URI for agent manifest
	AgentUpdateInterval int    // AGENT_UPDATE_INTERVAL - seconds (default 300)
```

Replace with:

```go
	// Agent self-update (empty = disabled). Updates are driven by the
	// startup check and the admin-triggered /trigger-update endpoint —
	// there is no background polling.
	AgentGCSManifest string // AGENT_GCS_MANIFEST - GCS URI for agent manifest
```

- [x] **Step 3.3: Delete the env-var read**

Find near line 234:

```go
		AgentGCSManifest:    os.Getenv("AGENT_GCS_MANIFEST"),
		AgentUpdateInterval: getEnvInt("AGENT_UPDATE_INTERVAL", 300),
```

Replace with:

```go
		AgentGCSManifest: os.Getenv("AGENT_GCS_MANIFEST"),
```

- [x] **Step 3.4: Build and test config package**

Run: `cd backend && go build ./internal/config/... && go test ./internal/config/...`

Expected: no build errors; tests pass (or "no test files" — config may not have its own tests).

- [x] **Step 3.5: Full backend build**

Run: `cd backend && go build ./...`

Expected: clean build.

- [x] **Step 3.6: Commit**

```bash
git add backend/internal/config/config.go
git commit -m "$(cat <<'EOF'
config: remove AgentUpdateInterval / AGENT_UPDATE_INTERVAL

The only reader was the selfupdate polling goroutine, which was
removed in the previous commits. Keeping the field around would
leave a misleading knob that operators could twiddle with no effect.
EOF
)"
```

## Task 4: Verify no stragglers anywhere in the tree

**Files:**
- None modified in this task — this is a verification gate.

A single grep across the whole repo catches any reference the plan missed (scripts, cloud-init, systemd unit files, `.env.example`, CI config, etc.).

- [x] **Step 4.1: Grep for any remaining references to removed identifiers**

Run:
```bash
grep -rn "AgentUpdateInterval\|AGENT_UPDATE_INTERVAL\|maxConsecutiveFailures\|defaultInterval" \
  /home/mantiz/OpenClawMachines \
  --include='*.go' --include='*.sh' --include='*.yaml' --include='*.yml' \
  --include='Makefile' --include='*.md' --include='*.env' --include='*.env.example' \
  2>/dev/null
```

Expected: zero matches.

**If any match appears in a Go file:** fix it and re-run steps 2.5 / 3.5 to re-build.

**If a match appears in `docs/superpowers/specs/` or `docs/superpowers/plans/`:** those are historical planning docs (this spec and plan). The plan is allowed to reference the identifiers being deleted. Leave those alone and move on.

**If a match appears in `scripts/`, `Makefile`, or a systemd unit file:** the operator was setting `AGENT_UPDATE_INTERVAL` via environment. Delete the reference.

- [x] **Step 4.2: Grep for any remaining reference to the deleted `Run` method**

Run: `grep -rn "updater\.Run\b\|\.Run(ctx)" backend/cmd/agent backend/internal/selfupdate --include='*.go'`

Expected: zero matches.

- [x] **Step 4.3: Run the full Go test suite**

Run: `cd backend && go test ./... 2>&1 | tail -30`

Expected: all packages pass (`ok`) or show "no test files". No `FAIL` lines.

- [x] **Step 4.4: Run `go vet` to catch anything the build missed**

Run: `cd backend && go vet ./...`

Expected: no output.

- [x] **Step 4.5: Commit any fix-ups (if steps 4.1-4.2 found stragglers)**

If no stragglers, skip this step. If fixes were needed:

```bash
git add <fixed files>
git commit -m "cleanup: remove straggling references to AGENT_UPDATE_INTERVAL / Run"
```

## Task 5: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

Two sections reference the old behavior and will mislead anyone reading the file.

- [x] **Step 5.1: Rewrite the Artifact Delivery Chain paragraph**

Find in `CLAUDE.md` under `## Artifact Delivery Chain`:

```markdown
- **Agent self-update polls every 5 min.** When a new agent binary is detected, it restarts via systemd — **this kills all running VMs on that host.** Only upload a new agent (`make upload-agent`) when agent code actually changed.
```

Replace with:

```markdown
- **Agent updates are manual.** After `make upload-agent`, hosts stay on their current version until an admin clicks the "Update" button on the AdminHosts page (one host at a time). The update restarts the agent via systemd — **this kills running VMs on that host.** Fresh hosts still pull the latest agent at first boot; only steady-state updates require the manual click. Only upload a new agent (`make upload-agent`) when agent code actually changed.
```

- [x] **Step 5.2: Rewrite the "Agent not updating" row in Common Fixes**

Find in the `## Common Fixes` table:

```markdown
| Agent not updating | `make upload-agent` then wait ~5min for self-update, or `make debug-agent` to check. **Warning: restarts host, kills running VMs.** |
```

Replace with:

```markdown
| Agent not updating | `make upload-agent` (uploads to GCS), then click "Update" on the host row in AdminHosts. No polling — hosts do not auto-update. **Warning: restarts agent, kills running VMs on that host.** |
```

- [x] **Step 5.3: Check the "What Needs Rebuilding?" table**

Find in `CLAUDE.md`:

```markdown
| Agent (`backend/cmd/agent`, `backend/internal/agentapi`, `backend/internal/selfupdate`) | `make upload-agent` — hosts self-update from GCS. **Restarts all hosts, kills running VMs.** Only upload when agent code changes. |
```

Replace with:

```markdown
| Agent (`backend/cmd/agent`, `backend/internal/agentapi`, `backend/internal/selfupdate`) | `make upload-agent` — uploads to GCS. Hosts do not auto-update. Click "Update" per host in AdminHosts to apply. **Each click kills running VMs on that host.** Only upload when agent code changes. |
```

- [x] **Step 5.4: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(claude-md): agent updates are manual, not polled

Update the Artifact Delivery Chain, Common Fixes, and rebuild-table
entries to reflect that the 5-min polling loop is gone. The manual
"Update" button in AdminHosts is now the only post-boot update path.
EOF
)"
```

## Task 6: Final verification gate

**Files:**
- None modified — final green-gate before push.

- [x] **Step 6.1: Run `make test-go`**

Run: `cd /home/mantiz/OpenClawMachines && make test-go 2>&1 | tail -30`

Expected: all Go tests pass.

- [x] **Step 6.2: Run `make lint` or `go vet` across the whole backend**

Run: `cd /home/mantiz/OpenClawMachines/backend && go vet ./...`

Expected: no output.

- [x] **Step 6.3: Confirm the commit history on the branch**

Run: `git log --oneline -7`

Expected: the last commits (newest first) are from Tasks 1-5:
1. `docs(claude-md): agent updates are manual, not polled`
2. (optional) `cleanup: ...` from Task 4.5
3. `config: remove AgentUpdateInterval / AGENT_UPDATE_INTERVAL`
4. `agent: stop running the self-update polling goroutine`
5. `selfupdate: remove periodic Run loop and failure-counter backoff`

- [x] **Step 6.4: Push to remote**

Run: `git push`

Expected: branch updated on origin. Do not open a new PR from inside this task — the work lives on whatever branch the engineer is currently on.

- [x] **Step 6.5: Document in this plan that execution is complete**

Mark every checkbox in this plan as done. No further edits.

---

## Rollout note (for whoever deploys this)

This is a code-only backend change. No rootfs rebuild, no DB migration.

Deploy order:
1. Merge to `main`.
2. `make deploy-backend` — no functional change in the control plane, but consistent with the standard rollout order.
3. `make upload-agent` — pushes the new (no-polling) agent binary. **The currently-running agents will pick this up via their existing polling loop within 5 minutes — this is the final auto-update.** After the fleet transitions to this binary, polling is gone forever.
4. After ~10 minutes, verify no further auto-updates happen: `make show-agent-manifest` vs the per-host agent version badges in AdminHosts.
5. Smoke-test the manual button on one host to confirm the manual path still works.

This transition behavior (one last auto-update to install the no-polling agent) is by design and is called out explicitly in the spec — do not treat it as a regression.
