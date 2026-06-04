# PR #16 Code Review Findings

**PR:** feat: 3rd-party provider support (part 2) - enrollment and admin UI
**Branch:** `3rdpartyprov-part2`
**Date:** 2026-03-09
**Scope:** 37 files changed, +3,956 / -896 lines

---

## Critical Issues (score >= 80)

### 1. Frontend/backend response shape mismatch (score: 90)

**Files:** `frontend/src/pages/admin/AdminEnrollHost.tsx:29-31`, `backend/internal/api/enrollment.go:58-62`, `frontend/src/lib/api.ts:414-415`

The backend `handleCreateEnrollmentToken` returns a flat JSON object:
```json
{"token": "ocm_enroll_xxx", "expires_at": "...", "install_command": "..."}
```

But the frontend types the response as `{ token: EnrollmentToken; install_command: string }` and accesses `resp.token.token` and `resp.token.expires_at`. Since `resp.token` is a string at runtime, `.token` on it is `undefined`.

**Impact:** Enrollment wizard UI will display `undefined` for token and expiry values. The install command copy button will also fail.

**Fix:** Either change the backend to nest the token fields inside a `token` object, or change the frontend to access `resp.token` directly as a string and `resp.expires_at`.

---

### 2. `handleAgentShutdownNotify` not updated for per-host tokens (score: 85)

**File:** `backend/internal/api/server.go:1522-1539`

The heartbeat handler was updated to check per-host `agent_token` (via store lookup) before falling back to the fleet-wide token. But `handleAgentShutdownNotify` still only checks the fleet-wide `s.agentToken`. Its comment even says "same as heartbeat" but the implementations now diverge.

**Impact:** A registered host enrolled with a per-host token (and no fleet-wide token configured) will successfully heartbeat but get 403 on shutdown-notify, causing unclean shutdown handling.

**Fix:** Extract the per-host token auth logic into a shared helper and use it in both handlers.

---

### 3. `AgentToken` field serialized in admin API responses (score: 85)

**File:** `backend/internal/store/store.go:107`

The `Host` struct has `AgentToken *string json:"agent_token,omitempty"`. Since `handleListHosts` directly serializes the host list via `writeJSON`, per-host agent tokens are returned in plaintext to any admin API consumer.

**Impact:** Per-host authentication secrets exposed to admin users. An admin viewing the hosts list in the UI or via API sees all per-host agent tokens.

**Fix:** Change the json tag to `json:"-"` to prevent serialization, similar to how `SigningKey` is handled on Machine. If the token needs to be visible somewhere, create a dedicated admin endpoint with explicit access control.

---

## Notable Issues (score 75, below posting threshold)

### 4. SSRF via unvalidated `agent_endpoint` (score: 75)

**File:** `backend/internal/agentclient/client.go:440-443`

The `agentURL` method returns `*host.AgentEndpoint` directly as the base URL without validation. This value is agent-controlled (set during registration or heartbeat). A malicious registered host could set `agent_endpoint` to an internal URL (e.g., `http://metadata.google.internal`) and the control plane would make requests to it with the fleet-wide agent token in the Authorization header.

**Fix:** Validate `agent_endpoint` against an allowlist of schemes/ports, or at minimum reject private/internal IP ranges and non-HTTP schemes.

---

### 5. `agentClient` sends fleet-wide token to per-host-token hosts (score: 75)

**File:** `backend/internal/agentclient/client.go:455-464`

The `agentClient` is instantiated once with the fleet-wide `cfg.AgentToken` and sends it as Bearer token in `doRequest` to ALL agents. But registered hosts validate against their unique per-host `agent_token`. Every control plane request (CreateVM, StopVM, Health) to a registered host will fail with 401.

**Fix:** Store per-host tokens in the `agentClient` or look up the correct token from the Host record before making requests.

---

### 6. Hardcoded values / `os.Getenv()` in handlers (score: 75)

**File:** `backend/internal/api/enrollment.go:55,176,179,181,198`

Enrollment handlers call `os.Getenv()` directly for `BACKEND_URL`, `GCS_SERVICE_ACCOUNT_KEY`, `ROOTFS_GCS_MANIFEST`, and `AGENT_GCS_MANIFEST` instead of reading from the server config struct. The config struct already has fields for `BackendURL`, `RootfsGCSManifest`, and `AgentGCSManifest`. This violates the codebase pattern where all env vars are read at startup via `config.go`.

