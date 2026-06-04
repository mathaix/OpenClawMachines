# Design: Fix `loadConfig()` in Metadata Mode

## Status: Implemented

Option A was implemented with refinements from Codex review. See "Implementation Details" below.

## The Problem

The openclaw fork has **two separate config loading paths** that are not connected:

### Path 1: `HttpConfigSource` (works correctly)
- **Where**: `gateway-cli` module, `createConfigSource()` at line 20622
- **When**: Gateway startup (`cfgSource.startup()`)
- **What it does**: Fetches the full assembled config from `$OCM_METADATA_URL/v1/config` via HTTP
- **Result**: Gateway boots with the correct config (trustedProxies, controlUi, allowedOrigins, etc.)

### Path 2: `loadConfig()` (broken before fix)
- **Where**: `config` module (`config-BrdONeSe.js`), exported as `i` (aliased to `loadConfig`)
- **When**: Called at runtime by 62+ callsites in `gateway-cli` alone — WebSocket handler, TUI, channel plugins, agent runners, etc.
- **What it did when `OCM_CONFIG_SOURCE=metadata`**:
  ```js
  function loadConfig() {
      if (process.env.OCM_CONFIG_SOURCE === "metadata") {
          const token = process.env.OPENCLAW_GATEWAY_TOKEN?.trim() || ...;
          return token ? { gateway: { auth: { token } } } : {};
      }
  }
  ```
- **Result**: Returned `{}` (since we removed the token env var). All 62 callsites got an empty config.

### Impact

Every runtime config check failed:
- `configSnapshot.gateway?.trustedProxies` → `undefined` → `[]` (proxy headers untrusted)
- `configSnapshot.gateway?.controlUi?.allowedOrigins` → `undefined` (no origins allowed)
- `configSnapshot.gateway?.controlUi?.dangerouslyAllowHostHeaderOriginFallback` → `undefined` → `false`
- The "origin not allowed" error was **unfixable** via config assembly — no matter what we put in `assembler.go`, `loadConfig()` never saw it.

## Module Boundary

The two paths live in **different compiled modules**:

```
config-BrdONeSe.js          gateway-cli-2U6avAcu.js
┌─────────────────────┐     ┌───────────────────────────────┐
│ loadConfig()        │◄────│ import { i as loadConfig }    │
│ configCache (local) │     │                               │
│ clearConfigCache()  │     │ HttpConfigSource              │
│ setMetadataConfig() │◄────│   .startup() → full config    │
│ _metadataConfig     │     │   .start() → polls version    │
│                     │     │   .read() → re-fetches        │
└─────────────────────┘     └───────────────────────────────┘
```

## Implementation Details (Option A — chosen)

### Changes to `src/config/io.ts`

Added a module-level cache variable and setter, updated `clearConfigCache()` and `loadConfig()`:

```typescript
let _metadataConfig: OpenClawConfig | null = null;

export function setMetadataConfig(config: OpenClawConfig): void {
  _metadataConfig = config;
}

export function clearConfigCache(): void {
  configCache = null;
  _metadataConfig = null;  // Prevent test leakage
}

export function loadConfig(): OpenClawConfig {
  if (
    process.env.OCM_CONFIG_SOURCE === "metadata" ||
    process.env.OPENCLAW_CONFIG_SOURCE === "http"
  ) {
    if (_metadataConfig) {
      return applyConfigOverrides(_metadataConfig);  // Consistent with file-based path
    }
    // Fallback before startup completes (CLI preflight in run.ts:142)
    const token =
      process.env.OPENCLAW_GATEWAY_TOKEN?.trim() ||
      process.env.OCM_GATEWAY_TOKEN?.trim() ||
      undefined;
    return (token ? { gateway: { auth: { token } } } : {}) as OpenClawConfig;
  }
  // ... rest unchanged (file-based loading)
}
```

Key Codex review refinements:
- **`applyConfigOverrides()`** is called on the cached config for consistency with the file-based path (already imported at line 43)
- **Fallback kept** for pre-startup CLI preflight (`run.ts:142`) — returns token-only config before `setMetadataConfig()` is called
- **`clearConfigCache()`** also clears `_metadataConfig` to prevent test leakage

### Changes to `src/config/config.ts`

Added `setMetadataConfig` to the re-export list.

### Changes to `src/gateway/server.impl.ts`

Wired cache **after auth bootstrap** (not after `cfgSource.startup()`):

```typescript
import { isNixMode, loadConfig, setMetadataConfig } from "../config/config.js";

// ... in startServer():
cfgAtStart = authBootstrap.cfg;

// Populate loadConfig() cache so runtime callers get the full config
if (
  process.env.OCM_CONFIG_SOURCE === "metadata" ||
  process.env.OPENCLAW_CONFIG_SOURCE === "http"
) {
  setMetadataConfig(cfgAtStart);
}
```

**Critical**: Must be after `authBootstrap.cfg` (line 194), not after `cfgSource.startup()` (line 185), because auth bootstrap may inject a generated token into the config.

### Changes to `src/config/http-source.ts`

Wired cache update on reload in `read()`:

```typescript
import { setMetadataConfig } from "./io.js";

// In read(), after building the normalized config:
const config = normalizeConfigPaths(applyTalkApiKey(...));

// Update loadConfig() cache so runtime callers see reloaded config
setMetadataConfig(config);

return { ... };
```

## Integration Test Coverage

Added two new subtests to `TestGatewaySuite` in `backend/internal/integration/gateway_test.go`:

### `InitLogs`
Reads `/var/log/openclaw-init.log` inside the VM and warns if no config log lines are found.

### `GatewayLogs`
Reads `/var/log/openclaw-gateway.log` inside the VM and:
- **Fails** on `"origin not allowed"` (indicates broken config wiring)
- **Warns** on `"token_missing"` and `"Proxy headers detected from untrusted"` (cosmetic issues)
- Logs the first 200 lines for debugging

Both use the existing `testTerminalCommand()` helper.

## Files Changed

| File | Repo | Change |
|------|------|--------|
| `src/config/io.ts` | openclaw fork | Added `_metadataConfig`, `setMetadataConfig()`, updated `clearConfigCache()`, updated `loadConfig()` |
| `src/config/config.ts` | openclaw fork | Re-exported `setMetadataConfig` |
| `src/config/http-source.ts` | openclaw fork | Import + call `setMetadataConfig(config)` in `read()` |
| `src/gateway/server.impl.ts` | openclaw fork | Import + call `setMetadataConfig(cfgAtStart)` after auth bootstrap |
| `rootfs/openclaw-fork.tgz` | OpenClawMachines | Rebuilt from fork |
| `backend/internal/integration/gateway_test.go` | OpenClawMachines | Added `InitLogs` and `GatewayLogs` subtests |

## Verification

1. Fork builds cleanly: `cd /home/mantiz/openclaw && pnpm build && pnpm pack`
2. Go tests pass: `cd /home/mantiz/OpenClawMachines/backend && go test ./internal/configassembly/ -v`
3. Smoke test: `make smoke-test` — boots VM, runs gateway suite including log inspection
4. `GatewayLogs` subtest should NOT contain `"origin not allowed"`
