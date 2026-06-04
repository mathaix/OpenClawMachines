# Config Lifecycle Fix — Use OpenClaw Native Commands

## Summary

Replace the custom `assembleConfigForMachine` → `config.patch` pipeline with direct `openclaw config set/unset` calls via the agent exec endpoint. This makes UI-driven configuration follow the same path as terminal configuration, fixing stale key deletion and auth-profiles drift.

## Problem

Three issues flagged by Codex review on the native config mode implementation:

1. **Merge patch can't delete stale keys** — `pushNativeConfig` sends the full assembled config via `config.patch` (JSON merge patch). When a capability is removed, the key is absent from the patch but not explicitly nulled, so it persists in `openclaw.json`.

2. **auth-profiles.json not regenerated** — After linking/unlinking LLM credentials on a running machine, `pushMachineConfig` patches `openclaw.json` but `auth-profiles.json` (used by gateway ModelRegistry) stays at boot-time state. New provider models are undiscoverable until VM restart.

3. **Seed config missing capabilities** — Non-issue. At first boot, the user hasn't added capabilities yet. The seed config correctly includes only Nebius platform provider + gateway defaults + webchat. Users incrementally add capabilities after boot.

## Intended User Workflow

### First Boot (One-Click Deploy)
User gets a functional system out of the box:
- Nebius platform provider (token-budgeted, no API key needed)
- Webchat via gateway Control UI
- Default model (DeepSeek V3)
- Pre-seeded SOUL, IDENTITY, and MEMORY defaults (future enhancement)

### Incremental Configuration
From the dashboard or terminal, user adds capabilities:
- Link an Anthropic/OpenAI API key → provider added
- Enable Telegram channel → channel configured
- Enable a skill → skill added to allowBundled
- Change model → model updated

**The UI and terminal must produce identical results.** When the UI configures something, it should make the same `openclaw` calls the user would type in the terminal.

## Architecture: OpenClaw Native Commands

OpenClaw provides atomic config operations (documented in `docs/cli/config.md`):

```bash
# Get/set/unset individual config paths
openclaw config get agents.defaults.model.primary
openclaw config set channels.telegram.enabled true
openclaw config set channels.telegram.botToken "123:abc"
openclaw config unset channels.telegram              # removes key entirely
openclaw config validate

# Interactive wizards (terminal only)
openclaw onboard
openclaw configure --section model --section channels

# Non-interactive onboarding
openclaw onboard --non-interactive --auth-choice anthropic-api-key --anthropic-api-key "$KEY"
```

### Value Parsing

`openclaw config set` parses values as **JSON5 when possible**, otherwise treats them as strings. Complex objects like exec secret refs are auto-detected:

```bash
# Auto-parsed as JSON object (no flag needed):
openclaw config set models.providers.anthropic.apiKey '{"source":"exec","provider":"ocm","id":"anthropic-key"}'

# Use --strict-json to force JSON5 parsing for ambiguous values:
openclaw config set gateway.port 19001 --strict-json
```

Since the agent exec endpoint passes argv directly via `exec.CommandContext` (no shell interpretation), JSON strings are passed as single argv elements without escaping issues.

### Hot-Reload Behavior

The gateway watches `openclaw.json` for changes with a **300ms debounce** (`gateway.reload.debounceMs: 300`). Multiple rapid `openclaw config set` calls within the debounce window are coalesced into a single reload. Our seed config sets `reload.mode: "hot"`, which hot-applies safe changes (model, channels, skills) without a full restart.

## Critical Constraint: LLM Providers Must Use Proxy Pathway

**This is the most important architectural constraint in this design.**

OCM routes all LLM API calls through an API proxy (port 4000) running inside the VM. The proxy:
- Injects the real API key at runtime (keys never stored in config files)
- Enables usage tracking and budget enforcement
- Provides centralized credential management (keys stored encrypted in DB)

### How Provider Config Works in OCM

```
User adds Anthropic key in UI
  → Key stored encrypted in OCM database
  → ocm-secrets binary (exec secret provider) fetches key from metadata server at runtime
  → Config points provider baseUrl to proxy: http://192.168.100.1:4000/anthropic/v1
  → Config uses exec secret ref for apiKey: {"source":"exec","provider":"ocm","id":"anthropic-key"}
  → Gateway sends LLM requests to proxy → proxy injects real key → forwards upstream
```