CLAUDE.md explicitly states in TDD step 6: "Check for hardcoded values that should be config."

**Fix:** Read these values from `s.cfg` instead of `os.Getenv()`. Add `GCSServiceAccountKey` to config if needed.

---

### 7. Reconciler doesn't release placements on host termination (score: 75)

**Files:** `backend/internal/reconciler/host.go` (`cleanupMachinesOnHost`), `backend/internal/fleet/placement.go`

When the reconciler detects a terminated host, `cleanupMachinesOnHost` marks machines as "error" but never calls `PlacementService.Release()`. Placement records stay in "active" or "reserved" state forever. The reconciler doesn't even have a reference to `PlacementService`.

**Impact:** Leaked placement records accumulate in `machine_placements` table. Low urgency since host termination is infrequent, and capacity counters on a terminated host are moot.

**Fix:** Inject `PlacementService` into the reconciler and call `Release(ctx, machineID, ReleaseHard)` during cleanup.

---

## Additional Observations (score < 75, not actionable)

### 8. TOCTOU race in enrollment token consumption (score: 55)

`handleAgentRegister` does app-level used/expired check, then `CreateRegisteredHost`, then `MarkEnrollmentTokenUsed`. Two concurrent requests could both create hosts. Unlikely in practice since enrollment is a manual admin operation.

### 9. Heartbeat auth parses body before verifying token (score: ~50)

The refactored heartbeat handler decodes JSON and does a DB lookup (GetHost) before authentication. Enables timing oracle for valid host IDs. Low practical impact.

### 10. `docs/CurrentFeature.md` gutted to one-liner (score: ~50)

Replaced with `# Current Feature: 3rdpartyprov-part2` instead of documenting progress. CLAUDE.md says "ALWAYS update docs/CurrentFeature.md during feature work."

---

## OpenAI Codex Review (independent)

Codex independently identified 5 issues, all corroborating our findings:

### [P1] Enrolled hosts stuck in `provisioning` — never become schedulable

**File:** `backend/internal/store/postgres.go:1949`

`CreateRegisteredHost` hard-codes status to `'provisioning'`, but the heartbeat handler only auto-recovers hosts from `unreachable` → `ready`. Since placement queries filter on `status='ready'`, enrolled hosts can heartbeat forever but never get machines scheduled to them.

**This is a NEW finding not in our original review.** The install script does not transition the host to `ready` either — there is no code path that promotes a registered host from `provisioning` to `ready`.

**Fix:** Either have the first successful heartbeat promote `provisioning` → `ready`, or set the initial status to `ready` in `CreateRegisteredHost`.

---

### [P1] `agentClient` sends fleet-wide token to registered hosts (= our #5)

Codex confirms: `doRequest` always sends `c.agentToken` (fleet-wide), but enrolled agents validate against their per-host `FC_AGENT_TOKEN`. All control-plane calls (CreateVM, StopVM, health, logs) will 401 for registered hosts.

---

### [P2] `handleAgentShutdownNotify` rejects per-host tokens (= our #2)

Codex confirms: the install script writes the per-host token as `FC_AGENT_TOKEN`, so the agent's shutdown notification will use that token. The handler only accepts fleet-wide token → 403.

---

### [P3] `AgentToken` exposed in admin JSON responses (= our #3)

Codex confirms: host serialization leaks the bearer credential to admin API consumers.

---

### [P3] Frontend enrollment token response contract mismatch (= our #1)

Codex confirms: backend returns flat `{token: string}`, frontend expects `{token: EnrollmentToken}`, causing `undefined` in the wizard UI.

---

## Recommended Fix Priority (updated with Codex findings)

1. **Fix #1** (type mismatch) -- blocks enrollment UI from working at all
2. **NEW: provisioning→ready transition** -- enrolled hosts never become schedulable (Codex P1)
3. **Fix #5** (agentClient wrong token) -- blocks control plane communication with registered hosts
4. **Fix #3** (AgentToken JSON exposure) -- secret leak, one-line fix
5. **Fix #2** (shutdown-notify auth) -- functional gap for registered hosts
6. **Fix #4** (SSRF) -- security hardening
7. **Fix #6** (hardcoded values) -- code quality / pattern compliance
8. **Fix #7** (placement leak) -- data hygiene
