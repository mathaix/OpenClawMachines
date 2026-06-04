# OCM Client — Requirements Document

## Overview

Build a unified client platform (CLI + iOS + Android) from a shared Go core that handles provider setup, channel pairing, OAuth flows, fleet bootstrapping, and local device integration — then pushes credentials securely to the backend.

**Principle:** Business logic lives in a shared Go library (`ocmkit`). Each platform (CLI, iOS, Android) provides a thin native shell for UI and hardware access. The backend API is the same for all clients.

---

## User Requirements

### 1. Provider Setup (LLM APIs)

Users must be able to configure LLM providers (Anthropic, OpenAI, Google) via the CLI with two access methods:

#### Method A: Bring Your Own Key (BYOK)
- User provides their own API key from the provider's console
- CLI displays provider-specific instructions (where to get the key, direct link)
- Masked input for pasting the key
- CLI validates the key locally against the provider's API before sending to backend
- Key is sent to backend for encrypted storage
- Optionally link to a specific machine and push config in one step
- User is billed directly by the provider — OCM has no visibility into spend

#### Method B: OCM Subscription (Managed Access)
- User subscribes to OCM and gets access to LLM providers through OCM's pooled API keys
- No API key needed from the user — OCM manages the underlying provider credentials
- CLI handles the subscription flow:
  1. Start local HTTP callback server (e.g., `:9876`)
  2. Open browser to OCM's subscription/billing page
  3. User selects plan and providers (e.g., "Pro: Anthropic + OpenAI")
  4. User completes payment (Stripe checkout or existing subscription)
  5. CLI receives OAuth callback confirming active subscription
  6. Backend configures the machine to route through OCM's LLM proxy
- Usage is metered and billed through OCM (per-token or per-request pricing)
- Budget controls enforced by OCM's API proxy (existing `apiproxy` system)
- Backend handles token lifecycle — user never sees or manages provider keys

#### Method C: OCM Platform Default (Nebius-backed)
- A newly created machine must be usable without BYOK or subscription setup.
- The CLI should show the platform default as "OCM platform model" or the
  user-facing model label, not as a raw Nebius credential requirement.
- For Hermes machines, the default must work out of the box through the same
  metadata-injected platform Nebius key/proxy path used by OpenClaw.
- Current default: `openai/gpt-oss-120b` (mapped internally to
  `nebius/openai/gpt-oss-120b`).

#### How Subscriptions Work

```
BYOK mode:
  User's API key → encrypted in DB → injected into VM → VM calls provider directly

Subscription mode:
  User subscribes → OCM provisions proxy access → VM calls OCM proxy → proxy calls provider
  ├─ OCM manages pooled provider keys
  ├─ OCM meters usage per-account/per-machine
  ├─ Budget limits enforced at proxy layer
  └─ User sees usage dashboard, not raw API keys
```

#### Provider Access Matrix

| Provider | BYOK (API Key) | OCM Subscription | Notes |
|----------|---------------|-----------------|-------|
| Anthropic | Yes | Yes | Claude models via proxy |
| OpenAI | Yes | Yes | GPT/o-series via proxy |
| Google | Yes | Yes | Gemini via proxy |
| OCM platform model | No | Included | Nebius-backed default; no user key required |

#### Subscription Management Commands

```
ocm subscription status                    # Show current plan, usage, billing
ocm subscription plans                     # List available subscription plans
ocm subscription subscribe [--plan PLAN]   # Subscribe (opens browser for payment)
ocm subscription cancel                    # Cancel subscription
ocm subscription usage [--machine SLUG]    # Detailed usage breakdown
```

### 2. Channel Setup (Messaging Platforms)

Users must be able to pair messaging channels with guided, channel-specific wizards that handle the full flow: credential capture → validation → channel enable → machine link → config push.

#### Telegram
- Guide user through @BotFather setup (create bot, get token)
- Paste bot token (masked input)
- Validate token locally via `getMe` API
- Save credential, enable channel on machine, push config

#### Discord
- Guide user through Discord Developer Portal (create application, add bot, get token)
- Paste bot token (masked input)
- Validate token locally via `users/@me` API
- Save credential, enable channel on machine, push config

#### Slack
- OAuth flow: CLI starts local callback server, opens browser to Slack OAuth install URL
- User installs app in their Slack workspace
- CLI captures bot token from OAuth callback
- Save credential, enable channel on machine, push config

#### WhatsApp
- Guide user through Meta Business Suite setup
- Paste access token (masked input)
- Validate token locally via WhatsApp Business API
- Save credential, enable channel on machine, push config

### 3. Device Pairing (`ocm pair`)

A single command that bootstraps a machine with all necessary configuration:

```
ocm pair <machine-slug>
```

Interactive wizard flow:
1. Default to the OCM platform model so the machine can chat immediately.
2. Optionally select additional LLM providers to configure (multi-select:
   anthropic, openai, google, openrouter, etc.).
3. For each selected provider: run the setup flow (API key or OAuth).
4. Select channels to enable (multi-select: telegram, discord, slack, whatsapp).
5. For each selected channel: run the channel-specific pairing wizard.
6. Set machine identity (name, avatar) — optional.
7. Set spending budget — optional.
8. Push all configuration to the running VM.
9. Verify VM is live and responding, including a model health check against
   the default platform model when no BYOK model was selected.

### 4. Machine Connectivity

The CLI must connect to both OpenClaw and Hermes machines through the same
top-level command family. Users should not need to know which internal service
port a machine kind uses.

Required behavior:

- `ocm machines ssh <slug>` works for `kind=openclaw` and `kind=hermes`. For
  Hermes, the shell environment must have `hermes` on PATH and the default
  working directory should be the machine workspace.
- `ocm machines dashboard <slug> --open` opens the correct authenticated web
  UI. For OpenClaw this is the control UI; for Hermes this is the tunneled
  Hermes dashboard with webchat enabled. The command should target the same
  default "interface" surface users see in the web app, not a separate hidden
  route.
- `ocm machines serve chat <slug>` is kind-aware. For OpenClaw it keeps the
  current OpenClaw gateway behavior; for Hermes it targets the Hermes
  OpenAI-compatible chat endpoint.
- `ocm machines logs <slug> --follow` works for both kinds. For Hermes it
  should stream the relevant Hermes services (`hermes-gateway`,
  `hermes-dashboard`, `pty-server`, `authproxy`, tunnel).
- JSON output must include the resolved `kind`, selected surface, URL or SSH
  target, and whether the machine is running. Failed connections should return
  actionable hints, for example "start the machine" or "dashboard service is
  not ready".

Optional convenience:

```
ocm machines connect <slug>                 # interactive picker: dashboard, ssh, chat, logs
ocm machines connect <slug> --surface ssh
ocm machines connect <slug> --surface dashboard --open
```

### 5. Fleet Bootstrapping (Multi-Agent Orchestration)

