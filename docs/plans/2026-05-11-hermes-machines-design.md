# Hermes Machines — VM Design

**Status:** Running design document (active analysis)
**Started:** 2026-05-11
**Branch:** `hermes-agent`
**Author:** Mathew

> This is a living analysis document. Update as we learn more, not a frozen
> specification. Sections marked **Open question** need a decision before we
> commit to an implementation plan.

## 0. What Hermes actually is (and isn't)

There's a naming overlap worth nailing down up front, because it affects what
the product even is:

- **"Hermes" the model family** — LLMs that Nous Research has *trained*
  (Hermes 2, Hermes 3, Hermes 4). These are weights you serve through
  Ollama / vLLM / Nous Portal. They're a possible *target* of a model
  picker, no different from Claude or GPT-4.
- **"Hermes Agent" the harness** — the Python application at
  `github.com/NousResearch/hermes-agent`. This is what we're putting in the
  VM. **It is not a model.** It's an agent loop that calls *any* LLM
  (OpenRouter, Anthropic, OpenAI, Gemini, Bedrock, Hermes-the-model via Nous
  Portal, local vLLM, …) through a unified provider abstraction.

These are unrelated decisions. A Hermes machine could be configured to run
Claude Opus through OpenRouter and never touch a Hermes-the-model endpoint;
or it could point at Nous Portal and run Hermes-4. The harness is
model-agnostic.

### So is Hermes Agent the same as OpenClaw?

**Category-wise, yes:** both are general-purpose, multi-platform agent
harnesses (CLI + messaging gateway + skills + tools + cron + MCP), both call
external LLMs, both run anywhere from a laptop to a $5 VPS to a MicroVM.
For most users on day one, the experience is recognizably the same shape.

**Differentiated by:**

1. **A real self-learning loop.** OpenClaw's skills are hand-written and
   hand-improved. Hermes's are *agent-curated*:
   - **Autonomous skill creation** after complex tasks (agent writes a new
     procedural skill into `~/.hermes/skills/` when it notices it's done
     something worth remembering)
   - **In-use skill improvement** — skills get refined during real use, not
     just at write-time
   - **Memory curation with periodic nudges** — agent prompts itself to
     persist knowledge; `~/.hermes/memories/` is a living artifact
   - **Honcho dialectic user modeling** (`plugins/memory/honcho/`) — builds a
     deepening per-user model across sessions, surfaced as context
   - **FTS5 session search + LLM summarization** for cross-session recall —
     past conversations stay queryable, not just archived
2. **A pluggable backbone, not a built-in stack.** Hermes's `plugins/` tree
   makes the agent's substrate swappable:
   - `plugins/memory/` — honcho, mem0, supermemory, plain-file (pick one)
   - `plugins/context_engine/` — pluggable context windowing strategies
   - `plugins/model-providers/` — openrouter, anthropic, gmi, etc. (the
     ~20-provider catalog comes from here)
   - `plugins/kanban/` — multi-agent board with dispatcher + worker model
   - `plugins/observability/`, `plugins/image_gen/`, `plugins/disk-cleanup/`,
     `plugins/hermes-achievements/`, …
3. **An RL/training pipeline aimed at itself.** `tinker-atropos/` and
   `environments/` (`hermes_base_env.py`, `web_research_env.py`,
   `hermes_swe_env/`, `terminal_test_env/`) are Atropos-style RL environments
   for fine-tuning tool-calling models *on the trajectories the harness
   generates*. This is research-grade today, but it means a Hermes machine is
   continuously producing usable training data. Optional and orthogonal to
   running the agent.
4. **An editor-attach surface.** `hermes acp` exposes the Agent Client
   Protocol so Claude Code / Cursor / Zed can drive the running Hermes as a
   remote worker. OpenClaw doesn't have a direct analog.
5. **A wider messaging surface out of the box** — ~22 platforms vs our 4.

### Implications for the *product* (not just the VM)

The self-learning loop changes how a Hermes machine accrues value:

- **The data volume becomes precious.** OpenClaw machines are valuable mostly
  for their config and channel state — wipe-and-recreate is annoying but
  recoverable. A Hermes machine that's been running for three months is
  irreplaceable: it has agent-written skills tuned to the user, a curated
  memory store, a personalized user model, and a searchable session history.
  Backups, snapshots, and migration flows aren't optional polish — they're
  the *product*.
- **"Bring my agent with me" becomes a real story.** Export/import of
  `$HERMES_HOME` is meaningful in a way OpenClaw's `/data` isn't (you can
  hand the same agent to a friend, or move it between providers).
- **Telemetry and trace storage matter more.** Trajectory data is reusable
  (for fine-tuning, for skill mining, for analytics). Opik traces we already
  capture line up with this naturally.
- **The pluggable backbone is a UX axis.** Users can pick their memory
  provider, their context engine, their observability sink the same way they
  pick a channel today. That's a "settings" surface, not just a developer
  knob.
- **Multi-agent ("kanban") workflows** are a near-term product expansion —
  spinning up worker sub-agents from a board dispatcher fits naturally on top
  of MicroVM-per-agent.

### Summary in one line

OpenClaw and Hermes are in the same product *category* (managed agent
machines), but Hermes is the version where the agent **gets better the
longer you run it**, with a pluggable substrate underneath. Same VM
plumbing, different value curve.

## 1. Goal

Run the **Hermes Agent** harness (Nous Research, Python-based, MIT) inside a
Firecracker MicroVM with the same one-click provisioning experience that
OpenClaw Machines gives today — pick a model, paste channel tokens, click
"create" → working agent reachable from Telegram/Slack/Discord/etc., a TUI, and
optionally a browser dashboard.

The shape of the product is unchanged: per-user MicroVMs on shared hosts,
fronted by per-VM Cloudflare Tunnels, with credentials assembled server-side
and injected at boot. Only the *guest payload* changes — Hermes instead of
OpenClaw.

## 2. What Hermes brings to the table

| Surface | Hermes capability | OCM analog |
|---|---|---|
| Entry point | `hermes` CLI (Python, Fire-based dispatch) | `openclaw` Node CLI |
| Messaging gateway | `hermes gateway` covering Telegram, Discord, Slack, WhatsApp, Signal, Matrix, Email, SMS, Teams, Feishu, DingTalk, WeChat, Home Assistant (~22 platforms) | OpenClaw gateway, 4 channels in our keep-list |
| Models | OpenRouter, Anthropic, OpenAI, Gemini, Bedrock, Ollama/Ollama Cloud, GLM/z.ai, Kimi/Moonshot, MiniMax, Arcee, HuggingFace, OpenCode Zen/Go, Nous Portal, local LM Studio/vLLM | OpenRouter-centric via LiteLLM proxy |
| Browser | Playwright (`tools/browser_tool.py`), Camofox stealth, CDP debug | Separate browser VM (Alpine + Chromium + stealth ext) |
| MCP | Built-in `hermes mcp serve` exposing ~10 tools (sessions, messages, attachments, approvals) | OpenClaw's MCP server |
| Cron | In-process `cron/scheduler.py` (croniter) | OCM platform-level scheduler |
| Dashboard | `hermes dashboard` on `:9119` (Vue 3 web UI, Vite) | Filebrowser on `:9000` + control UI through gateway |
| Skills/plugins | Bundled `skills/`, installable `optional-skills/` from Skills Hub, **auto-written skills** after complex tasks | OpenClaw skills (hand-edited) |
| Pluggable backbone | `plugins/{memory,context_engine,model-providers,kanban,observability,image_gen,…}/` — swappable substrates | Hard-coded substrate |
| Memory model | Provider plugins: honcho, mem0, supermemory, plain-file. Periodic self-nudges. FTS5 session search + LLM summarization for cross-session recall | MEMORY.md / USER.md, no learning loop |
| User model | Honcho dialectic modelling — deepens per-user across sessions | Static persona file |
| Editor integration | `hermes acp` (Agent Client Protocol) — Claude Code/Cursor can attach | None today |
| Multi-agent | `plugins/kanban/` — board dispatcher + worker sub-agents | None today |
| Self-improvement | **Real loop:** autonomous skill creation, in-use skill refinement, memory curation, user-model deepening, Atropos RL envs (`tinker-atropos/`, `environments/`) for fine-tuning tool-calling models on captured trajectories | Trace capture only, no closed loop |
| OpenClaw migration | `hermes claw migrate` imports SOUL.md, MEMORY.md, USER.md, skills, channel tokens, API keys from `~/.openclaw/` | n/a |

State lives at `$HERMES_HOME` (`~/.hermes` on host, `/opt/data` in the official
image) — same single-directory model OCM uses for `/data`. Files:
`config.yaml`, `.env`, `auth.json`, `SOUL.md`, `sessions/`, `skills/`, `cron/`,
`workspace/`, `home/` (per-profile HOME for subprocesses).

**Bootstrap-friendly:** `HERMES_AUTH_JSON_BOOTSTRAP` env var lets an
orchestrator seed the OAuth refresh token on first boot only — the entrypoint
already guards with `[ ! -f auth.json ]`. This is exactly the seam we need.

## 3. What OCM brings that we keep

The Firecracker side of OCM is *generic*: it doesn't know what's in the VM.
Reusable as-is (or with small tweaks):

| Component | File(s) | Reuse decision |
|---|---|---|
| Firecracker orchestrator | `backend/internal/orchestrator/firecracker_linux.go` | **Reuse unchanged.** TAP + kernel + rootfs handoff is payload-agnostic. |
| Metadata server | `backend/internal/metadata/server_linux.go` | **Reuse, extend.** Add Hermes-shaped `/v1/secrets` response. |
| Bridge networking, DNAT, iptables | `backend/internal/orchestrator/` + `scripts/cleanup-stale-dnat.sh` | **Reuse.** |
| Per-VM Cloudflare Tunnel | API-side provisioning + agent fetch | **Reuse.** |
| Agent self-update | `backend/internal/selfupdate/` | **Reuse.** |
| Artifact delivery (manifest.json → GCS → metadata) | `register-*-release.sh`, `selfupdate` | **Reuse with new DB kinds:** add `kind=hermes-rootfs` for Hermes rootfs rows and `kind=hermes` for Hermes runtime rows. GCS manifests can still use `"kind": "hermes-rootfs"` / `"kind": "hermes-runtime"`. |
| `configassembly` framework | `backend/internal/configassembly/assembler.go` | **Reuse skeleton, replace emitter.** Hermes wants YAML+env, not openclaw.json. |
| Frontend credential UI, machine list, admin pages | `frontend/` | **Reuse with new model/channel catalog.** |
| Per-VM data volume + migrations (`ocm-migrate`) | `scripts/init-openclaw.sh` + `scripts/migrations/` | **Reuse.** |
| `authproxy` (Cloudflare JWT → VM ports) | `scripts/openclaw-runtime-externals/` + bin | **Reuse, retarget ports.** |
| `ocmptyd` (browser-based shell) | rootfs bin | **Reuse.** Hermes ships its own TUI, but giving users a tty into the VM is a winning OCM UX we keep. |

