# Native Config Mode: Configuration Lifecycle

**Date:** 2026-03-19
**Status:** Sections 1–8 are the original design (backend-as-gatekeeper). Section 9 is the accepted decision to simplify to MVP.

## Purpose

Document how configuration flows through the system in native mode, from machine boot through post-install changes. Identify all actors, paths, gaps, and edge cases. Establish the architecture for a backend-mediated config model where the OCM backend is the single source of truth.

---

## 1. Architecture Principle

**The backend is the gatekeeper.** All configuration changes — regardless of who initiates them — flow through the OCM backend API. The backend validates, authorizes, persists to DB, and pushes to the running VM. The in-VM gateway treats its config as **managed** and cannot modify it directly.

```
┌────────────────────────────────────────────────────────────┐
│                    CONFIG SOURCES                          │
│                                                            │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│  │ OCM Dashboard│  │  OCM CLI     │  │ OpenClaw Agent   │ │
│  │ (React UI)   │  │  (planned)   │  │ (in-VM, via      │ │
│  │              │  │              │  │  tools/skills)   │ │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘ │
│         │                 │                    │           │
│         └─────────────────┼────────────────────┘           │
│                           ▼                                │
│                 ┌──────────────────┐                       │
│                 │  OCM Backend API │                       │
│                 │  (Cloud Run)     │                       │
│                 │                  │                       │
│                 │  • Validate      │                       │
│                 │  • Authorize     │                       │
│                 │  • Persist to DB │                       │
│                 │  • Push to VM    │                       │
│                 └────────┬─────────┘                       │
│                          │                                 │
│                          ▼                                 │
│                 ┌──────────────────┐                       │
│                 │  Agent (Host VM) │                       │
│                 │  exec endpoint   │                       │
│                 └────────┬─────────┘                       │
│                          │                                 │
│                          ▼                                 │
│                 ┌──────────────────┐                       │
│                 │  Gateway (in-VM) │                       │
│                 │  config.patch    │                       │
│                 │  hot-reload      │                       │
│                 │                  │                       │
│                 │  openclaw.json   │                       │
│                 │  (read-only)     │                       │
│                 └──────────────────┘                       │
└────────────────────────────────────────────────────────────┘
```

### Why backend-mediated?

1. **Authorization** — The backend knows what the account is allowed to do (billing, quotas, available models, permitted plugins).
2. **Consistency** — DB is the source of truth. Dashboard always shows current state.
3. **Auditability** — Every config change has an API call, a timestamp, and an actor.
4. **Safety** — The agent cannot install arbitrary plugins, switch to models outside its tier, or reconfigure auth.

---

## 2. Phase 1: Boot & Seed Config

### Flow

```
Machine Start
    │
    ▼
Backend: RuntimeService.Start()
    │
    ├─ Fetch credentials from DB (LLM keys, channel keys)
    ├─ Determine default model
    │   └─ DB preferred_model → platformModelMap → gateway model ref
    ├─ AssembleSeedConfig()
    │   ├─ Exec secret provider: /usr/local/bin/ocm-secrets
    │   ├─ Provider configs with baseUrl → proxy (include /v1)
    │   ├─ Nebius models array (explicit, with api: "openai-completions")
    │   ├─ Gateway defaults (auth, controlUi, reload: hot)
    │   └─ Models catalog (all platform tiers)
    ├─ agentClient.CreateVM()
    │   ├─ Seed config JSON
    │   ├─ LLM keys (encrypted)
    │   ├─ Channel keys (encrypted)
    │   └─ config_mode: "native"
    │
    ▼
Agent (GCE Host VM)
    │
    ├─ Store seed config in metadata server (/v1/config)
    ├─ Store LLM/channel keys in metadata
    ├─ Boot Firecracker microVM
    │
    ▼
VM Init Script (PID 1)
    │
    ├─ Fetch /v1/machine → detect config_mode: "native"
    ├─ Setup versioned config dir on /data volume
    │   └─ /data/ocm/configs/YYYYMMDDTHHMMSSZ/
    │   └─ Symlink: config-current → latest version
    │   └─ Preserve user edits on restart (copy forward)
    │   └─ Keep 5 versions, garbage collect older
    │
    ├─ write_seed_config()
    │   ├─ Check: does openclaw.json already exist?
    │   │   ├─ YES (restart) → skip, reuse existing
    │   │   └─ NO  (first boot) → fetch /v1/config, write file
    │   └─ File owned by root (read-only to gateway)  ← NEW
    │
    ├─ write_auth_profiles()
    │   ├─ Fetch /v1/providers
    │   ├─ For each provider: create profile with nonce as key
    │   └─ Write auth-profiles.json
    │
    ├─ Start gateway (port 18789)
    │   └─ openclaw gateway --port 18789 --bind loopback --allow-unconfigured
    ├─ Start PTY server (port 7681)
    ├─ Start auth proxy + cloudflared
    │
    ▼
    ✓ Ready for chat
```

