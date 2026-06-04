# Config Simplification: Eliminate Metadata Config Round-Trips

## Status: Design Decided — Revised after Codex Review

## Design Principles

1. **The file on disk (`openclaw.json`) is the source of truth after first boot.**
2. **All boot-time data the agent already has should be written to the data volume pre-boot, not fetched via HTTP from inside the VM.**
3. **Bridge IP is a constant (`192.168.100.1`)** — all hosts use the same default. Proxy URLs are portable across hosts.
4. **Config changes only happen on running machines** via `openclaw config set/unset` through exec. Config UI is disabled when stopped. No changes while stopped = no stale config problem.
5. **Platform secrets use OpenClaw's native SecretRef system** — resolved via `ocm-secrets` exec provider, not env vars.
6. **The nonce is generated once at first boot and persisted on the data volume.** It is never regenerated — it's only a lookup token for the LLM API proxy on the bridge network and has no security value outside the VM.

## Problem

Currently, the init script makes multiple HTTP round-trips to the metadata service to fetch data the agent already has:

```
agent has config, providers, souls
  → registers everything in metadata server
  → boots VM
  → init script curls /v1/config     → writes openclaw.json
  → init script curls /v1/providers  → writes auth-profiles.json
  → init script curls /v1/souls      → writes SOUL.md files
  → init script curls /v1/secrets    → sets OPIK_API_KEY env var
```

These round-trips are unnecessary for config, providers, and souls. For platform secrets (like `OPIK_API_KEY`), the env var approach bypasses OpenClaw's native secret management. The gateway has a built-in SecretRef system (`exec`, `file`, `env` providers) that resolves secrets at activation time — we should use it.

## Proposed Change

### Config, Providers, Souls: Write to Data Volume Pre-Boot

```
agent has config, providers, souls
  → mounts data volume on host
  → writes openclaw.json, auth-profiles.json, soul files
  → unmounts
  → boots VM — gateway reads files from disk
```

### Platform Secrets: Use OpenClaw's Native SecretRef via `ocm-secrets`

Instead of injecting `OPIK_API_KEY` as an env var, use OpenClaw's SecretRef system:

**In `openclaw.json`:**
```json5
{
  plugins: {
    entries: {
      "opik-openclaw": {
        enabled: true,
        config: {
          enabled: true,
          apiKey: {
            source: "exec",
            provider: "ocm",
            id: "OPIK_API_KEY"
          },
          projectName: "admin",
          workspaceName: "openclawmachines",
          tags: ["openclaw", "machine-id-xyz"]
        }
      }
    }
  }
}
```

**Resolution flow:**
```
Gateway startup
  → resolves all SecretRefs in config (activation phase)
  → finds apiKey: { source: "exec", provider: "ocm", id: "OPIK_API_KEY" }
  → runs /usr/local/bin/ocm-secrets with id: "OPIK_API_KEY"
  → ocm-secrets distinguishes:
      - LLM proxy keys (anthropic-key, openai-key, etc.) → returns nonce
      - Platform secrets (OPIK_API_KEY) → fetches from metadata /v1/secrets
  → gateway receives resolved plain string
  → plugin gets the API key, never knows it was a SecretRef
```

**On hot reload** (e.g., key rotation): gateway re-resolves SecretRefs, `ocm-secrets` fetches fresh value from `/v1/secrets`. No restart needed.

### Files Written to Data Volume Pre-Boot (First Boot Only)

| File | Source | Path on data volume |
|------|--------|-------------------|
| `openclaw.json` | Full assembled config (with SecretRefs, BridgeIP baked in) | `/data/home/openclaw/.openclaw/openclaw.json` |
| `auth-profiles.json` | Derived from provider names + nonce | `/data/home/openclaw/.openclaw/config-current/auth-profiles.json` |
| Soul files (SOUL.md) | `cfg.Souls` entries | `/data/home/openclaw/.openclaw/workspace/SOUL.md` (per agent) |

### What Changes

