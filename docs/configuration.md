# OpenClaw Gateway Configuration — Analysis & Gaps

**Status:** Research findings — partially validated by E2E tests (2026-02-28)
**Source:** Cross-referencing a local OpenClaw install (`~/.openclaw/`) against the OCM codebase
**Branch:** `configuration`
**E2E test suite:** `backend/internal/gatewaye2e/gateway_test.go`

---

## 1. OpenClaw Local Config Structure

A local OpenClaw installation lives at `~/.openclaw/` with this layout:

```
~/.openclaw/
├── openclaw.json                    # Main gateway config
├── agents/
│   └── main/
│       ├── agent/
│       │   └── auth-profiles.json   # LLM credentials (tokens, OAuth, API keys)
│       └── sessions/                # Conversation sessions
├── workspace/                       # Agent personality + memory (git repo)
│   ├── AGENTS.md                    # Operating instructions, loaded every session
│   ├── BOOTSTRAP.md                 # First-run onboarding (deleted after setup)
│   ├── SOUL.md                      # Agent personality + behavioral rules
│   ├── IDENTITY.md                  # Agent name, avatar, emoji, vibe
│   ├── USER.md                      # Human's name, timezone, preferences
│   ├── TOOLS.md                     # Environment-specific tool notes
│   ├── HEARTBEAT.md                 # Periodic task checklist
│   └── .openclaw/workspace-state.json
├── identity/
│   ├── device.json                  # Ed25519 keypair for device auth
│   └── device-auth.json             # Device tokens + scopes
├── devices/
│   ├── paired.json                  # All paired devices (CLI, web, macOS app)
│   └── pending.json                 # Pending pairing requests
├── completions/                     # Shell completions (bash, zsh, fish, ps1)
├── cron/
│   └── jobs.json                    # Scheduled tasks
├── canvas/
│   └── index.html                   # Canvas UI
├── logs/                            # Gateway logs
├── exec-approvals.json              # Execution approval gate (socket path + token)
├── exec-approvals.sock              # Unix socket for command approval
└── update-check.json                # Last update check timestamp
```

**Source:** `ls -laR ~/.openclaw/`

---

## 2. `openclaw.json` — Main Gateway Config

The central configuration file controls gateway behavior, auth profiles, model defaults, and security settings.

**Source:** `~/.openclaw/openclaw.json`

```json
{
  "meta": {
    "lastTouchedVersion": "2026.2.26",
    "lastTouchedAt": "2026-02-28T16:07:15.761Z"
  },
  "wizard": {
    "lastRunAt": "2026-02-28T16:07:15.758Z",
    "lastRunVersion": "2026.2.26",
    "lastRunCommand": "configure",
    "lastRunMode": "local"
  },
  "auth": {
    "profiles": {
      "openai-codex:default": { "provider": "openai-codex", "mode": "oauth" },
      "anthropic:aa":         { "provider": "anthropic",    "mode": "token" }
    }
  },
  "agents": {
    "defaults": {
      "model": { "primary": "anthropic/claude-sonnet-4-6" },
      "models": { "anthropic/claude-sonnet-4-6": {} },
      "workspace": "/Users/mantiz/.openclaw/workspace"
    }
  },
  "commands": { "native": "auto", "nativeSkills": "auto", "restart": true, "ownerDisplay": "raw" },
  "session": { "dmScope": "per-channel-peer" },
  "gateway": {
    "port": 18789,
    "mode": "local",
    "bind": "loopback",
    "auth": { "mode": "token", "token": "<gateway-token>" },
    "tailscale": { "mode": "off", "resetOnExit": false },
    "nodes": {
      "denyCommands": ["camera.snap", "camera.clip", "screen.record",
                       "calendar.add", "contacts.add", "reminders.add"]
    }
  }
}
```

### Sections OCM config assembly currently generates

| Section | Generated? | Source |
|---------|-----------|--------|
| `gateway.controlUi` | Yes | `backend/internal/configassembly/assembler.go:11-23` (`platformDefaults`) |
| `gateway.reload` | Yes | `assembler.go:24-26` |
| `gateway.trustedProxies` | Yes | `assembler.go:29` |
| `skills.allowBundled` | Yes | `assembler.go:204-215` |
| `ui.assistant` (name, avatar) | Yes | `assembler.go:186-197` |

### Sections OCM config assembly does NOT generate

| Section | Needed? | Notes |
|---------|---------|-------|
| `auth.profiles` | **Yes** | Gateway needs to know which providers are configured and their auth mode |
| `agents.defaults.model` | **Yes** | Determines which LLM model the gateway uses by default |
| `agents.defaults.models` | Yes | Available model list |
| `agents.defaults.workspace` | Partial | Init script creates `/home/openclaw/.openclaw/workspace` but doesn't configure this key |
| `commands` | Probably | Controls native command behavior |
| `session.dmScope` | Probably | Controls DM session isolation |
| `gateway.port` | No | Set via CLI flags in init script, not config file |
| `gateway.mode` | No | Set via `OCM_CONFIG_SOURCE=metadata` env var |
| `gateway.auth` | No | Token is injected via `OPENCLAW_GATEWAY_TOKEN` env var |
| `gateway.tailscale` | No | Not used in MicroVM context |
| `gateway.nodes.denyCommands` | **Yes** | Security: should restrict dangerous commands in hosted environment |

**Config assembly source:** `backend/internal/configassembly/assembler.go`
**Init script config setup:** `scripts/init-openclaw.sh:289-301`

---

## 3. `auth-profiles.json` — The Missing Link

This is how the OpenClaw gateway stores LLM credentials. It lives at `~/.openclaw/agents/main/agent/auth-profiles.json`.

**Source:** `~/.openclaw/agents/main/agent/auth-profiles.json`

### Structure