### Seed config structure

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
      "enabled": false
    },
    "reload": { "mode": "hot" }
  },
  "models": {
    "providers": {
      "nebius": {
        "baseUrl": "http://192.168.100.1:4000/nebius/v1",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "nebius-key" },
        "api": "openai-completions",
        "models": [
          { "id": "deepseek-ai/DeepSeek-V3-0324", "name": "DeepSeek V3", "api": "openai-completions", ... },
          { "id": "deepseek-ai/DeepSeek-R1-0528", "name": "DeepSeek R1", "api": "openai-completions", ... }
        ]
      },
      "anthropic": {
        "baseUrl": "http://192.168.100.1:4000/anthropic/v1",
        "apiKey": { "source": "exec", "provider": "ocm", "id": "anthropic-key" },
        "models": []
      }
    }
  },
  "agents": {
    "defaults": {
      "model": { "primary": "nebius/deepseek-ai/DeepSeek-V3-0324" },
      "models": {
        "nebius/deepseek-ai/DeepSeek-V3-0324": {},
        "nebius/deepseek-ai/DeepSeek-R1-0528": {},
        "nebius/openai/gpt-oss-20b": {}
      }
    }
  },
  "commands": { "native": "auto", "restart": true },
  "plugins": { "entries": {} }
}
```

### Key design decisions at boot

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Seed-then-own | Config only written if `openclaw.json` doesn't exist | Preserves user customizations across restarts |
| Config versioning | Timestamped dirs on `/data` volume, symlinked | Survives rootfs upgrades, allows rollback |
| File ownership | `root:root`, mode `0644` | Gateway reads, only backend (via exec as root) writes |
| Control UI | Disabled (`enabled: false`) | All config changes go through OCM backend |
| Auth profiles | Written at boot only | Providers discovered from metadata `/v1/providers` |

---

## 3. Phase 2: Post-Install Configuration

After the machine is running, configuration changes can come from three sources. All go through the backend.

### 3a. Model Selection

**Current status:** DB save works. Live push just added (untested).

```
User selects model in Dashboard
    │
    ▼
Frontend: PUT /v1/accounts/{id}/machines/{id}/model
    │  body: { "model": "deepseek/deepseek-r1" }
    │
    ▼
Backend: handleSetMachineModel()
    │
    ├─ Validate model against allowedModels whitelist
    ├─ Save to DB: SetMachinePreferredModel()
    ├─ Map model: "deepseek/deepseek-r1" → "nebius/deepseek-ai/DeepSeek-R1-0528"
    ├─ Build patch JSON:
    │   { "agents": { "defaults": { "model": { "primary": "nebius/..." } } } }
    ├─ pushNativeConfig() → exec config.patch on VM
    │
    ▼
