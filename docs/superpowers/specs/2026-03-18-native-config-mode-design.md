# Native Config Mode — Eliminating the OpenClaw Fork

## Status: Draft

## Supersedes

This design supersedes the "Managed Appliance" decision recorded in `docs/configuration_architecture.md` (2026-03-15, Decision 1: Option A). That decision made the control plane the owner of extension lifecycle, safety policy, and agent management.

**Why the change:** The managed appliance model required maintaining a fork of OpenClaw with a custom ConfigSource abstraction (~1,200 lines of patches). Every upstream release requires cherry-picking, conflict resolution, and rebuild. The maintenance cost outweighs the benefit. OpenClaw's native exec secret provider and file-based config make the fork unnecessary. The new model — **managed runtime** — gives users the same security guarantees (API keys never enter the VM) while eliminating the fork entirely. The platform assists with installs but does not own the config.

## Problem

OCM maintains a fork of OpenClaw with ~1,200 lines of patches (ConfigSource strategy pattern, HTTP config source, metadata poller). Every upstream release requires cherry-picking these patches onto the new tag, resolving conflicts, rebuilding the fork tarball, and baking it into the rootfs. This is the single largest maintenance burden in the project.

The fork exists because OCM currently serves `openclaw.json` dynamically via an HTTP metadata endpoint, requiring a custom config loading strategy. But OpenClaw already has a native exec secret provider that can bridge to the metadata service, and the config can be written to disk at boot instead of served over HTTP.

## Solution

Introduce a global feature flag (`OCM_CONFIG_MODE=native`) that switches OCM from the fork-based config pipeline to a vanilla OpenClaw installation with:

1. A file-based `openclaw.json` seed written at boot
2. An `ocm-secrets` exec provider binary for credential resolution
3. LiteLLM proxy for API key security (unchanged)
4. Dashboard as convenience helper, VM as source of truth

When the flag is off (default: `OCM_CONFIG_MODE=fork`), the existing fork-based pipeline continues to work unchanged.

## Product Model Shift

This design shifts OCM from **managed appliance** to **managed runtime**:

| Aspect | Before (fork/managed appliance) | After (native/managed runtime) |
|--------|--------------------------------|-------------------------------|
| Config ownership | Platform owns, assembles, serves | Platform seeds, user owns after boot |
| Skills | Platform controls allowBundled filter | User installs/manages freely |
| Plugins | Platform controls entries | User installs/manages freely |
| Agents | Platform controls list | User configures freely |
| API keys | Platform injects via proxy (nonce) | Same — exec provider returns nonce |
| Dashboard role | Authoritative control plane | Convenience helper (runs commands via exec) |

The user can SSH in, use the terminal, or use the dashboard to install skills, plugins, and agents. The platform assists but does not control.

## Architecture

### Data Flow

```
Machine Start:
  Backend → init script writes openclaw.json seed to /home/openclaw/.openclaw/openclaw.json
  Backend → metadata service serves nonce tokens + proxy config

Runtime:
  OpenClaw starts → reads openclaw.json from disk (vanilla file-based config)
  OpenClaw needs API key → calls /usr/local/bin/ocm-secrets (exec provider)
  ocm-secrets → calls metadata service at 169.254.169.253
  metadata service → returns nonce token (not real key)
  OpenClaw → sends API request to LiteLLM proxy with nonce
  LiteLLM proxy → looks up real key, forwards to upstream provider

Dashboard assists:
  User clicks "install plugin" → Backend → exec endpoint → npm install in VM
  User clicks "install skill" → Backend → exec endpoint → clawhub install in VM
```

### Components

#### 1. `ocm-secrets` Binary

A small Go binary at `/usr/local/bin/ocm-secrets` that bridges OpenClaw's exec secret provider to the OCM metadata service.

**Location:** `backend/cmd/ocm-secrets/main.go`

**Protocol (OpenClaw exec provider v1):**

Input (stdin):
```json
{
  "protocolVersion": 1,
  "provider": "ocm",
  "ids": ["anthropic-key", "openai-key"]
}
```

Output (stdout):
```json
{
  "protocolVersion": 1,
  "values": {
    "anthropic-key": "nonce-abc123",
    "openai-key": "nonce-def456"
  }
}
```