1. **Orchestrator** (`backend/internal/orchestrator/firecracker_linux.go`):
   - After `ensureDataVolume()`, mount the ext4 image on the host
   - On first boot (files don't exist): write `openclaw.json`, `auth-profiles.json`, soul files
   - On subsequent boots: skip (files already on data volume from previous run)
   - Unmount before booting the VM
   - Nonce is generated once at first boot and persisted (never regenerated)

2. **Init script** (`scripts/init-openclaw.sh`):
   - Remove `write_seed_config()` — file already on disk
   - Remove `write_auth_profiles()` call at boot — file already on disk
   - Remove soul files fetch (`curl /v1/souls`) — files already on disk
   - Remove `curl /v1/providers` — no longer needed
   - Remove `curl /v1/config` — no longer needed
   - Remove `OPIK_API_KEY` env var injection — handled by SecretRef
   - Keep: metadata fetch for `/v1/machine` (identity), SSH keys
   - Update `/etc/profile.d/openclaw-providers.sh` to derive provider info from `openclaw.json` on disk instead of curling `/v1/providers`

3. **`ocm-secrets`** (`backend/cmd/ocm-secrets/main.go`) — **Implemented**:
   - Proxy key IDs: `anthropic-key`, `openai-key`, `nebius-key`, `google-key`, `openrouter-key` → return nonce (no auth required)
   - All other IDs (platform secrets) → requires `OCM_SECRETS_AUTH` env var matching nonce
     - Authenticated: HTTP GET to `http://{gateway_ip}/v1/secrets`, extract the requested key
     - Not authenticated: returns error in exec protocol response (access denied)
   - Gateway IP from `/run/ocm-gateway-ip` (fallback: `192.168.100.1`), nonce header for auth
   - Errors (network, missing key, auth failure) → error in exec protocol response, gateway fails fast

4. **Config assembly** (`backend/internal/configassembly/assembler.go`) — **Implemented**:
   - Opik plugin uses SecretRef: `{ source: "exec", provider: "ocm", id: "OPIK_API_KEY" }`
   - Channel credentials use SecretRef: `{ source: "exec", provider: "ocm", id: "channel-{provider}-{fieldName}" }`
   - `BridgeIP` must be set in `AssemblyParams` so `cdpUrl` for browser-enabled machines is baked into the assembled config (not rewritten at serve time)

5. **`assembleConfigForMachine`** (`backend/internal/api/machine_config.go`) — **Implemented**:
   - Splits credentials into LLM and channel categories using `llmProviderSet`
   - Populates `ChannelCredentials` in `AssemblyParams` for SecretRef injection

6. **Config ops** (`backend/internal/api/config_ops.go`) — **Implemented**:
   - `skills.allowBundled` and `agents.list` use `diffObjectSection` for correct set/unset handling

7. **Helper scripts** (cleanup required):
   - Update `scripts/ocm-metadata` — remove `/v1/instance`, `/v1/config-version` usage; derive from on-disk files or `/v1/machine`
   - Update `scripts/ocm-test-llm` — derive provider info from `openclaw.json` on disk instead of `/v1/providers`
   - Update `/etc/profile.d/openclaw-providers.sh` — read from disk instead of curling metadata

8. **Metadata service endpoints**:

   | Endpoint | Action |
   |----------|--------|
   | `/v1/config` | Remove — replaced by direct file write |
   | `/v1/config-version` | Remove — no runtime polling in stock OpenClaw |
   | `/v1/providers` | Remove — derivable from openclaw.json on disk |
   | `/v1/llm` | Remove — subset of providers |
   | `/v1/instance` | Remove — legacy, duplicated by /v1/machine |
   | `/v1/souls` | Remove — written to data volume pre-boot |
   | `/health` | **Keep** — readiness probe |
   | `/v1/machine` | **Keep** — VM identity (machine_id, gateway_token, tunnel_token) |
   | `/v1/secrets` | **Keep** — platform secrets, accessed via `ocm-secrets` exec provider |
   | `/v1/logs` | **Keep** — log forwarding from VM to host |
   | `/v1/admin/*` | **Keep** — control plane proxy |
   | `/v1/ssh-principals` | **Keep** — SSH cert auth |
   | `/v1/cf-ca-pubkey` | **Keep** — Cloudflare CA key |

9. **Frontend** (config UI):
   - Config controls disabled when machine is stopped
   - Config changes only allowed on running machines

### What Stays the Same

- Live config pushes: `openclaw config set/unset` via agent exec endpoint (on running machines only)
- Live soul updates: written via exec before config reload
- Metadata service: still runs for identity, secrets, logs, admin proxy, SSH
- Data volume: ext4 image at `{DataDir}/{machineID}.ext4`, mounted at `/data` inside VM
- Gateway: reads `openclaw.json` from disk (stock OpenClaw behavior)
- API proxy: still on `bridge_ip:4000`, still uses metadata for credential lookup

## Config Lifecycle

```
┌─────────────────────────────────────────────────────────┐
│                    MACHINE CREATED                       │
│                                                         │
│  Backend assembles full config (assembleConfigForMachine)│
│  Stores in DB (used as seed for first boot only)        │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│                    FIRST START                           │
│                                                         │
│  1. ensureDataVolume() creates ext4 image               │
│  2. Generate nonce (persisted on data volume)           │
│  3. Mount ext4 on host                                  │
│  4. Write openclaw.json (with SecretRefs, BridgeIP)     │
│     Write auth-profiles.json (provider names + nonce)   │
│     Write soul files                                    │
│  5. Unmount                                             │
│  6. Boot VM                                             │
│  7. Gateway resolves SecretRefs via ocm-secrets          │
│     - LLM keys → nonce (proxy handles real keys)        │
│     - Platform secrets → ocm-secrets fetches /v1/secrets │
│  8. Gateway ready                                       │
│  9. OnRunning callback pushes full config via            │
│     openclaw config set/unset (capabilities, plugins,   │
│     identity, agents — everything from DB)              │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│                    RUNNING                               │
│                                                         │
│  Config UI enabled                                      │
│  User changes model/capabilities/plugins/etc.           │
│     → Backend diffs old vs new config                   │
│     → Sends openclaw config set/unset ops via exec      │
│     → Gateway hot-reloads from file watcher             │
│     → SecretRefs re-resolved on reload (fresh secrets)  │
│     → Disk file is updated by gateway (source of truth) │
│                                                         │
│  User updates agent personality                         │
│     → Backend writes soul files via exec                │
│     → Then pushes config reload                         │
│                                                         │
│  User can also edit files manually in VM                │
│     → Their edits are preserved                         │
└──────────────────────┬──────────────────────────────────┘
                       │
                       ▼
┌─────────────────────────────────────────────────────────┐
│                    STOPPED                               │
│                                                         │
│  Config UI DISABLED — no changes possible               │
│  Data volume persists with all files as-is              │
│  On next start: VM boots with existing files on disk    │
│  (no rewrite, no metadata fetch for config/souls)       │
│  Nonce persists — auth-profiles.json still valid        │
└─────────────────────────────────────────────────────────┘
```

## Secret Resolution Architecture

```
┌─────────────────────────────── VM ────────────────────────────────┐
│                                                                    │
│  openclaw.json                                                     │
│  ├── models.providers.anthropic.apiKey:                            │
│  │     { source: "exec", provider: "ocm", id: "anthropic-key" }  │
│  ├── models.providers.openai.apiKey:                               │
│  │     { source: "exec", provider: "ocm", id: "openai-key" }     │
│  ├── models.providers.openrouter.apiKey:                           │
│  │     { source: "exec", provider: "ocm", id: "openrouter-key" } │
│  ├── channels.telegram.botToken:                                   │
│  │     { source: "exec", provider: "ocm", id: "channel-telegram-botToken" } │
│  └── plugins.entries.opik-openclaw.config.apiKey:                  │
│        { source: "exec", provider: "ocm", id: "OPIK_API_KEY" }   │
│                                                                    │
│  Gateway resolves SecretRefs at activation                         │
│       │                                                            │
│       ▼                                                            │
│  /usr/local/bin/ocm-secrets                                        │
│       │                                                            │
│       ├── id = "anthropic-key"  → return nonce (proxy key)        │
│       ├── id = "openai-key"     → return nonce (proxy key)        │
│       ├── id = "openrouter-key" → return nonce (proxy key)        │
│       └── id = "OPIK_API_KEY"   → fetch from metadata /v1/secrets │
│       └── id = "channel-telegram-botToken" → fetch from /v1/secrets│
│                                         │                          │
└─────────────────────────────────────────│──────────────────────────┘
                                          │
                                          ▼
┌─────────────────────── Host (Agent) ───────────────────────┐
│                                                             │
│  Metadata server (bridge_ip:80)                             │
│  └── /v1/secrets → { "OPIK_API_KEY": "real-key",           │
│                       "channel-telegram-botToken": "tok" }  │
│                                                             │
│  API proxy (bridge_ip:4000)                                 │
│  └── nonce → swaps for real LLM API key → upstream         │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## `ocm-secrets` (Implemented)

### Behavior
- Proxy key IDs: `anthropic-key`, `openai-key`, `nebius-key`, `google-key`, `openrouter-key` → return nonce (no auth required)
- All other IDs (platform secrets) → requires `OCM_SECRETS_AUTH` env var matching nonce
  - Authenticated: HTTP GET to `http://{gateway_ip}/v1/secrets`, extract the requested key
  - Not authenticated: returns error in exec protocol response (access denied)
- Gateway IP from `/run/ocm-gateway-ip` (fallback: `192.168.100.1`), nonce header for auth
- Errors (network, missing key, auth failure) → error in exec protocol response, gateway fails fast

### Security Model

The gateway process has `OCM_SECRETS_AUTH` set in its env block (init script). When it spawns
`ocm-secrets` as a child process via the exec provider, the child inherits this env var.
A prompt-injected agent calling `ocm-secrets` directly won't have this env var → access denied.

```
Gateway process (has OCM_SECRETS_AUTH=<nonce>)
  └── spawns ocm-secrets → child inherits env → auth succeeds → returns secret

Prompt-injected agent (no OCM_SECRETS_AUTH)
  └── calls ocm-secrets directly → auth fails → access denied
```

**Accepted risk:** The nonce is world-readable at `/run/ocm-nonce`. A determined in-VM attacker
could read it and set `OCM_SECRETS_AUTH`. This is accepted because:
- The VM is a single-user trust domain
- The env-var gate raises the bar from "trivially callable" to "requires deliberate multi-step exploitation"
- Hardening (file permissions, separate user) is a future improvement

### Test Coverage (33 tests)

| Category | Count | Tests |
|----------|-------|-------|
| Proxy key backward compat | 3 | All known IDs return nonce, no auth needed |
| Auth enforcement (security) | 5 | No auth, wrong auth, partial match, whitespace, multiple IDs |
| Platform secret resolution | 5 | Valid auth, multiple keys, missing key, fetch error, nonce passthrough |
| Mixed requests | 4 | Proxy succeeds when platform denied/fails, partial success |
| HTTP fetcher integration | 4 | httptest server: success, non-200, bad JSON, connection refused |
| E2E + edge cases | 5 | Full flow, unauthorized caller, case sensitivity, empty metadata |
| Input validation | 5 | Invalid JSON, empty IDs, missing/empty/whitespace nonce |
| Protocol compliance | 3 | Version field, errors omitted on success, present on denial |

## Channel Secrets via SecretRef (Implemented)

Channel bot tokens now use the same SecretRef pattern as LLM provider keys:

**In `openclaw.json` (assembled by `configassembly`):**
```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "botToken": {
        "source": "exec",
        "provider": "ocm",
        "id": "channel-telegram-botToken"
      }
    }
  }
}
```

**Resolution flow:**
1. Config assembly emits SecretRef when `ChannelCredentials` has a matching provider
2. Metadata `/v1/secrets` endpoint merges platform secrets + channel tokens
   - Channel token IDs: `channel-{provider}-{fieldName}` (e.g., `channel-telegram-botToken`)
3. Gateway resolves SecretRef via `ocm-secrets` (exec provider, auth-gated)
4. `ocm-secrets` fetches from metadata `/v1/secrets`

**What this replaces:**
- `injectChannelCredentials()` in metadata server (runtime injection at serve time)
- Channel tokens are now resolved at gateway activation, not injected into config JSON

## Opik Plugin SecretRef (Implemented)

The Opik plugin API key now uses SecretRef instead of env var injection:

```json
{
  "plugins": {
    "entries": {
      "opik-openclaw": {
        "enabled": true,
        "config": {
          "apiKey": {
            "source": "exec",
            "provider": "ocm",
            "id": "OPIK_API_KEY"
          }
        }
      }
    }
  }
}
```

Resolved via `ocm-secrets` → metadata `/v1/secrets` → real API key.

## Key Technical Details

- Data volume path: `{DataDir}/{machineID}.ext4`
- Inside VM: `/data` mount, `/home/openclaw → /data/home/openclaw`
- Bridge IP: constant `192.168.100.1` across all hosts (default, never overridden)
- Proxy base URL: `http://192.168.100.1:4000/{provider}/v1` — baked into openclaw.json, portable
- Agent already has `cfg.OpenClawConf` and `cfg.Souls` when calling `RegisterMachine()`
- Mounting ext4 on host: fresh volume only (just created by `mkfs.ext4`), never a guest-modified volume
- `auth-profiles.json` derived from: provider names (from openclaw.json) + nonce (generated once, persisted)
- Nonce generated once at first boot, persisted on data volume — never regenerated (it's only a proxy lookup token)
- OpenClaw resolves SecretRefs at activation time into an in-memory snapshot (fail-fast)
- On hot reload, SecretRefs are re-resolved — atomic swap to new snapshot or keep last-known-good
- `OCM_SECRETS_AUTH` env var set in gateway env block only (not PTY server, not profile.d)
- `BridgeIP` must be set in `AssemblyParams` so browser `cdpUrl` is baked into config (not rewritten at serve time)
- Config changes while stopped are not possible — UI disabled, backend should enforce this too

## Codex Review Findings (Addressed)

### From First Review (branch diff)
1. ~~Boot uses seed config, not full assembled config~~ → First boot writes full assembled config to data volume; OnRunning pushes remaining config via exec
2. ~~ChannelCredentials empty during live config push~~ → **Fixed**: `assembleConfigForMachine` now splits credentials and populates `ChannelCredentials`
3. ~~Stale config on reboot~~ → Not an issue: config changes only on running machines, disk is source of truth
4. ~~`openrouter-key` missing from proxy key set~~ → **Fixed**: added to `proxyKeyIDs`

### From Plan Review
1. ~~`auth-profiles.json` stale nonce on reboot~~ → Not an issue: nonce is generated once and persisted, never regenerated
2. ~~"No changes while stopped" breaks onboarding~~ → Not an issue: onboarding starts the machine first, then pushes config to running VM
3. ~~`ocm-secrets` security: nonce is world-readable~~ → Accepted risk: VM is single-user trust domain, env-var gate raises the bar, hardening is future work
4. ~~Mounting guest-controlled ext4 on host~~ → Not an issue: host only mounts freshly created volumes, never guest-modified ones
5. ~~Removing `/v1/providers` breaks shell env~~ → Address in implementation: update `profile.d` and helper scripts to read from disk
6. ~~Opik secret ID inconsistency~~ → Doc typo: code correctly uses `OPIK_API_KEY` (fixed in this revision)
7. ~~Removing `/v1/config` breaks `cdpUrl` rewrite~~ → Address in implementation: set `BridgeIP` in `AssemblyParams` so `cdpUrl` is baked in
8. ~~`/v1/instance` and `ocm-metadata` consumers~~ → Address in implementation: update/remove helper scripts