```json
{
  "version": 1,
  "profiles": {
    "<provider>:<profile-id>": {
      "type": "token|oauth|api_key",
      "provider": "<provider-name>",
      // For type=token:
      "token": "<value>",
      // For type=oauth:
      "access": "<JWT access token>",
      "refresh": "<refresh token>",
      "expires": 1773156731140,
      "accountId": "<account-id>"
    }
  },
  "usageStats": {
    "<provider>:<profile-id>": {
      "lastUsed": 1772293818667,
      "errorCount": 0
    }
  }
}
```

### Observed profiles

| Profile ID | Type | Provider | Token Format | Auth Header |
|-----------|------|----------|-------------|-------------|
| `anthropic:aa` | `token` | `anthropic` | `sk-ant-oat01-...` (OAT setup-token) | `Authorization: Bearer <token>` |
| `openai-codex:default` | `oauth` | `openai-codex` | JWT from `auth.openai.com` | `Authorization: Bearer <JWT>` |

### Anthropic token profile

```json
{
  "type": "token",
  "provider": "anthropic",
  "token": "sk-ant-oat01-..."
}
```

Generated locally by running `claude setup-token` in the Claude Code CLI. The OAT prefix (`sk-ant-oat`) triggers Bearer auth instead of `x-api-key` in both the OCM backend validation and API proxy.

**Validation source:** `backend/internal/api/credentials.go:270-335`, `cli/internal/commands/validate.go:36-87`
**Proxy injection source:** `backend/internal/apiproxy/providers.go:45-48`

### OpenAI OAuth profile

```json
{
  "type": "oauth",
  "provider": "openai-codex",
  "access": "<JWT>",
  "refresh": "rt_...",
  "expires": 1773156731140,
  "accountId": "071e633d-..."
}
```

Generated by running `openclaw onboard --auth-choice openai-codex`, which triggers an OAuth browser flow against `auth.openai.com`. The JWT contains:

- **Audience:** `https://api.openai.com/v1`
- **Issuer:** `https://auth.openai.com`
- **Plan type:** `chatgpt_plan_type: "plus"` (from JWT claims)
- **Scopes:** `openid`, `profile`, `email`, `offline_access`
- **Client ID:** `app_EMoamEEZ73f0CkXaXp7hrann`

This is fundamentally different from a pasteable API key — it's an OAuth flow with expiring access tokens and refresh capability.

### ~~Gap: OCM never writes `auth-profiles.json`~~ — FIXED

> **Status:** Fixed in init-script commits on `configuration` branch. The init script now generates `auth-profiles.json` with `type: "api_key"` for all configured LLM providers at VM boot time. See `scripts/init-openclaw.sh:535-547`.

The current credential delivery path (Approach A — proxy model) uses the API proxy on `169.254.169.253:4000`, which injects keys into upstream requests. The gateway sends requests to the proxy with nonce API keys, and the proxy swaps in real keys from the metadata server. This means `auth-profiles.json` is **not strictly needed** when the proxy model is wired up, but the gateway expects it to exist for provider discovery.

~~However, the gateway currently doesn't know to route through the proxy — the `baseUrl` overrides for each provider are set in `/etc/profile.d/openclaw-providers.sh` (for login shells) but **not passed to the gateway process environment**.~~

> **Status:** Also fixed. Provider `BASE_URL` env vars are now passed directly to the gateway process environment block. See `scripts/init-openclaw.sh:553-581`.

**Init script provider env vars:** `scripts/init-openclaw.sh:306-341`
**Gateway startup (now includes provider env vars):** `scripts/init-openclaw.sh:553-581`

---

## 4. OCM CLI Setup Wizard — OpenAI Is Wrong

### Current implementation

The CLI setup wizard (`ocm providers setup <provider>`) offers two auth paths for providers in `subscriptionInfo`:

**Source:** `cli/internal/commands/providers_setup.go:16-34`

```go
var subscriptionInfo = map[string]struct {
    PlanNames string
    AuthName  string
    TokenName string
    TokenCmd  string
}{
    "anthropic": {
        PlanNames: "Claude Pro / Max / Team",
        AuthName:  "Anthropic token",
        TokenName: "setup-token",
        TokenCmd:  "claude setup-token",     // ✅ Real command (Claude Code CLI)
    },
    "openai": {
        PlanNames: "ChatGPT Plus / Pro / Team",
        AuthName:  "OpenAI token",
        TokenName: "setup-token",
        TokenCmd:  "chatgpt setup-token",    // ❌ Does not exist
    },
}
```

### The problem

- **`claude setup-token`** is a real Claude Code CLI command that generates an OAT token (`sk-ant-oat01-...`). The token is a single pasteable string. This works.
- **`chatgpt setup-token`** does not exist. There is no `chatgpt` CLI tool. OpenAI subscription auth uses an OAuth browser flow, not a pasteable token.

### What OpenClaw actually does for OpenAI

The OpenClaw CLI has `openclaw onboard --auth-choice openai-codex` which:

1. Opens a browser to `auth.openai.com`
2. User authenticates with their ChatGPT account
3. OAuth callback returns JWT access token + refresh token
4. Tokens are stored in `auth-profiles.json` with `type: "oauth"`

**Source:** `~/.openclaw/completions/openclaw.zsh:88-113` (onboard command definition)

### Full `--auth-choice` taxonomy

The `openclaw onboard` command supports ~50 auth choices:

```
token | openai-codex | chutes | vllm | apiKey |
openai-api-key | mistral-api-key | openrouter-api-key |
kilocode-api-key | ai-gateway-api-key | cloudflare-ai-gateway-api-key |
moonshot-api-key | kimi-code-api-key | gemini-api-key |
zai-api-key | xiaomi-api-key | minimax-api | synthetic-api-key |
venice-api-key | together-api-key | huggingface-api-key |
opencode-zen | xai-api-key | litellm-api-key | qianfan-api-key |
volcengine-api-key | byteplus-api-key | moonshot-api-key-cn |
github-copilot | google-gemini-cli | zai-coding-global | zai-coding-cn |
zai-global | zai-cn | minimax-portal | qwen-portal | copilot-proxy |
custom-api-key | skip | setup-token | oauth | claude-cli | codex-cli |
minimax-cloud | minimax
```

**Source:** `~/.openclaw/completions/openclaw.zsh:97`

OCM only models 6 providers: `anthropic`, `openai`, `google`, `discord`, `telegram`, `whatsapp`.

**Source:** `cli/internal/commands/providers.go:16`

---

## 5. Credential Delivery — Two Approaches

### Current state (Approach A — Proxy Model, implemented and E2E tested)

```
DB (encrypted) → decrypt at machine start → agent CreateVM request → metadata server
    ↓
    ├── /v1/providers → { "llm": { "anthropic": "http://169.254.169.253:4000/anthropic" } }
    ├── /v1/llm → provider list with base URLs
    ├── API proxy (port 4000) → intercepts requests, injects real keys, proxies upstream
    │
    └── Gateway process → ✅ Configured with provider BASE_URL env vars + auth-profiles.json
```

**What works (full chain validated by E2E tests — `backend/internal/gatewaye2e/`):**
- Credential storage, encryption, validation: `backend/internal/api/credentials.go`
- Credential decryption at machine start: `backend/internal/api/server.go:761-774`
- Credential delivery to agent: `backend/internal/agentclient/client.go:106-132`
- Metadata server stores credentials in memory: `backend/internal/metadata/metadata.go:38-39`
- API proxy injects real keys per provider: `backend/internal/apiproxy/providers.go`
- Provider URL map served via metadata: `backend/internal/metadata/server_linux.go:121-168`
- Init script fetches provider URLs: `scripts/init-openclaw.sh:306-341`
- Init script writes `auth-profiles.json`: `scripts/init-openclaw.sh:535-547`
- Init script passes provider `BASE_URL` env vars to gateway process: `scripts/init-openclaw.sh:553-581`
- Anthropic `api_key` credential type: E2E tested, 200 OK
- Anthropic `subscription_key` credential type: E2E tested, 200 OK (Bearer + `anthropic-beta` header)
- OpenAI `api_key` credential type: proxy forwarding validated (needs valid key for upstream 200)

**What's still broken:**
- ~~Gateway process env block doesn't include provider `BASE_URL` overrides~~ — **FIXED**
- ~~Gateway doesn't know to route LLM calls through the proxy~~ — **FIXED**
- ~~`auth-profiles.json` is never written~~ — **FIXED**
- `CredentialEntry` too simple for OAuth (see below)

### Credential type handling in API proxy

| Provider | `credential_type` | Auth injection | Source | E2E tested? |
|----------|-------------------|---------------|--------|-------------|
| Anthropic (api_key) | `api_key` | `x-api-key: <key>` | `providers.go:49` | **Yes** — 200 OK |
| Anthropic (subscription) | `subscription_key` | `Authorization: Bearer <key>` + `anthropic-beta: oauth-2025-04-20` | `providers.go:45-48` | **Yes** — 200 OK |
| OpenAI (any) | `api_key` | `Authorization: Bearer <key>` | `providers.go:115-117` | **Partial** — proxy forwards correctly, needs valid key |
| Google | `api_key` | `?key=<key>` query param | `providers.go:249-252` | No |
| Telegram | `token` | Key embedded in URL path `/bot<key>/` | `providers.go:168-178` | No |
| Discord | `token` | `Authorization: Bot <key>` | `providers.go:208` | No |
| WhatsApp | `token` | `Authorization: Bearer <key>` | `providers.go:232` | No |

### OCM `CredentialEntry` data model

**Source:** `backend/internal/metadata/metadata.go:21-27`

```go
type CredentialEntry struct {
    Value          string `json:"value"`
    CredentialType string `json:"credential_type"` // api_key, subscription_key, token, oauth
}
```

This is a single `Value` string. For OAuth providers like `openai-codex`, this can't capture:
- Access token + refresh token (two separate values)
- Expiry timestamp
- Account ID

The `AccountCredential` DB model does have `RefreshToken` and `ExpiresAt` columns (`backend/internal/store/store.go:163-176`), but these aren't propagated through `CredentialEntry` to the proxy or metadata server.

### OAuth token refresh

**Source:** `backend/internal/api/oauth.go`

The refresh infrastructure exists but has gaps:

```go
var oauthTokenEndpoints = map[string]string{
    "google": "https://oauth2.googleapis.com/token",
    // ← No OpenAI endpoint configured
}
```

The TODO at `oauth.go:42-44` notes: _"Wire this into the proxy handler (apiproxy/handler.go) to automatically refresh OAuth credentials before forwarding requests."_

---

## 6. Workspace Files — Agent Personality System

The OpenClaw workspace (`~/.openclaw/workspace/`) defines the agent's personality and operating instructions. These files are loaded every session.

### File purposes

| File | Purpose | Loaded when |
|------|---------|-------------|
| `AGENTS.md` | Master operating instructions — memory system, safety rules, group chat behavior, heartbeat protocol | Every session, first |
| `SOUL.md` | Agent personality — core truths, boundaries, behavioral style | Every session, after AGENTS.md |
| `IDENTITY.md` | Agent name, creature type, vibe, emoji, avatar | Every session |
| `USER.md` | Human's name, timezone, pronouns, preferences | Every session |
| `BOOTSTRAP.md` | First-run onboarding conversation guide — deleted after initial setup | First run only |
| `TOOLS.md` | Environment-specific notes (camera names, SSH hosts, TTS voices) | As needed |
| `HEARTBEAT.md` | Periodic task checklist for proactive agent behavior | On heartbeat polls |
| `memory/YYYY-MM-DD.md` | Daily interaction logs | Recent days |
| `MEMORY.md` | Curated long-term memory (personal, main-session only) | Main session only |