Error output (stdout):
```json
{
  "protocolVersion": 1,
  "values": {},
  "errors": {
    "anthropic-key": { "message": "key not configured" }
  }
}
```

**Behavior:**
- Reads JSON request from stdin
- Reads the VM nonce from `/run/ocm-nonce` (written by the init script at boot, readable by all users)
- Returns the nonce as the value for every requested ID — the API proxy authenticates requests using the nonce and swaps in real keys upstream
- Exits with code 0 on success, non-zero on fatal errors (missing nonce file, empty nonce, invalid input, no IDs requested)
- No HTTP calls — the nonce is a local file read, making this fast and free of network dependencies

**Design decision (2026-03-19):** All provider keys resolve to the same nonce value. The proxy identifies the VM by `(source IP, nonce)` and looks up the real key per provider from `LLMKeys`. This eliminates the need for `ocm-secrets` to call the metadata service (which would require nonce-based auth — a chicken-and-egg problem). If non-proxy secrets are needed in the future (e.g., a plugin needing a real API key), `ocm-secrets` can be extended with a fallback to `/v1/secrets` for unrecognized IDs.

**Build:** Static Go binary (`CGO_ENABLED=0 GOOS=linux`), compiled alongside the agent binary. Injected into rootfs by `build-rootfs.sh`.

**Security:**
- Owned by `root:root`, mode `0755` (readable/executable by all, writable only by root)
- The `openclaw` user cannot modify this binary
- Only returns nonces, never real API keys
- Metadata service is reachable only from inside the VM (host-only network)

#### 2. Seed Config (`openclaw.json`)

Written to `/home/openclaw/.openclaw/openclaw.json` by the init script on **first boot only**. If the file already exists (persisted on `/data` volume), the seed write is skipped — user and dashboard customizations survive reboots. Contains sensible defaults that the user can modify freely after boot.

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
  },
  "gateway": {
    "mode": "local",
    "auth": { "mode": "token" },
    "controlUi": {
      "enabled": true,
      "allowInsecureAuth": true,
      "dangerouslyDisableDeviceAuth": true,
      "allowedOrigins": ["*"]
    },
    "reload": { "mode": "hot" },
    "nodes": {
      "denyCommands": [
        "camera.snap", "camera.clip", "screen.record",
        "calendar.add", "contacts.add", "reminders.add"
      ]
    }
  },
  "models": {
    "providers": {
      "anthropic": {
        "baseUrl": "http://192.168.100.1:4000/anthropic",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "anthropic-key" }
      },
      "openai": {
        "baseUrl": "http://192.168.100.1:4000/openai",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "openai-key" }
      },
      "google": {
        "baseUrl": "http://192.168.100.1:4000/google",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "google-key" }
      },
      "openrouter": {
        "baseUrl": "http://192.168.100.1:4000/openrouter",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "openrouter-key" }
      },
      "nebius": {
        "baseUrl": "http://192.168.100.1:4000/nebius",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "nebius-key" }
      }
    }
  },
  "agents": {
    "defaults": {
      "model": { "primary": "{{DEFAULT_MODEL}}" },
      "workspace": "/home/openclaw/.openclaw/workspace"
    }
  },
  "commands": {
    "native": "auto",
    "nativeSkills": "auto",
    "restart": true,
    "ownerDisplay": "raw"
  },
  "session": {
    "dmScope": "per-channel-peer"
  }
}
```

**Template variables** (substituted by init script at boot):
- `{{DEFAULT_MODEL}}` — the resolved model ID from machine config in metadata. The init script applies platform model mapping (e.g., `deepseek/deepseek-r1` → `deepseek-ai/DeepSeek-R1-0528`) before writing. The mapping table is passed via metadata so it stays in sync with the backend.

**Provider inclusion rules:**
- `nebius` is **always included** regardless of user key configuration — platform models route through it
- All other providers (`anthropic`, `openai`, `google`, `openrouter`) are only included if the user has configured a key for that provider
- The exec provider gracefully handles missing keys — if a user hasn't configured a provider but it's in the config, `ocm-secrets` returns an error for that ID and OpenClaw treats the provider as unconfigured

**Opik observability:** The seed config includes the `opik-openclaw` plugin entry with platform defaults (project: admin, workspace: openclawmachines). The user can disable or reconfigure it after boot. The `OPIK_API_KEY` env var is injected via the init script (same as today). **Note:** The `OpikAPIKey` must be passed in both the worker-mode and API-mode `RuntimeConfig` in `cmd/server/main.go` — the API-mode path was missing it initially (fixed 2026-03-19).

```json
  "plugins": {
    "entries": {
      "opik-openclaw": {
        "enabled": true,
        "config": {
          "enabled": true,
          "projectName": "admin",
          "workspaceName": "openclawmachines"
        }
      }
    }
  }
