# Opik Tracing Fix Analysis

Date: 2026-04-08
Status: Confirmed and validated against the OCM baseline harness

## Summary

The original hook-dispatch theory was useful, but incomplete.

Two separate failures existed in `@opik/opik-openclaw`:

1. Hooks were being registered too late in the OpenClaw lifecycle.
2. The hook closures and the started service were not sharing the same runtime state.

After those were fixed, one more runtime issue remained:

3. In the Nebius embedded path, OpenClaw fired hooks without `agentCtx.sessionKey`, but `sessionId` was still available and stable.

The final plugin fix was:

- register hooks during plugin registration, not only during `service.start()`
- move mutable runtime state to shared module-level state
- resolve missing session keys via fallback order:
  - `agentCtx.sessionKey`
  - `agentCtx.sessionId`
  - remembered `agentId -> sessionKey`
  - last active session key

That combination restored end-to-end trace export in the baseline harness.

## What We Verified

### 1. Baseline harness existed in OCM

The platform-side harness is:

- [scripts/test-opik-baseline.sh](/Users/mantiz/openclawmachines/scripts/test-opik-baseline.sh)

This script installs the Opik plugin, patches OpenClaw config to a mock Opik receiver, starts the gateway, sends a chat, and checks the mock receiver status.

### 2. Old baseline symptom

Before the plugin fix:

- plugin startup log appeared: `opik: exporting traces to project ...`
- but the mock receiver got `{"traces":0,"spans":0}`
- Nebius and Anthropic failures made provider behavior noisy, but the trace exporter still showed no traffic

### 3. Standalone Opik SDK repro

A standalone repro using the Opik SDK directly proved the SDK itself was not the problem.

The SDK sent:

- `POST /v1/private/traces/batch`
- `POST /v1/private/spans/batch`

That ruled out the mock contract and the Opik SDK transport as the primary issue.

### 4. Hook dispatch fix

After moving hook registration earlier, the gateway started logging:

- `running llm_input (1 handlers)`
- `running agent_end (1 handlers)`
- `running llm_output (1 handlers)`

That confirmed the original registry/dispatch suspicion was real, at least in part.

### 5. Shared-state failure

Temporary runtime diagnostics then showed:

- `service.start()` initialized an Opik client successfully
- hook callbacks still observed `hasClient=false`

This proved the started service instance and the hook-registration instance were different runtime objects.

So even though hooks fired, they were reading stale or uninitialized per-instance state.

### 6. Missing `sessionKey`

After fixing shared state, the gateway exposed the next blocker:

- `llm_input missing sessionKey`
- `agent_end missing sessionKey`
- `llm_output missing sessionKey`

In the Nebius embedded run, the hook context did not include `agentCtx.sessionKey`, but the session identifier still existed as `sessionId=opik-test-nebius`.

That meant the exporter still had no correlation key unless it fell back to `sessionId`.

## Final Root Cause

The tracing failure was not one bug. It was a chain:

1. Hook registration timing was wrong for the active OpenClaw lifecycle.
2. Runtime state lived inside one service instance closure, while hooks executed through another instance.
3. The embedded Nebius path did not reliably populate `agentCtx.sessionKey`.

The plugin needed all three fixes to work reliably in the OCM baseline harness.

## Final Fix Shape

In `@opik/opik-openclaw`:

### 1. Register hooks early

In `index.ts`:

- create the service
- call `service.registerHooks()`
- then call `api.registerService(service)`

This ensures hook wiring happens during plugin registration.

### 2. Share runtime state across instances

In `src/service.ts`:

- move mutable runtime state out of the per-service closure and into shared module-level state:
  - `client`
  - `activeTraces`
  - subagent/session correlation maps
  - flush queue
  - runtime config values

This ensures the hook closures and `service.start()` operate on the same client and trace registry.

### 3. Add session resolution fallback

The plugin now resolves a usable session key in this order:

1. `agentCtx.sessionKey`
2. `agentCtx.sessionId`
3. remembered `agentId -> sessionKey`
4. last active session key

This specifically fixes the Nebius embedded path where `sessionKey` was absent but `sessionId` matched the true OpenClaw session id.

## Validation

### Plugin checks

In `~/opik-openclaw`:

- `npm run typecheck` passed
- `npm test` passed: `98 passed, 1 skipped`

### Baseline verification

Using the OCM Nebius baseline harness with the local plugin tarball:

- test dir: [/tmp/opik-nebius-baseline-dbdcbcld](/tmp/opik-nebius-baseline-dbdcbcld)
- gateway log: [/tmp/opik-nebius-baseline-dbdcbcld/gateway.log](/tmp/opik-nebius-baseline-dbdcbcld/gateway.log)
- mock log: [/tmp/opik-nebius-baseline-dbdcbcld/mock.log](/tmp/opik-nebius-baseline-dbdcbcld/mock.log)

Observed result:

- `mock_status={"traces":1,"spans":1}`

Observed mock traffic:

- `POST /v1/private/traces/batch`
- `POST /v1/private/spans/batch`
- `PATCH /v1/private/traces/...`
- `PATCH /v1/private/spans/...`

This confirms traces are now firing end-to-end through the OpenClaw baseline harness.

## Important Caveat

The Nebius model request still failed with:

- `LLM request failed: network connection error.`

That is now separate from the Opik plugin issue.

The key point is that Opik traces still exported despite the model failure, which is exactly what we needed to prove about the plugin.

## Practical Conclusion

The earlier theory, “hooks are not firing,” was directionally correct but not complete.

The final diagnosis is:

- hooks needed to register earlier
- runtime state needed to be shared
- session correlation needed a fallback path

With those fixes, the plugin works in the OCM baseline harness.