**Sources:** All files at `~/.openclaw/workspace/`

### Key design insights from workspace files

1. **Memory is file-based, not DB-based.** The agent reads markdown files at session start. Files _are_ the memory system. (`AGENTS.md:22-36`)

2. **Security scoping for memory.** `MEMORY.md` is only loaded in the main (direct chat) session, never in shared contexts like Discord or group chats, to prevent personal data leakage. (`AGENTS.md:30-36`)

3. **Heartbeats enable proactive behavior.** The gateway sends periodic heartbeat polls. The agent checks `HEARTBEAT.md` for tasks, performs background work (email checks, calendar, memory maintenance), and can proactively reach out. (`AGENTS.md:128-208`)

4. **Cron vs heartbeat.** Heartbeats are batched, approximate-timing checks. Cron is for exact schedules and isolated tasks. (`AGENTS.md:137-154`)

5. **Platform-specific formatting.** Agents know to avoid markdown tables in Discord/WhatsApp, suppress embed previews with `<>` links in Discord, and use bold/CAPS instead of headers in WhatsApp. (`AGENTS.md:122-127`)

### Gap: OCM doesn't manage workspace files

The init script creates `$OPENCLAW_DIR/workspace` but doesn't seed any files:

```bash
# scripts/init-openclaw.sh:294
mkdir -p "$OPENCLAW_DIR/workspace"
```

For hosted machines, IDENTITY.md (agent name, avatar) is partially handled by the config assembly's `Identity` struct (`assembler.go:152-156`), but the workspace markdown files are a separate system. Users would need a way to customize SOUL.md, USER.md, etc. through the OCM dashboard or CLI.

---

## 7. Device Pairing & Auth

OpenClaw has a multi-device authentication system with Ed25519 keypairs, roles, and scoped permissions.

**Source:** `~/.openclaw/devices/paired.json`, `~/.openclaw/identity/`

### Observed device types

| Client ID | Client Mode | Platform | Scopes |
|-----------|------------|----------|--------|
| `openclaw-probe` | `probe` (CLI) | darwin | `operator.admin`, `.read`, `.write`, `.approvals`, `.pairing` |
| `openclaw-control-ui` | `webchat` | MacIntel | `operator.admin`, `.approvals`, `.pairing` |
| `openclaw-macos` | `ui` (desktop app) | macOS 15.3.1 | `operator.admin`, `.approvals`, `.pairing` |

### How OCM handles this

OCM disables device auth entirely:

```go
// backend/internal/configassembly/assembler.go:17-18
"dangerouslyDisableDeviceAuth": true,
"allowInsecureAuth":            true,
```

This is appropriate for hosted machines where the gateway is behind Cloudflare Tunnel + auth proxy, but worth documenting as a deliberate security tradeoff.

---

## 8. Execution Approvals

OpenClaw has a command approval gate via Unix socket.

**Source:** `~/.openclaw/exec-approvals.json`

```json
{
  "agents": {},
  "socket": {
    "path": "/Users/mantiz/.openclaw/exec-approvals.sock",
    "token": "<approval-token>"
  },
  "version": 1
}
```

The `gateway.nodes.denyCommands` in `openclaw.json` provides a simpler deny-list approach:

```json
"denyCommands": ["camera.snap", "camera.clip", "screen.record",
                 "calendar.add", "contacts.add", "reminders.add"]
```

### Gap: OCM platform defaults don't configure `nodes.denyCommands`

For hosted machines, we should configure command restrictions. The current platform defaults (`assembler.go:11-34`) don't set `gateway.nodes` at all.

---

## 9. Summary of Gaps

### ~~Critical~~ Resolved (validated by E2E tests)

| Gap | Resolution | Validated by |
|-----|------------|--------------|
| ~~Gateway can't reach LLM providers~~ | Init script now passes provider `BASE_URL` env vars to gateway process + writes `auth-profiles.json` | `gatewaye2e/`: ProxyAnthropicApiKey, ProxyAnthropicSubscriptionKey pass with 200 OK |
| ~~auth-profiles.json never written~~ | Init script generates it at `scripts/init-openclaw.sh:535-547` with `type: "api_key"` for all providers | `gatewaye2e/`: gateway boots and routes LLM calls through proxy |
| ~~Credential type handling unverified~~ | Anthropic `api_key` → `x-api-key`, `subscription_key` → `Bearer` + `anthropic-beta` header. OpenAI → `Bearer` always. | `gatewaye2e/`: 4 success-path + 3 failure-path proxy tests |

### Critical (still blocks functionality)

| Gap | Description | Files involved |
|-----|-------------|----------------|
| OpenAI CLI wizard is wrong | `chatgpt setup-token` doesn't exist; should be OAuth flow or removed entirely | `cli/internal/commands/providers_setup.go:29-33` |
| `CredentialEntry` too simple for OAuth | Single `Value` field can't hold access + refresh tokens + expiry. DB has the columns (`AccountCredential.RefreshToken`, `.ExpiresAt`) but they don't flow through to proxy/metadata | `backend/internal/metadata/metadata.go:24-27` |
| No OpenAI OAuth refresh endpoint | `oauthTokenEndpoints` only has Google | `backend/internal/api/oauth.go:19-21` |

### Important (affects hosted machine quality)