```

**Skills:** No `skills.allowBundled` filter is applied — all bundled skills are available by default. This is a deliberate change from fork mode, where the platform controlled which skills were enabled. In native mode, the user manages their own skill set.

**User ownership:** The file is written as `openclaw:openclaw` so the user (and OpenClaw itself) can modify it after boot. This is intentional — the user owns their config.

#### 3. Feature Flag

**Flag:** `OCM_CONFIG_MODE` environment variable on the backend (Cloud Run).

| Value | Behavior |
|-------|----------|
| `fork` (default) | Current pipeline: fork-based OpenClaw, HTTP config source, full config assembly |
| `native` | New pipeline: vanilla OpenClaw, file-based seed config, exec secret provider |

**Where it affects behavior:**

| Component | Fork mode | Native mode |
|-----------|-----------|-------------|
| Rootfs Dockerfile | Installs from `openclaw-fork.tgz` | Installs from npm (`openclaw@{version}`) |
| Init script | Sets `OCM_CONFIG_SOURCE=metadata` | Writes `openclaw.json` seed to disk |
| Config assembly | Full assembly (providers, skills, plugins, agents) | Minimal seed generation (gateway + exec provider + proxy URLs) |
| Metadata server | Serves full assembled config at `/v1/config` | Serves secrets at `/v1/secrets` (bulk) |
| Dashboard UI | Authoritative control (skills/plugins/agents tabs) | Convenience helpers (exec-based install commands) |

**Implementation:** The flag is read at:
1. **Build time** — Docker build arg selects fork vs vanilla OpenClaw installation
2. **Runtime** — Backend checks `OCM_CONFIG_MODE` to decide which config assembly path and metadata endpoints to use
3. **Init script** — Checks metadata for config mode to decide startup behavior

#### 4. Rootfs Changes

**Single rootfs image** with vanilla OpenClaw installed from npm. The fork tarball is no longer needed in native mode.

```dockerfile
ARG OPENCLAW_VERSION=2026.3.12
RUN pnpm install -g openclaw@${OPENCLAW_VERSION}
```

The de-hardlinking step is still needed (pnpm hardlinks, OpenClaw rejects `nlink>1`).

**New binary:** `ocm-secrets` is injected by `build-rootfs.sh` into `/usr/local/bin/ocm-secrets` (root-owned, mode 0755).

**Artifact pipeline:** Only one rootfs variant exists in GCS at a time. The `OCM_CONFIG_MODE` flag controls init script behavior, not rootfs content. The existing manifest system, agent self-update, and rollback mechanisms work unchanged — there is no need to track two rootfs variants.

**Note on fork mode coexistence:** During the transition (Phase 1-2), fork mode uses the existing rootfs already in GCS. Native mode uses the new rootfs. The switch happens by deploying the new rootfs and setting the flag. Rollback = deploy old rootfs + unset flag. Only one rootfs is active at a time.

#### 5. Init Script Changes

In native mode, the init script:

1. Fetches seed config data from metadata service (default model, configured providers, identity)
2. Applies platform model mapping (e.g., `deepseek/deepseek-r1` → Nebius model ID)
3. Writes `openclaw.json` to `/home/openclaw/.openclaw/openclaw.json` (owned by `openclaw:openclaw`)
4. Sets `OPENCLAW_GATEWAY_TOKEN` in the gateway env block (same mechanism as fork mode — the token comes from metadata, set in the `su -s` block)
5. Starts OpenClaw gateway normally (no `OCM_CONFIG_SOURCE` env var needed)

The gateway reads config from the standard file path — no HTTP polling, no sentinel file, no config source abstraction.

**Config watcher:** The existing config watcher (polls `/v1/config-version`, sends SIGUSR1) is **disabled** in native mode. Config changes are delivered via two mechanisms:

1. **User edits** — direct modification of `openclaw.json` via SSH/terminal. OpenClaw's built-in file watcher (`reload.mode: "hot"`) detects changes and hot-reloads.
2. **Dashboard config push** — the backend uses `config.patch` gateway RPC via the exec endpoint. This is a two-step process: (a) `openclaw gateway call config.get` to retrieve the current config hash, (b) `openclaw gateway call config.patch --params '{"raw": <json>, "baseHash": <hash>}'` to apply a JSON merge patch. Objects merge recursively, `null` deletes keys, arrays replace. The gateway validates, writes to disk, and restarts.

**Design decision (2026-03-19):** Dashboard config pushes use OpenClaw's native `config.patch` RPC rather than overwriting the file directly. This preserves user customizations in keys the platform didn't touch (JSON merge patch semantics), provides optimistic concurrency control via `baseHash`, and lets the gateway validate the config before applying. The backend detects `ConfigMode == "native"` and branches between the fork-mode metadata push and the native-mode exec-based patch.

**Credential rotation** does not require a config file change — `ocm-secrets` resolves fresh nonces from `/run/ocm-nonce` on each exec call, and nonces are rotated at the metadata service level.

**Auth profiles:** The existing `auth-profiles.json` mechanism (nonce-based credentials symlinked for the gateway) is **replaced** by the exec secret provider in native mode. The init script skips auth-profiles generation when `CONFIG_MODE=native`.

**Seed config write failure:** If the metadata service is unreachable or returns invalid data, the init script logs an error and exits non-zero. The gateway does not start without a valid seed config. The agent will report the VM as failed, same as any other init script failure. This is intentional — a missing seed config means the VM cannot function.

#### 6. Metadata Server Changes

No new endpoints needed. The `ocm-secrets` binary reads the nonce from a local file (`/run/ocm-nonce`), not from the metadata service.

The existing `/v1/config` endpoint serves the seed config JSON for first-boot initialization. The existing `/v1/secrets` endpoint continues to serve user-stored secrets (used by fork mode). The existing `/v1/providers` endpoint continues to serve LLM provider proxy URLs.

A new metadata field `config_mode` is added so the init script knows which startup path to take. The agent sets this based on the backend's `OCM_CONFIG_MODE` env var, passed through the provisioner → GCE instance metadata → agent → VM metadata chain.

#### 7. Dashboard UI Changes

In native mode, the Skills, Plugins, and Agents tabs become **convenience helpers** that use the existing `POST /api/accounts/{accountId}/machines/{machineId}/exec` endpoint (see `docs/CurrentFeature.md` for exec endpoint design). This endpoint proxies commands through the agent to the VM, with JWT auth + account ownership checks.

- **Read state:** Query installed skills/plugins/agents via exec endpoint (e.g., `clawhub list`, `openclaw plugins list`)
- **Install:** Trigger installation via exec endpoint (e.g., `clawhub install <slug>`, `npm install <package>`)
- **Uninstall:** Trigger removal via exec endpoint
- **Cannot override:** The VM is the source of truth. If the user installs something via SSH, the dashboard reflects it on next query.

The existing authoritative management UI (direct database writes for `machine_plugins`, `skill_catalog`) remains available in fork mode.

**ModelPicker and BYOK models:** The model picker dropdown shows all BYOK models (Anthropic, OpenAI, Google) but disables models whose provider keys are not configured for the machine. Disabled models display pricing information and an "Add API key" hint, so users are aware of billing costs before configuring a provider. The `configuredProviders` set is derived from the machine's linked credentials (`listMachineCredentials` API).

#### 8. Custom Registries

Users can point their OpenClaw instances at custom registries:

| Component | Registry | Configuration |
|-----------|----------|---------------|
| Skills | ClaWHub | `clawhub --registry <url>` flag or baked into rootfs config |
| Plugins | npm | `.npmrc` with `registry=<url>` or scoped `@scope:registry=<url>` |

**Platform-level defaults:** The rootfs can bake in default registry URLs via:
- `/home/openclaw/.clawhubrc` or equivalent config file for ClaWHub
- `/home/openclaw/.npmrc` for npm

**User override:** Users can change these after boot via SSH or terminal.

## Security Model

### API Key Protection

The security model is identical to today — real API keys never enter the VM:

```
Real key lifecycle:
  User adds key in dashboard
  → stored encrypted in Postgres (account_credentials)
  → injected into LiteLLM proxy config on machine start
  → proxy runs on host side (192.168.100.1:4000)
  → never crosses into VM