### Precondition: Exec Secret Provider Block

The exec secret provider config (`secrets.providers.ocm`) must exist in `openclaw.json` for exec secret refs to work. This block is written by the seed config at boot:

```json
{
  "secrets": {
    "providers": {
      "ocm": {
        "source": "exec",
        "command": "/usr/local/bin/ocm-secrets",
        "allowInsecurePath": true
      }
    }
  }
}
```

If a user deletes this block, all exec secret refs will fail. The `configSet` helper should verify this block exists before setting exec-ref values, and restore it if missing.

### What Happens If User Adds Provider Via Terminal Instead

If a user runs `openclaw onboard --auth-choice anthropic-api-key` in the terminal:
- OpenClaw sets `apiKey: "sk-ant-..."` directly in `openclaw.json`
- LLM calls go directly to `api.anthropic.com`, **bypassing the proxy**
- No usage tracking, no budget enforcement, no centralized credential management
- Key is stored in plaintext in the config file on disk

**This is by design** — terminal users have full control of their VM. But UI-driven provider configuration MUST always go through the proxy pathway.

### Provider Config Commands (UI-Driven)

When the UI adds/removes a provider, the backend calls these via agent exec:

**Add a provider (e.g., Anthropic):**
```bash
openclaw config set models.providers.anthropic.baseUrl "http://192.168.100.1:4000/anthropic/v1"
openclaw config set models.providers.anthropic.apiKey '{"source":"exec","provider":"ocm","id":"anthropic-key"}' --strict-json
openclaw config set models.providers.anthropic.models '[]' --strict-json
```

Note: `baseUrl` includes `/v1` suffix because piSdk only appends the resource path (`/chat/completions`). This matches `AssembleSeedConfig` (not `AssembleConfig` which omits `/v1` — that was the fork-mode path).

**Remove a provider:**
```bash
openclaw config unset models.providers.anthropic
```

**Add Nebius (platform provider, always present in seed config):**
Already present from boot. No action needed unless user removed it.

## Config Operations: UI Action → openclaw Commands

### Providers (LLM)

| UI Action | Commands |
|-----------|----------|
| Link Anthropic key | `config set models.providers.anthropic.baseUrl "http://<proxy>/anthropic/v1"` + `config set models.providers.anthropic.apiKey '{"source":"exec","provider":"ocm","id":"anthropic-key"}' --strict-json` |
| Unlink Anthropic key | `config unset models.providers.anthropic` |
| Link OpenAI key | Same pattern with `openai` provider name and `openai-key` exec ID |

### Model

| UI Action | Commands |
|-----------|----------|
| Change default model | `config set agents.defaults.model.primary "<resolved-model-id>"` + `config set agents.defaults.models.<model-id> '{}' --strict-json` |

Both the model primary and the models catalog must be updated together. The gateway rejects any model not listed in `agents.defaults.models` as "Unknown model".

### Channels

| UI Action | Commands |
|-----------|----------|
| Enable Telegram | `config set channels.telegram.enabled true` + `config set channels.telegram.botToken '{"source":"exec","provider":"ocm","id":"telegram-token"}' --strict-json` |
| Disable Telegram | `config unset channels.telegram` |
| Enable Discord | `config set channels.discord.enabled true` + `config set channels.discord.token '{"source":"exec","provider":"ocm","id":"discord-token"}' --strict-json` |
| Disable Discord | `config unset channels.discord` |

### Identity

| UI Action | Commands |
|-----------|----------|
| Set name | `config set ui.assistant.name "MyBot"` |
| Set avatar | `config set ui.assistant.avatar "https://..."` |
| Clear identity | `config unset ui.assistant` |

### Skills

| UI Action | Commands |
|-----------|----------|
| Enable skill | `config set skills.allowBundled '["web-search","memory","..."]' --strict-json` (full array) |
| Disable skill | Same — rebuild array without the skill |

### Browser

