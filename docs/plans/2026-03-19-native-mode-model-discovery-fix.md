# Native Mode: "Unknown Model" — Root Cause Analysis & Fix Plan

**Date:** 2026-03-19
**Branch:** `platform-models`
**Symptom:** `Agent failed before reply: Unknown model: deepseek-ai/DeepSeek-V3-0324`

## Background

After deploying native config mode and starting a machine, the gateway boots successfully and logs `[gateway] agent model: deepseek-ai/DeepSeek-V3-0324` — but rejects chat messages with "Unknown model." The model is set correctly in the seed config, and the proxy routes work in E2E tests.

## Root Cause

Three independent systems must agree for a model to work in the gateway. Native mode was missing two of them.

### 1. `auth-profiles.json` — Model Discovery (MISSING)

The gateway's ModelRegistry discovers available models from `auth-profiles.json`. This file maps provider names to auth credentials:

```json
{
  "version": 1,
  "profiles": {
    "nebius-proxy": { "type": "api_key", "provider": "nebius", "key": "<nonce>" },
    "anthropic-proxy": { "type": "api_key", "provider": "anthropic", "key": "<nonce>" }
  }
}
```

**What went wrong:** Fork mode writes this file during `start_gateway()` in `init-openclaw.sh` (line ~887). Native mode returns early at line 837 (`return # skip fork-mode path below`), so the `auth-profiles.json` generation code is never reached. Without this file, the ModelRegistry discovers zero models, and every model is rejected.

**Location:** `scripts/init-openclaw.sh`, lines 782-837 (native mode block) vs lines 887-911 (auth-profiles generation in fork mode only)

### 2. `agents.defaults.models` — Model Catalog (MISSING)

The gateway maintains a model catalog at `agents.defaults.models` in `openclaw.json`. Any model not in this map is rejected as "Unknown model."

```json
{
  "agents": {
    "defaults": {
      "model": { "primary": "deepseek-ai/DeepSeek-V3-0324" },
      "models": {
        "deepseek-ai/DeepSeek-V3-0324": {},
        "deepseek-ai/DeepSeek-R1-0528": {},
        "openai/gpt-oss-20b": {}
      }
    }
  }
}
```

**What went wrong:** Fork mode's `AssembleConfig()` populates `agents.defaults.models` (line 395 in `assembler.go`). `AssembleSeedConfig()` was written later and set `agents.defaults.model.primary` (which model to use) but not `agents.defaults.models` (which models exist). The gateway knew what model to use but didn't consider it valid.

**Location:** `backend/internal/configassembly/assembler.go`, `AssembleSeedConfig()` — line 633 set `model.primary` but `models` map was absent

### 3. `models.providers.<name>` — Provider Routing (OK)

Provider configs with `baseUrl` and `apiKey` (exec secret ref) were correctly generated. This wasn't a factor.

### 4. `models.providers.nebius.models` — Provider Model List (MISSING)

The gateway has no built-in nebius provider. Built-in providers (anthropic, openai, google) have hardcoded model lists in the gateway's JavaScript bundle — even with an empty `models: []` array, the gateway discovers their models automatically. For unknown providers like nebius, the `models` array IS the model list. An empty array means zero discoverable models.

**What went wrong:** Both `AssembleConfig()` (fork mode) and `AssembleSeedConfig()` (native mode) set `"models": []interface{}{}` for all providers, including nebius. This works for built-in providers but not for nebius.

**Additionally:** The gateway needs `api: "openai-completions"` on the provider config to know the wire protocol. Nebius uses OpenAI-compatible API format (same as Qianfan, ModelStudio, and other non-built-in providers in the gateway codebase).

**Location:** `backend/internal/configassembly/assembler.go`, lines 605-612 (seed config) and 377-382 (fork config)

## Why This Was Hard to Debug

1. **Model ID mapping obscures the trail.** The user-facing ID `deepseek/deepseek-v3` is mapped to `deepseek-ai/DeepSeek-V3-0324` by `platformModelMap` in `assembler.go`. The error shows the mapped ID, which appears nowhere in the init script or config assembly logic — only in the map definition.

2. **The gateway logs the model as if it's configured.** `[gateway] agent model: deepseek-ai/DeepSeek-V3-0324` appears at startup, suggesting the model is recognized. The "Unknown model" error only fires when a chat message actually tries to use it.

3. **E2E tests passed — but tested the wrong layer.** See "Why E2E Tests Didn't Catch This" below.

4. **Fork mode works fine.** The auth-profiles and model catalog are correctly generated in fork mode, so the issue only manifests when testing native mode end-to-end in a real VM.