Gateway: hot-reloads with new default model
```

**What's needed for BYOK models (anthropic, openai, google):**
- Model already in gateway's built-in catalog (no explicit model entry needed)
- But `auth-profiles.json` must have the provider → needs regeneration (see Section 3b)

### 3b. Linking New Providers / Credentials

**Current status:** DB save works. No live push. Requires VM restart.

```
User adds Anthropic API key in Dashboard
    │
    ▼
Frontend: POST /v1/accounts/{id}/credentials
    │  body: { "provider": "anthropic", "key": "sk-ant-..." }
    │
    ▼
Backend: handleCreateCredential()
    │
    ├─ Encrypt and store key in DB
    ├─ Link to machine (if machine-specific)
    │
    ├─ ⚠ GAP: No live push to running VM
    │
    │  NEEDED:
    ├─ Update metadata server LLM keys (add new provider key)
    ├─ Regenerate auth-profiles.json on VM (add provider profile)
    ├─ Push config.patch to add models.providers.<name> entry
    ├─ Gateway hot-reloads → new provider available
    │
    ▼
Gateway: discovers new provider via auth-profiles.json + config
```

**This is the most complex change** because three things must update atomically:
1. Metadata server (real API key for proxy)
2. `auth-profiles.json` (provider discovery for ModelRegistry)
3. `openclaw.json` (provider config with baseUrl, models)

### 3c. Configuring Channels (Telegram, Discord, etc.)

**Current status:** Works in fork mode. Not wired for native mode.

```
User configures Telegram in Dashboard
    │
    ▼
Frontend: POST /v1/accounts/{id}/credentials
    │  body: { "provider": "telegram", "key": "<bot-token>" }
    │
    ▼
Backend:
    ├─ Store credential
    ├─ Assemble channel config block
    │   { "channels": { "telegram": { "adapter": "telegram", ... } } }
    ├─ pushNativeConfig() with channel config patch
    │
    ▼
Gateway: discovers Telegram channel, connects to bot API
```

**Channel credentials are different from LLM credentials:**
- LLM keys go through the API proxy (nonce → real key swap)
- Channel keys go directly to the gateway (it connects to Telegram/Discord/WhatsApp directly)
- Channel keys need to be in `openclaw.json` directly, not via exec secret provider

### 3d. Plugins

**Current status:** Plugin entries placeholder exists in seed config. No install flow.

```
User installs plugin from Dashboard
    │
    ▼
Frontend: POST /v1/accounts/{id}/machines/{id}/plugins
    │  body: { "plugin": "github-integration", "config": {...} }
    │
    ▼
Backend:
    ├─ Validate plugin against allowed catalog
    ├─ Store plugin config in DB
    ├─ Push config.patch:
    │   { "plugins": { "entries": { "github-integration": { ... } } } }
    │
    │  If plugin needs binary/package:
    ├─ exec: install plugin package on VM
    │
    ▼
Gateway: loads plugin on next reload
```

### 3e. Skills

**Current status:** Not implemented for native mode.

Skills in OpenClaw are command bundles (`commands.native`, `commands.nativeSkills`). The seed config sets `"native": "auto"` which auto-discovers available skills. Custom skills would need:

1. Skill definition pushed to VM filesystem
2. Config patch to register the skill
3. Gateway reload to pick it up

### 3f. Agent-Initiated Config Changes

**Current status:** Not implemented. This is the key new capability.

```
User in chat: "switch to Claude Sonnet"
    │
    ▼
OpenClaw Agent (in-VM)
    │
    ├─ Agent has an MCP tool / skill: "ocm_set_model"
    ├─ Tool calls OCM Backend API:
    │   PUT /v1/machines/{id}/model
    │   Authorization: Bearer <machine-proxy-token>
    │
    ▼
Backend: handleSetMachineModel()  (same path as Dashboard)
    │
    ├─ Validate, save to DB, map model
    ├─ pushNativeConfig()
    │
    ▼
Gateway: hot-reloads with new model