Nonce lifecycle:
  Machine starts → backend generates nonce
  → nonce stored in metadata service
  → ocm-secrets reads nonce from metadata
  → OpenClaw uses nonce as apiKey when calling proxy
  → proxy validates nonce, swaps for real key, forwards upstream
```

### Platform Binary Protection

| File | Owner | Mode | Purpose |
|------|-------|------|---------|
| `/usr/local/bin/ocm-secrets` | `root:root` | `0755` | Exec secret provider — cannot be modified by `openclaw` user |
| `/usr/local/bin/agent` | `root:root` | `0755` | Agent binary — cannot be modified by `openclaw` user |
| `/home/openclaw/.openclaw/openclaw.json` | `openclaw:openclaw` | `0644` | User-owned config — intentionally modifiable |

The `allowInsecurePath: true` flag in the exec provider config is required because OpenClaw runs as `openclaw` but the binary is owned by `root`. OpenClaw's `assertSecurePath` function (in `src/secrets/resolve.ts`) checks that the exec binary is owned by `process.getuid()` and is not world/group-writable. When `allowInsecurePath` is true, the ownership and permission checks are skipped (only the absolute-path check remains). This is safe because:
- The binary only returns nonces, never real keys
- The metadata service is only reachable from inside the VM
- Even a replaced binary could only intercept nonces, which are useless outside the proxy

**`allowedOrigins: ["*"]`:** The gateway's `controlUi.allowedOrigins` is set to `["*"]` because the auth proxy rewrites the Origin header to its internal address (e.g., `http://127.0.0.1:18789`), so explicit origin lists never match the real browser origin. OpenClaw v2026.3.8+ rejects unknown origins by default when `allowedOrigins` is absent. Security is enforced at the auth proxy layer (machine JWT validation via Cloudflare Access), not at the gateway origin check.

