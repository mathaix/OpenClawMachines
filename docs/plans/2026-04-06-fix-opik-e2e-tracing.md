# Fix: Opik E2E Tracing Test

Date: 2026-04-06
Status: In Progress — root cause identified, two fixes needed

## Problem

`TestGatewayE2E_OpikTracing` fails — the opik plugin never loads in the gateway process, so the mock receiver gets zero traces/spans after a successful LLM call.

## Root Cause (Two Issues)

### Issue 1: Config assembly overwrites plugin provenance

The test setup ran steps in the wrong order:

1. `tryInstallOpikPlugin` → installs plugin files AND writes provenance to `openclaw.json`
2. `configassembly.AssembleConfig` → generates a fresh `openclaw.json` from scratch
3. Writes assembled config → **overwrites provenance from step 1**

**Fix (implemented in `5be11f8`):** Reorder to assemble config first, write to disk, then install plugin (which merges provenance into existing config). This matches real deployment order.

### Issue 2: Gateway subprocess can't load TypeScript plugins

Even after the reorder fix, the opik plugin still doesn't load. Investigation revealed:

**The opik plugin (`@opik/opik-openclaw@0.2.9`) ships TypeScript-only** — its entry point is `index.ts` with no compiled `index.js`. The `package.json` declares `"openclaw": { "extensions": ["./index.ts"] }`.

OpenClaw uses `tsx` (bundled in its `node_modules`) to load TypeScript plugins. However, the test's `startGatewayProcess` creates a **stripped minimal environment** for the gateway subprocess:

```go
cmd.Env = []string{
    "HOME=" + homeDir,
    "PATH=" + os.Getenv("PATH"),
    "NODE_ENV=production",
    "OCM_MACHINE_ID=" + machineID,
    "OPENCLAW_GATEWAY_TOKEN=" + gatewayToken,
    "OPENCLAW_STATE_DIR=" + stateDir,
    "OPENCLAW_DISABLE_BONJOUR=1",
}
```

This stripped env is missing `NODE_PATH`, which means Node.js can't resolve `tsx` from OpenClaw's bundled `node_modules`. Verified:

```bash
# Without NODE_PATH — tsx not resolvable
$ node -e "require('tsx/cjs/api')"
# Error: Cannot find module 'tsx/cjs/api'

# With NODE_PATH — tsx resolves
$ NODE_PATH=/usr/lib/node_modules/openclaw/node_modules node -e "require('tsx/cjs/api')"
# OK
```

**Result:** The gateway starts fine (its own code is compiled JS) but silently skips `.ts` plugins because the TypeScript loader can't initialize. No error logged — the plugin is simply not discovered.

## Evidence

- Gateway log at startup: zero `[plugins]` lines, zero opik references
- `openclaw plugins list` (inherits full env) shows opik as `loaded` at `global:opik-openclaw/index.ts`
- Gateway log from working March 29 run shows `[plugins] opik: exporting traces` — plugin loaded correctly then
- The opik plugin directory exists with valid `openclaw.plugin.json`, `index.ts`, and `node_modules/`
- The `openclaw.json` on disk has correct `plugins.installs`, `plugins.entries`, and `plugins.allow`

## Fix

Two changes, both in `backend/internal/gatewaye2e/gateway_test.go`:

### Fix 1: Plugin install order (done)
Assemble config → write to disk → install plugin → start gateway.
Already implemented in commit `5be11f8`.

### Fix 2: Add NODE_PATH to gateway subprocess env
In `startGatewayProcess`, add `NODE_PATH` pointing to OpenClaw's bundled `node_modules` so the gateway's TypeScript loader can resolve `tsx`:

```go
// In startGatewayProcess, add to cmd.Env:
"NODE_PATH=" + filepath.Join(filepath.Dir(openclawBin), "..", "lib/node_modules/openclaw/node_modules"),
```

Or more robustly, resolve the path from the actual openclaw binary:
```go
openclawPkg := filepath.Dir(filepath.Dir(openclawBin)) // /usr/lib/node_modules/openclaw
"NODE_PATH=" + filepath.Join(openclawPkg, "node_modules"),
```

### Why the stripped env?

The test uses a minimal env to isolate the gateway from the host's config. This is good practice — but it accidentally strips `NODE_PATH` which the TypeScript loader needs. The fix adds only the specific path needed, not the full env.

### Alternative approaches considered

1. **Inherit full env** — works but loses test isolation
2. **Pre-compile the opik plugin** — works but adds build complexity and diverges from how plugins are installed in production
3. **Use a JS-only plugin for testing** — would work but doesn't test the real opik plugin

## Changes required

**File: `backend/internal/gatewaye2e/gateway_test.go`**

1. In `startGatewayProcess` (~line 637): resolve the openclaw package's `node_modules` path from the binary location and add `NODE_PATH` to the subprocess env.

2. Verify the `NODE_PATH` resolution works for both npm (`/usr/lib/node_modules/openclaw/node_modules`) and pnpm global installs.

## Validation

1. Run `make test-gateway-e2e` and verify `TestGatewayE2E_OpikTracing` passes
2. Verify gateway log shows `[plugins] opik: exporting traces` at startup
3. Verify mock receiver gets traces/spans after LLM call
4. Verify other tests still pass (no regression from env change)
5. Also fix the `TestGatewayE2E_ChatSend` WebSocket panic seen with 4.2 (separate issue)

## Risk

Low — test-only changes. No production code modified. The `NODE_PATH` addition is narrowly scoped to the openclaw package's own dependencies.