Agent confirms to user: "Switched to Claude Sonnet 4.6"
```

**Agent tools needed** (exposed as MCP server or OpenClaw skills):

| Tool | Backend API | Description |
|------|-------------|-------------|
| `ocm_set_model` | `PUT /machines/{id}/model` | Change default model |
| `ocm_list_models` | `GET /machines/{id}/model` | Show available models |
| `ocm_add_provider` | `POST /credentials` | Add BYOK API key |
| `ocm_list_providers` | `GET /credentials` | Show linked providers |
| `ocm_install_plugin` | `POST /machines/{id}/plugins` | Install a plugin |
| `ocm_get_config` | `GET /machines/{id}/config` | View current config |

**Auth for agent → backend calls:**
The machine already has a `proxy_token` (used for exec auth). The agent tools can use this same token to authenticate API calls back to the backend. The backend validates: "this token belongs to machine X, and machine X belongs to account Y."

---

## 4. Blocking Direct Config Writes

In native mode, the gateway's built-in config mechanisms must be disabled to enforce the backend-mediated model.

### 4a. Disable Control UI

The OpenClaw Control UI is a web settings panel built into the gateway. It writes directly to `openclaw.json`, bypassing the backend.

**Fix:** Set `controlUi.enabled: false` in the seed config.

```go
// In AssembleSeedConfig():
"controlUi": map[string]interface{}{
    "enabled": false,   // was: true
},
```

**Impact:** Users cannot change settings via the gateway's web UI. All changes go through the OCM Dashboard instead.

### 4b. Block `openclaw configure` Wizard

The `openclaw configure` CLI wizard sets up providers and writes to `openclaw.json` and `auth-profiles.json` directly.

**Fix:** Alias in PTY server environment to a helpful message.

```bash
# In init-openclaw.sh, PTY server env block:
alias openclaw-configure='echo "Configuration is managed by OCM. Use the dashboard at https://openclawmachines.com or ask the agent to configure."'
# Override the actual command:
cat > /usr/local/bin/openclaw-configure-blocked << 'BLOCKED'
#!/bin/bash
echo "This machine is managed by OpenClaw Machines."
echo "Use the OCM dashboard or ask the agent to change settings."
echo "Dashboard: https://openclawmachines.com"
exit 1
BLOCKED
chmod +x /usr/local/bin/openclaw-configure-blocked
```

**Alternative:** The `openclaw configure` command is part of the gateway binary. We cannot remove it without forking. The alias/wrapper approach is pragmatic.

### 4c. File Permissions on openclaw.json

Make the config file readable by the gateway (runs as `openclaw` user) but writable only by root. The backend's exec commands run as root via the PTY server.

**Fix:** In `write_seed_config()`:

```bash
# Write config as root, readable by openclaw user
echo "$seed_config" > "$config_file/openclaw.json"
chown root:openclaw "$config_file/openclaw.json"
chmod 0640 "$config_file/openclaw.json"  # root writes, openclaw reads
```

**For config.patch to work**, the backend's exec command must run as root:

```bash
# Backend exec calls:
sudo openclaw gateway call config.patch --params '...'
```

**Caveat:** The gateway's `config.patch` RPC writes to `openclaw.json` internally. If the file is read-only to the `openclaw` user, `config.patch` will fail. Two solutions:

1. **Run gateway as root** — not ideal for security
2. **Use a suid helper** — a small binary that writes config as root
3. **Backend writes directly** — skip `config.patch`, have the exec command write the file directly and send SIGHUP to gateway

Option 3 is simplest. The backend already assembles the full config. Instead of `config.patch` (which needs gateway write access), the exec command:
1. Writes `openclaw.json` as root
2. Sends signal to gateway to reload

### 4d. Summary: Blocking Matrix

| Direct-write path | Blocking mechanism | Effort |
|---|---|---|
| Control UI (web settings) | `controlUi.enabled: false` in seed config | One line in assembler.go |
| `openclaw configure` wizard | Wrapper script that shows dashboard URL | Init script change |
| Direct `openclaw.json` edits | File owned by root, mode 0640 | Init script change |
| `config.patch` from inside VM | Only callable via exec (needs proxy token auth) | Already restricted |

---

## 5. Edge Cases

### 5a. VM Restart (Stop + Start)

```
VM restarts
    │
    ├─ Init script runs again
    ├─ write_seed_config() checks: openclaw.json exists?
    │   └─ YES → skip write, reuse existing config
    ├─ write_auth_profiles() runs → regenerates from /v1/providers
    ├─ Gateway starts with existing config
    │
    ▼
    ✓ User's config preserved