| Gap | Description | Files involved |
|-----|-------------|----------------|
| Config assembly incomplete | Missing `auth.profiles`, `agents.defaults.model`, `session`, `commands` sections | `backend/internal/configassembly/assembler.go` |
| Workspace files not seeded | `SOUL.md`, `IDENTITY.md`, etc. not created for hosted machines | `scripts/init-openclaw.sh:294` |
| No `denyCommands` for hosted VMs | Security-sensitive commands not restricted | `assembler.go` platformDefaults |
| OAuth refresh not wired to proxy | Proxy can't auto-refresh expiring OAuth tokens | `backend/internal/api/oauth.go:42-44` |
| Provider taxonomy too narrow | OCM models 6 providers; OpenClaw supports ~50 auth choices | `cli/internal/commands/providers.go:16` |

### Nice to have

| Gap | Description |
|-----|-------------|
| No cron/heartbeat configuration | Hosted machines can't use periodic task system |
| No execution approval integration | Command approval gate not configured |
| `configure` command not modeled | OpenClaw has `openclaw configure` for section-by-section config; OCM has no equivalent |

---

## 10. Reference: Key Source Files

### OCM Backend
- `backend/internal/configassembly/assembler.go` — Config generation for MicroVMs
- `backend/internal/api/credentials.go` — Credential CRUD + validation
- `backend/internal/api/oauth.go` — OAuth token refresh
- `backend/internal/api/server.go:730-779` — Credential decryption at machine start
- `backend/internal/apiproxy/providers.go` — Per-provider key injection
- `backend/internal/apiproxy/handler.go` — Proxy request handler
- `backend/internal/metadata/metadata.go` — `CredentialEntry` struct, metadata store
- `backend/internal/metadata/server_linux.go` — Metadata HTTP endpoints
- `backend/internal/agentclient/client.go:106-132` — CreateVM with credentials
- `backend/internal/agentapi/handlers.go:39-46` — Agent receives credentials
- `backend/internal/orchestrator/types.go:58-59` — VM config with LLM/channel keys

### OCM CLI
- `cli/internal/commands/providers_setup.go` — Setup wizard (has wrong OpenAI `TokenCmd`)
- `cli/internal/commands/providers.go` — Provider list + CRUD commands
- `cli/internal/commands/validate.go` — Client-side key validation
- `cli/internal/commands/pair.go` — Machine pairing (also uses `subscriptionInfo`)

### OCM Init Script
- `scripts/init-openclaw.sh:289-301` — Creates `.openclaw/` directory
- `scripts/init-openclaw.sh:306-341` — Provider env vars in `/etc/profile.d/`
- `scripts/init-openclaw.sh:553-581` — Gateway process startup (missing provider env vars)

### OCM Documentation
- `docs/ocm-llm-configuration.md` — LLM credential delivery architecture
- `docs/designs/account-credentials-and-billing.md` — Credential system design
- `docs/architecture.md` — Overall system architecture

### OpenClaw Local Install
- `~/.openclaw/openclaw.json` — Main config (reference for what OCM needs to generate)
- `~/.openclaw/agents/main/agent/auth-profiles.json` — LLM credential store (format spec)
- `~/.openclaw/completions/openclaw.zsh` — Full CLI command surface (auth-choice taxonomy)
- `~/.openclaw/workspace/` — Workspace file templates (AGENTS.md, SOUL.md, etc.)
- `~/.openclaw/devices/paired.json` — Device auth model
- `~/.openclaw/exec-approvals.json` — Command approval system

### Database Schema
- `backend/internal/store/store.go:163-176` — `AccountCredential` struct (has `RefreshToken`, `ExpiresAt`)
- `backend/migrations/007_account_credentials.sql` — Credentials table DDL

---

## 11. OCM CLI Interface Reference