| UI Action | Commands |
|-----------|----------|
| Enable browser | `config set browser.enabled true` + `config set browser.cdpUrl "http://<bridge-ip>:9222"` + `config set browser.attachOnly true` + `config set browser.headless true` + `config set browser.noSandbox true` |
| Disable browser | `config unset browser` |

### Plugins

| UI Action | Commands |
|-----------|----------|
| Enable plugin | `config set plugins.entries.<plugin-id>.enabled true` + `config set plugins.entries.<plugin-id>.config '<config-json>' --strict-json` |
| Disable plugin | `config unset plugins.entries.<plugin-id>` |

## Implementation Changes

### 1. Add `config` to allowedExecSubcommands

In `backend/cmd/agent/ptyserver.go`:
```go
var allowedExecSubcommands = map[string]bool{
    "pairing": true,
    "status":  true,
    "doctor":  true,
    "gateway": true,
    "config":  true,  // NEW: for openclaw config set/unset
}
```

### 2. Add `ConfigSet` / `ConfigUnset` helpers to agentclient

In `backend/internal/agentclient/client.go`, add helpers that call `openclaw config set <path> <value>` and `openclaw config unset <path>` via the existing `ExecCommand` method.

```go
func (c *Client) ConfigSet(ctx context.Context, host *store.Host, machineID, proxyToken, path, value string, strictJSON bool) error
func (c *Client) ConfigUnset(ctx context.Context, host *store.Host, machineID, proxyToken, path string) error
```

### 3. Add `ConfigBatch` helper for atomic multi-set operations

Multiple `config set` calls for a single logical operation (e.g., adding a provider = 3 calls) should be wrapped in a batch helper that:
- Executes all set/unset calls in sequence
- On failure partway through, logs a warning but does NOT attempt rollback (the gateway debounce means partial state is transient — the next successful call completes the operation)
- Returns all errors so the caller can decide how to handle

Partial state is acceptable because:
- The 300ms debounce means the gateway likely won't reload until all calls complete
- Individual `config set` calls are fast (<100ms each via exec)
- The user can retry from the UI, which will re-issue all commands

### 4. Replace `pushNativeConfig` with granular config commands

In `backend/internal/api/machine_config.go`:
- `pushNativeConfig` currently assembles the entire config and sends it as a single `config.patch`
- Replace with a function that issues the appropriate `openclaw config set/unset` commands for the specific change being made
- `handleSetMachineModel` also calls `pushNativeConfig` — update to use `ConfigSet` for model changes

### 5. Keep `assembleConfigForMachine` for preview/diff only

The config assembly pipeline is still useful for:
- **Preview**: showing the user what their config looks like before pushing
- **Diff computation**: comparing current vs desired state
- **Stored config**: saving assembled config to DB for audit/versioning

But it should NOT be the mechanism that writes to the gateway.

### 6. Seed config (`AssembleSeedConfig`) stays as-is

The boot-time seed config is correct — it provides the minimal working config (Nebius + gateway + webchat). It includes the `secrets.providers.ocm` block needed for exec secret refs. Capabilities are added incrementally via `openclaw config set` after boot.

### 7. Soul file writes remain separate

The current `pushMachineConfigInternal` writes SOUL.md files via `agentClient.WriteFile` before pushing config. This stays unchanged — soul files are written to disk directly, not via `openclaw config set`.

### 8. `allowedOrigins` stays in seed config

The seed config already sets `gateway.controlUi.allowedOrigins: ["*"]`. This persists across `config set/unset` calls since those calls only touch specific paths. No additional management needed.

## Migration: Existing Running Machines

Machines already running when this change deploys:
- They have `openclaw.json` written by the seed config at boot
- The `secrets.providers.ocm` block exists from the seed config
- First config push after this change will use `config set/unset` instead of `config.patch`
- No migration needed — `openclaw config set` works on any existing `openclaw.json`
- The `config.patch` RPC endpoint (`openclaw gateway call config.patch`) can be removed from the codebase after this change ships

## Concurrency

With `config.patch`, concurrency was controlled via `baseHash` (optimistic locking). With `openclaw config set`, there is no locking — it's last-write-wins at the key level. This is acceptable because:
- UI operations are user-initiated and serialized per machine
- Terminal operations are under user control
- Key-level granularity means concurrent writes to different keys don't conflict
- Same-key conflicts (e.g., UI and terminal both changing the model) result in last-write-wins, which is intuitive