```

**Key point:** Credentials in metadata server are re-populated by the backend on each `Start()` call. The seed config is NOT re-written — existing `openclaw.json` is reused.

### 5b. Rootfs Upgrade

```
New rootfs uploaded to GCS
    │
    ├─ Agent self-updates (polls every 5 min, only when idle)
    ├─ Agent restarts → re-stages rootfs
    ├─ Next VM start uses new rootfs
    │
    ▼
VM boots with new rootfs
    │
    ├─ Init script detects new ROOTFS_CONFIG_VERSION
    ├─ Creates new timestamped config directory
    ├─ Copies forward existing openclaw.json from previous version
    ├─ Copies forward auth-profiles.json, device.json
    │
    ▼
    ✓ User's config preserved across rootfs upgrade
```

**Risk:** New rootfs may include new OpenClaw gateway version with different config schema. The copy-forward of `openclaw.json` may contain keys the new version doesn't understand (ignored) or miss keys the new version requires (defaults used).

**Mitigation:** The seed config is minimal. New required keys should have defaults in the gateway. Schema-breaking changes need a migration path (version check + transform in init script).

### 5c. OpenClaw Version Upgrade

The OpenClaw gateway binary is bundled in the rootfs (`rootfs/openclaw-fork.tgz`). Version upgrades happen via rootfs rebuild.

```
make update-openclaw VERSION=v2026.x.y
make build-upload-rootfs
    │
    ▼
Same as rootfs upgrade (5b above)
```

**Additional concern:** The gateway's `config.patch` RPC semantics may change between versions. The backend must use the same patch format the running gateway version expects. For now this is not versioned — the backend assumes current format.

### 5d. Concurrent Edits

If the user directly edits `openclaw.json` in the VM while the backend pushes a config.patch:

**Current behavior:** `config.patch` uses `baseHash` for optimistic concurrency. If the file changed since the hash was read, the patch is rejected. The backend would need to retry (re-read hash, re-apply).

**With file permissions (Section 4c):** Direct edits by the `openclaw` user are blocked (file is root-owned). Only the backend can write. Eliminates the race condition.

### 5e. Credential Added to Running VM

```
User adds OpenAI key in dashboard while VM is running
    │
    ▼
Backend:
    ├─ Store credential in DB
    ├─ ⚠ GAP: need to update metadata server LLM keys
    ├─ ⚠ GAP: need to regenerate auth-profiles.json
    ├─ ⚠ GAP: need to push provider config via config.patch
    │
    ▼
    Three things must happen atomically (see Section 3b)
```

### 5f. Agent Self-Update During Active Session

```
Agent detects new version in GCS
    │
    ├─ selfupdate.skip reason=vms_running
    │   └─ Agent only self-updates when idle (no VMs)
    │
    ▼
    No impact on running sessions
