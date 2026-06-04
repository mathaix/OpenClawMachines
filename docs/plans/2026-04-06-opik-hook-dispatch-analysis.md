# Opik Hook Dispatch Failure — Root Cause Analysis

Date: 2026-04-06
Status: Symptom verified, root cause still unconfirmed

## Problem

Opik plugin loads and its service starts (`[plugins] opik: exporting traces to project ...`), but LLM hooks (`llm_input`, `llm_output`, `agent_end`) never fire. The mock trace receiver gets zero requests. This affects both the E2E test (`TestGatewayE2E_OpikTracing`) and production.

## Verified Behavior

The config issue is fixed: `plugins.entries.opik-openclaw.enabled=true`, `plugins.entries.opik-openclaw.config.enabled=true`, `plugins.allow=["opik-openclaw"]`, and `plugins.installs.opik-openclaw` are present. OpenClaw 2026.4.2 starts the opik plugin service and logs `opik: exporting traces ...`.

Nebius verification on 2026.4.2 with `nebius/openai/gpt-oss-120b` returned `OK` from the model, proving the LLM call completed successfully without relying on the depleted Anthropic key. The mock Opik receiver still got `{"traces":0,"spans":0}`.

Temporary instrumentation in the installed opik plugin showed:
- `opik-debug: registered llm hooks`
- no `opik-debug: llm_input hook fired`
- no `opik-debug: llm_output hook fired`
- no `opik-debug: agent_end hook fired`

Conclusion: hooks are registered from `service.start()`, but the gateway does not dispatch those hooks during the embedded run. The specific registry-caching explanation below is a hypothesis, not a confirmed root cause.

## Hypothesis: Registry Caching Splits Hook State

OpenClaw's plugin loader caches the plugin registry. When the gateway starts, `loadOpenClawPlugins` may return a cached registry copy. The global hook runner may be re-initialized with this cached copy. But the `api.on()` closures from plugin `register()` could still reference the original registry.

Result: `api.on("llm_input", handler)` pushes to the original registry's `typedHooks` array, while `hasHooks("llm_input")` reads from the cached registry's (empty) `typedHooks` array.

### Code path

**Plugin load time** (`loader-BkOjign1.js`):

1. `loadOpenClawPlugins()` calls `createPluginRegistry()` → creates `registry_A` with empty `typedHooks: []`
2. Plugin's `register(api)` is called. The `api.on` closure captures `registry_A`:
   ```javascript
   // line 1252
   on: (hookName, handler, opts) => registerTypedHook(record, hookName, handler, opts, params.hookPolicy)
   // registerTypedHook pushes to registry_A.typedHooks
   ```
3. Plugin calls `api.registerService(opikService)` → service stored in `registry_A.services`
4. Registry is cached: `registryCache.set(cacheKey, { registry: registry_A, ... })`
5. `activatePluginRegistry(registry_A)` → `initializeGlobalHookRunner(registry_A)` ✓

**On a second load** (e.g., config reload, CLI command, or any code path that calls `loadOpenClawPlugins` again with the same cache key):

6. `getCachedPluginRegistry(cacheKey)` returns the cached entry
7. `activatePluginRegistry(cached.registry)` → `initializeGlobalHookRunner(cached.registry)` 
   - This re-creates the hook runner with a fresh `createHookRunner(cached.registry)`
   - If `cached.registry` is the same object reference as `registry_A`, no problem
   - **But if the cache stores/returns a different object**, the hook runner now reads from `registry_B`

**Service start time** (`gateway-cli-CWpalJNJ.js:25839`):

8. `startPluginServices({ registry: params.pluginRegistry })` iterates services
9. `service.start(ctx)` runs opik's start method
10. Opik calls `api.on("llm_input", handler)` — pushes to `registry_A.typedHooks`

**LLM call time**:

11. `getGlobalHookRunner()` returns the runner initialized in step 7
12. `hookRunner.hasHooks("llm_input")` reads `registry.typedHooks` — which may be a different instance
13. Returns `count=0, total=0` → dispatch skipped

### Evidence

Debug instrumentation confirmed:

```
[opik-debug] api.on type=function api.on.name=on 
  api.on.toString=(hookName, handler, opts) => registerTypedHook(record, hookName, handler, opts, params.hookPolicy)
[opik-debug] calling api.on(llm_input) now

# 16 seconds later, during LLM call:
[hook-debug] hasHooks(llm_input) count=0 total=0
[hook-debug] hasHooks(agent_end) count=0 total=0
[hook-debug] hasHooks(llm_output) count=0 total=0
```

