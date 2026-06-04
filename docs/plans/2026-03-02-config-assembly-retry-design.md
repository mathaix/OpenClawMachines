# Config Assembly: Retry Credentials + Fatal Startup Failure

## Problem

`assembleConfigForMachine` hard-fails on credential DB errors (introduced in d00a123), but:
1. No retry — a transient Neon blip fails immediately
2. `startMachineInternal` treats config assembly failure as non-fatal, booting VMs with empty config (blank gateway, no features)

## Design

### Change 1: Retry credential queries in `machine_config.go`

Add a `retryDBQuery` helper matching the existing `putWithRetry` pattern in `kvstore/cloudflare.go`:
- 3 attempts
- 200ms / 400ms backoff (linear, faster than KV's 500ms since Neon is lower latency)
- Wraps `ListMachineCredentials` and `ListAccountCredentials` calls only

If all retries exhaust, the existing hard error is returned.

### Change 2: Fatal config assembly in `server.go`

Remove the "Non-fatal: VM can boot without assembled config (backward compat)" path in `startMachineInternal`. If `assembleConfigForMachine` fails after retries, `startMachineInternal` returns an error. The user gets a clear error message and can retry once the DB is healthy.

### What doesn't change

- GET/POST handlers already hard-fail on assembly errors
- Capabilities listing (line 197) already hard-fails with no retry
- Soft warnings for registry entry lookup, template parse, and overrides parse remain unchanged

## Files

- `backend/internal/api/machine_config.go` — add `retryDBQuery`, wrap credential queries
- `backend/internal/api/server.go` — remove non-fatal config assembly path