```

The agent self-update is safe — it won't restart while VMs are running. The user must stop all VMs, wait for self-update (up to 5 min), then start a new VM.

---

## 6. Test Harness Design

### Current test coverage

| Test tier | What it exercises | Config paths tested |
|-----------|-------------------|---------------------|
| `make test-go` (~20s) | Unit tests, assembler logic | Seed config generation, model mapping, protected keys |
| `make test-gateway-e2e` (~12s) | Gateway + proxy, no VM | Proxy auth, model catalog, WebSocket chat |
| `make test-integration` (~35min) | Real Firecracker VM | Full init script, gateway boot, terminal commands |

### Gaps

1. **No test for config.patch round-trip** — config push → gateway reload → verify new model works
2. **No test for credential addition** — add key → verify provider available → send chat
3. **No test for agent tools** — agent calls backend API → verify config changes
4. **E2E proxy tests bypass gateway** — send HTTP directly to proxy, skip model resolution
5. **Cloudflare dependency** — integration tests need real tunnel for browser access

### Proposed test harness

**Goal:** Test the full config→gateway→chat path without Firecracker or Cloudflare. Run in `make test-gateway-e2e` tier (~12s).

```
Test Harness Architecture
    │
    ├─ Metadata server (in-process, existing)
    ├─ API proxy (in-process, existing)
    ├─ Gateway process (spawned, existing)
    ├─ Backend API mock (NEW — in-process HTTP server)
    │   └─ Handles: PUT /model, POST /credentials, POST /config/push
    │   └─ Calls config.patch on gateway via exec simulation
    │
    ▼
Test scenarios:
    │
    ├─ TestConfigPatch_ModelChange
    │   └─ Push model change via config.patch
    │   └─ Verify gateway accepts new model via WebSocket chat.send
    │
    ├─ TestConfigPatch_AddProvider
    │   └─ Push new provider config
    │   └─ Update auth-profiles.json
    │   └─ Verify new provider appears in models.list
    │
    ├─ TestConfigPatch_RemoveProvider
    │   └─ Push config removing a provider
    │   └─ Verify provider disappears from models.list
    │
    ├─ TestSeedConfig_FirstBoot
    │   └─ Write seed config to temp dir
    │   └─ Start gateway with seed config
    │   └─ Verify model catalog populated
    │   └─ Verify chat works with default model
    │
    ├─ TestSeedConfig_Restart
    │   └─ Write seed config, start gateway, stop
    │   └─ Modify openclaw.json (simulate user change)
    │   └─ Start gateway again — verify it uses modified config
    │
    ├─ TestUpgrade_ConfigPreserved
    │   └─ Write seed config v1
    │   └─ Simulate rootfs upgrade (new config version)
    │   └─ Verify old openclaw.json copied forward
