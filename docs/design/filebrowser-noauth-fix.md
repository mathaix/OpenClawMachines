# Filebrowser Noauth Fix — Design Document

## Problem

Users see a login box when opening the Files tab in the machine workspace. Filebrowser should run in noauth mode (no login required), but the noauth configuration silently fails in production VMs.

## Root Cause (hypothesis — no captured stderr to confirm)

The init script (`scripts/init-openclaw.sh`) configures noauth via DB commands before starting filebrowser:

```bash
filebrowser config init -d /tmp/filebrowser.db 2>/dev/null || true
filebrowser users add admin adminadminadmin -d /tmp/filebrowser.db 2>/dev/null || true
filebrowser config set --auth.method=noauth -d /tmp/filebrowser.db 2>/dev/null || true
filebrowser --root /workspace --address 127.0.0.1 --port 8082 ...
```

**Three problems:**

1. **One or more DB setup commands silently fail** in production. All three commands (`config init`, `users add`, `config set`) suppress errors via `2>/dev/null || true`. The exact failing command is unknown because there is no log evidence. Hypothesis: BBolt DB lock contention or timing, but could also be `users add` or `config init` failing. Since noauth requires user id 1 to exist in the DB, any DB setup failure breaks the auth flow.

2. **The watchdog restart path omits `--auth.method=noauth`**, so any filebrowser crash restarts without noauth, recreating the login box even if initial boot was correct.

3. **Integration tests didn't catch this** because:
   - The `/api/login` + `X-Auth` test flow correctly validates noauth in the test VM (where DB setup succeeds), but doesn't exercise the watchdog restart path.
   - `DirectAccess_NoAuth` checks server HTML for `id="login"`, but filebrowser serves the same SPA shell regardless of auth mode — the login form is rendered by client-side JavaScript.
   - The `|| true` suppression means there's no log evidence of failure.

## Fix

### 1. Pass `--auth.method=noauth` as a CLI flag (primary fix)

Instead of configuring noauth in the DB (which can fail due to lock contention), pass it directly on the filebrowser command line:

```bash
filebrowser config init -d /tmp/filebrowser.db 2>/dev/null || true
filebrowser users add admin adminadminadmin -d /tmp/filebrowser.db 2>/dev/null || true
filebrowser \
  --root /workspace \
  --address 127.0.0.1 \
  --port 8082 \
  --baseURL /files \
  --database /tmp/filebrowser.db \
  --auth.method=noauth \
  >> /var/log/filebrowser.log 2>&1 &
```

This eliminates the DB config step that was failing. The CLI flag overrides whatever auth method is in the database.

The watchdog restart block must also include `--auth.method=noauth`.

### 2. Remove silent error suppression from DB setup

Change error handling so failures are logged:

```bash
filebrowser config init -d /tmp/filebrowser.db >> /var/log/filebrowser.log 2>&1 || true
filebrowser users add admin adminadminadmin -d /tmp/filebrowser.db >> /var/log/filebrowser.log 2>&1 || true
```

The `|| true` stays (DB might already be initialized on restart), but errors go to the log file instead of `/dev/null`.

### 3. Add integration test for noauth verification

Add a check that filebrowser logs contain "Auth method: noauth" (or equivalent) after startup, confirming the CLI flag was honored. Also add a test that verifies POSTing empty credentials to `/api/login` returns 200 (which only works with noauth).

Current test already does the POST check (`filebrowserLogin` posts `{}` and expects 200), but should also verify the log output.

## Files Changed

| File | Change |
|------|--------|
| `scripts/init-openclaw.sh` | Add `--auth.method=noauth` CLI flag to filebrowser startup and watchdog restart; redirect DB setup errors to log |
| `backend/internal/agentapi/proxy.go` | Forward X-Auth headers through proxy (already done) |
| `backend/internal/integration/filebrowser_test.go` | Verify noauth via login endpoint (already done); add log verification |

## Proxy Chain Reference

```
Browser → Cloudflare Worker → Agent Proxy (9091) → Auth Proxy (8080) → Filebrowser (8082)
```

- **Cloudflare Worker** strips machine slug from path: `/{slug}/files/...` → `/files/...`
- **Agent Proxy** authenticates via X-Proxy-Token, forwards to VM's auth proxy, rewrites `/files/` paths back to `/{slug}/files/` in HTML/JS/CSS responses
- **Auth Proxy** (inside VM) routes `/files/*` to filebrowser on port 8082 without machine token (CF Access handles edge auth)
- **Filebrowser** runs with `--baseURL /files`, noauth mode auto-authenticates via JWT

## Token Flow (noauth mode)

1. SPA loads, JavaScript POSTs to `/files/api/login` with empty body
2. Filebrowser noauth handler generates JWT for user id 1 (must exist in DB)
3. SPA stores token, sends as `X-Auth` header on subsequent API calls
4. Agent proxy forwards `X-Auth` header to VM (only strips `authorization`, `x-proxy-token`, `x-machine-token`)

## Test Gaps Identified

| Gap | Impact | Fix |
|-----|--------|-----|
| HTML-only noauth check doesn't detect JS-rendered login form | False pass — noauth failure not caught | POST to `/api/login` with empty body (already fixed) |
| `2>/dev/null \|\| true` hides init errors | No log evidence of failure | Redirect to filebrowser.log |
| No log-level verification of auth mode | Can't confirm noauth is active from logs | Add log check in integration test |

## Deployment

This changes the init script (rootfs). Deployment sequence:

1. Commit and push
2. `make build-upload-rootfs` — builds new rootfs with updated init script
3. Wait for agent self-update (~5 min when idle)
4. Agent restarts, re-stages rootfs from GCS
5. New VMs boot with `--auth.method=noauth` CLI flag