## What This Fixes

| Issue | How It's Fixed |
|-------|---------------|
| Merge patch can't delete | `openclaw config unset` properly removes keys from config |
| auth-profiles.json stale | Gateway manages its own internal state on config change + hot-reload |
| First boot missing capabilities | Non-issue — user hasn't added capabilities yet |

## What This Does NOT Change

- **Seed config generation** (`AssembleSeedConfig`) — unchanged, used at boot
- **Config assembly** (`assembleConfigForMachine`) — kept for preview/diff, not for writing
- **Credential storage** — still encrypted in DB, fetched via `ocm-secrets` exec provider
- **Proxy architecture** — LLM calls still route through port 4000 proxy
- **Terminal access** — users can still run any `openclaw` command directly; if they add providers directly, they bypass the proxy (this is acceptable — power users choosing direct access)
- **Soul file writes** — still via `agentClient.WriteFile`, not config commands
- **Custom providers** — `CustomProviderConfig` in metadata is passed at VM creation for proxy routing; config-level custom provider support is a separate feature

## Known Risks

These are accepted risks that must be tested and simulated before shipping:

1. **`openclaw doctor --fix` may rewrite proxy config** — Doctor removes "unrecognized" keys and repairs config to OpenClaw defaults. This could strip the `secrets.providers.ocm` block, rewrite exec secret refs to direct API keys, or reset `dangerouslyDisableDeviceAuth`. The next UI-driven `config set` will restore the proxy pathway, but there's a transient disruption window. Accepted — test and simulate.

2. **Hot-reload coverage is unverified per config section** — The docs say `reload.mode: "hot"` applies "safe changes" but don't enumerate which sections are safe vs require restart. Provider and channel changes may require a full gateway restart. Must test empirically.

3. **`openclaw config unset` teardown behavior unknown** — When unsetting `channels.telegram`, does the gateway tear down the bot connection cleanly, or leave an orphaned polling loop? Same for providers — does unsetting remove from ModelRegistry immediately? Must test.

4. **Exec secret provider timing dependency** — When setting `apiKey: {"source":"exec",...}`, the gateway calls `ocm-secrets` → metadata server → needs credential loaded. If config is set before metadata has the credential, first LLM call fails. Same race exists in current `config.patch` approach. Must verify ordering.

5. **Loss of full config snapshot on push** — Currently `SetMachineAssembledConfig` stores a full snapshot to DB. With granular `config set`, no single snapshot exists. Options: fetch back via `openclaw config get` after push, or accept DB snapshot drift.

6. **OpenClaw version compatibility** — `config set/unset` behavior, JSON5 parsing, and path validation may change across OpenClaw versions. Tighter coupling to CLI contract than before.

7. **User runs `openclaw onboard` or `openclaw configure` in terminal** — These wizards write provider config with direct API keys, bypassing the proxy. Acceptable — power user choice. Next UI push restores proxy pathway for that provider.

## Testing

### Unit Tests
- `ConfigSet`/`ConfigUnset` agentclient helpers (mock exec)
- `ConfigBatch` partial-failure behavior

### Integration Tests
- Add provider via `openclaw config set`, verify gateway discovers models
- Remove provider via `openclaw config unset`, verify gateway removes it
- Enable/disable channel via `config set/unset`, verify gateway connects/disconnects
- Verify `openclaw config set` with `--strict-json` correctly parses exec secret ref objects
- Verify hot-reload debounce handles rapid sequential sets without transient errors

### Edge Case Simulations
- Run `openclaw doctor --fix` after UI config push → verify what gets rewritten, then re-push from UI and verify recovery
- Run `openclaw onboard --non-interactive --auth-choice anthropic-api-key` in terminal → verify provider bypasses proxy, then push from UI and verify proxy pathway restored
- Kill gateway mid-config-set sequence → verify partial state, then retry from UI
- Set provider config before metadata server has credential → verify error and recovery
- Change `models.providers` on running gateway → verify hot-reload applies without restart
- `config unset channels.telegram` on active Telegram connection → verify clean teardown