## 4. What changes for Hermes

### 4.1 New rootfs

`rootfs/Dockerfile.hermes` (new) replaces `Dockerfile.openclaw`. Base on the
same Debian 13 layer for consistency. Bake:

- **Python 3.11+** (Hermes requires ≥3.11; current OCM rootfs already
  installs `python3` via apt, but verify the exact Debian-provided version
  during the Hermes rootfs build)
- **uv** (already in OCM rootfs — pinned + SHA-verified, keep that pattern)
- **Node 20+** (Hermes needs npm for `web/`, `ui-tui/`, Playwright shell; OCM has 24)
- **Playwright system deps** + pre-installed Chromium-shell (`npx playwright install --with-deps chromium --only-shell`)
- **ffmpeg** (voice transcription), **ripgrep** (already there)
- The Hermes wheel + its `[all]` extras, installed into a system venv at `/opt/hermes/.venv`
- `/opt/hermes/.playwright` for the Playwright browser store (outside data volume so it survives recreate)
- The bundled `skills/` tree under `/opt/hermes/skills` (sync'd to `$HERMES_HOME/skills` at boot, identical to Hermes's docker entrypoint pattern)

Strip from OCM's current rootfs (probably not needed for Hermes):
- ❓ Bun (Hermes is Python; only needed if a skill calls Bun)
- ❓ The big CLI-tools shelf (fd, bat, yq, xsv, xh, ast-grep, delta) — Hermes
  bundles fewer, but agents *do* shell out, so keep ripgrep + fd + jq
  conservatively
- ❌ `browser-harness` (Python) — Hermes replaces this with Playwright
- ❌ Stealth extension as a separate component — Hermes ships Camofox instead;
  reuse the same extension only if a customer wants it via skill
- ❌ `markitdown` (Hermes has equivalents in skills)
- ❌ Most `openclaw-runtime-externals` (npm packages OpenClaw needed)