The `ocm` CLI is built with Go + [cobra](https://github.com/spf13/cobra). Source: `cli/internal/commands/`.

**Global flags:** `--config <path>`, `--json` (JSON envelope output on all commands)

### Command Tree

```
ocm                                          # Show banner + help
├── version                                  # Print version, commit, build time
├── login [--token TOKEN]                    # Browser-based CF Access login (or --token for non-interactive)
├── logout                                   # Clear stored token
│
├── config
│   ├── show                                 # Show API URL, token, account, default machine
│   ├── set-api-url URL                      # Set API base URL
│   ├── set-account SLUG                     # Set default account by slug
│   ├── machine-show [--machine NAME]        # Show assembled OpenClaw config JSON
│   └── push [--machine NAME]               # Push config to a running machine
│
├── machines
│   ├── list                                 # List all machines (NAME, STATUS, SIZE, CREATED)
│   ├── create --name NAME [--size SIZE]     # Create machine (standard/large/xlarge)
│   ├── get [NAME]                           # Get machine details
│   ├── start [NAME]                         # Start a machine
│   ├── stop [NAME]                          # Stop a machine
│   ├── delete [NAME]                        # Delete a machine
│   ├── use NAME                             # Set default machine
│   ├── logs [NAME] [-f] [--lines N]        # Stream logs (SSE)
│   ├── ssh [NAME] [--user USER] [-- ARGS]  # SSH via CF tunnel + short-lived cert
│   ├── ssh-proxy HOSTNAME                   # (hidden) ProxyCommand for SSH
│   ├── credentials
│   │   ├── list [--machine NAME]            # List linked credentials
│   │   ├── link --credential ID [--machine] # Link credential to machine
│   │   └── unlink --credential ID [--machine] # Unlink credential
│   └── secrets
│       ├── list [--machine NAME]            # List secrets (key, created, updated)
│       ├── set KEY [--machine NAME]         # Set secret (interactive or --value-from-stdin)
│       └── delete KEY [--machine NAME]      # Delete secret
│
├── providers
│   ├── list [--all]                         # List credentials (or --all for built-in + custom providers)
│   ├── add PROVIDER [--type TYPE]           # Add credential (interactive key + label prompt)
│   ├── remove ID                            # Remove credential by ID
│   ├── validate PROVIDER                    # Re-validate / refresh credential
│   ├── setup PROVIDER [--machine NAME]      # Guided wizard: instructions → key → validate → store → link → push
│   │   [--type TYPE] [--label LABEL]
│   │   [--key-from-stdin]
│   ├── register NAME --upstream-host HOST   # Register custom provider
│   │   --auth-method METHOD [--scheme]
│   │   [--auth-header] [--path-prefix]
│   │   [--is-llm]
│   └── unregister NAME                      # Remove custom provider
│
├── channels
│   ├── available                             # List all channels in registry
│   ├── list [--machine NAME]                # List channels enabled on machine
│   ├── enable CHANNEL [--machine NAME]      # Enable channel
│   ├── disable CHANNEL [--machine NAME]     # Disable channel
│   ├── configure CHANNEL KEY VALUE          # Set config override on channel
│   │   [--machine NAME]
│   └── setup CHANNEL [--machine NAME]       # Guided wizard: instructions → token → validate →
│       [--token-from-stdin] [--label]       #   store → link → enable → push
│
├── skills
│   ├── available                             # List all skills in registry
│   ├── list [--machine NAME]                # List skills on machine
│   ├── enable SKILL [--machine NAME]        # Enable skill
│   └── disable SKILL [--machine NAME]       # Disable skill
│
├── tools
│   ├── available                             # List all tools in registry
│   ├── list [--machine NAME]                # List tools on machine
│   ├── enable TOOL [--machine NAME] [--mode] # Enable tool
│   └── disable TOOL [--machine NAME]        # Disable tool
│
├── identity
│   ├── set [--machine NAME] --name NAME     # Set identity name/avatar
│   │   [--avatar URL]
│   └── show [--machine NAME]               # Show identity
│
├── budget
│   ├── set --limit DOLLARS [--machine NAME] # Set spending budget
│   └── clear [--machine NAME]              # Remove budget
│
├── usage [--machine NAME]                   # Show LLM usage records (provider, model, tokens, cost)
│
└── pair [NAME]                              # Interactive guided setup wizard
                                             #   Step 1: Select LLM providers → setup each
                                             #   Step 2: Select channels → setup each
                                             #   Step 3: Set identity (optional)
                                             #   Step 4: Set budget (optional)
                                             #   Step 5: Push config
```

### Built-in Providers

| Provider | Type | Validation Endpoint | Auth Header |
|----------|------|-------------------|-------------|
| `anthropic` | LLM | `GET /v1/models` | `x-api-key` (api_key) or `Bearer` (subscription_key/oauth) |
| `openai` | LLM | `GET /v1/models` | `Bearer` |
| `google` | LLM | `GET /v1beta/models` | `x-goog-api-key` |
| `discord` | Channel | `GET /api/v10/users/@me` | `Bot <token>` |
| `telegram` | Channel | `GET /bot<token>/getMe` | Token in URL path |
| `whatsapp` | Channel | `GET /v18.0/me` | `Bearer` |

**Source:** `cli/internal/commands/validate.go`

### Credential Types

| Type | Description | Used by |
|------|------------|---------|
| `api_key` | Standard API key (default for LLM providers) | anthropic, openai, google |
| `subscription_key` | OAuth/setup-token (e.g. `sk-ant-oat*`) | anthropic |
| `token` | Bot token (default for channel providers) | discord, telegram, whatsapp |
| `oauth` | OAuth flow token | (future) |

**Source:** `cli/internal/commands/providers.go:18`

### Subscription Auth Flow

Providers in `subscriptionInfo` get a two-choice prompt during `setup` and `pair`:

1. **Subscription key** — user runs a local CLI command (e.g. `claude setup-token`) to generate a pasteable token
2. **API key** — user creates a pay-per-use key from the provider console

Currently configured:

| Provider | Subscription CLI Command | Status |
|----------|------------------------|--------|
| `anthropic` | `claude setup-token` | Works (generates `sk-ant-oat*` OAT token) |
| `openai` | `chatgpt setup-token` | **Broken** — this command doesn't exist |

**Source:** `cli/internal/commands/providers_setup.go:16-34`

### Machine Resolution

Most commands that target a machine use `resolveMachine()` with this priority:

1. Explicit `--machine NAME` or positional arg → match by name (case-insensitive) then slug
2. Default machine from config (`cfg.DefaultMachineID`)
3. Auto-select if only one machine exists
4. Error with available machine names

**Source:** `cli/internal/commands/machines.go:120-167`

### SSH Implementation

`ocm machines ssh` uses Cloudflare Access short-lived SSH certificates:

1. Derives SSH hostname: `ssh-{slug}.{domain}` (default domain: `openclawmachines.com`)
2. Fetches CF Access token (cached in `~/.cloudflared/`, opens browser on first use)
3. Generates short-lived SSH cert signed by CF Access CA via `sshgen.GenerateShortLivedCertificate()`
4. `syscall.Exec` replaces process with `ssh` using:
   - `-i <cert-key-path>` — cert identity
   - `-o ProxyCommand=<self> machines ssh-proxy %h` — tunnel via own binary
   - `-o StrictHostKeyChecking=no`
5. Hidden `ssh-proxy` subcommand proxies stdin/stdout over CF Access WebSocket tunnel

**Dependencies:** `github.com/cloudflare/cloudflared` (sshgen, token packages)

**Source:** `cli/internal/commands/machines_ssh.go`, `machines_ssh_proxy.go`, `cli/internal/tunnel/`

### JSON Output Envelope

All commands support `--json` and use a standard envelope:

```json
{
  "status": "ok",        // or "error"
  "action": "machines.list",
  "result": { ... },     // command-specific payload
  "error": "..."         // only when status=error
}
```

**Source:** `cli/internal/commands/output.go:14-19`

### Machine Sizes

| Size | vCPUs | Memory |
|------|-------|--------|
| `standard` | 2 | 2048 MB |
| `large` | 4 | 4096 MB |
| `xlarge` | 8 | 8192 MB |

**Source:** `cli/internal/commands/machines.go:21-32`

### Config File

Stored at `~/.config/ocm/config.json`:

| Field | Purpose |
|-------|---------|
| `api_url` | Backend API base URL |
| `token` | CF Access JWT token |
| `token_expires` | Token expiry timestamp |
| `default_account_id` | Current account ID |
| `default_account_slug` | Current account slug |
| `default_machine_id` | Default machine ID |
| `default_machine_name` | Default machine name |
| `cf_app_domain` | Cloudflare app domain override |

**Source:** `cli/internal/config/`

---

## 12. CLI Simplification Analysis

### Problem Statement

By cross-referencing the OpenClaw onboarding wizard (`openclaw onboard --auth-choice`) with the current OCM CLI, several UX problems become apparent. The core issue: **the CLI exposes backend plumbing (credentials, linking, capabilities, config push) instead of user-intent operations ("connect my Anthropic key to my bot").**

### What OpenClaw Gets Right

OpenClaw's `onboard` command embodies one key insight: the user's mental model is "set up my bot", not "manage credentials, link them to machines, enable capabilities, and push config." It hides all the plumbing behind a single linear flow that covers login → machine selection → provider auth → model selection → channel setup → identity → verification.

It supports ~50 auth choices via `--auth-choice` with both interactive (browser OAuth) and non-interactive (pasteable token/key) paths, and all steps are optional if already completed.

### Current OCM CLI Problems

#### 1. Too many concepts leak to the user

To get a working bot, a user currently needs to understand: providers, credentials, credential types, credential IDs, machine linking, capabilities, config push, and the distinction between "adding" a credential vs "setting up" a provider vs "pairing" a machine. These are all backend implementation details.

What the user actually thinks:
- "I want to connect my Anthropic key to my bot"
- "I want to add Telegram"
- "Is my bot working?"

#### 2. `pair` is incomplete and misnamed

- Assumes login + machine already exist (skips the two hardest first-time steps)
- Doesn't set the LLM model
- Named "pair" — an OpenClaw device-pairing term that means nothing in OCM's hosted context
- Really a half-baked setup wizard that covers steps 3-5 of a 7-step journey

**Source:** `cli/internal/commands/pair.go`

#### 3. Config push is a footgun

Every configuration change (provider, channel, identity, budget) requires a manual `ocm config push` to take effect on the running machine. Users will forget.

The guided wizards (`providers setup`, `channels setup`) auto-push, but the individual commands (`providers add`, `channels enable`, `identity set`, `budget set`) don't. This creates a silent failure mode where the DB state is updated but the running machine is stale.

#### 4. Two paths for everything, neither complete

| Operation | Command A | Command B | Command C |
|-----------|-----------|-----------|-----------|
| Add LLM key | `providers add` (manual, no validate/link/push) | `providers setup` (guided, validates/links/pushes if `--machine`) | `pair` (guided, validates/links, pushes at end) |
| Add channel | `channels enable` (no credential) | `channels setup` (guided, full pipeline) | `pair` (guided, full pipeline) |

Three ways to do the same thing, all with different gaps and behaviors.

#### 5. No observability

No way to ask "is my bot healthy?" or "what's configured and what's missing?" Users hit silent failures (e.g. credential not linked, config not pushed, machine not running) and have no diagnostic path other than reading logs.

#### 6. OpenAI auth flow is broken

`subscriptionInfo` tells users to run `chatgpt setup-token`, which doesn't exist. OpenAI subscription auth in OpenClaw uses an OAuth browser flow (`openclaw onboard --auth-choice openai-codex`), not a pasteable token. There is no equivalent CLI tool for OpenAI.

**Source:** `cli/internal/commands/providers_setup.go:28-33`

### Proposed Changes

#### Principle: Collapse the plumbing

Instead of exposing the credential→link→capability→push pipeline, make it implicit. Every command that changes machine configuration should auto-push by default (with `--no-push` escape hatch for batch operations).

#### Change 1: Replace `pair` with `ocm setup`

A complete onboarding wizard covering the full journey:

```
ocm setup
```

```
Step 1: Login          — skip if already authenticated
Step 2: Machine        — select existing or create new
Step 3: LLM Provider   — pick provider, paste key, validate
Step 4: Channel        — optional, pick + configure
Step 5: Identity       — optional, set name
Step 6: Verify         — push config, show summary, confirm it's live
```

Non-interactive mode for automation:
```
ocm setup --provider anthropic --key sk-ant-... --channel telegram --token 123:ABC --name "My Bot"
```

**Key reuse:** All step implementations come from existing functions: `pairSetupProvider()`, `pairSetupChannel()`, `pairSetIdentity()`, `pairSetBudget()` in `pair.go`. The new command composes them with better flow control (skip already-done steps, include login + machine creation).

Deprecate `pair` with: `"Deprecated: Use 'ocm setup' instead for a more complete experience."` in its `Long` description.

#### Change 2: Auto-push on every mutation

Add auto-push after every command that changes machine configuration:

| Command | Currently pushes? | Proposed |
|---------|-------------------|----------|
| `providers add` | No | Auto-push if machine resolvable |
| `providers setup --machine` | Yes | Keep |
| `channels enable` | No | Auto-push |
| `channels disable` | No | Auto-push |
| `channels configure` | No | Auto-push |
| `identity set` | No | Auto-push |
| `budget set` | No | Auto-push |
| `budget clear` | No | Auto-push |

Implementation: add a `pushConfigIfMachine()` helper that calls `POST /api/accounts/{id}/machines/{id}/config/push` and prints "Configuration pushed." on success. Add `--no-push` flag to all mutation commands.

#### Change 3: Add `ocm status`

Single dashboard view combining machine info + providers + channels + usage:

```
$ ocm status

Machine: My Bot (running)
  URL:     https://my-bot.openclawmachines.com
  Size:    standard (2 vCPU / 2GB)
  Config:  v12

Providers:
  anthropic  ****sk4f  validated 2h ago
  openai     ****xk9m  validated 1d ago

Channels:
  telegram   enabled
  discord    enabled

Usage (24h): $2.34 / 142 requests
```

Calls existing endpoints in parallel (no backend changes):
- `GET /api/accounts/{id}/machines` — machine list + status
- `GET /api/accounts/{id}/credentials` — linked credentials
- `GET /api/accounts/{id}/machines/{id}/capabilities` — enabled channels/skills

Supports `--all` (all machines), `--json`, and optional `[NAME]` positional arg.

**New file:** `cli/internal/commands/status.go`

#### Change 4: Add `ocm doctor`

Diagnostic command that runs sequential health checks:

```
$ ocm doctor

OCM Doctor

[PASS] Configuration file exists
[PASS] API is reachable
[PASS] Token is valid (user@example.com)
[WARN] Token expires in 18 hours — run 'ocm login' to refresh
[PASS] Machine "My Bot" exists and running
[PASS] Anthropic credential linked (validated 2h ago)
[WARN] No channels enabled — run 'ocm setup' to add one
[PASS] Config pushed (version matches)

7 passed, 2 warnings, 0 failures
```

| Check | Endpoint | Pass/Warn/Fail |
|-------|----------|----------------|
| Config file exists | Local file | PASS/FAIL |
| API reachable | `GET /health` | PASS/FAIL |
| Token valid | `GET /api/auth/me` | PASS/FAIL |
| Token not expiring soon | Local config `token_expires` | PASS/WARN (<24h) |
| Account configured | Local config `default_account_id` | PASS/FAIL |
| Machine exists | `GET /api/accounts/{id}/machines` | PASS/FAIL |
| Machine running | Machine `.status` field | PASS/WARN |
| LLM credential linked | `GET /api/accounts/{id}/credentials` | PASS/WARN |
| Credential recently validated | Credential `.last_validated` | PASS/WARN (>7d) |
| Channel enabled | Capabilities endpoint | PASS/WARN |
| Config pushed | Machine `.config_version` | PASS/WARN |

Supports `--machine NAME` and `--json`.

**New file:** `cli/internal/commands/doctor.go`

#### Change 5: Fix the OpenAI auth flow

Remove the `"openai"` entry from `subscriptionInfo` in `providers_setup.go:28-33`. This entry tells users to run `chatgpt setup-token` which doesn't exist. Without it, `ocm providers setup openai` will skip the subscription-key prompt and go straight to the API key instructions (which are correct).

Also remove the same reference in `pair.go` where `subscriptionInfo` is consumed.

If/when we implement an OAuth browser flow for OpenAI (like OpenClaw's `openai-codex` auth choice), we can add it back properly as a browser-based flow, not a nonexistent CLI command.

**Files:** `cli/internal/commands/providers_setup.go:28-33`, `cli/internal/commands/pair.go:171`

#### Change 6: Add shell completions

```
ocm completion bash|zsh|fish|powershell
```

Uses cobra's built-in `GenBashCompletion()`, `GenZshCompletion()`, `GenFishCompletion()`. Zero backend changes. ~30 lines of code. Meaningfully improves discoverability.

**New file:** `cli/internal/commands/completion.go`

#### Change 7: Simplify `providers add` vs `providers setup`

Currently two commands with overlapping purposes:
- `providers add` — prompts for key + label, stores credential, no validation, no linking
- `providers setup` — shows instructions, validates, stores, optionally links + pushes

Merge the behavior: make `providers add` always validate before storing (like `setup` does), and auto-link + push if a machine is resolvable. Keep `providers setup` as an alias that also shows the instruction text.

This way there's one command for scripts (`providers add anthropic --key sk-... --label prod`) and one for interactive users (`providers setup anthropic`), but both produce the same result.

### Prioritization

| Change | Impact | Effort | Priority |
|--------|--------|--------|----------|
| Fix OpenAI auth flow | Unblocks broken flow | Tiny (delete 5 lines) | P0 |
| Auto-push on mutations | Eliminates silent failures | Small (add push call to ~8 commands) | P0 |
| Shell completions | Discoverability | Tiny (~30 lines) | P1 |
| `ocm status` | Instant visibility | Medium (new file, ~150 lines) | P1 |
| `ocm doctor` | Self-service debugging | Medium (new file, ~200 lines) | P1 |
| `ocm setup` (replace `pair`) | Complete onboarding in one command | Medium (new file, reuses existing functions) | P1 |
| Merge `providers add`/`setup` | Less confusion | Small | P2 |

### What to deprioritize from original plan

| Feature | Reason |
|---------|--------|
| `ocm models` (list/show/set) | Gateway picks a default model. Nice-to-have, not blocking. |
| `ocm workspace` (list/cat/edit) | Niche — the web UI file browser already handles this. |
| `ocm message send` | Users message through the channel (Telegram, Discord), not the CLI. |
| Adding 4 new built-in providers | The `providers register` custom provider system already lets power users add openrouter, mistral, together, xai. Built-in is polish. |
| `ocm logs` enhancements (--level, --grep) | Useful but not urgent — users can pipe through grep. |

### Theme

**Fewer commands that each do the complete job, rather than many commands that each do one step of a multi-step pipeline.**