## What Gets Eliminated

When native mode is validated and fork mode is removed:

| Component | Lines | Status |
|-----------|-------|--------|
| OpenClaw fork patches (ConfigSource, HttpSource, MetadataSource) | ~1,200 | Deleted |
| Fork build pipeline (`update-openclaw.sh`, `build-openclaw-fork` target) | ~200 | Deleted |
| Fork repo maintenance (cherry-picks per release) | Ongoing | Eliminated |
| Full config assembly (providers, skills, plugins, agents rendering) | ~400 | Simplified to seed generation |
| HTTP config source init script logic (`OCM_CONFIG_SOURCE=metadata`) | ~50 | Replaced with file write |

**What remains:**
- Seed config generation (gateway defaults, exec provider, proxy URLs)
- LiteLLM proxy (unchanged)
- VM lifecycle (create, start, stop, destroy, backups)
- Exec endpoint (used by dashboard helpers)
- Account/billing/auth
- Cloudflare tunnel provisioning

## Migration Path

1. **Phase 1:** Implement native mode behind `OCM_CONFIG_MODE=native` flag. Both rootfs variants coexist. Test with a single machine.
2. **Phase 2:** Validate native mode works end-to-end. Migrate remaining machines.
3. **Phase 3:** Remove fork mode code, fork repo, and fork build pipeline. `native` becomes the only mode.

## Testing Strategy

- **Unit tests:** `ocm-secrets` protocol handling (valid input, missing IDs, malformed JSON, metadata service errors)
- **Config seed tests:** Verify seed JSON is valid OpenClaw config for various provider combinations
- **Gateway E2E tests:** Verify exec provider resolves secrets correctly, proxy routing works
- **Integration tests:** Full boot → OpenClaw starts → exec provider → proxy → upstream API call
- **Regression:** Fork mode continues to pass all existing tests unchanged