## Why E2E Tests Didn't Catch This

The E2E test harness (`setupTestEnv()` in `gateway_test.go`) correctly writes `auth-profiles.json` — it even has a comment explaining why:

```go
// Write auth-profiles.json so the gateway discovers available providers
// and populates its model catalog. Mirrors init-openclaw.sh:535-547.
// The gateway reads this at startup via ensurePiAuthJsonFromAuthProfiles(),
// converts it to auth.json, and feeds it to ModelRegistry. Without this
// file, models.list returns an empty catalog.
```

The test author knew `auth-profiles.json` was required and wrote it manually in Go code. But the native-mode init script was never updated to do the same. The tests bypass the shell script entirely.

### Coverage gap by layer

| Layer | auth-profiles.json | agents.defaults.models | Catches "Unknown model"? |
|-------|-------------------|----------------------|--------------------------|
| E2E test harness (`setupTestEnv`) | Written manually in Go (line 192) | Inherited from fork-mode `AssembleConfig` | Yes — but only for fork-mode providers |
| Fork-mode init script | Written at line 887 | Set by `AssembleConfig` (line 395) | Yes |
| Native-mode init script | **Skipped** (early return at line 837) | **Missing** from `AssembleSeedConfig` | **No** |

### Why each test type missed it

- **`TestNativeMode_ProxyAnthropicApiKey`** — validates the nonce-to-real-key swap at the HTTP proxy layer. A chat message goes through: gateway model check → proxy → upstream. This test hits the proxy directly, skipping the gateway model check entirely.

- **`TestNativeMode_SeedConfigMetadataRoundTrip`** — validates that the seed config JSON structure survives metadata storage and retrieval. It checks exec provider refs and config_mode, but never starts a gateway with the seed config or sends a chat message.

- **`TestGatewayE2E_ChatSend`** — the only test that goes through the gateway's model validation via WebSocket `chat.send`. But it runs against a fork-mode config with Anthropic models (using `auth-profiles.json` written by `setupTestEnv`), never a native-mode config with DeepSeek models.

- **`TestGatewayE2E_ModelsCatalog`** — verifies the gateway's `/models` endpoint returns models. But the gateway was started with fork-mode auth-profiles (Anthropic/OpenAI), so the model catalog is populated. A native-mode test would have shown an empty catalog.

### What would have caught it

1. **A gateway-level native-mode test:** Boot the gateway with a native-mode seed config (not just proxy-level), write `auth-profiles.json` with nebius provider entries, and send a `chat.send` WebSocket message using `deepseek-ai/DeepSeek-V3-0324`. This tests the full model resolution chain.

2. **An init script integration test (`make test-integration`):** Boot a real Firecracker VM in native mode and verify chat works. This is the only test tier that exercises the actual init script. However, it's expensive (~35 min) and wasn't run for this change.

3. **A startup assertion in the init script:** After the gateway reports ready, verify `curl localhost:18789/gateway/api/models` returns a non-empty model catalog before declaring the VM ready. This would have surfaced the issue at boot time instead of at first chat.

## Fix Status

### Fix A: `agents.defaults.models` in seed config — DEPLOYED

**Commit:** `f9aca2b` (deployed to Cloud Run)
**File:** `backend/internal/configassembly/assembler.go`

Added `modelsCatalog` map to `AssembleSeedConfig()` that registers the default model plus all platform tier models. New VMs get the updated seed config from the backend.

### Fix B: `auth-profiles.json` in native mode init — COMMITTED, NEEDS ROOTFS REBUILD

**Commit:** `d3e1162` (pushed to `platform-models`, not yet in rootfs)
**File:** `scripts/init-openclaw.sh`

Added auth-profiles.json generation to the native mode block, before the gateway starts. Fetches providers from `/v1/providers` metadata endpoint and writes the same profile format as fork mode, using the nonce as the API key.

```bash
# Added to native mode block (before "return" on line 837):
prov_json=$(curl -sf "${NONCE_ARGS[@]}" "$METADATA_URL/v1/providers")
for pname in $(echo "$prov_json" | jq -r '.llm // {} | keys[]'); do
    profiles=$(echo "$profiles" | jq --arg id "${pname}-proxy" --arg prov "$pname" --arg key "$METADATA_NONCE" \
        '.[$id] = {"type":"api_key","provider":$prov,"key":$key}')
done
echo "{\"version\":1,\"profiles\":${profiles}}" | jq . > "$auth_file"
```

### Fix C: Nebius provider model entries — COMMITTED, NEEDS DEPLOY + ROOTFS REBUILD