```

**Removing Cloudflare dependency:**
The test harness connects to the gateway directly (localhost:18789) via WebSocket. No Cloudflare tunnel needed. The auth proxy is not tested here — it has its own test path. This tests the config→gateway→chat path in isolation.

**Removing Firecracker dependency:**
The gateway runs as a local process (already done in `setupTestEnv()`). Config files are written to a temp directory. The exec endpoint is simulated by directly calling `openclaw gateway call config.patch` on the running process.

---

## 7. Implementation Priority

| Priority | Item | Effort | Impact |
|----------|------|--------|--------|
| P0 | Disable Control UI in seed config | 1 line | Prevents direct config writes |
| P0 | File permissions on openclaw.json | Init script change | Enforces backend-only writes |
| P1 | Model change live push (test + verify) | Small | Users can switch models without restart |
| P1 | Credential addition live push | Medium | Users can add BYOK keys to running VMs |
| P1 | Config.patch E2E test | Medium | Prevents "deploy and pray" regressions |
| P2 | Agent MCP tools for config | Medium | Agent can configure itself via backend |
| P2 | `openclaw configure` blocking wrapper | Init script | Clean UX for blocked path |
| P3 | Channel config (Telegram, etc.) live push | Medium | Channels configurable post-boot |
| P3 | Plugin install flow | Large | Plugin ecosystem |
| P3 | OCM CLI config commands | Large | CLI as config source |

---

## 8. Open Questions

1. **Config.patch vs direct write:** Should the backend continue using `config.patch` RPC (which requires gateway write access to the file), or should it write the file directly via exec and signal the gateway to reload? Direct write is simpler with root-owned files, but `config.patch` has merge semantics and validation.

2. **Auth for agent→backend calls:** The machine's `proxy_token` authenticates exec calls. Should the same token authenticate agent tool calls to the backend API? Or does the agent need a separate credential?

3. **Config schema versioning:** When the OpenClaw gateway version upgrades, the config schema may change. Do we need a version field in `openclaw.json` and a migration system?

4. **Credential refresh for running VMs:** OAuth tokens expire and need refresh. The control plane has the refresh token. How does the refreshed access token reach the running VM's metadata server?

---

## 9. Decision: Simplify to MVP

**Date:** 2026-03-19
**Status:** Accepted

### Context

After writing sections 1–8 and getting a Codex review, we realized the full backend-as-gatekeeper architecture is over-engineered for the current stage. The Codex review surfaced two critical blockers:

1. **`config.patch` is blocked** — `allowedExecSubcommands` in `ptyserver.go` only allows `pairing|status|doctor`. The already-coded `pushNativeConfig()` can't execute.
2. **Agent-initiated config has no auth model** — The `proxy_token` is for host→agent proxy calls, not backend API auth. No machine-scoped auth routes exist on the backend.

Additionally, we identified three fundamental design problems with the full plan:

### Problems with the full backend-mediated model

**1. Breaking OpenClaw's core value proposition.**
The plan blocks direct config writes (Control UI, `openclaw configure`, file permissions). OpenClaw is designed to be a self-configuring AI agent. Users who SSH in or use the agent's built-in tools would hit walls. The agent's ability to install plugins, configure channels, etc. is a selling point — we'd be disabling features that upstream OpenClaw provides.

**2. Backend as single point of failure.**
Making ALL config changes go through the backend means if Cloud Run is down, the VM can't reconfigure itself at all. Today, in-VM changes work independently. The full architecture couples VM liveness to backend availability.

**3. `config.patch` concurrency.**
The optimistic concurrency model (`baseHash`) means if the backend pushes a config change while the gateway is mid-reload or another change is in flight, the patch is rejected. No retry/queue mechanism was designed.

Additional risks identified:
- Root-owned file permissions can break OpenClaw upgrades — if `openclaw.json` is owned by root but the gateway runs as `openclaw`, OpenClaw's own update/migration process may fail silently.
- `auth-profiles.json` only written at boot — credential link/unlink has no live push path. Fixing it requires the agent to rewrite auth-profiles AND restart the gateway.
- Scope creep into agent tooling — building MCP tools/skills for agent→backend config is significant new surface area (auth, rate limiting, capability scoping) that delays core fixes.

### The simplified model

Instead of "backend is the gatekeeper," adopt: **"backend pushes, VM owns."**

The backend can push config changes to the running VM (model change, credential push), but the VM remains the owner of its config. In-VM changes (Control UI, `openclaw configure`, SSH edits) continue to work as OpenClaw designed them. The backend does NOT need to know about in-VM changes.

```
┌───────────────────────────────────────────────────────────────┐
│                    CONFIG OWNERSHIP MODEL                      │
│                                                               │
│  Backend (Cloud Run)          VM (Firecracker)                │
│  ┌─────────────────────┐     ┌───────────────────────────┐   │
│  │ Seeds config at boot │────▶│ openclaw.json on /data    │   │
│  │ Pushes changes (UI)  │────▶│ Gateway hot-reloads       │   │
│  │ Tracks preferred_model│    │                           │   │
│  │ in DB (for UI display │    │ User/agent can also:      │   │
│  │ and future seed)      │    │  • Control UI             │   │
│  └─────────────────────┘     │  • openclaw configure     │   │
│                               │  • Direct file edits      │   │
│  Backend does NOT sync back.  │  • Plugin installs        │   │
│  VM config is the user's.     │                           │   │
│                               │ These work independently  │   │
│                               │ of backend availability.  │   │
│                               └───────────────────────────┘   │
└───────────────────────────────────────────────────────────────┘
```

### Why not sync VM changes back to the backend?

What the backend uses `preferred_model` for:
1. **UI display** — show which model is selected in the dashboard
2. **Seed config** — pick the default model on first boot

What breaks if an in-VM change isn't synced back:
- Dashboard shows old model selection → **cosmetic, not blocking**
- Next UI push could overwrite in-VM change → **user chose to use UI, expected behavior**
- Seed config on fresh VM uses DB value → **correct — fresh VM gets platform default**

Billing is based on actual API proxy usage, not config state. No financial impact from stale DB.

This is the same model as any cloud VM: the cloud provider doesn't track your `/etc/nginx.conf` changes. If you want the platform to know about a change, you make it through the platform.

### Config persistence and backup

`openclaw.json` lives at `/home/openclaw/.openclaw/openclaw.json`. Because `/home/openclaw` is symlinked to `/data/home/openclaw` (init-openclaw.sh line 85), all config is on the data volume:

```
/home/openclaw → /data/home/openclaw    (symlink)
/data/ocm/configs/<ts>/                  (versioned config dir)
    ├── auth-profiles.json
    ├── device.json
    └── IDENTITY.md