- `api.on` is the real `registerTypedHook` function (not `noopOn`)
- `api.on("llm_input", handler)` is called successfully
- But the hook runner's registry has `total=0` typed hooks
- Conclusion: consistent with hooks being registered into a registry that the active hook runner does not read, but this still needs upstream verification.

## `flushSuccesses=1` Does Not Mean Traces Were Sent

The Opik SDK's `client.flush()` succeeds (doesn't throw) even when the buffer is empty. The `flushSuccesses=1` metric at shutdown is a no-op flush — no HTTP requests reach the mock receiver. Confirmed: debug instrumentation showed `hasHooks(llm_input) count=0` AND `flushSuccesses=1` in the same run. No trace data was ever created because the hooks never fired.

## Version History

| Version | `service.start()` called? | Hooks dispatch? | Notes |
|---------|---------------------------|-----------------|-------|
| 2026.3.12 | No (missing lifecycle) | N/A | `registry.services` populated but never iterated |
| 2026.3.28 | No (missing lifecycle) | N/A | Same as 3.12 |
| 2026.4.2 | Yes | No (registry split) | Service starts, hooks registered to wrong registry |

The March 29 working log (from memory) showed `[plugins] [hooks] running llm_input` — this may have been a version where the caching bug didn't exist, or where the registry wasn't cached (single load path).

## Separate Issue: WebSocket Read Deadline (4.2)

This is NOT related to opik. OpenClaw 4.2 takes ~6s from `chat.send` to first LLM request (vs ~2s on 3.28) due to additional plugin loading during agent startup. The E2E test's 5-second per-read WS deadline expired, causing gorilla/websocket to close the connection and panic. Fixed by increasing to 15s (commit `a71a338`). This fix is required for all gateway E2E tests on 4.2, not just opik.

## Additional Findings

### `config.enabled` requirement

The opik service checks `ctx.config.enabled` (inside `plugins.entries.opik-openclaw.config`) before registering hooks:

```typescript
// service.ts:444
if (!opikCfg?.enabled) return;
```

This is separate from the entry-level `plugins.entries.opik-openclaw.enabled`. Both are required:
- Entry-level `enabled: true` → OpenClaw loads the module and calls `register(api)`
- `config.enabled: true` → opik service activates and registers hooks

Our `configassembly` sets both (line 646), but `openclaw plugins install` sets neither. Production machines whose config was assembled before this code existed are missing `config.enabled`.

### WebSocket read deadline

OpenClaw 4.2 takes ~6s from `chat.send` acceptance to first LLM request (vs ~2s on 3.28) due to additional plugin loading and hook registration during agent startup. The E2E test's 5-second per-read WebSocket deadline caused gorilla/websocket to close the connection before the agent started. Fixed by increasing to 15s (commit `a71a338`).

## Upstream Investigation Needed

**File:** OpenClaw `src/plugins/loader.ts` (or equivalent in the dist bundle at `loader-BkOjign1.js`)

**Hypothesis:** `activatePluginRegistry` re-initializes the global hook runner with the cached registry reference. If the hook runner is re-created, it may read from a different registry snapshot than the one existing `api.on()` closures push to.

**Possible fixes:**

1. **Don't re-create the hook runner on cache hit** — if the registry reference is the same, skip `initializeGlobalHookRunner`
2. **Make `registerTypedHook` push to the globally active registry** instead of the closure-captured one — resolve the registry via the global singleton at call time
3. **Ensure cache returns the exact same object reference** — verify `getCachedPluginRegistry` doesn't clone or wrap the registry

**Filed as:** openclaw/openclaw#61941 (update needed with this analysis)

## Workaround

Until the upstream fix, opik tracing cannot work via the plugin service API on any OpenClaw version:
- 3.28: `service.start()` never called (missing lifecycle)
- 4.2: `service.start()` called but hooks land in wrong registry

The only workaround would be to register hooks during `register()` instead of `start()`, but that requires changes to the opik plugin itself.

## Recommended Patch Strategy

The current failure is no longer in OCM config assembly. That part is fixed:

- `plugins.entries.opik-openclaw.enabled=true`
- `plugins.entries.opik-openclaw.config.enabled=true`
- `apiUrl` and inline `apiKey` are present in `openclaw.json`
- the plugin service logs `opik: exporting traces ...`

So the practical patch point is the Opik plugin itself, not the OCM config path.

### Patch Goal

Move hook registration out of `service.start()` and into `register(api)`.

Why:

- `register(api)` runs during plugin load, before the later service lifecycle edge that appears to split registry state
- `api.on(...)` registered during `register()` should attach to the same registry instance used by the active hook runner
- this avoids depending on the plugin service startup path for hook wiring

### Patch Shape

The plugin should be reorganized into two layers:

1. **Hook registration layer** in `register(api)`
2. **Transport/flush lifecycle** in the background service

#### 1. Register hooks immediately

In `register(api)`:

- create shared plugin state
- register `llm_input`, `llm_output`, `agent_end`, and tool/subagent hooks immediately with `api.on(...)`
- keep `api.registerService(...)` for startup validation, timers, and shutdown flush

Conceptually:

```ts
export default {
  id: "opik-openclaw",
  register(api) {
    const state = createOpikState(api)

    api.on("llm_input", (event, ctx) => state.captureLlmInput(event, ctx))
    api.on("llm_output", (event, ctx) => state.captureLlmOutput(event, ctx))
    api.on("agent_end", (event, ctx) => state.captureAgentEnd(event, ctx))
    api.on("before_tool_call", (event, ctx) => state.captureToolStart(event, ctx))
    api.on("after_tool_call", (event, ctx) => state.captureToolEnd(event, ctx))

    api.registerService(createOpikService(state))
  }
}
```

#### 2. Keep service startup for runtime plumbing

`service.start(ctx)` should only do runtime setup:

- validate `ctx.config.enabled`
- create/configure the Opik client
- validate or create the target project
- mark the shared state as ready

`service.stop()` should still flush and tear down timers.

### Shared State Requirements

The hook handlers and the service should use the same state object:

- config snapshot
- enabled flag
- client reference
- trace/span buffer
- mutex/queue for buffered events
- flush timer / shutdown hook state

The hook handlers must not assume the client exists yet. They should:

- no-op if disabled
- buffer minimal event data until startup finishes
- never throw if the client has not been initialized

### Minimum-Risk Variant

If plugin internals make deferred startup awkward, the lower-risk workaround is:

- instantiate the Opik client in `register(api)` as soon as config is available
- use the service only for periodic flush and shutdown flush

That removes the service-start ordering from the hot path entirely.

## Why This Is Patchable In OCM

This is realistic for OCM because:

- Opik is bundled via the OpenClaw artifact build, not the rootfs
- OCM already owns the artifact assembly pipeline
- a patched plugin can ship as a new OpenClaw artifact revision without a rootfs release

So the likely delivery path is:

1. carry a patched `@opik/opik-openclaw` in the artifact build
2. publish a new OpenClaw artifact revision
3. upgrade one test machine
4. verify the mock Opik receiver sees traces/spans again

## Scope Of The Patch

This patch should be treated as a workaround, not the final root-cause fix.

The long-term fix still belongs upstream in OpenClaw core:

- avoid registry/hook-runner divergence on cache hits
- or make hook registration resolve against the globally active registry

But the plugin-side workaround is likely the fastest way to restore tracing in production.

## Release Review Conclusion

Review of recent upstream OpenClaw releases did not show any release note explicitly fixing Opik tracing, plugin hook dispatch, or the registry caching problem. The likely relevant work in upstream release notes was around generic provider transport/header handling, not plugin hook lifecycle.

Therefore:

- upgrading OpenClaw alone should not be assumed to fix the issue
- a local plugin patch is the more reliable next move

## Files Modified in This Investigation

| File | Change | Commit |
|------|--------|--------|
| `gateway_test.go` | Reorder config assembly before plugin install | `5be11f8` |
| `gateway_test.go` | Add `config.enabled = true` to opik config | `61a5ede` |
| `gateway_test.go` | Stop ConfigPush_AddPlugin from clobbering opik | `61a5ede` |
| `gateway_test.go` | Increase WS read deadline 5s → 15s | `a71a338` |
| `scripts/test-opik-baseline.sh` | Standalone baseline test | `61a5ede` |
| `docs/plans/2026-04-06-fix-opik-e2e-tracing.md` | Earlier plan (superseded by this doc) |  |