**File:** `backend/internal/configassembly/assembler.go`

The gateway has no built-in nebius provider (unlike anthropic/openai/google). Built-in providers have hardcoded model lists in the gateway code; the gateway discovers zero models for unknown providers even with a valid `auth-profiles.json` entry. The `models.providers.nebius.models` array was empty (`[]`), so the gateway knew nebius existed but associated no models with it.

**Fix:** Added `buildNebiusModelsList()` that populates the nebius provider's `models` array with explicit model entries including `api: "openai-completions"` (Nebius uses OpenAI-compatible API format). Applied to both `AssembleConfig()` (fork mode) and `AssembleSeedConfig()` (native mode).

```go
var nebiusModelDefs = []struct {
    ID        string
    Name      string
    Reasoning bool
}{
    {"deepseek-ai/DeepSeek-R1-0528", "DeepSeek R1", true},
    {"deepseek-ai/DeepSeek-V3-0324", "DeepSeek V3", false},
    {"openai/gpt-oss-20b", "GPT OSS 20B", false},
}

// Each model entry includes: id, name, api: "openai-completions",
// reasoning, input: ["text"], contextWindow: 131072, maxTokens: 8192
```

**Why `api: "openai-completions"` is required:** The gateway uses this field to determine how to format API requests. Without it, the gateway doesn't know the wire protocol for nebius. Other non-built-in providers in the gateway (Qianfan, ModelStudio) use the same pattern.

**Why `name` is required:** The OpenClaw config schema (`openclaw.json`) validates that each model entry has a `name` field. Without it, schema validation fails.

## Remaining Steps

1. `make deploy-backend` — deploy the updated seed config with nebius model entries
2. `make build-upload-rootfs` — rebuild rootfs with the init script fix
3. Wait for agent self-update (~5 min) + restart
4. Stop existing machine, delete `/home/openclaw/.openclaw/openclaw.json` from `/data` volume (or create new machine)
5. Start machine — verify gateway boots without "Unknown model"
6. Send a chat message — verify DeepSeek V3 responds

## Codex Review Findings (2026-03-19)

### IMPORTANT

1. **Native-mode gateway E2E test not feasible in current harness.** The seed config hardcodes an exec secret provider at `/usr/local/bin/ocm-secrets` which requires `/run/ocm-nonce`. The E2E harness doesn't provision either. A true native-mode gateway test requires `make test-integration` (real Firecracker VM) or a mock `ocm-secrets` binary — significant harness work.

2. **Startup guard route unverified.** The suggested `curl localhost:18789/gateway/api/models` may not exist as an HTTP endpoint — the repo only tests model enumeration via WebSocket `models.list`. Even if it exists, "non-empty catalog" doesn't prove the configured default model is present and usable. A stronger guard would check that `agents.defaults.model.primary` is in the catalog.

3. **Runtime credential additions won't take effect.** `auth-profiles.json` is only written at `start_gateway()` time. Native mode skips the fork-mode config watcher (`scripts/init-openclaw.sh:1031`). If a user adds a BYOK provider key on a running VM, the gateway won't discover it until a manual restart. The doc should document this restart requirement.

### MINOR

4. **Model catalog diverges from fork mode intentionally.** Native mode registers all platform tiers in `agents.defaults.models`; fork mode only registers the single default model. This is intentional (native users should be able to switch tiers) but should be called out explicitly.

5. **`openai/gpt-oss-20b` mapping is provisional.** `platformModelMap` has a TODO on this entry. The doc claims "all platform tier models" but one mapping is still unverified.

## Structural Takeaway

The E2E test harness and the init script are two independent implementations of the same gateway setup logic, and they drifted. The test author understood the gateway's requirements and implemented them correctly in Go — but that knowledge was never encoded as a shared contract. When native mode was added to the init script, it re-derived the requirements from scratch and missed two of three.

The early-return pattern in `start_gateway()` means native mode inherits nothing from fork mode. Every new gateway prerequisite added to fork mode silently doesn't apply to native mode. The E2E tests can't catch this because they bypass the init script entirely.

## Prevention

1. **Extract shared startup steps** (auth-profiles generation, symlinks, env setup) into shell helper functions called by both native and fork paths. This eliminates the early-return inheritance gap.
2. **Accept integration tests as the native-mode validation tier.** The E2E harness can't fake the exec secret provider without significant work. `make test-integration` (real Firecracker VM) is the only test tier that exercises the actual init script.
3. **Document the restart requirement** for runtime credential changes in native mode until a config watcher equivalent is built.