Users should be able to describe a high-level goal and have an LLM (Claude Code, Codex) spin up and configure an entire fleet of purpose-built agents using the OCM CLI.

#### The Vision

The user gives a broad instruction like:

> "I want agents for property management"

The LLM figures out what agents are needed, creates them, configures each one with the right providers, channels, skills, identity, and system prompt — all via the CLI.

#### How It Works

The LLM uses `ocm` commands to:

1. **Create multiple machines** with descriptive names
2. **Configure each with role-specific settings** — different providers, channels, skills, budgets, and system prompts
3. **Wire them to the right communication channels** — tenant-facing agents on WhatsApp, internal agents on Slack, etc.
4. **Set per-agent identity** — name, avatar, personality appropriate to the role
5. **Push all configs and verify** each agent is healthy

#### Example: Property Management Fleet

User says: *"I want agents for property management"*

Claude Code runs:

```bash
# 1. Create the fleet
ocm machines create --name tenant-support --size standard --json
ocm machines create --name maintenance-coordinator --size standard --json
ocm machines create --name leasing-agent --size standard --json
ocm machines create --name bookkeeper --size standard --json

# 2. Start them all
ocm machines start tenant-support --json
ocm machines start maintenance-coordinator --json
ocm machines start leasing-agent --json
ocm machines start bookkeeper --json

# 3. Configure shared provider (all agents need an LLM)
ocm providers setup anthropic --method byok --key-from-stdin --json <<< "$ANTHROPIC_API_KEY"
# Link to all machines
ocm machines credentials link tenant-support --credential 42 --json
ocm machines credentials link maintenance-coordinator --credential 42 --json
ocm machines credentials link leasing-agent --credential 42 --json
ocm machines credentials link bookkeeper --credential 42 --json

# 4. Configure channels per role
# Tenant support — WhatsApp (tenant-facing) + Slack (internal escalation)
ocm channels setup whatsapp --token-from-stdin --machine tenant-support --json <<< "$WHATSAPP_TOKEN"
ocm channels setup slack --token-from-stdin --machine tenant-support --json <<< "$SLACK_TOKEN"
ocm channels groups enable slack tenant-support-escalations --machine tenant-support --policy open --json

# Maintenance coordinator — Slack only (internal)
ocm channels setup slack --token-from-stdin --machine maintenance-coordinator --json <<< "$SLACK_TOKEN"
ocm channels groups enable slack maintenance-requests --machine maintenance-coordinator --policy open --json

# Leasing agent — WhatsApp (prospect-facing) + email
ocm channels setup whatsapp --token-from-stdin --machine leasing-agent --json <<< "$WHATSAPP_TOKEN"

# Bookkeeper — Slack only (internal, finance channel)
ocm channels setup slack --token-from-stdin --machine bookkeeper --json <<< "$SLACK_TOKEN"
ocm channels groups enable slack finance --machine bookkeeper --policy mention --json

# 5. Set identities
ocm identity set --machine tenant-support --name "PropertyBot" --avatar house --json
ocm identity set --machine maintenance-coordinator --name "FixIt" --avatar wrench --json
ocm identity set --machine leasing-agent --name "LeaseBot" --avatar key --json
ocm identity set --machine bookkeeper --name "LedgerBot" --avatar calculator --json

# 6. Set system prompts (role-specific instructions)
ocm machines secrets set tenant-support system_prompt "You are a property management tenant support agent. Handle maintenance requests, lease questions, and complaints. Escalate urgent issues to the #tenant-support-escalations Slack channel." --json
ocm machines secrets set maintenance-coordinator system_prompt "You coordinate maintenance work orders. Receive requests from tenant support, dispatch to vendors, track completion. Post updates to #maintenance-requests." --json
ocm machines secrets set leasing-agent system_prompt "You are a leasing agent. Answer prospect inquiries about available units, schedule tours, and follow up on applications. Be friendly and professional." --json
ocm machines secrets set bookkeeper system_prompt "You handle rent tracking, expense categorization, and financial reporting. Only respond to authorized finance team members." --json

# 7. Set budgets
ocm budget set --machine tenant-support --limit 50 --json
ocm budget set --machine maintenance-coordinator --limit 30 --json
ocm budget set --machine leasing-agent --limit 50 --json
ocm budget set --machine bookkeeper --limit 20 --json

# 8. Push all configs
ocm config push --machine tenant-support --json
ocm config push --machine maintenance-coordinator --json
ocm config push --machine leasing-agent --json
ocm config push --machine bookkeeper --json

# 9. Verify all healthy
ocm machines get tenant-support --json
ocm machines get maintenance-coordinator --json
ocm machines get leasing-agent --json
ocm machines get bookkeeper --json
```

The entire fleet is live in minutes. The user never touched a config file.

#### Other Fleet Examples an LLM Could Bootstrap

| Instruction | Agents Created |
|-------------|---------------|
| "agents for property management" | tenant-support, maintenance-coordinator, leasing-agent, bookkeeper |
| "customer support team" | tier-1-support (WhatsApp/Telegram), escalation-agent (Slack), knowledge-manager (internal) |
| "e-commerce operations" | order-support (WhatsApp), inventory-tracker (Slack), returns-agent (email), analytics-bot (internal) |
| "dev team assistants" | code-reviewer (Slack/Discord), incident-responder (Slack + PagerDuty), docs-writer (internal), onboarding-buddy (Slack) |
| "marketing team" | social-media-manager (Telegram), content-writer (Slack), campaign-tracker (internal), competitor-analyst (internal) |

#### CLI Requirements for Fleet Bootstrapping

The CLI must support:

1. **Batch operations** — create/start/configure multiple machines without interactive prompts
2. **Credential sharing** — one credential linked to many machines (don't re-enter the same API key per agent)
3. **System prompts via secrets** — `ocm machines secrets set <slug> system_prompt "..."` to give each agent its role
4. **Scriptable from end to end** — an LLM can run the entire sequence above with zero human input (except initial credential entry)
5. **Fleet status overview** — `ocm machines list --json` shows all agents with their health, channels, and provider status in one call

### 5. Credential-Machine Linking

- Users can see which credentials are linked to which machines
- Credentials can be shared across machines or scoped to individual ones
- Linking/unlinking credentials automatically triggers config push to affected VMs

### 6. Local Device Integration

The OCM CLI runs on the user's local machine (Mac, Linux, Windows) and should be able to bridge local hardware interfaces to remote agents. This enables agents to see, hear, and speak through the user's device.

#### Supported Local Interfaces

| Interface | Capability | Use Case |
|-----------|-----------|----------|
| **Browser** | Open URLs, capture screenshots, scrape pages | Agent researches listings, fills forms, reads emails |
| **Microphone** | Stream audio input to agent | Voice commands, phone call transcription, meeting notes |
| **Speaker** | Play audio/TTS from agent | Voice responses, alerts, notifications |
| **Camera** | Capture photos/video | Property inspection photos, document scanning, video calls |
| **Screen** | Capture screen content | Agent sees what user sees, assists with on-screen tasks |
| **Clipboard** | Read/write clipboard | Agent copies data between apps |
| **Notifications** | macOS native notifications | Agent alerts user of important events |
| **File system** | Read/write local files | Agent processes local documents, exports reports |

#### Architecture

```
Local Mac (OCM CLI)                              Remote VM (Agent)
────────────────────                            ──────────────────

ocm connect my-bot --devices mic,speaker,camera
  │
  ├─ Microphone capture ──── WebSocket ────────→ Agent receives audio stream
  ├─ Camera capture ──────── WebSocket ────────→ Agent receives video/images
  ├─ Speaker playback ←───── WebSocket ────────← Agent sends TTS/audio
  ├─ Browser control ←────── WebSocket ────────← Agent sends browser commands
  │   └─ Screenshots ─────── WebSocket ────────→ Agent receives page content
  ├─ Screen capture ──────── WebSocket ────────→ Agent sees user's screen
  └─ Notifications ←──────── WebSocket ────────← Agent sends alerts
```

The CLI acts as a local device bridge — it accesses hardware via macOS APIs and streams data to/from the remote agent over the existing Cloudflare tunnel.

#### Commands

```bash
# Connect local devices to a running agent
ocm connect <machine-slug> [--devices mic,speaker,camera,screen,browser,clipboard]

# Stream microphone to agent (voice interaction)
ocm voice <machine-slug>                  # bidirectional: mic → agent, agent → speaker
ocm voice <machine-slug> --listen-only    # mic only, no TTS playback
ocm voice <machine-slug> --speak-only     # TTS only, no mic

# Open a browser session controlled by the agent
ocm browse <machine-slug>                 # agent can navigate, click, fill forms
ocm browse <machine-slug> --url "..."     # start at a specific URL

# Capture and send a photo/screenshot to the agent
ocm capture photo --machine <slug>        # take photo with camera, send to agent
ocm capture screen --machine <slug>       # screenshot, send to agent
ocm capture clipboard --machine <slug>    # send clipboard contents to agent

# Stream notifications from agent to macOS
ocm notify <machine-slug>                 # background process, shows native notifications
```

#### Voice Interaction Example

```
$ ocm voice tenant-support

  Voice connected to "PropertyBot" (tenant-support)
  Mic: MacBook Pro Microphone ✓
  Speaker: MacBook Pro Speakers ✓
  Press Ctrl+C to disconnect.

  [listening...]

  You: "What maintenance requests came in today?"

  PropertyBot: "You have 3 open requests today. Unit 4B reported a
  leaking faucet — I've already dispatched a plumber for tomorrow
  morning. Unit 2A has a broken AC, marked as urgent. And unit 7C
  requested a new smoke detector battery. Want me to go through
  the details?"

  You: "Schedule the AC repair for today if possible"

  PropertyBot: "Checking vendor availability... Rodriguez HVAC has
  an opening at 2 PM today. I'll confirm the appointment and notify
  the tenant in unit 2A via WhatsApp. Done."
```

#### Browser Control Example

```
$ ocm browse leasing-agent --url "https://zillow.com"

  Browser session started — LeaseBot is navigating
  ─────────────────────────────────────────────────
  [LeaseBot] Searching Zillow for comparable listings in your area...
  [LeaseBot] Found 12 comparable units. Capturing pricing data...
  [LeaseBot] Screenshot saved: /tmp/ocm-comparables-2026-02-25.png
  [LeaseBot] Summary: Average 2BR rent in 94103 is $3,450/mo.
             Your units are priced 5% below market.
             Recommend adjusting units 3A and 5B to $3,400/mo.
```

#### macOS Integration Details

| Interface | macOS API | Dependencies |
|-----------|----------|-------------|
| Microphone | AVFoundation / CoreAudio | Permission: Microphone access |
| Speaker | AVFoundation / NSSpeechSynthesizer | None (built-in) |
| Camera | AVFoundation | Permission: Camera access |
| Screen capture | ScreenCaptureKit (macOS 12.3+) | Permission: Screen Recording |
| Notifications | UserNotifications framework | Permission: Notifications |
| Clipboard | NSPasteboard | None (built-in) |
| Browser | Launch via `open` + CDP (Chrome DevTools Protocol) | Chrome/Chromium installed |

#### Privacy & Permissions

- All device access requires explicit user opt-in (macOS permission prompts)
- Device streams are encrypted in transit (Cloudflare tunnel TLS)
- No device data is stored by OCM — streams are real-time only
- User can revoke device access at any time (`Ctrl+C` or `ocm disconnect`)
- `ocm connect --devices` explicitly declares which devices are shared — no silent activation

### 7. Group Configuration

Channels like Telegram, Discord, Slack, and WhatsApp operate in group contexts (group chats, channels, servers). The CLI must make it easy to configure which groups the bot participates in and how it behaves in each.

#### Group Discovery
- After pairing a channel, the CLI should list available groups/channels the bot has access to
- Telegram: list groups the bot has been added to (via `getUpdates` or webhook)
- Discord: list servers (guilds) and channels the bot is a member of
- Slack: list workspaces and channels the bot is installed in
- WhatsApp: list available phone numbers and linked groups

#### Group Selection & Policy

```
ocm channels groups <channel> --machine SLUG
```

- List all groups the bot can see
- Enable/disable the bot per group (opt-in model — bot is silent in groups unless enabled)
- Set per-group behavior policy:
  - `open` — bot responds to all messages
  - `mention` — bot only responds when mentioned (@bot)
  - `command` — bot only responds to slash commands
  - `passive` — bot listens and logs but does not respond
- Set per-group allow/deny lists for specific users

#### Group Management Commands

```
ocm channels groups list <channel> --machine SLUG
ocm channels groups enable <channel> <group-id> --machine SLUG [--policy mention]
ocm channels groups disable <channel> <group-id> --machine SLUG
ocm channels groups set-policy <channel> <group-id> --policy <open|mention|command|passive> --machine SLUG
```

#### Group Configuration in Pair Wizard

During `ocm pair`, after each channel is set up:
1. Bot token is validated and saved
2. CLI fetches available groups from the platform API
3. User selects which groups to activate (multi-select)
4. User sets default group policy (applies to all selected groups)
5. Config is pushed with per-group settings

#### Example Flow

```
$ ocm channels groups list telegram --machine my-bot

  Groups for Telegram (@MyClawBot):
  ─────────────────────────────────
  ID          │ Name               │ Type     │ Status   │ Policy
  -114001234  │ Engineering Team   │ group    │ enabled  │ mention
  -114005678  │ Random             │ group    │ disabled │ —
  -114009012  │ Announcements      │ channel  │ disabled │ —

$ ocm channels groups enable telegram -114005678 --machine my-bot --policy open
  ✓ Enabled bot in "Random" (policy: open)
  ✓ Config pushed to VM
```

---

## Backend Changes Required

| Change | Description |
|--------|-------------|
| Add `slack` provider | Add to `validProviders` map, implement `validateSlackKey()`, seed registry entry |
| Add `slack` to registry | Seed `channel-slack` registry entry with config template |
| Subscription model | `account_subscriptions` table: plan, status, stripe_customer_id, stripe_subscription_id, provider access list, usage limits |
| Subscription API | `GET/POST /api/subscription` — status, subscribe, cancel, plan list |
| Subscription verification | OAuth callback endpoint for confirming subscription after Stripe checkout |
| Usage metering | Extend `apiproxy` to track per-account/per-machine token usage against subscription limits |
| Proxy credential mode | When machine uses subscription, proxy injects OCM's pooled key instead of user's BYOK key |

---

## CLI Architecture

### New Packages

```
cli/
  internal/
    commands/
      setup_provider.go    # ocm providers setup <provider>
      setup_channel.go     # ocm channels setup <channel>
      pair.go              # ocm pair <machine-slug>
    oauth/
      server.go            # Local HTTP callback server for OAuth flows
      providers.go         # Per-provider OAuth configuration (URLs, scopes)
    wizard/
      prompt.go            # Interactive prompts: masked input, select, multi-select, confirm
      steps.go             # Multi-step wizard framework with progress display
```

### Command Tree (New/Modified)

```
# Provider setup (BYOK or subscription)
ocm providers setup <provider> [--machine SLUG] [--method byok|subscription]
ocm providers validate <id>        # currently a stub — implement

# Channel pairing
ocm channels setup <channel> --machine SLUG
ocm channels groups list <channel> --machine SLUG
ocm channels groups enable <channel> <group-id> --machine SLUG [--policy mention]
ocm channels groups disable <channel> <group-id> --machine SLUG
ocm channels groups set-policy <channel> <group-id> --policy <open|mention|command|passive> --machine SLUG

# Full machine bootstrap
ocm pair <machine-slug>

# Machine connectivity (kind-aware: OpenClaw or Hermes)
ocm machines ssh SLUG
ocm machines dashboard SLUG [--open]
ocm machines serve chat SLUG [--host 127.0.0.1] [--port 0]
ocm machines logs SLUG [--follow] [--service SERVICE]
ocm machines connect SLUG [--surface ssh|dashboard|chat|logs] [--open]

# Credential management
ocm machines credentials list SLUG
ocm machines credentials link SLUG --credential ID
ocm machines credentials unlink SLUG --credential ID
ocm machines secrets set SLUG KEY VALUE
ocm machines secrets get SLUG KEY
ocm machines secrets delete SLUG KEY

# Subscription management
ocm subscription status
ocm subscription plans
ocm subscription subscribe [--plan PLAN]
ocm subscription cancel
ocm subscription usage [--machine SLUG]
```

---

## Bug Fixes (Pre-requisites)

| Bug | Fix |
|-----|-----|
| `ocm config machine-show` hits `/config` instead of `/assembled-config` | Fix endpoint path |
| CLI accepts `discord_bot` but backend only accepts `discord` | Rename in CLI `validProviderNames` |
| `ocm providers validate` prints "Not yet implemented" | Implement using existing backend validation |

---

## UX Guidelines

- All credential input must be masked (no plaintext keys on screen)
- Validation happens locally before sending to backend (fail fast)
- Each wizard step shows clear progress (step N of M)
- On success, display a summary of what was configured
- On failure, display the specific error and how to retry
- `--non-interactive` flag for CI/scripted usage (reads from flags/env/stdin)
- All wizards should be idempotent — running setup again updates rather than duplicates

---

## LLM-Friendly CLI Design

The CLI must be designed so that AI coding assistants (Claude Code, ChatGPT, Gemini CLI, etc.) can drive it autonomously on behalf of the user. This is a first-class requirement, not an afterthought.

### Design Principles

1. **Every interactive wizard must have a non-interactive equivalent.** An LLM can't click through multi-select menus. Every operation must be expressible as a single command with flags.

2. **Structured output for machine consumption.** All commands support `--json` flag that outputs structured JSON instead of human-formatted tables. LLMs parse JSON reliably; they struggle with ASCII tables.

3. **Discoverable via `--help`.** Every command, subcommand, and flag must have clear help text. An LLM's first step is always `ocm --help` and `ocm <command> --help`. The help text IS the documentation for AI agents.

4. **Idempotent and safe.** An LLM may retry commands. Running the same setup twice must not create duplicates or break state. All mutating commands should report whether they changed anything or were a no-op.

5. **Predictable error output.** Errors must go to stderr in a parseable format. Success goes to stdout. An LLM needs to distinguish "it worked" from "it failed" programmatically.

6. **Stdin for secrets.** LLMs can pipe secrets from environment variables or password managers. Support `--key-from-stdin` and `--token-from-stdin` so credentials never appear in command history.

### Non-Interactive Command Equivalents

Every wizard step must be callable as a standalone command:

```bash
# Instead of interactive "ocm pair my-bot" wizard, an LLM runs:
ocm providers setup anthropic --method byok --key-from-stdin --machine my-bot --json <<< "$ANTHROPIC_API_KEY"
ocm providers setup openai --method byok --key-from-stdin --machine my-bot --json <<< "$OPENAI_API_KEY"
ocm channels setup telegram --token-from-stdin --machine my-bot --json <<< "$TELEGRAM_BOT_TOKEN"
ocm channels groups enable telegram -114001234 --machine my-bot --policy mention --json
ocm identity set --machine my-bot --name "MyClaw" --avatar robot --json
ocm config push --machine my-bot --json

# Subscription flow (LLM can't do browser checkout, but can check status)
ocm subscription status --json
ocm subscription usage --machine my-bot --json
```

### JSON Output Format

All `--json` output follows a consistent envelope:

```json
{
  "status": "ok",
  "action": "providers.setup",
  "result": {
    "provider": "anthropic",
    "method": "byok",
    "credential_id": 42,
    "last_four": "xY2k",
    "validated": true,
    "linked_to": "my-bot"
  }
}
```

Error output (stderr, `--json`):

```json
{
  "status": "error",
  "action": "providers.setup",
  "error": "key validation failed: invalid API key",
  "hint": "Check that your API key is correct at https://console.anthropic.com/settings/keys"
}
```

### Introspection Commands

LLMs need to understand the current state before making changes:

```bash
# "What machines exist and what's their status?"
ocm machines list --json

# "What's configured on this machine?"
ocm config machine-show --machine my-bot --json

# "What credentials are available?"
ocm providers list --json

# "What channels are enabled and what groups are active?"
ocm channels list --machine my-bot --json
ocm channels groups list telegram --machine my-bot --json

# "What providers does the subscription cover?"
ocm subscription status --json

# "What registry entries (channels/skills/tools) are available?"
ocm channels available --json
ocm skills available --json
ocm tools available --json
```

### Example: Claude Code Configuring a Machine

A user tells Claude Code: "Set up my-bot with Anthropic and Telegram in the Engineering group."

Claude Code runs:

```bash
# 1. Check current state
ocm machines get my-bot --json
ocm providers list --json

# 2. Set up Anthropic (key from env)
ocm providers setup anthropic --method byok --key-from-stdin --machine my-bot --json <<< "$ANTHROPIC_API_KEY"

# 3. Set up Telegram channel
ocm channels setup telegram --token-from-stdin --machine my-bot --json <<< "$TELEGRAM_BOT_TOKEN"

# 4. List available groups
ocm channels groups list telegram --machine my-bot --json

# 5. Enable the right group (parses JSON to find group ID by name)
ocm channels groups enable telegram -114001234 --machine my-bot --policy mention --json

# 6. Push config
ocm config push --machine my-bot --json

# 7. Verify
ocm machines get my-bot --json  # confirm status=running, health=ok
```

Every step returns JSON. Claude Code parses it, handles errors, and proceeds. No interactive prompts needed.

### Why CLI, Not MCP

The OCM CLI is a standard Unix CLI tool. LLMs like Claude Code, Codex, and Gemini CLI already know how to run shell commands, parse JSON output, and pipe stdin. No special integration needed — the CLI just has to be well-behaved:

- `--json` on every command → LLM parses output reliably
- `--key-from-stdin` → LLM pipes secrets without exposing them in `ps` or shell history
- Exit codes → 0 = success, non-zero = failure, LLM retries or adjusts
- `--help` → LLM discovers available commands and flags without documentation
- stderr for errors, stdout for data → LLM distinguishes success from failure

An MCP server adds a dependency and protocol layer that isn't necessary. Claude Code, Codex, and Gemini CLI are all CLI-native — they run `bash` commands. The OCM CLI should be a tool they can pick up and use immediately, the same way they use `git`, `kubectl`, or `docker`.

### What Makes a CLI LLM-Friendly

The bar is: **can an LLM read `ocm --help`, figure out what to do, and configure a machine end-to-end without human intervention?**

This means:
1. **`--help` is complete and accurate** — it's the only documentation an LLM reads
2. **No required interactive prompts** — every operation has a flag-only equivalent
3. **JSON output is the contract** — `--json` on every command, consistent schema
4. **Commands are composable** — output of one command feeds into the next
5. **Errors are actionable** — error messages tell the LLM what went wrong and what to try instead
6. **Dry-run support** — `--dry-run` on mutating commands so the LLM can preview changes
7. **No hidden state** — current configuration is always queryable via introspection commands

---

## End-to-End Flow Examples

### Example 1: BYOK Setup (Bring Your Own Key)

```
$ ocm pair my-bot

  Machine Pairing: my-bot
  ═══════════════════════

  Step 1/5: LLM Access
  ─────────────────────
  How do you want to access LLM providers?
  > Bring my own API keys (BYOK)
    Use OCM subscription (managed)

  Select providers to configure:
  [x] Anthropic
  [x] OpenAI
  [ ] Google

  Anthropic — API Key
    Get your key at: https://console.anthropic.com/settings/keys
    Paste API key: ************************************
    ✓ Key validated
    ✓ Credential saved (encrypted, last4: ...xY2k)

  OpenAI — API Key
    Get your key at: https://platform.openai.com/api-keys
    Paste API key: ************************************
    ✓ Key validated
    ✓ Credential saved (encrypted, last4: ...3bQf)

  Step 2/5: Channels
  ──────────────────
  Select channels to enable:
  [x] Telegram
  [ ] Discord
  [ ] Slack
  [ ] WhatsApp

  Telegram — Bot Setup
    1. Open Telegram and search for @BotFather
    2. Send /newbot and follow the prompts
    3. Copy the bot token

    Paste bot token: ************************************
    ✓ Token validated (bot: @MyClawBot)
    ✓ Credential saved
    ✓ Channel enabled

  Step 3/5: Groups
  ────────────────
  Telegram groups @MyClawBot has access to:
  [x] Engineering Team     (group)
  [ ] Random               (group)
  [ ] Announcements         (channel)

  Default group policy:
  > mention — respond when @mentioned
    open — respond to all messages
    command — slash commands only
    passive — listen only

  ✓ 1 group enabled (policy: mention)

  Step 4/5: Identity (optional)
  ─────────────────────────────
  Bot name [MyClawBot]: MyClaw
  Avatar [robot]: robot

  Step 5/5: Push Configuration
  ────────────────────────────
  Pushing config to my-bot...
  ✓ 2 providers linked (BYOK)
  ✓ 1 channel enabled (telegram)
  ✓ 1 group configured (Engineering Team, policy: mention)
  ✓ Identity set (MyClaw)
  ✓ Config pushed to VM
  ✓ VM health check passed

  Machine "my-bot" is ready!
```

### Example 2: Subscription Setup (Managed Access)

```
$ ocm pair my-bot

  Machine Pairing: my-bot
  ═══════════════════════

  Step 1/5: LLM Access
  ─────────────────────
  How do you want to access LLM providers?
    Bring my own API keys (BYOK)
  > Use OCM subscription (managed)

  Current subscription: None

  Opening browser to subscribe...
  → https://openclawmachines.com/subscribe

  Available plans:
    Starter    $29/mo — Anthropic (Claude Haiku)
    Pro        $99/mo — Anthropic + OpenAI (all models)
    Enterprise  Custom — All providers, volume pricing

  Waiting for subscription confirmation...
  ✓ Subscription active: Pro plan
  ✓ Providers available: Anthropic, OpenAI
  ✓ Budget: $99/mo (usage metered via OCM proxy)

  Step 2/5: Channels
  ──────────────────
  ...
  (same channel/group/identity flow as BYOK)
```

### Example 3: Standalone Provider Subscription

```
$ ocm subscription subscribe --plan pro

  Opening browser for checkout...
  → https://openclawmachines.com/subscribe?plan=pro

  Waiting for confirmation...
  ✓ Subscribed to Pro plan ($99/mo)
  ✓ Providers: Anthropic (all Claude models), OpenAI (all GPT models)
  ✓ Next billing date: March 25, 2026

$ ocm subscription status

  Plan:     Pro ($99/mo)
  Status:   Active
  Providers: Anthropic, OpenAI
  Usage:    $34.12 / $99.00 this period
  Machines: my-bot (linked), test-bot (linked)
  Next billing: March 25, 2026

$ ocm subscription usage --machine my-bot

  Usage for my-bot (Feb 1 - Feb 25):
  ──────────────────────────────────
  Provider   │ Model              │ Requests │ Tokens     │ Cost
  Anthropic  │ claude-sonnet-4-6  │ 1,234    │ 2.1M       │ $18.40
  Anthropic  │ claude-haiku-4-5   │ 8,921    │ 12.3M      │ $6.15
  OpenAI     │ gpt-4o             │ 456      │ 890K       │ $4.45
  ──────────────────────────────────────────────────────────
  Total                           │ 10,611   │ 15.3M      │ $29.00
```

---

## Multi-Platform Architecture

### Shared Go Core (`ocmkit`)

All business logic lives in a shared Go library. It is compiled via `gomobile` into native libraries for each platform:

- `.xcframework` for iOS (imported by Swift/SwiftUI)
- `.aar` for Android (imported by Kotlin/Compose)
- Regular Go package for CLI (imported directly)
- Same Go code also powers the backend

```
                    ┌─────────────────────────────────────┐
                    │           Go Shared Core             │
                    │           (ocmkit library)            │
                    │                                       │
                    │  • API client (auth, REST, WebSocket) │
                    │  • Credential management              │
                    │  • Config assembly + push             │
                    │  • Fleet orchestration                │
                    │  • Voice encoding/decoding (Opus)     │
                    │  • Device ↔ agent streaming           │
                    └──────┬─────────┬──────────┬──────────┘
                           │         │          │
              ┌────────────┤         │          ├────────────┐
              │            │         │          │            │
     ┌────────▼──────┐  ┌──▼─────────▼──┐  ┌───▼──────────┐
     │   OCM CLI     │  │   iOS App     │  │ Android App  │
     │   (Go)        │  │  (SwiftUI)    │  │  (Compose)   │
     │               │  │               │  │              │
     │ • cobra TUI   │  │ • Native UI   │  │ • Native UI  │
     │ • macOS APIs  │  │ • AVFoundation│  │ • CameraX    │
     │ • CDP browser │  │ • APNs push   │  │ • FCM push   │
     │ • stdin/stdout│  │ • Siri/Shortc.│  │ • Widgets    │
     └───────────────┘  └───────────────┘  └──────────────┘
              │                │                  │
              └────────────────┼──────────────────┘
                               │
                        ┌──────▼──────┐
                        │  OCM Backend │
                        │  (Go, Cloud  │
                        │   Run)       │
                        └──────┬───────┘
                               │
                        ┌──────▼──────┐
                        │  Agent VMs   │
                        │ (Firecracker)│
                        └─────────────┘
```

### Shared Core Package Structure

```
ocmkit/
  client/
    api.go                # REST client — auth, machines, credentials, config
    websocket.go          # WebSocket — voice streaming, log streaming, events
    auth.go               # CF Access JWT + bearer token management
  config/
    assembly.go           # Config assembly + push to VM
    credentials.go        # Credential CRUD, linking, validation
    providers.go          # Provider setup logic (BYOK + subscription)
    channels.go           # Channel setup logic + group management
  fleet/
    orchestrator.go       # Multi-machine create, configure, verify
    templates.go          # Role templates (property-mgmt, support, etc.)
  voice/
    encoder.go            # PCM → Opus encoding for streaming
    decoder.go            # Opus → PCM decoding for playback
    vad.go                # Voice activity detection (silence trimming)
  stream/
    bridge.go             # Bidirectional device ↔ agent data streaming
    protocol.go           # Stream message framing (audio, image, text, control)
```

### Platform-Specific Layers

Each platform provides a thin native shell that handles UI and hardware. All business logic goes through `ocmkit`.

#### CLI (Go — already exists, extend)

```
cli/
  internal/
    commands/             # cobra commands (existing)
    wizard/               # Interactive TUI prompts (bubbletea/huh)
    devices/
      mic_darwin.go       # macOS microphone (AVFoundation via CGo)
      speaker_darwin.go   # macOS speaker (AVFoundation or `say`)
      camera_darwin.go    # macOS camera (AVFoundation or `imagesnap`)
      screen_darwin.go    # macOS screen capture (screencapture CLI)
      browser.go          # CDP via chromedp (cross-platform)
      clipboard.go        # golang.design/x/clipboard (cross-platform)
      notify_darwin.go    # macOS notifications (osascript)
```

#### iOS (SwiftUI)

```
ios/
  OCM/
    App/
      OCMApp.swift                # App entry point
    Views/
      AgentListView.swift         # Machine list + status
      AgentChatView.swift         # Chat with agent (text + voice)
      VoiceView.swift             # Voice interaction (tap-to-talk, hands-free)
      PairWizardView.swift        # Setup wizard (providers, channels, groups)
      FleetView.swift             # Fleet overview (all agents)
      SettingsView.swift          # Subscription, credentials, account
    Services/
      AudioService.swift          # AVAudioEngine — mic capture + speaker playback
      CameraService.swift         # AVCaptureSession — photo/video capture
      NotificationService.swift   # UNUserNotificationCenter + APNs
      LocationService.swift       # CoreLocation — property-aware context
      SiriService.swift           # SiriKit / App Intents — voice shortcuts
      ShareExtension/             # Share sheet → send to agent
      WidgetExtension/            # Home screen widget (agent status, quick actions)
    Bridge/
      OCMKit.swift                # Swift wrapper around gomobile .xcframework
      DeviceBridge.swift          # Pipes native device data through ocmkit streams
```

#### Android (Kotlin + Jetpack Compose)

```
android/
  app/src/main/
    ui/
      AgentListScreen.kt         # Machine list + status
      AgentChatScreen.kt         # Chat with agent (text + voice)
      VoiceScreen.kt             # Voice interaction
      PairWizardScreen.kt        # Setup wizard
      FleetScreen.kt             # Fleet overview
      SettingsScreen.kt          # Subscription, credentials, account
    services/
      AudioService.kt            # AudioRecord + AudioTrack
      CameraService.kt           # CameraX — photo/video capture
      NotificationService.kt     # FCM push notifications
      LocationService.kt         # FusedLocationProvider
    bridge/
      OCMKit.kt                  # Kotlin wrapper around gomobile .aar
      DeviceBridge.kt            # Pipes native device data through ocmkit streams
    widget/
      AgentStatusWidget.kt       # Home screen widget
```

### Feature Matrix by Platform

| Feature | CLI (Mac/Linux) | iOS | Android | Web |
|---------|----------------|-----|---------|-----|
| Machine CRUD | ✓ | ✓ | ✓ | ✓ |
| Provider setup (BYOK) | ✓ | ✓ | ✓ | ✓ |
| Provider setup (subscription) | ✓ | ✓ (in-app purchase) | ✓ (Play billing) | ✓ (Stripe) |
| Channel pairing | ✓ | ✓ | ✓ | ✓ |
| Group management | ✓ | ✓ | ✓ | ✓ |
| Fleet bootstrapping | ✓ (LLM-driven) | ✓ (wizard) | ✓ (wizard) | — |
| Voice interaction | ✓ (macOS) | ✓ (native) | ✓ (native) | ✓ (WebRTC) |
| Camera capture | ✓ (macOS) | ✓ (native) | ✓ (native) | ✓ (getUserMedia) |
| Push notifications | ✓ (macOS native) | ✓ (APNs) | ✓ (FCM) | ✓ (Web Push) |
| Background agent | — | ✓ (Background Refresh) | ✓ (WorkManager) | ✓ (Service Worker) |
| Location context | — | ✓ (CoreLocation) | ✓ (Fused) | — |
| Voice assistant | — | ✓ (Siri Shortcuts) | ✓ (Google Assistant) | — |
| Share extension | — | ✓ (Share Sheet) | ✓ (Share Intent) | — |
| Home screen widget | — | ✓ (WidgetKit) | ✓ (App Widget) | — |
| LLM-scriptable | ✓ (primary) | — | — | — |
| Offline queue | — | ✓ | ✓ | ✓ (SW) |

### Mobile-Specific Capabilities

Features that mobile enables beyond what CLI and web can do:

| Feature | Description |
|---------|-------------|
| **Push notifications** | Agent alerts user instantly — maintenance request filed, tenant reply, budget warning |
| **Always-on voice** | Phone as a voice interface to your agents — ask questions while walking a property |
| **Photo → agent** | Snap photo of property damage, agent files maintenance request with the image attached |
| **Location awareness** | Agent knows which property you're at, auto-pulls relevant tenant info and work orders |
| **Siri / Google Assistant** | "Hey Siri, ask PropertyBot about today's maintenance requests" |
| **Share extension** | Share a document, email, or photo from any app directly to a specific agent |
| **Widget** | Glanceable agent status on home screen — open requests, unread messages, budget remaining |
| **Offline queue** | Commands queue when offline (in elevator, underground), sync when connection returns |
| **In-app purchase** | Subscribe to OCM plans via App Store / Play Store billing (native payment flow) |

### Build & Distribution

| Component | Build Tool | Output | Distribution |
|-----------|-----------|--------|-------------|
| `ocmkit` | `gomobile bind` | .xcframework + .aar | Bundled in apps |
| CLI | `go build` (cross-compile) | Binary per platform | GCS download (existing `upload-cli.sh`) |
| iOS app | Xcode + xcodebuild | .ipa | App Store / TestFlight |
| Android app | Gradle | .apk / .aab | Play Store / direct APK |
| Web app | Vite (existing) | Static bundle | Cloud Run (existing) |

### CI/CD Pipeline

```
ocmkit (shared core)
  │
  ├─ go test ./...                    # Unit tests
  ├─ gomobile bind -target ios        # Build .xcframework
  ├─ gomobile bind -target android    # Build .aar
  │
  ├─► CLI
  │     └─ go build → upload-cli.sh → GCS
  │
  ├─► iOS
  │     └─ xcodebuild → TestFlight → App Store
  │
  ├─► Android
  │     └─ gradle assembleRelease → Play Store
  │
  └─► Web (unchanged)
        └─ vite build → Cloud Run
```

A single change to `ocmkit` triggers rebuilds across all platforms. Shared logic is tested once, platform-specific code is tested per-platform.

### Development Phasing

| Phase | Scope | Timeline |
|-------|-------|----------|
| **Phase 1** | CLI upgrade — provider setup, channel pairing, fleet bootstrap, `--json`, LLM-friendly | First |
| **Phase 2** | Extract `ocmkit` from CLI — shared Go library, clean API boundary | After CLI stabilizes |
| **Phase 3** | iOS app — SwiftUI shell, agent list, chat, voice, pair wizard | After ocmkit extraction |
| **Phase 4** | Android app — Compose shell, same features as iOS | After iOS |
| **Phase 5** | Device bridge — mic, camera, screen streaming across all platforms | After mobile apps |

Phase 1 is where all the business logic gets written. Phases 2-5 are mostly packaging and native UI — the hard work is already done in Go.

---

## Device Bridge Architecture

The CLI acts as a local device bridge — it connects hardware on the user's machine (browser, microphone, camera, screen) to a remote agent running in a VM. The bridge relays commands and data over WebSocket through the existing Cloudflare tunnel.

### Connection Path

```
LLM inside VM
  │  "search zillow for 94103 rentals"
  ▼
OpenClaw Gateway (port 18789, localhost inside VM)
  │  tool call: browser.navigate("https://zillow.com")
  ▼
Agent device endpoint (port 8080, /device/browser)
  │  WebSocket: { "action": "navigate", "url": "..." }
  ▼
Cloudflare Tunnel (encrypted, outbound from VM)
  │
  ▼
CLI: `ocm browse my-bot` (user's Mac)
  │  receives command, executes locally
  ▼
chromedp → Chrome DevTools Protocol (localhost:9222)
  │
  ▼
Chrome browser window (user sees it happening)
  │  result flows back through the same path
  ▼
Screenshot bytes: Chrome → CLI → tunnel → agent → LLM
  │
  ▼
LLM sees the page, decides next action
```

### Bridge Protocol

The agent and CLI communicate over WebSocket using a simple JSON command protocol. The agent sends high-level actions; the CLI translates them to platform-specific API calls (CDP for browser, AVFoundation for mic/camera, etc.).

#### Browser Commands

```
Agent → CLI:
──────────────────────────────────────────────────────
{ "id": 1, "action": "navigate", "url": "https://zillow.com" }
{ "id": 2, "action": "screenshot" }
{ "id": 3, "action": "click", "selector": "#search-btn" }
{ "id": 4, "action": "type", "selector": "#search-input", "text": "94103" }
{ "id": 5, "action": "get_text", "selector": ".results-count" }
{ "id": 6, "action": "evaluate", "js": "document.title" }
{ "id": 7, "action": "scroll", "direction": "down", "amount": 500 }
{ "id": 8, "action": "wait", "selector": ".listing-card", "timeout": 5000 }
{ "id": 9, "action": "pdf" }

CLI → Agent:
──────────────────────────────────────────────────────
{ "id": 1, "status": "ok" }
{ "id": 2, "status": "ok", "image": "base64:iVBOR..." }
{ "id": 3, "status": "ok" }
{ "id": 4, "status": "ok" }
{ "id": 5, "status": "ok", "text": "47 results" }
{ "id": 6, "status": "ok", "result": "94103 Real Estate - Zillow" }
{ "id": 7, "status": "ok" }
{ "id": 8, "status": "error", "error": "timeout: selector not found" }
{ "id": 9, "status": "ok", "pdf": "base64:JVBER..." }
```

#### Voice Commands (Bidirectional Audio Stream)

```
CLI → Agent (mic capture):
──────────────────────────────────────────────────────
{ "type": "audio", "codec": "opus", "sample_rate": 48000, "data": "base64:..." }
{ "type": "audio", "codec": "opus", "sample_rate": 48000, "data": "base64:..." }
{ "type": "vad", "event": "speech_end" }

Agent → CLI (TTS playback):
──────────────────────────────────────────────────────
{ "type": "audio", "codec": "opus", "sample_rate": 48000, "data": "base64:..." }
{ "type": "control", "action": "listening" }
```

#### Camera / Screen Commands

```
Agent → CLI:
──────────────────────────────────────────────────────
{ "action": "capture_photo" }
{ "action": "capture_screen" }
{ "action": "capture_screen", "region": { "x": 0, "y": 0, "w": 800, "h": 600 } }
{ "action": "start_stream", "device": "camera", "fps": 1 }
{ "action": "stop_stream", "device": "camera" }

CLI → Agent:
──────────────────────────────────────────────────────
{ "action": "capture_photo", "status": "ok", "image": "base64:...", "format": "jpeg" }
{ "action": "capture_screen", "status": "ok", "image": "base64:...", "format": "png" }
```

### Agent-Side Endpoints

New WebSocket endpoints on the agent proxy router (`/device/*`):

```
/device/browser     ← Browser bridge (CDP commands/results)
/device/voice       ← Voice bridge (bidirectional Opus audio)
/device/camera      ← Camera bridge (image capture, video stream)
/device/screen      ← Screen bridge (screenshot, screen stream)
/device/clipboard   ← Clipboard bridge (read/write text, images)
/device/notify      ← Notification bridge (agent → user alerts)
```

Each endpoint is a persistent WebSocket connection. The agent's LLM interacts with them as tools — "take a screenshot", "navigate to URL", "read what's on screen". The agent doesn't know or care whether the device is a Mac, iPhone, or Android — it sends the same commands. The client-side bridge handles the platform-specific execution.

### CLI Bridge Implementation

```go
// cli/internal/devices/bridge.go

// DeviceBridge manages connections between local hardware and a remote agent.
type DeviceBridge struct {
    machineSlug string
    agentWS     *websocket.Conn
    devices     map[string]Device     // "browser", "voice", "camera", etc.
}

// Device is the interface each local device adapter implements.
type Device interface {
    // HandleCommand executes a command from the agent and returns the result.
    HandleCommand(cmd Command) Result
    // Close releases device resources.
    Close()
}
```

```go
// cli/internal/devices/browser.go

// BrowserDevice controls a local Chrome instance via CDP.
type BrowserDevice struct {
    ctx    context.Context      // chromedp browser context
    cancel context.CancelFunc
}

func (b *BrowserDevice) HandleCommand(cmd Command) Result {
    switch cmd.Action {
    case "navigate":
        err := chromedp.Run(b.ctx, chromedp.Navigate(cmd.URL))
        return Result{Status: statusFromErr(err)}

    case "screenshot":
        var buf []byte
        err := chromedp.Run(b.ctx, chromedp.CaptureScreenshot(&buf))
        return Result{Status: statusFromErr(err), Image: base64.StdEncoding.EncodeToString(buf)}

    case "click":
        err := chromedp.Run(b.ctx, chromedp.Click(cmd.Selector, chromedp.ByQuery))
        return Result{Status: statusFromErr(err)}

    case "type":
        err := chromedp.Run(b.ctx, chromedp.SendKeys(cmd.Selector, cmd.Text, chromedp.ByQuery))
        return Result{Status: statusFromErr(err)}

    case "get_text":
        var text string
        err := chromedp.Run(b.ctx, chromedp.Text(cmd.Selector, &text, chromedp.ByQuery))
        return Result{Status: statusFromErr(err), Text: text}

    case "evaluate":
        var result interface{}
        err := chromedp.Run(b.ctx, chromedp.Evaluate(cmd.JS, &result))
        return Result{Status: statusFromErr(err), Result: result}
    }
    return Result{Status: "error", Error: "unknown action: " + cmd.Action}
}
```

```go
// cli/internal/devices/voice_darwin.go

// VoiceDevice captures microphone audio and plays back TTS via macOS APIs.
type VoiceDevice struct {
    mic     *portaudio.Stream   // or AVFoundation via CGo
    speaker *portaudio.Stream
    encoder *opus.Encoder
    decoder *opus.Decoder
}

func (v *VoiceDevice) HandleCommand(cmd Command) Result {
    switch cmd.Action {
    case "start_listening":
        go v.streamMicToAgent()       // captures PCM, encodes Opus, sends to agent
        return Result{Status: "ok"}

    case "stop_listening":
        v.mic.Stop()
        return Result{Status: "ok"}

    case "speak":
        v.playAudio(cmd.AudioData)    // decodes Opus, plays through speaker
        return Result{Status: "ok"}
    }
    return Result{Status: "error", Error: "unknown action"}
}
```

```go
// cli/internal/devices/camera_darwin.go

// CameraDevice captures photos via macOS imagesnap or AVFoundation.
type CameraDevice struct{}

func (c *CameraDevice) HandleCommand(cmd Command) Result {
    switch cmd.Action {
    case "capture_photo":
        // Shell out to imagesnap (no CGo needed)
        tmpFile := filepath.Join(os.TempDir(), "ocm-capture.jpg")
        exec.Command("imagesnap", "-q", tmpFile).Run()
        data, _ := os.ReadFile(tmpFile)
        os.Remove(tmpFile)
        return Result{Status: "ok", Image: base64.StdEncoding.EncodeToString(data), Format: "jpeg"}
    }
    return Result{Status: "error", Error: "unknown action"}
}
```

### Device Bridge Per Platform

The same `Device` interface works across platforms. Each platform provides its own implementations:

| Device | CLI (macOS) | iOS (Swift) | Android (Kotlin) |
|--------|-------------|-------------|-------------------|
| Browser | `chromedp` (CDP) | `WKWebView` | `WebView` |
| Microphone | `portaudio` / `imagesnap` | `AVAudioEngine` | `AudioRecord` |
| Speaker | `portaudio` / `say` | `AVAudioEngine` | `AudioTrack` |
| Camera | `imagesnap` / AVFoundation CGo | `AVCaptureSession` | `CameraX` |
| Screen | `screencapture` CLI | `UIScreen.capture` | `MediaProjection` |
| Clipboard | `golang.design/x/clipboard` | `UIPasteboard` | `ClipboardManager` |
| Notifications | `osascript` | `UNUserNotification` | `NotificationManager` |

The agent sends the same commands regardless of platform. The bridge adapts.

### Security Model

- **Explicit device opt-in** — `ocm connect --devices mic,camera` or `ocm browse`. No silent activation.
- **User sees everything** — browser window is visible, mic indicator shows in macOS menu bar.
- **Encrypted transport** — all device data flows through the Cloudflare tunnel (TLS).
- **No server storage** — device streams are real-time relay only. OCM backend never sees or stores device data. The data path is: `CLI ↔ CF Tunnel ↔ Agent VM`. Backend is not in the path.
- **Revocable** — `Ctrl+C` or `ocm disconnect` terminates all device bridges immediately.
- **Scoped per session** — device access is not persistent. Each `ocm connect` / `ocm browse` / `ocm voice` is a new session that ends when the command exits.
- **macOS permission gates** — camera, microphone, screen recording all require macOS permission prompts on first use. The CLI cannot bypass these.