Machine backup → snapshots /data volume → all config included
```

Config survives: VM restart, rootfs upgrade, OpenClaw version upgrade, and machine backup/restore.

### Simplified boot flow

```
First boot:
  Backend assembles seed config → metadata → VM writes openclaw.json → gateway starts

Restart:
  openclaw.json already on /data → skip seed → gateway starts with existing config
  auth-profiles.json regenerated from /v1/providers (always fresh)

Rootfs upgrade:
  New rootfs, same /data volume → openclaw.json preserved → gateway starts
```

### Simplified live config change (from UI)

```
User changes model in Dashboard
  → Backend saves to DB
  → Backend execs: openclaw gateway call config.patch (via agent exec endpoint)
  → Gateway hot-reloads → openclaw.json updated on disk by gateway
  → Done. No file permission games, no root ownership needed.
```

### Exec whitelist: `gateway` is consistent with `pairing`

The exec endpoint already allows `openclaw pairing`, which **writes state** (creates `device.json` for device identity). Adding `openclaw gateway` is the same pattern — an OpenClaw CLI subcommand that modifies gateway state. No new category of access.

### What changes from the original plan

| Original plan | Simplified MVP | Why |
|---|---|---|
| Backend is the gatekeeper | Backend pushes, VM owns | Don't fight upstream OpenClaw's design |
| Disable Control UI | Leave enabled | Users expect it, it works, no reason to block |
| Block `openclaw configure` | Leave working | Same — it's a feature, not a bug |
| Root-owned openclaw.json | Leave as `openclaw:openclaw` | Avoids breaking OpenClaw upgrades and config.patch |
| Agent MCP tools for config | Deferred | Large scope, unclear value right now |
| Sync VM changes to backend | Don't sync | Backend doesn't need to know, no financial impact |
| Complex test harness | Use existing E2E tier | `config.patch` test in gateway E2E is sufficient |

### MVP implementation

The actual work is two items:

1. **Add `"gateway": true` to `allowedExecSubcommands`** in `backend/cmd/agent/ptyserver.go` — unblocks the already-coded `pushNativeConfig()` path
2. **Deploy** — `make upload-agent` (ptyserver change is agent-side) + `make deploy-backend` (model push code already committed)

The `handleSetMachineModel` live push and `pushNativeConfig()` are already committed (commit `0867636`). The only thing blocking them is the exec whitelist.

### Open questions resolved

| Question (from Section 8) | Resolution |
|---|---|
| 1. `config.patch` vs direct write | **`config.patch`** — gateway's own mechanism, hot-reload built in, no root ownership needed |
| 2. Auth for agent→backend calls | **Deferred** — not building agent tools in MVP |
| 3. Config schema versioning | **Not needed** — gateway handles schema compat internally |
| 4. Credential refresh | **Already solved** — `oauth_refresh.go` pushes refreshed keys to metadata server without gateway restart |