**Target size:** ~2 GB (down from OCM's ~3-4 GB) thanks to dropping the
externals stack and bun. Playwright + Chromium is the biggest single addition
but it's the price of browser tooling.

**Open question:** Do we bake a single rootfs, or split "hermes-lite" (no
Playwright) + "hermes-browser" the way we split openclaw + browser-vm today?
*Initial bias: single rootfs.* Hermes uses Playwright in-process, so the
remote-CDP-from-another-VM pattern that justifies OCM's split is gone. We'd
keep the *option* to pair a browser VM for stealth-heavy scraping, but the
default is a single VM with Playwright local.

### 4.2 New runtime bundling

OCM ships the OpenClaw npm package as a versioned tarball separate from the
rootfs, so the runtime can roll forward without rebuilding the OS. We want the
same shape for Hermes:

- **Artifact:** `hermes-{VERSION}-py3-linux-amd64.tar.zst` containing a
  pre-built wheel set (`uv export --frozen` → `.venv` tarball) OR a frozen
  `uv sync` snapshot
- **GCS layout:** `gs://openclawmachines/hermes/releases/{VERSION}/manifest.json`
- **`scripts/build-hermes-runtime.sh`** (new, analog of
  `build-openclaw-runtime.sh`): pins the upstream Hermes git ref or PyPI
  version, runs `uv sync`, tarballs the venv, sha256s it, uploads to GCS.
- **`scripts/register-hermes-release.sh`** (new): inserts into the
  `artifact_releases` table with **DB `kind='hermes'`** (mirroring
  OpenClaw's pattern, which uses DB `kind='openclaw'` while its GCS
  manifest carries `"kind": "openclaw-runtime"`). The GCS manifest uses
  `"kind": "hermes-runtime"` for symmetry. **Keep this DB-vs-manifest
  distinction consistent everywhere** — the version picker and
  `host_artifact_state` query by DB kind.
- **Hermes rootfs releases** use **DB `kind='hermes-rootfs'`** and GCS
  manifest `"kind": "hermes-rootfs"`. Do not call runtime rows
  `hermes-runtime` in Postgres; reserve that name for the manifest payload.
- **Version selection at boot** mirrors OpenClaw: a channel manifest
  (`manifest-stable.json`) names the current version; users can pin per-machine.

**Open question:** Is bundling worth it for Python? OpenClaw's bundling exists
because npm's resolution is slow and npm-flat-vs-pnpm-symlink quirks. `uv
sync` from a frozen lockfile is fast (~10s warm). We could instead just bake
the full venv into the rootfs and roll the rootfs more often. *Initial bias:*
bundle anyway — keeps the rootfs-vs-runtime decoupling, lets us hotfix Hermes
without a full rootfs rebuild, and matches an existing operational mental model.

### 4.3 New init script

`scripts/init-hermes.sh` (new). The phase structure of `init-openclaw.sh` (57
KB) carries over almost identically:

1. Mount /proc, /sys, /dev, /dev/pts, /run, /tmp ✅ unchanged
2. Detect & mount `/dev/vdb` → `/data`, bind-mount persistent dirs ✅ unchanged
3. Swap on `/data` ✅ unchanged
4. `ocm-migrate` for data-volume schema upgrades ✅ unchanged
5. Entropy seeding ✅ unchanged
6. Read `metadata_nonce` from `/proc/cmdline` ✅ unchanged
7. **NEW:** Fetch Hermes runtime from GCS → extract to `/data/ocm/runtime/hermes/current/`, point `$VIRTUAL_ENV` at it
8. **NEW:** Pull config from metadata server's `/v1/secrets` (Hermes-shaped — see §4.4), write to:
   - `$HERMES_HOME/config.yaml`
   - `$HERMES_HOME/.env` (model keys, channel tokens)
   - `$HERMES_HOME/auth.json` (via `HERMES_AUTH_JSON_BOOTSTRAP` env, first boot only)
   - `$HERMES_HOME/SOUL.md` (default persona, or user-customised from UI)
9. Skills sync — copy `/opt/hermes/skills` → `$HERMES_HOME/skills` (Hermes's own `tools/skills_sync.py` handles this idempotently)
10. **NEW:** Start runit services: `hermes-gateway`, `hermes-dashboard`,
    `pty-server`, `cloudflared`, `authproxy`. **Cron is *not* a separate
    service** — Hermes ticks cron jobs from inside the gateway process
    (`gateway/run.py` calls `_start_cron_ticker` on boot); `hermes cron`
    is only a management/tick CLI, not a daemon. Adding a separate
    `hermes-cron` service would either fail or run a duplicate scheduler.
11. Cloudflared tunnel run ✅ unchanged
12. Log forwarding to `/v1/logs` ✅ unchanged

The triple env-scoping gotcha noted in CLAUDE.md (gateway env, PTY env,
`/etc/profile.d/`) applies to Hermes too — any tokens that need to be visible
to spawned subprocesses (e.g. `hermes <skill>` shelling out) need to land in
the PTY server env block, not just `/etc/profile.d/`.

### 4.4 New config assembler

`configassembly` keeps its shape — three layers (platform defaults → user
config → credential injection) — but emits Hermes's schema:

- **`config.yaml`** for non-secret settings (`model.default`,
  `model.provider`, `platforms.*`, `platform_toolsets`, `dashboard.*`,
  toolset toggles, cron jobs, paths). Hermes's
  `cli-config.yaml.example` is the canonical schema reference.
- **`.env`** for secrets, using Hermes's env-var names:
  - Models: `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
    `GOOGLE_API_KEY`, `OLLAMA_API_KEY`, `GLM_API_KEY`, `KIMI_API_KEY`,
    `MINIMAX_API_KEY`, `ARCEEAI_API_KEY`, `HF_TOKEN`, `OPENCODE_ZEN_API_KEY`,
    etc.
  - Channels: `TELEGRAM_BOT_TOKEN`, `DISCORD_BOT_TOKEN`, `SLACK_APP_TOKEN`,
    `SLACK_BOT_TOKEN`, plus the long-tail Hermes-only ones (Matrix, Feishu,
    DingTalk, WeChat, Teams) as we add them to the keep-list
- **`auth.json`** — Hermes's device-code/OAuth provider store for providers
  such as Nous Portal, OpenAI Codex, and MiniMax OAuth. Seed via
  `HERMES_AUTH_JSON_BOOTSTRAP` env var on first boot only. **Do not assume
  every subscription credential lands here:** Anthropic's Hermes-native PKCE
  flow stores refreshable credentials in `$HERMES_HOME/.anthropic_oauth.json`,
  while plain Anthropic API keys remain `ANTHROPIC_API_KEY` in `.env`.

`ChannelTokenFields` in `backend/internal/configassembly/assembler.go` is
the right pattern for OpenClaw's emitter, but Hermes needs a kind-aware
projection table because the destination names are env vars, not
`openclaw.json` fields. Keep the existing `ChannelTokenFields` invariant for
OpenClaw (`TestChannelKeepListMatchesBuildScript` guards the scrubbed
OpenClaw build), and add a Hermes-specific `channel -> env-var(s)` map with
its own invariant test as we expose additional channels.

**ProviderToChannel** mapping needs an audit. Hermes's surface has more
distinct provider concepts (Telegram vs Telegram Web, Slack OAuth vs Bot
Token, etc.). Start with the OCM-supported four (Telegram/Discord/Slack/
WhatsApp) and expand.

### 4.5 New backend "kind" awareness

Today the backend distinguishes the OpenClaw machine flow from the paired
browser-VM flow via a separate `browser_vms` table joined via
`machines.browser_vm_id`. Browser VMs are **not** a machine "kind"; they're a
distinct resource. We keep that split.

Add a `kind` enum on the `machines` table with exactly two values:

- `openclaw` — existing
- `hermes` — new

Browser VMs remain in the `browser_vms` table and keep their existing
linkage (`machines.browser_vm_id`). The agent dispatch reads `machines.kind`
to pick rootfs/runtime manifests and the right config emitter; browser-VM
provisioning continues through its own table-driven path unchanged.
Frontend create-machine flow grows a kind picker (OpenClaw vs Hermes).
Pricing, quotas, and admin views learn the new kind.

### 4.6 Frontend changes (minimal)

The existing one-click flow already does the right shape:

- "Choose your model" page → maps to a credential provider → we extend the
  catalog with Hermes-specific providers (z.ai/GLM, Kimi, MiniMax, Arcee,
  Nous Portal, OpenCode Zen/Go, HF Inference Providers, Bedrock,
  Ollama Cloud)
- Out of the box, a Hermes machine must be able to chat through OCM's
  platform Nebius-backed default model without BYOK. Use the same platform
  model path as OpenClaw (`openai/gpt-oss-120b` today, mapped internally to
  `nebius/openai/gpt-oss-120b`) when the user has not selected another model.
- "Connect channels" page → already has Telegram/Discord/Slack — we add the
  Hermes-only channels here as they're proven out
- "Browser integration" toggle → for now: a checkbox that enables Playwright
  in-VM (since Hermes does it locally). Drop the "pair browser VM" UI for
  Hermes-kind machines unless someone needs stealth.

The **dashboard** is interesting: Hermes ships a real web UI at `:9119`. We
should expose this through the existing per-VM Cloudflare Tunnel — replaces
or supplements the current Filebrowser link in the machine detail page.
For the first hosted UX, this is not just a passive iframe: the dashboard
must expose Hermes's browser webchat, including the PTY/WebSocket backing
service (`hermes dashboard --tui` or `HERMES_DASHBOARD_TUI=1`) and prefix-safe
proxying for `/dashboard/api/*` and `/dashboard` WebSockets. Users should be
able to open the Chat tab without dropping into SSH.

Placement requirement: Hermes webchat/dashboard should appear where the
OpenClaw control interface appears today. In the existing `MachineWorkspace`
split view, the "gateway/interface" pane should point at Hermes dashboard for
`kind=hermes`, not a separate or harder-to-find product surface. A
machine-detail "Dashboard" tab may still exist as a deep link, but the primary
launch point should be the same Workspace/Interface affordance users already
use for OpenClaw.

Keep the OCM terminal view as a separate first-class surface for Hermes. A
running Hermes machine should have the same "Workspace"/terminal affordance
as OpenClaw, backed by `ocmptyd` on `:7681`. The default terminal tabs should
be Hermes-aware (Shell plus a Hermes TUI tab that runs `hermes`, not
`openclaw`). Kanban is not part of this baseline; it can ride on the dashboard
plugin system later after the core dashboard, webchat, and terminal paths are
working.

The OCM CLI must be kind-aware for Hermes machines too. Users should be able
to run the same fleet/connection commands against a Hermes machine without
learning its internal service layout: `ocm machines ssh <slug>` opens a shell
where `hermes` is on PATH, `ocm machines dashboard <slug> --open` opens the
tunneled Hermes dashboard/webchat, `ocm machines serve chat <slug>` targets
Hermes's OpenAI-compatible chat endpoint, and `ocm machines logs <slug>`
streams Hermes service logs.

## 5. One-click installations — what each "click" wires up

For a Hermes machine, the bring-up checklist becomes:

| Click | What gets injected | Where it lands in the VM |
|---|---|---|
| Pick a model (e.g. Anthropic Sonnet via OpenRouter) | `OPENROUTER_API_KEY` (or direct provider key) + `model.provider` / `model.default` in config.yaml | `$HERMES_HOME/.env` + `$HERMES_HOME/config.yaml` |
| Use default platform model | No user credential required; select OCM's Nebius-backed platform default (`openai/gpt-oss-120b` today) | `$HERMES_HOME/config.yaml` model default + metadata-injected platform Nebius key |
| Pick a personality | Selected SOUL.md content | `$HERMES_HOME/SOUL.md` |
| Connect Telegram | `TELEGRAM_BOT_TOKEN` + `platforms.telegram` config | `.env` + `config.yaml` |
| Connect Slack | `SLACK_APP_TOKEN`, `SLACK_BOT_TOKEN` + `platforms.slack` config | `.env` + `config.yaml` |
| Connect Discord | `DISCORD_BOT_TOKEN` + `platforms.discord` config | `.env` + `config.yaml` |
| Enable MCP | Generate bridge token + enable the MCP bridge adapter | bridge config + runit unit wrapping `hermes mcp serve` stdio; no native Hermes port exists |
| Enable browser tools | Playwright already baked; flip toolset toggle in config.yaml | `config.yaml` |
| Enable cron | User-defined schedules become entries in `$HERMES_HOME/cron/` | rendered from metadata `/v1/secrets` |
| Enable dashboard + webchat | Expose `:9119` through the per-VM tunnel and start the dashboard with embedded chat enabled (`--tui` or `HERMES_DASHBOARD_TUI=1`) | tunnel route + authproxy entry + PTY/WebSocket-compatible prefix proxying |
| Open VM terminal | Reuse `ocmptyd` for shell access and Hermes TUI sessions | `:7681` terminal route + frontend Workspace/Terminal view |
| Connect with OCM CLI | Resolve kind-aware connection targets for shell, dashboard, webchat/API, and logs | `ocm machines ssh`, `ocm machines dashboard --open`, `ocm machines serve chat`, `ocm machines logs` |
| Allowlist editor (ACP) | Generate bridge token + enable the ACP bridge adapter | bridge config + runit unit wrapping `hermes acp` stdio; no native Hermes port exists |
| Migrate from OpenClaw machine | Pre-seed `$HERMES_HOME` from the user's existing OCM machine state | one-shot script that uses Hermes's own `claw migrate` against an OCM data-volume snapshot |

The migration row is a quietly powerful product story: any OCM user can flip
their machine kind to `hermes` and keep their persona, skills, memories,
allowlist, and tokens. We should make this a first-class flow.

## 5a. UI integrations

The OCM frontend already has the right shape for a kind-pluggable product —
we're mostly extending existing surfaces, not building new ones. Per-component
audit:

### 5a.1 Create flow (`components/CreateMachineModal.tsx`, `pages/MachineCreate.tsx`)

Today's modal collects: name, region, rootfs version, openclaw version, vCPU,
memory, disk. The payload posts `openclaw_version` + `rootfs_version`.

**Hermes changes:**
- Add a **kind picker** at the top of the modal: `OpenClaw` ⇄ `Hermes` (radio
  or segmented control). Render kind-specific version dropdowns
  conditionally — Hermes machines pick from `hermes_version` + `hermes_rootfs_version`,
  not the OpenClaw ones.
- Payload grows a `kind` field; backend uses it to pick the right manifest
  family and config emitter.
- Replace "Pair browser VM" with a "Browser tools" toggle for Hermes-kind
  machines (Playwright is in-VM; no pairing needed). Keep the existing browser
  VM flow available behind an "Advanced → use paired browser VM" disclosure
  for stealth-heavy workloads.
- Hermes-only optional: **persona dropdown** (pick a SOUL.md preset) and
  **starter skills** multiselect. Default persona = "balanced"; default skills
  = empty.

### 5a.2 Onboarding wizard (`pages/OnboardingWizard.tsx`, `components/onboarding/`)

Seven steps today: Identity → Account → Provider → Model → Channels →
Machine → Launch. Most of these are kind-agnostic and reuse cleanly.

**Hermes changes:**
- `StepProvider` and `StepModel` need a Hermes-aware catalog. See §5a.5
  below.
- `StepChannels` needs a Hermes-aware channel list (start with the same 4,
  expose more behind a feature flag). See §5a.6.
- `StepMachine` needs the kind picker. Hermes can be the default for new
  signups once we're confident.
- `StepLaunch`'s "what's about to happen" preview text gets kind-aware
  copy ("starting your Hermes agent…").

### 5a.3 Machine detail page (`pages/MachineView.tsx` + `pages/machine-tabs/`)

Tabs today: Overview, Model, Channels, Browser, Files, Integrations, Traces,
Usage, Resources, WebSearch, Backups. All of these *should* render for both
kinds, with content differences per tab.

**Hermes-specific tab behavior:**

| Tab | OCM today | Hermes |
|---|---|---|
| Overview | Shows tunnel URL → control UI + filebrowser + terminal | Show the same primary **Workspace/Interface** entry point, but for Hermes it opens the Hermes dashboard/webchat (`:9119`) in the interface pane + Terminal/Workspace. Drop filebrowser link by default. |
| Dashboard / Interface | OpenClaw control UI appears in the workspace gateway pane | Hermes dashboard/webchat must appear in that same workspace gateway/interface pane. It must support working Chat tab, sessions, files, config, skills, dashboard-relative REST, and WebSocket calls under the OCM proxy prefix. |
| Terminal / Workspace | Full browser terminal, Shell + OpenClaw TUI + logs | Same browser terminal, but Hermes-aware: Shell + Hermes TUI (`hermes`) + logs. This must be reachable from machine cards and the machine detail header, not hidden behind the dashboard. |
| Model | Provider/model picker, BYOK key entry | Same picker; **larger catalog** (see §5a.5). Add "fallback chain" UI (Hermes supports priority-ordered providers natively). |
| Channels | Telegram/Discord/Slack/WhatsApp wizard | Same wizard, **wider channel catalog** behind a feature flag. |
| Browser | "Pair browser VM" + paired VM detail | **For Hermes:** "Browser tools enabled (in-VM)" status + Playwright headless/headed toggle + optional Camofox stealth toggle + "Use paired browser VM instead" advanced option. |
| Files | Filebrowser iframe at `:9000` | Hermes dashboard's file view (via `:9119`) — or keep filebrowser for raw file UX. |
| Integrations | Composio OAuth connect | Same. Plus: **MCP server toggle** ("Expose this machine's transcripts/tools to my editor"), **ACP toggle** ("Let Claude Code/Cursor attach as a driver"). |
| Traces | Opik traces | Same plumbing; Hermes's trajectory data lands in the same Opik account. |
| Usage | Per-model spend + token counts | Same. |
| Resources | CPU/memory metrics per VM | Same. |
| WebSearch | Provider keys for SerpAPI/Tavily/etc. | Same. |
| Backups | Data-volume snapshots | Same. |

**New tabs (Hermes-only, optional):**
- **Persona** — edit `SOUL.md` in the browser. Hermes treats persona as a
  product-level concept; today users wrestle with files. Live editor +
  presets.
- **Skills** — `MachineSkills.tsx` already exists for OpenClaw. For Hermes,
  wire it to the Skills Hub catalog (open-source GitHub-based marketplace) so
  users can browse + one-click install community skills.
- **Cron** — Hermes has rich built-in scheduling. A simple "scheduled jobs"
  list with add/edit/delete is high-value.

### 5a.3a Verified component reuse: model and messaging interfaces

The two most-used UI surfaces in OCM today — the **model picker / configure
modal** and the **channel wizard** — were spot-checked for kind coupling
with mixed results. The model picker is clean. Messaging reuses the same
credential-vault/projector model, but the active channel UI needs more than a
catalog swap:

- `ModelPicker.tsx` — **zero** OpenClaw-specific references. Takes
  `ModelEntry[]` with `source ∈ {platform, byok, subscription}`. Reuses
  byte-identical for Hermes.
- `ConfigureModelsModal.tsx` — **zero** OpenClaw-specific references.
  Render-the-catalog-and-take-an-API-key flow reuses.
- `AnthropicSubscriptionConnect.tsx`, `OpenAICodexConnect.tsx` — OAuth flow
  reuses. *Backend projection* changes: store the refresh token in the
  per-account credential vault under a kind-neutral schema; let
  `configassembly` project it into the exact target the guest reads:
  `openclaw.json` for OpenClaw; for Hermes, `auth.json` for device-code
  providers such as Nous/OpenAI Codex/MiniMax OAuth, `.anthropic_oauth.json`
  for Hermes-native Anthropic PKCE, or `.env` for plain API-key providers.
- `ChannelsTab.tsx` is the active per-machine channel UI (`MachineView`
  renders this tab). It currently bakes `MESSAGING_CHANNELS`, runs
  `openclaw pairing approve`, and ships an OpenClaw-branded Slack manifest.
  Hermes needs a backend-served channel catalog **plus** kind-aware pairing
  actions and kind-aware app-manifest branding.
- `ChannelSetup.tsx` is a legacy/account-level component with its own
  hard-coded `CHANNELS` const. Either retire it or move it to the same
  backend-served catalog so it does not drift from `ChannelsTab`.

**The mental model that makes this work:** the OCM credential vault is the
single source of truth. `configassembly` is a *projector* that emits the
same credential row into whatever shape the guest expects. A user pastes a
Telegram bot token **once** against their account; pointing it at an
OpenClaw machine writes `"botToken": "..."` into `openclaw.json`, pointing
it at a Hermes machine writes `TELEGRAM_BOT_TOKEN=...` into `.env`. **One
credential row, two emissions, same UI.**

This is the property that also makes the OpenClaw → Hermes machine
migration trivial: credentials don't move between vaults, only the
projection changes.

Concrete backend changes to make the reuse real:

1. **`/api/v1/models?kind=hermes`** — kind-aware catalog endpoint. Returns
   the same `ModelEntry[]` shape the frontend already consumes, scoped to
   what the target guest supports.
2. **`/api/v1/channels?kind=hermes`** — same shape for channels. Replaces
   the hard-coded `MESSAGING_CHANNELS` / `CHANNELS` frontend consts.
3. **`configassembly` projection table** — extend the existing
   `ChannelTokenFields` pattern with `ProviderEnvVars` for Hermes's broader
   model catalog (`OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, …,
   `OPENCODE_GO_API_KEY`, `HF_TOKEN`). Drive the emitter from this map.
4. **Subscription token projection** — exact Hermes credential targets
   (`auth.json`, `.anthropic_oauth.json`, or `.env`, depending on provider)
   vs inlined-in-`openclaw.json` for OpenClaw. Same OAuth refresh token row
   in the vault; provider-specific projection codepaths.

The frontend changes are limited to:
- Threading a `kind` prop into the model picker host (so it requests the
  right catalog) and the channel-tab host (same).
- Moving `MESSAGING_CHANNELS` in `ChannelsTab.tsx` (and the legacy
  `ChannelSetup.tsx` `CHANNELS` const if it stays) to a fetched list.
- Making pairing approval kind-aware: OpenClaw can keep
  `openclaw pairing approve`; Hermes must call the Hermes equivalent or hide
  the approval widget until that command path exists.
- Making generated app manifests kind-aware (Slack currently says
  "OpenClaw").

That's the real "use the same UI for Hermes" delta on these two surfaces:
low-risk reuse for models, moderate work for channels.

### 5a.4 Components to add or extend

| Component | Status | Change |
|---|---|---|
| `CreateMachineModal.tsx` | Existing | Add kind picker + Hermes-only optional fields |
| `ModelPicker.tsx` | Existing — uses `ModelEntry` with `source ∈ {platform, byok, subscription}` | Reusable as-is; just feed it a Hermes-specific list |
| `ConfigureModelsModal.tsx` | Existing | Extend with Hermes-only providers (z.ai, Kimi, MiniMax, etc.) |
| `ChannelsTab.tsx` | Existing — active MachineView channel UI, hard-coded `MESSAGING_CHANNELS` | Move catalog to backend, make pairing command/app manifest kind-aware, widen supported channels by kind |
| `ChannelSetup.tsx` | Existing legacy/account-level component — hard-coded `CHANNELS` | Retire, or move to the same backend catalog if still used |
| `SecretVault.tsx` | Existing | Reuse — Hermes secrets store in the same vault |
| `MachineConfig.tsx` | Existing | Add kind-aware sections (persona, skills) |
| `MachineWorkspace.tsx` | Existing | Make the gateway/interface pane kind-aware: OpenClaw control UI for OpenClaw, Hermes dashboard/webchat for Hermes |
| **`PersonaEditor.tsx`** | **New** | SOUL.md editor with presets |
| **`HermesDashboard.tsx`** | **New (thin)** | iframe target/helper for `:9119` via tunnel + auth proxy, mounted into the existing workspace interface pane |
| **`KindBadge.tsx`** | **New (small)** | Shows "OpenClaw" / "Hermes" pill on machine cards + machine detail header |
| `MachineCard.tsx` | Existing | Render the kind badge |
| `BrowserVMsPage.tsx` | Existing | Filter to OpenClaw-kind machines for pairing; Hermes has its own browser tools |
| `OnboardingWizard.tsx` | Existing | Inject kind picker; route Hermes users through Hermes-flavored copy |

### 5a.5 Model catalog expansion

The current `ModelPicker` is provider-agnostic — it takes a `ModelEntry[]`.
Where the work lives is the *catalog source*: today `ConfigureModelsModal`
implies OpenRouter as the primary path with a few BYOK options (OpenAI,
Anthropic) and Anthropic/OpenAI subscriptions.

Hermes's wider universe (see §2 and the env names in §4.4) means:

- **Catalog grouped by class:** "Aggregators" (OpenRouter, OpenCode Zen/Go,
  HuggingFace Inference), "Direct cloud" (Anthropic, OpenAI, Gemini, Bedrock,
  Ollama Cloud), "Specialty" (z.ai/GLM, Kimi/Moonshot, MiniMax, Arcee, Nous
  Portal), "Local" (LM Studio, Ollama, vLLM — point-at-endpoint URL only).
- **Backend extension:** `configassembly` learns a `provider → env-var-name`
  map for each Hermes provider; the create-machine endpoint injects the right
  `.env` line based on what credentials the user has stored.
- **Fallback chain UI:** small reorderable list of 2-3 providers. Hermes
  honors this natively.
- **"Detect from credentials" mode:** if `model.provider = "auto"`, Hermes
  picks whichever has a key. Surface this as a checkbox in the UI.

### 5a.6 Channel catalog expansion

`ChannelTokenFields` in the backend is the source of truth (and is invariant-
tested) for token-backed OpenClaw channels. The active frontend
`ChannelsTab` currently bakes its catalog inline (`MESSAGING_CHANNELS`), and
the legacy `ChannelSetup` component has a separate `CHANNELS` const. To
support Hermes-only channels (Matrix, Feishu, DingTalk, WeChat, Teams, SMS,
Email, Home Assistant) **without** breaking OpenClaw machines:

- Backend exposes a `/api/v1/channels?kind=hermes|openclaw` endpoint that
  returns the supported channel set + their token field schemas.
- `ChannelsTab` consumes that instead of hard-coding `MESSAGING_CHANNELS`
  (and `ChannelSetup` does the same if it remains in the product).
- Per-machine, the UI only shows channels compatible with that machine's
  kind.
- Rollout order (proposed): Matrix → WhatsApp/Signal (proven elsewhere) →
  Email → Teams → Feishu/DingTalk/WeChat as customer demand arrives.

### 5a.7 Dashboard, webchat, terminal + ACP exposure

Hermes's web dashboard at `:9119` is its strongest UI advantage. The
tunneling story we already have for the OpenClaw control UI applies:

1. Add `:9119` to the per-VM tunnel route map.
2. `authproxy` validates Cloudflare Access JWT → forwards to localhost:9119.
3. Start the dashboard with webchat enabled (`hermes dashboard --tui` or
   `HERMES_DASHBOARD_TUI=1`) so the browser Chat tab can open the local
   PTY/WebSocket service.
4. Preserve the proxy prefix for dashboard REST and WebSocket calls. The
   Hermes SPA understands `X-Forwarded-Prefix`; the OCM agent/auth proxy must
   provide the equivalent prefix when serving it at `/{machineSlug}/dashboard/`
   or `/api/accounts/{id}/machines/{id}/dashboard/`.
5. The frontend routes Hermes dashboard/webchat into the same workspace
   gateway/interface pane where OpenClaw shows its control UI today
   (`/workspace/:id?view=gateway` and the equivalent production data-plane
   URL).
6. The Overview action and machine-card action should lead to that same
   Workspace/Interface surface. An embedded `MachineView` tab is acceptable as
   a shortcut, but it must not be the only way to find webchat.

The terminal is separate from the dashboard. Keep `/workspace/:id` working for
Hermes machines and expose it with a visible "Workspace" or "Terminal" action
on machine cards and the machine detail page. The default tabs should be
Shell, Hermes TUI (`command=hermes`), and logs. Do not reuse the OpenClaw TUI
tab (`command=openclaw`) for Hermes machines.

CLI access is part of the same requirement. The `ocm` CLI should resolve a
Hermes machine by slug or ID, inspect `kind=hermes`, and connect to the right
surface:

- `ocm machines ssh <slug>`: same Cloudflare Access SSH path as OpenClaw,
  with a Hermes-ready environment.
- `ocm machines dashboard <slug> --open`: opens the authenticated dashboard
  URL, including the webchat-enabled route.
- `ocm machines serve chat <slug>`: serves or prints the Hermes
  OpenAI-compatible endpoint for local/third-party clients.
- `ocm machines logs <slug> --follow`: streams `hermes-gateway`,
  `hermes-dashboard`, `pty-server`, `authproxy`, and tunnel logs.

Model bootstrap is also part of MVP. If no model is explicitly selected,
Hermes machines should default to OCM's platform Nebius-backed model
(`openai/gpt-oss-120b` today) and route through the same metadata-injected
platform key/proxy path OpenClaw uses. Missing platform Nebius configuration is
an operator readiness failure, not a user BYOK requirement.

Kanban can come later. It should not block the hosted Hermes MVP; once the
dashboard/webchat and terminal surfaces are proven, Kanban can be enabled as a
dashboard plugin route with its own smoke coverage.

**ACP** is more sensitive (an attached editor can drive the agent). Gate
behind an opt-in toggle in `IntegrationsTab`; emit an access token the user
copies into their editor. This is **not** a tunnel-only change: `hermes acp`
is stdio-only, so the toggle depends on an ACP bridge service first. The UI
pattern can mirror the existing CLI auth flow (`pages/CliAuth.tsx`) once that
bridge exists.

### 5a.8 OpenClaw → Hermes machine migration UI

`hermes claw migrate` is a one-shot script. For our UX:

- "Convert to Hermes" button on an OpenClaw machine's Overview tab.
- Confirmation modal lists what will move (persona, memories, skills,
  channel tokens, model keys) and what won't.
- Backend orchestrates: takes a snapshot of the OpenClaw data volume, spins
  up a temporary Hermes VM, runs `hermes claw migrate` pointing at the
  snapshot mount, swaps the machine record to `kind=hermes` + new rootfs/
  runtime versions, hot-swaps the tunnel.
- This is the kind of magic that closes the loop — a power user can flip a
  switch and keep everything.

### 5a.9 Admin pages (`pages/admin/`, `components/Admin*.tsx`)

- Add kind filtering to machine lists.
- Add `kind=hermes` and `kind=hermes-rootfs` to artifact registration UIs.
- `AdminOpenClawPin.tsx` (which pins the OpenClaw version per host) gets a
  Hermes sibling — `AdminHermesPin.tsx`.

### 5a.10 Out-of-scope for the first cut

To keep the first cut tractable, defer:
- Persona Hub (community SOUL.md marketplace)
- Skills Hub embedded browsing (link out to agentskills.io for now)
- Cron UI (users can edit `~/.hermes/cron/` via the file UI initially)
- Telegram-style "second-screen" mobile UX (Hermes can run on a $5 VPS for
  this; our value-add is the curated control plane, which we extend incrementally)

## 5b. UX integrations — does each one carry over?

Walking the integrations we already built for OpenClaw, asking for each:
*does the existing UX work for Hermes, what changes, what's new?* Short
answer: **most carry over with the existing components, a few need backend
shims, a small handful are new.**

### 5b.1 Model selection & BYOK

**OpenClaw today:** `ModelPicker` + `ConfigureModelsModal` + tiers
(`platform`/`byok`/`subscription`); `AnthropicSubscriptionConnect` and
`OpenAICodexConnect` for OAuth-based subscription models.

**Hermes carry-over:**
- ✅ `ModelPicker.tsx` works as-is — it's catalog-driven (`ModelEntry[]`).
- ✅ Tier concept (platform/BYOK/subscription) maps cleanly. **Subscription
  tier expands** to include Nous Portal (Nous's own subscription) alongside
  Anthropic + OpenAI Codex.
- ✅ `AnthropicSubscriptionConnect` and `OpenAICodexConnect` keep working —
  Hermes consumes the same OAuth refresh tokens via
  `plugins/model-providers/anthropic` and `plugins/model-providers/openai-codex`.
- 🔧 **Catalog grows substantially.** Hermes's `plugins/model-providers/` has
  ~29 providers (openrouter, anthropic, gemini, bedrock, deepseek, copilot,
  qwen-oauth, xai, ai-gateway, kimi-coding, kilocode, alibaba, gmi,
  huggingface, minimax, nous, nvidia, ollama-cloud, opencode-zen, stepfun,
  xiaomi, zai, plus per-vendor variants). Backend learns a per-provider
  `env_var_name + base_url + auth_style` map.
- ➕ **New: fallback chain UI.** Hermes supports priority-ordered
  providers with auto-retry on quota/latency. Small reorderable list.
- ➕ **New: "auto-detect from credentials" mode** (`model.provider = "auto"`).
  Single checkbox in the model picker.

### 5b.2 Observability (Opik traces, feedback, tags)

**OpenClaw today:** `TracesTab.tsx`, `TraceFeedbackPanel`, `TraceTagsPanel`,
`Observability.tsx` — all backed by Opik. Per-machine + global. Indexed tag
search, span debug cues.

**Hermes carry-over:**
- ✅ **All four components reuse unchanged.** The UI talks to Opik, not to
  the agent — payload doesn't matter.
- 🔧 Hermes ships `plugins/observability/langfuse` out of the box. We need
  to either:
  - (a) Write a thin `plugins/observability/opik` plugin (or contribute one
    upstream) so Hermes emits to the same Opik account; or
  - (b) Add Langfuse support to OCM's observability backend and let users
    pick. Lean: (a) — same backend keeps the rest of the UI working.
- ➕ **New: trajectory export.** Hermes generates RL-grade trajectories that
  Opik can capture, but there's an additional surface: a "download
  trajectories for this machine" action that bundles them in Atropos
  format. Defer to phase 5; the backend has the data either way.
- ➕ **New: skill-creation events.** Auto-written skills are interesting
  trace events ("agent wrote skill X after task Y"). Worth surfacing as a
  filter facet in `TracesTab`.

### 5b.3 Browser automation

**OpenClaw today:** Separate browser VM rootfs (Alpine + Chromium + stealth
ext). Frontend has `BrowserVMsPage`, `BrowserVMDetailPage`,
`BrowserVMLivePage` (CDP live-view + screencast), `BrowserTab` on the
machine view showing the paired VM. Sizing, IP allocation, WebRTC slots.

**Hermes carry-over:**
- 🔧 **Architectural shift for default Hermes machines.** Hermes uses
  Playwright *in-VM* (`tools/browser_tool.py` plus Camofox for stealth). The
  default Hermes machine doesn't need a paired VM. So:
  - `BrowserTab` for Hermes-kind machines shows "in-VM Playwright" status
    + headless/headed toggle + Camofox stealth toggle + "Use paired browser
    VM instead (advanced)" disclosure.
  - The "advanced" disclosure surfaces the existing pairing flow.
- ✅ **The paired browser VM stays available.** Hermes already supports
  remote CDP via `BROWSER_CDP_URL` and `browser.cdp_url` in config.yaml —
  we point it at a paired browser VM and the existing `BrowserVMsPage` /
  `BrowserVMLivePage` / `BrowserVMDetailPage` keep working *exactly as
  today.* No code changes for the stealth use case.
- ➕ **New: in-VM live view.** For default Hermes machines, give users a
  "watch the agent browse" panel. Hermes can serve a screencast through its
  dashboard; embed it. Defer to phase 5 — the default Playwright headless
  experience is fine for v1.
- ➕ **New: accessibility-snapshot view.** Hermes's per-element `uid` refs
  are a uniquely auditable way to see what the agent saw. A "last DOM
  snapshot" panel is a nice-to-have, defer.

### 5b.4 Workspace + Files

**OpenClaw today:** `FileBrowser.tsx` iframe at `:9000`, `MachineWorkspace`
page, `FilesTab`. The filebrowser binary baked into the rootfs.

**Hermes carry-over:**
- ✅ Filebrowser keeps working in a Hermes VM (it's a generic binary
  serving `/data`). Zero-change path.
- 🔧 Hermes dashboard at `:9119` has its own (richer) file/session/skill
  view. Default Hermes machines can route the existing "Files" link there
  instead, but the iframe pattern is identical.
- ➕ **New: portable workspace export.** Zip + download
  `$HERMES_HOME/workspace` (and optionally the whole `$HERMES_HOME`). More
  important for Hermes than OpenClaw — see §0 (precious data volume).

### 5b.5 Chat (talking to a machine from the web UI)

**OpenClaw today:** `ChatPage.tsx` embeds the gateway's control-UI in an
iframe (`dataPlaneUrl(..., "gateway/")`); it's a *frame around the
guest's own chat UI*, not a chat-completions client. `GatewayDashboard.tsx`
for status.

**Hermes carry-over:**
- ✅ **The iframe pattern reuses cleanly** — same component shape, different
  target. Point the iframe at Hermes's dashboard at `:9119` instead of the
  OpenClaw gateway control UI. Same auth-proxy plumbing, same render. This
  is the recommended path.
- 🔧 **For programmatic chat** (curl / Open WebUI / `ocm machines serve
  chat`), use Hermes's `POST /v1/chat/completions` or `/v1/responses`
  endpoint directly — these are real HTTP APIs and require no UI changes
  on our side.
- ➕ **Optional later:** build a native browser chat client that lives in
  the OCM shell and speaks chat-completions directly (no iframe). Better
  UX unification across kinds; deferrable.

### 5b.6 Terminal (web-based shell into the VM)

**OpenClaw today:** `Terminal.tsx` over the `ocmptyd` PTY server on
`:7681`, fronted by the tunnel + auth proxy.

**Hermes carry-over:**
- ✅ **Zero changes.** `ocmptyd` doesn't know what runs in the VM. We keep
  it baked in the rootfs.

### 5b.7 Usage & billing

**OpenClaw today:** `UsageDashboard.tsx`, `UsageTab.tsx`, per-model spend
+ token counts via Opik billing migration (per `Feature_28.md` precedent).

**Hermes carry-over:**
- ✅ Components reuse unchanged.
- 🔧 **Attribution change:** Hermes's fallback chain means a request may
  hit provider B after provider A fails. The billing pipeline must capture
  *which provider actually served the request*, not just the configured
  default. Opik spans already carry this — verify it's surfaced.
- 🔧 New providers in the catalog need price-list entries. Cold-launch path:
  ship with the 4–5 highest-traffic providers priced, others "unmetered" until
  we add them.

### 5b.8 Composio (third-party tool platform)

**OpenClaw today:** `IntegrationsTab.tsx` does Composio OAuth popup
(`composio-connect` window), backend stores the credential.

**Hermes carry-over:**
- 🔧 Hermes doesn't have a Composio plugin in `plugins/` today. Two options:
  - (a) Write a Hermes plugin that consumes the Composio creds we store
    and registers Composio tools at startup.
  - (b) Expose the Composio REST proxy (we already have one — see
    `Feature_29.md`) directly to Hermes's HTTP tool surface so it just sees
    "a tool endpoint."
- Lean: **(b)** for speed — Hermes can consume any HTTP API as a tool, no
  plugin authoring required.

### 5b.9 Web search

**OpenClaw today:** `WebSearchTab.tsx` collects keys for SerpAPI / Tavily /
Brave / Exa.

**Hermes carry-over:**
- ✅ Component reuses unchanged.
- 🔧 The keys land in `$HERMES_HOME/.env` as `TAVILY_API_KEY` /
  `SERPAPI_API_KEY` / etc. Hermes's tools pick them up automatically. Just
  needs the right env-var names in the secrets map.

### 5b.10 Backups & snapshots

**OpenClaw today:** `BackupsTab.tsx`, manual data-volume backup/restore
(per `2026-03-11-backup-design.md`).

**Hermes carry-over:**
- ✅ Plumbing reuses unchanged.
- 🚨 **Elevated priority.** A Hermes machine's accumulated state *is* the
  product (see §0). Push for:
  - Automatic daily snapshot (vs OpenClaw's manual today)
  - 30-day retention default
  - One-click `$HERMES_HOME` archive export (portable agent)
  - Restore-to-different-machine as a first-class path (move your agent
    between regions / providers / kinds)

### 5b.11 Secrets vault

**OpenClaw today:** `SecretVault.tsx` — generic key/value store.

**Hermes carry-over:** ✅ **Zero changes.** The secrets vault is
payload-agnostic.

### 5b.12 Skills

**OpenClaw today:** `MachineSkills.tsx` — list, enable/disable, install.

**Hermes carry-over:**
- ✅ Component reuses unchanged.
- 🔧 Catalog source changes: point at Skills Hub (agentskills.io,
  GitHub-based open standard Hermes uses) instead of OpenClaw's internal
  registry.
- ➕ **New: badge for auto-written skills.** Skills the agent wrote itself
  should be visibly distinct from installed-from-Hub or hand-written ones.
  "Created by your agent on 2026-04-23 after task: refactor caching" —
  this is the moment the self-learning loop becomes visible UX.
- ➕ **New: skill provenance + edit history.** Hermes refines skills in
  use; show a small diff history so users can see how a skill evolved.

### 5b.13 Plugins

**OpenClaw today:** `MachinePlugins.tsx` lists OpenClaw plugins
(opik-openclaw, composio, etc.).

**Hermes carry-over:**
- 🔧 **Repurpose.** Hermes's `plugins/` tree is bigger and more central:
  memory providers, context engines, model providers, kanban,
  observability, image gen, etc. The existing component is a good shell;
  the catalog underneath becomes Hermes's plugin registry. Grouping by
  category (memory / observability / model-provider / …) is the change.
- ➕ **New: substrate pickers.** "Pick your memory provider" (honcho / mem0
  / supermemory / file) is plugin selection. Surface defaults explicitly,
  let advanced users change.

### 5b.14 Logs

**OpenClaw today:** `LogConsole.tsx` streams logs from the agent host.

**Hermes carry-over:** ✅ **Zero changes** — generic log streaming.

### 5b.15 Resources (CPU/memory metrics)

**OpenClaw today:** `ResourcesTab.tsx` shows per-VM CPU + memory (per
`Feature_83.md`).

**Hermes carry-over:** ✅ **Zero changes** — instrumentation is at the
host/agent level, payload-agnostic.

### 5b.16 Channels / messaging gateway

**OpenClaw today:** `ChannelsTab.tsx` in `MachineView` for Telegram /
Discord / Slack / WhatsApp. There is also a legacy/account-level
`ChannelSetup.tsx` component with a separate hard-coded catalog.

**Hermes carry-over:** See §5a.6 — the catalog widens behind a backend
endpoint; the active tab reuses the shell but needs kind-aware pairing and
Slack app-manifest branding. ~22 platforms eventually available; ship the
existing 4 first and expand by demand.

### 5b.17 MCP exposure (machine → editor)

**OpenClaw today:** OpenClaw has an MCP server; we don't surface it
explicitly in the OCM UI today.

**Hermes carry-over:**
- 🚨 **`hermes mcp serve` is stdio-only.** It runs `run_stdio_async()` —
  it reads/writes JSON-RPC over stdin/stdout and has no `--host`/`--port`/
  `--token` options. A tunnel route has nothing to forward to. **A bridge
  service is required** that exposes the MCP stdio transport over an
  authenticated network protocol — either run the in-VM agent's MCP via
  streamable-HTTP (the modern MCP transport) and front it with `authproxy`,
  or wrap stdio with a small adapter (`socat`-style or
  websocket-over-stdio). Either way, this is **infrastructure to build,
  not a tunnel route to add**.
- ➕ Once the bridge exists, the UX is a toggle in `IntegrationsTab`:
  "Expose this machine over MCP" + token issuance + JSON snippet for the
  user's MCP client config.

### 5b.18 ACP exposure (editor → machine)

**OpenClaw today:** No analog.

**Hermes carry-over:**
- 🚨 **Same stdio constraint applies.** `hermes acp` reserves stdout for
  ACP JSON-RPC and has no network transport. Needs the same kind of
  bridge as MCP (or a separate one — ACP and MCP are different protocols).
- ➕ Once bridged, the UX is the same toggle + token + snippet pattern.

### 5b.19 Cron / scheduled jobs

**OpenClaw today:** No on-VM cron UI (we have an OCM-platform-level
scheduler, but it's separate).

**Hermes carry-over:**
- 📝 **Cron runs inside the gateway process**, not as a separate daemon.
  `gateway/run.py` calls `_start_cron_ticker` at boot; `hermes cron` is
  a management CLI, not a daemon. So on the VM-runtime side there's
  nothing to start separately — `hermes-gateway` already covers it.
- ➕ **New tab** (defer to phase 5): "Schedules." List/add/edit/delete
  Hermes cron jobs. Hermes stores them in `$HERMES_HOME/cron/`; UI is a
  thin CRUD over that directory.
- Day-1 fallback: users edit cron entries via the filebrowser.

### 5b.20 Persona / SOUL.md

**OpenClaw today:** No first-class persona UI; SOUL.md lives in files.

**Hermes carry-over:**
- ➕ **New tab** (defer to phase 5): "Persona." Live editor + presets +
  preview. This is one of the bigger product differentiators against
  OpenClaw machines.

### 5b.21 User-model dashboard (Honcho)

**OpenClaw today:** No analog.

**Hermes carry-over:**
- ➕ **New** (defer to phase 5): "What your agent has learned about you."
  Surface the Honcho user model. Sensitive feature — opt-in with clear
  controls to wipe / export.

### Summary table

| Integration | Reuse | Backend shim | New UI | Phase |
|---|---|---|---|---|
| Model selection | ✅ Picker, tiers, OAuth connects | Provider→env-var map, fallback chain | Fallback chain, auto-detect checkbox | 3-4 |
| Default platform model | ✅ Nebius platform model path | Hermes config emitter must default to platform model when no BYOK model is selected | "Ready out of the box" state, no provider prompt required | 1 |
| Observability | ✅ All 4 trace components | Opik plugin for Hermes | Skill-creation events filter (defer) | 3-4 / 5 |
| Browser automation | ✅ BrowserVMs* pages for stealth | None for default; pairing for advanced | "In-VM Playwright" status panel | 3-4 |
| Workspace/Files | ✅ Filebrowser | None | Portable workspace export | 4 / 5 |
| Webchat | ✅ Existing workspace gateway/interface pane | Dashboard `:9119` route, `--tui`/embedded-chat service, prefix-safe REST + WebSocket proxying | Hermes dashboard/webchat in the same pane where OpenClaw control UI appears | 1 |
| Terminal | ✅ `WebTerminal` + `ocmptyd` | Existing `:7681` terminal route; kind-aware commands | Hermes Workspace/Terminal action, Shell + Hermes TUI tabs | 1 |
| OCM CLI connect | ✅ SSH/logs/chat CLI flows | Kind-aware dashboard/chat/terminal target resolution for Hermes | `ocm machines dashboard`, Hermes-aware `ssh`, `serve chat`, `logs` | 1 |
| Kanban | 🔧 Hermes dashboard plugin | Dashboard plugin route after core dashboard is stable | Kanban board tab | Later |
| Usage | ✅ Components | Per-request provider attribution | — | 3-4 |
| Composio | ✅ OAuth popup | Expose REST proxy to Hermes as HTTP tool | — | 4 |
| Web search | ✅ Tab | Env-var name map | — | 3 |
| Backups | ✅ Tab | Auto-snapshot defaults | One-click portable export | 4 |
| Secrets vault | ✅ Zero changes | — | — | 3 |
| Skills | ✅ Component | Skills Hub catalog source | "Auto-written" badge, provenance | 4 / 5 |
| Plugins | 🔧 Repurpose | Hermes plugin registry feed | Substrate pickers (memory/context/…) | 4 / 5 |
| Logs | ✅ Zero changes | — | — | 1 |
| Resources | ✅ Zero changes | — | — | 1 |
| Channels | ✅ Component | `/api/v1/channels?kind=` endpoint | Wider catalog over time | 3-4 |
| MCP exposure | — | stdio→network bridge service, then tunnel route | Toggle + snippet | 5 |
| ACP exposure | — | stdio→network bridge service, then tunnel route | Toggle + snippet | 5 |
| Cron | — | Read `$HERMES_HOME/cron/` | Schedules tab | 5 |
| Persona | — | Read/write SOUL.md | Persona tab | 5 |
| Honcho user-model | — | Read Honcho store | "What we've learned" view | 5+ |

**Headline:** ~16 of ~21 integrations carry over with the components we
already shipped. The other 5 are new value Hermes unlocks (MCP/ACP toggles,
cron UI, persona editor, user-model dashboard).

## 5c. How users actually talk to a Hermes Machine

A common misread of Hermes (because `hermes` is also a CLI) is to think of
it as terminal-first. It isn't. The terminal/TUI is the *developer's* entry
point; the *product's* entry points are everywhere the user already lives.
Eight distinct surfaces, all reachable on a hosted machine:

| # | Surface | What it is | How a user reaches it on a hosted Hermes Machine | Exposure mechanism |
|---|---|---|---|---|
| 1 | **Messaging gateway** | ~22 platforms: Telegram, Discord, Slack, WhatsApp, Signal, Matrix, Email, Teams, Feishu, DingTalk, WeCom, Weixin, SMS, Home Assistant, BlueBubbles, QQ, webhook, api_server, msgraph_webhook (Teams meetings) | User sends a message from the platform they already use; Hermes-in-VM picks it up via the platform's webhook or polling and replies through the same channel | Per-platform creds injected at boot via `configassembly` → `$HERMES_HOME/.env`. No VM-side ingress needed for poll-based platforms (Telegram, Discord). Webhook-based platforms (Slack, Teams, Email) need the existing per-VM tunnel + a stable URL — already supported by the OpenClaw flow. |
| 2 | **Web dashboard** (`:9119`) | Hermes's own Vue 3 SPA — chat, sessions, skills, approvals, traces, model picker | OCM Machine view → existing Workspace/Interface action → same pane where OpenClaw control UI appears, backed by `https://<machine>.claratool.com/dashboard/` | Per-VM Cloudflare Tunnel + `authproxy` validates the user's Cloudflare Access JWT before forwarding to localhost:9119. Same pattern as the existing control UI. |
| 3 | **TUI** (`hermes`) | Full-screen terminal app — multiline editing, slash commands, streaming tool output | Three ways: (a) browser terminal in the OCM Machine view (via `ocmptyd` on `:7681`); (b) `ocm machines ssh <slug>` then run `hermes`; (c) a Hermes-aware terminal tab that starts `hermes` directly | `ocmptyd` is already in the rootfs; SSH already provisioned via Cloudflare SSH/Access. Both unchanged, but CLI target resolution must be kind-aware. |
| 4 | **OpenAI-compatible HTTP API** | `POST /v1/chat/completions` exposed by the gateway — same shape as OpenAI | Any OpenAI client: curl, Open WebUI, desktop apps; the OCM `ChatPage.tsx`; the `ocm machines serve chat <slug>` CLI flow we already built (per `Feature_76.md`) | Tunneled gateway endpoint + the gateway's own token auth (`OPENCLAW_GATEWAY_TOKEN` analog — Hermes has its own gateway-mode token). |
| 5 | **MCP server** (`hermes mcp serve`) | Lets editors *read* the agent — list sessions, read transcripts, send messages, manage approvals (~10 tools) | Claude Code / Cursor / Zed configure an MCP client pointing at a bridged MCP endpoint with a per-machine token | **Requires a bridge service** — `hermes mcp serve` is stdio-only (`run_stdio_async()`, no host/port). Either run MCP streamable-HTTP transport and front with `authproxy`, or wrap stdio with a network adapter. Then add tunnel route + `IntegrationsTab` toggle. |
| 6 | **ACP server** (`hermes acp`) | Lets editors *drive* the agent — editor delegates work to Hermes-as-worker | Editor attaches over ACP; Hermes runs the requested task and reports back | Same constraint as MCP — `hermes acp` reserves stdout for ACP JSON-RPC; needs a bridge service before tunneling. |
| 7 | **Voice** | `faster-whisper` STT for voice memos; native to messaging | User sends a Telegram/WhatsApp voice memo; Hermes transcribes and treats as text | Already baked via the `[voice]` extras in the rootfs. No new exposure — voice arrives *through* the messaging gateway. |
| 8 | **Custom webhook / API** | `gateway/platforms/webhook.py` and `api_server.py` are general-purpose channel adapters | A third-party app POSTs JSON to `https://<machine>.../webhook/...`; Hermes treats each request as a channel session | Per-VM tunnel route + per-channel auth token. Same plumbing as the messaging webhooks (3). |

### What a real day looks like

To put the matrix in motion:

- **Morning, on the phone:** Telegram message — "what's on my plate today?"
  Hermes pulls from cron, sessions, and the user model; replies through
  Telegram. *Gateway surface.*
- **Mid-day, at the desktop:** Open the OCM Machine view → click "Open
  Dashboard" → chat with longer attachments in Hermes's web UI; glance at
  what skills the agent has auto-written this week. *Web dashboard
  surface.*
- **Working in an editor:** Cursor or Claude Code, ACP-attach to the
  hosted Hermes, delegate a multi-step refactor. The agent works while you
  stay in your editor. *ACP surface.*
- **Programmatic, from your own infra:** A cron job in your stack POSTs to
  the chat-completions endpoint → Hermes responds → your job script reads
  JSON back. *HTTP API surface.*
- **Debugging the agent itself:** SSH into the VM or use the browser
  terminal → run `hermes` TUI → inspect session state and skill files
  directly. *TUI surface.*
- **Mid-meeting voice note:** Telegram voice memo → "schedule follow-up
  with X tomorrow, draft the email by EOD." *Voice surface via messaging.*

All six of those happen against the same Hermes Machine. The "machine" is
the persistent home of the agent; the *interface* is wherever you are right
now.

### Gateway + chat surfaces — the OpenClaw mapping

The two surfaces most central to the OCM product story are the **gateway**
and the **chat interface(s)**. Both are present in Hermes; the HTTP-API
side is actually richer than OpenClaw's.

**Gateway** (`hermes gateway` / `gateway/run.py`, `GatewayRunner` class):

| Concept | OpenClaw gateway | Hermes gateway |
|---|---|---|
| Long-running daemon | `openclaw gateway --port 18789 --bind loopback` | `hermes gateway start` (or `run` for foreground) |
| Module shape | config, session, delivery, channels, hooks, pairing | identical: `config.py`, `session.py`, `delivery.py`, `hooks.py`, `pairing.py`, `restart.py`, `status.py`, `platforms/` |
| Per-platform adapters | 4 in our keep-list (Telegram, Discord, Slack, WhatsApp) | ~22 in `gateway/platforms/` |
| Token model | Shared gateway token (`OPENCLAW_GATEWAY_TOKEN`) bypasses pairing for local clients | Same shape — bearer token for local/programmatic access; per-platform OAuth/webhook handled inside each adapter |
| Bind mode | `loopback` (auth enforced at the tunnel/auth-proxy layer) | Same — bind to localhost, expose through the per-VM tunnel |
| Reload mode | `hot` | Same — also supported |
| Control UI auth bypass | `dangerouslyDisableDeviceAuth` for browser clients (security at proxy layer) | Equivalent pattern; Hermes dashboard authenticates via tunneled CF Access JWT |

**Mapping is essentially 1:1.** Our existing init script's "start the
gateway, point the auth proxy at it" pattern lifts straight over — replace
the binary, keep the seams.

**Chat interfaces** — Hermes ships *three* concurrent surfaces:

1. **Per-platform messaging** through the gateway's platform adapters
   (Telegram/Discord/Slack/…) — same shape as OpenClaw.
2. **OpenAI-compatible HTTP API** embedded in the gateway as the
   `api_server` adapter (`gateway/platforms/api_server.py`, an aiohttp
   server). Five endpoints — richer than what we ship today:

   | Endpoint | Purpose |
   |---|---|
   | `POST /v1/chat/completions` | OpenAI Chat Completions format, SSE streaming, stateless by default with opt-in session continuity via `X-Hermes-Session-Id` header |
   | `POST /v1/responses` | OpenAI Responses API format, stateful via `previous_response_id` |
   | `GET /v1/responses/{response_id}` | Retrieve a stored response |
   | `DELETE /v1/responses/{response_id}` | Delete a stored response |
   | `GET /v1/capabilities` | **Discovery endpoint** — machine-readable description of the API surface, models, and session features for external UIs to introspect |

   Auth via `API_SERVER_KEY` (HTTP bearer token). Configured under
   `platforms.api_server` in `config.yaml`.

3. **Web dashboard** (`hermes dashboard` / Vue 3 SPA on `:9119`) — Hermes's
   own chat UI with streaming tool output, approval UI, session browser,
   skill/model pickers. **Separate process** from the gateway, talks to the
   same agent.

### What this means for our existing chat UX

Three pieces of OCM UX line up *directly* against Hermes's surfaces with
no architectural work:

- **`ChatPage.tsx`** (browser chat) — **does NOT speak
  chat/completions today.** It calls `dataPlaneUrl(..., "gateway/")` and
  renders the result in an iframe; pointing it at Hermes's chat-completions
  endpoint would render raw JSON/SSE in the iframe, not a chat UI. Two
  real options:
    - **(a) Iframe the Hermes dashboard at `:9119`** — already a rich chat
      UI; same iframe pattern, different target URL. Lowest-friction path
      and the recommended default.
    - **(b) Build a new browser chat client** that POSTs to
      `/v1/chat/completions` (or `/v1/responses` for server-side
      statefulness). More work, but gives us a chat surface that lives
      inside the OCM shell rather than the Hermes shell.
  Lean: ship (a) first; consider (b) if we want to unify chat UX across
  kinds.
- **`ocm machines serve chat`** CLI flow (per `Feature_76.md`) → same path,
  speaks chat/completions → works.
- **Open WebUI / third-party OpenAI clients** → users can point any
  OpenAI-compatible client at their Hermes Machine's chat-completions URL.
  Zero changes on our side; immediate ecosystem.
- **Auto-introspection via `/v1/capabilities`** → `ConfigureModelsModal`
  and `ChatPage` can hit this endpoint on connect and adapt to whatever
  the machine reports (available models, supported features). Future-proofs
  the UI against Hermes adding new capabilities.

### Implications for OCM

- **Tunnel route map grows.** Per-VM today routes
  `{control-ui, filebrowser, pty}`. For Hermes, add `{dashboard,
  chat-completions, responses, capabilities, webhook}` — chat
  endpoints sit alongside the dashboard route. Auth proxy needs per-route
  auth policy (some bearer-token, some CF Access JWT, some both). **MCP
  and ACP are NOT in this set** — both are stdio-only in Hermes today
  (see §5b.17 / §5b.18); they require a bridge service before they can be
  exposed over the tunnel.
- **`IntegrationsTab` becomes the "where can I reach this machine from?"
  hub.** Toggles for each surface, copy-to-clipboard for tokens and
  endpoints, per-surface revocation.
- **No surface is mandatory.** A user could turn everything off except
  Telegram and the dashboard. The cost of a surface should be exposure
  surface, not friction — opt-in toggles, not feature flags.
- **Voice and webhooks ride messaging.** They're not separate concerns
  from a routing/UX standpoint.
- **The TUI is the safety net.** When everything else is broken, SSH +
  `hermes` works. We bake `ocmptyd` and keep SSH-by-Cloudflare-Access for
  exactly this reason.

## 6. Things to *drop* (vs. OCM's current VM)

- **OpenClaw runtime externals** (npm packages for IRC/Mattermost/Matrix
  shims) — Hermes covers these natively
- **Stealth Chromium extension** (the 13-patch package in `rootfs/stealth-extension/`) —
  Camofox subsumes; keep only if a customer's skill needs it
- **Browser-harness Python package** — Hermes uses Playwright directly
- **Channel scrubbing logic** at runtime build (a Hermes concern, not OCM's
  build pipeline — Hermes's bundling already controls which platforms ship)
- **Filebrowser** as the primary file UI — Hermes dashboard is richer; keep
  filebrowser only if users miss the raw HTTP file tree
- **The browser VM** as a paired companion (for the default Hermes machine —
  keep available for stealth-heavy use cases)

## 7. Things to *probably* keep even though Hermes has its own version

- **`ocmptyd`** — Hermes has a great TUI, but exposing a raw shell into the VM
  through Cloudflare Tunnel is a power-user feature OCM users already love.
- **`authproxy`** — per-VM Cloudflare JWT validation; replaces nothing in
  Hermes's stack and we need it for the dashboard and tunnel.
- **`ocm-migrate`** — Hermes has its own schema, but our agent-side migrations
  are about the *data volume* (XFS quirks, etc.), not the app.
- **Runit supervision** — Hermes's docker entrypoint just `exec`s `hermes
  gateway`. In a single-process container that's fine; in a MicroVM that's PID
  1 and needs supervision for gateway, cron, dashboard, MCP, pty, tunnel
  separately. Keep runit.

## 8. Open questions / decisions blocking implementation

1. **Single rootfs vs split.** Lean: single rootfs, drop the paired browser
   VM for Hermes-kind machines by default. Revisit if memory pressure forces
   it.
2. **Bundle Hermes as a runtime tarball, or bake into rootfs?** Lean: bundle,
   for parity with OpenClaw's roll-forward story.
3. **Auth seeding.** `HERMES_AUTH_JSON_BOOTSTRAP` is the seam, but who issues
   the OAuth token? Are we asking users to log into Nous Portal first and
   paste a refresh token, or do we proxy a device flow through the OCM UI?
4. **Dashboard exposure.** Default-on (rich UX, every machine has a URL) or
   opt-in (smaller attack surface)? Lean: default-on, gated by the same
   Cloudflare Access policy as the existing control UI.
5. **Hermes version cadence.** Hermes releases roughly monthly (v0.10 → v0.13
   over the recent history). Tie our channel manifest cadence to theirs, with
   a 1-week soak before flipping `stable`.
6. **Storage layout.** `$HERMES_HOME=/data/hermes` bound to `/opt/data` so
   Hermes thinks it's in its standard place but the persistent volume is
   shared with the rest of the OCM data-volume conventions.
7. **Channel keep-list growth strategy.** Hermes supports ~22 platforms; we
   currently support 4. Open each new channel in order of customer demand,
   keeping the invariant test green at each step.
8. **MCP and ACP exposure.** Both are powerful. Probably gated behind a
   per-machine toggle (security-sensitive — MCP can read transcripts; ACP can
   drive the agent from an editor).
9. **Backup/export SLA.** Because a Hermes machine's accumulated state
   *is* the product value (auto-written skills, curated memory, user model,
   searchable session history — see §0), the backup story for Hermes
   machines is more critical than for OpenClaw. Snapshot cadence, retention,
   and one-click export of `$HERMES_HOME` as a portable archive should be
   first-class. We have backup plumbing (`BackupsTab.tsx`,
   `2026-03-11-backup-design.md`) — Hermes raises the bar on how often we
   snap and how easy export is.
10. **Pluggable substrate as a UX axis.** Hermes's memory provider, context
    engine, and observability sink are plugin-selected. Day-one default
    choices vs. surfacing them in the UI — defer the picker to phase 5, but
    decide defaults now (honcho for memory? mem0? plain-file?).
11. **Multi-agent (kanban) machines.** `plugins/kanban/` lets a single
    Hermes spawn worker sub-agents. We could naturally back this with
    MicroVM-per-worker. Out of scope for the first cut, but the abstraction
    is worth not painting ourselves out of.
12. **RL/trajectory capture.** A Hermes machine generates training data
    constantly. Is that a customer-facing product feature ("export your
    agent's trajectories") or internal only? Affects how we store + index
    traces.

## 9. Suggested implementation phases

A first cut, to flesh out as we get into it.

- **Phase 0 — Spike (this branch):** Hand-build a Hermes rootfs locally,
  manually boot it in Firecracker on the claude-swarm host, prove
  `hermes gateway` works with a Telegram bot using a hand-written `config.yaml`
  + `.env`. No automation yet; just prove the VM works.
- **Phase 1 — Build pipeline:** `Dockerfile.hermes`,
  `scripts/build-hermes-rootfs.sh`, `scripts/build-hermes-runtime.sh`,
  `scripts/upload-hermes.sh`, `scripts/register-hermes-release.sh`,
  manifest in GCS. Mirror OpenClaw's pipeline structure exactly.
- **Phase 2 — Boot pipeline:** `scripts/init-hermes.sh`, runit services,
  per-VM tunnel wiring, dashboard exposure.
- **Phase 3 — Backend:** `kind=hermes` on machines table, `configassembly`
  emitter for `config.yaml` + `.env` + `auth.json`, channel mapping for the
  first 4 channels (parity with OpenClaw).
- **Phase 4 — Frontend:** kind picker in create-machine flow + onboarding
  (`CreateMachineModal.tsx`, `OnboardingWizard.tsx`); kind-aware tabs in
  `MachineView`; dashboard link in Overview tab; Hermes-extended model
  catalog via `/api/v1/channels?kind=` and a `provider → env-var` map in
  `configassembly`; `KindBadge` + `MachineCard` change; admin filtering by
  kind. Defer: PersonaEditor, Skills Hub embed, cron UI.
- **Phase 5 — Migrations & expansion:** OpenClaw-machine → Hermes-machine
  migration flow, additional channels (Matrix, WhatsApp/Signal first), MCP
  exposure, ACP exposure.

## 10. Glossary cross-reference

| OCM term | Hermes term | Notes |
|---|---|---|
| OpenClaw runtime | Hermes runtime | The app code we bundle separately from the OS |
| `openclaw.json` | `config.yaml` + `.env` | Hermes splits secrets out |
| `/data/ocm/configs/` | `/opt/data/` (`$HERMES_HOME`) | Hermes default home |
| Channel | Platform | Same concept; "platform" is Hermes's word |
| Skill | Skill | Identical concept (Hermes calls the marketplace "Skills Hub") |
| Browser VM | Playwright in-VM | Architectural shift; keep browser VM as fallback |
| Filebrowser | `hermes dashboard` (`:9119`) | Richer; same exposure model via tunnel |
| MCP server (in OpenClaw) | `hermes mcp serve` | Same protocol, similar tool surface |
| `ocm` CLI | `hermes` CLI | The on-VM entry point |

## 10a. Review log

### Codex review — 2026-05-11

Codex flagged five concrete factual issues against an earlier draft of this
doc; each has been corrected inline.

| # | Severity | Issue | Where corrected |
|---|---|---|---|
| 1 | P2 | MCP/ACP claimed to be tunnel-route-only; actually stdio-only in Hermes (`run_stdio_async()`, ACP reserves stdout). A bridge service is required before exposure. | §5b.17, §5b.18, §5c "Implications for OCM" (tunnel-route list), §5c interaction matrix rows 5–6 |
| 2 | P2 | `ChatPage.tsx` claimed to speak chat/completions; actually an iframe wrapper around `dataPlaneUrl(..., "gateway/")`. Pointing it at `/v1/chat/completions` would render raw JSON/SSE. | §5b.5, §5c "What this means for our existing chat UX" |
| 3 | P2 | Init plan listed `hermes-cron` as a separate runit service; actually cron ticks from inside the gateway (`_start_cron_ticker`) — `hermes cron` is a CLI, not a daemon. | §4.3 step 10, §5b.19 |
| 4 | P3 | Proposed `machines.kind ∈ {openclaw, hermes, browser}`, but browser VMs live in their own `browser_vms` table joined via `machines.browser_vm_id`. | §4.5 |
| 5 | P3 | DB artifact kind would diverge between `hermes-runtime` and `hermes`. OpenClaw uses DB `kind='openclaw'` with GCS manifest `"kind": "openclaw-runtime"`; Hermes should mirror — DB `hermes`, manifest `hermes-runtime`. | §4.2 |

All five edits are documentation-only at this stage; no implementation has
been started.

### Codex follow-up review — 2026-05-11

Codex flagged five follow-up issues after checking the active repo files and
the local Hermes clone; each has been corrected inline.

| # | Severity | Issue | Where corrected |
|---|---|---|---|
| 1 | P2 | Hermes channel config was described as `gateway.*` with `DISCORD_TOKEN`; Hermes loads `platforms.*` and `DISCORD_BOT_TOKEN`. | §4.4, §5 bring-up checklist |
| 2 | P2 | MCP/ACP were still listed as port/tunnel work in the click matrix and summary table even though Hermes exposes stdio-only commands. | §5 bring-up checklist, §5a.7, §5b summary table |
| 3 | P2 | The channel UI audit focused on legacy `ChannelSetup.tsx`; the active per-machine UI is `ChannelsTab.tsx`, which still has OpenClaw-specific pairing and Slack manifest assumptions. | §5a.3a, §5a.4, §5a.6, §5b.16 |
| 4 | P2 | Artifact DB-kind naming was inconsistent across the reuse table, runtime section, and admin notes. | §3 reuse table, §4.2, §5a.9 |
| 5 | P2 | Subscription credential projection treated all Hermes OAuth as `auth.json`; Anthropic PKCE uses `.anthropic_oauth.json` while API keys stay in `.env`. | §4.4, §5a.3a |

## 11. References

- Hermes Agent repo: `/home/mantiz/hermes-agent` (commit at clone time)
- Hermes `Dockerfile`: shows the canonical bake (uv + Python 3.13 + Node 20 +
  Playwright + Chromium-shell + ffmpeg + ripgrep)
- Hermes `docker/entrypoint.sh`: privilege-drop + config bootstrap pattern
- OCM rootfs build: `rootfs/Dockerfile.openclaw`, `scripts/build-rootfs.sh`
- OCM runtime build: `scripts/build-openclaw-runtime.sh`,
  `scripts/upload-openclaw.sh`
- OCM init: `scripts/init-openclaw.sh`
- OCM config assembly: `backend/internal/configassembly/assembler.go`
- OCM artifact delivery: `scripts/register-rootfs-release.sh`,
  `scripts/register-openclaw-release.sh`,
  `backend/internal/selfupdate/`
- OCM agent orchestration: `backend/cmd/agent/main.go`,
  `backend/internal/orchestrator/firecracker_linux.go`
- OCM metadata server: `backend/internal/metadata/server_linux.go`
