# Engineering Learnings

Cross-cutting lessons from building and operating OpenClaw Machines. Updated as new patterns emerge.

---

## Testing

### Run the right test tier for the change
- `make test-go` (~20s) — after any backend change. Always.
- `make test-gateway-e2e` (~12s) — after changes to credentials, metadata, config assembly, proxy, or auth. Cheap, catches real gateway failures. Run proactively.
- `make test-integration` (~35min) — after init script, rootfs, or orchestrator changes. Expensive, needs sudo + Firecracker.

### E2E tests must cover the user-facing path
Unit tests passing does not mean the gateway works. Multiple production failures (origin errors, token_missing, invalid config) were never caught because tests only hit `/gateway/api/health` (a public endpoint) and never tested auth-protected gateway routes or WebSocket handshake with a real token. If a user would hit it, a test should hit it.

### OpenAI E2E key status
The OpenAI keys in GCP Secret Manager are stale (401 invalid key / 429 quota exceeded). These failures in `make test-gateway-e2e` are pre-existing, not regressions.

### Stale local openclaw checkout silently breaks e2e
`make test-gateway-e2e` resolves the gateway binary via `exec.LookPath("openclaw")` unless `OPENCLAW_BIN` is set. On most dev machines this is a symlink (`/usr/local/bin/openclaw → ~/openclaw/openclaw.mjs`) into a peer checkout that is **only refreshed when you remember to run `make setup-local-openclaw`**. After any OpenClaw upgrade in `versions.json`, the e2e tests start using new CLI flags or new gateway behaviours, and a stale local checkout produces cryptic `error: unknown option '--replace'` or `models.list returned zero models` failures that look like real regressions. Verified for the PR #87 (2026.5.19) upgrade: a checkout left at `2026.5.4-beta.1` failed 5 `TestGatewayE2E_ConfigPush_*` tests; all passed once `OPENCLAW_BIN` pointed at an npm-installed `openclaw@2026.5.19`. **Rule:** if `make test-gateway-e2e` regresses after pulling main, before investigating the tests, check `openclaw --version` against `versions.json` first. The `test-gateway-e2e` make target now refuses to run when those disagree (override with `OPENCLAW_SKIP_VERSION_CHECK=1` for intentional cross-version testing).

---

## Artifact Delivery Chain

### The full chain must be verified end-to-end
GCS bucket -> `manifest.json` -> Cloud Run env var -> provisioner -> GCE instance metadata -> agent reads metadata -> downloads artifact. "Uploaded to GCS" does not mean "deployed to VMs."

### Rootfs is staged once at agent startup
`stageBaseRootfs` in orchestrator `New()` runs at agent process start, NOT per-VM create. New rootfs in GCS won't take effect until the agent process restarts (via self-update).

### Agent self-update has a 5-minute polling window
Self-update only runs when idle (no VMs running). Stopping a VM and immediately starting a new one can miss the update window. After `make build-upload-rootfs`, the full sequence is: upload -> agent self-update -> agent restarts -> re-stages rootfs -> new VMs get new rootfs.

### OpenClaw runtime artifact: upload script picks stale files
The upload script (`scripts/upload-openclaw.sh`) globs `openclaw-*-linux-amd64.tar.zst`, sorts alphabetically, and takes the last match. When old revision files (e.g., `r5`, `r6`) exist alongside the new build (`v2026.4.2`), the revision-suffixed files sort after the base version and the script uploads the wrong one. **Always clean stale artifacts** from `/var/lib/ocm/openclaw-artifacts/` before uploading, or pass the path explicitly.

### OpenClaw runtime artifact: upstream externals are not declared as dependencies
The openclaw npm package's dist imports integration SDKs (`@slack/web-api`, `grammy`, `@buape/carbon`, `@larksuiteoapi/node-sdk`, `jimp`, etc.) as Vite externals but doesn't declare them as package dependencies. Without explicit installation, surface handlers fail at runtime with `MODULE_NOT_FOUND`. These externals are pinned in `scripts/openclaw-runtime-externals/package.json` with a committed lockfile for reproducible builds.

### OpenClaw runtime ext4 image is cached per version string
`ensureOpenClawReleaseImage` checks if `releasePath + ".ext4"` exists and skips creation if it does. If you re-upload the same version string with different contents, the agent won't rebuild the ext4 image. Use a new revision suffix (r7, r8, etc.) to force re-staging.

---

## Firecracker VM Environment

### Minimal /dev
No udev, no `/dev/fd` symlink by default. Process substitution `>(tee ...)` needs `/dev/fd -> /proc/self/fd` — must create explicitly after mounting `/proc`.

### PID 1 init
No systemd, no services. Everything in the init script is manual. `su -l` hangs (PAM `pam_keyinit.so`) — use `su -s` without `-l`.

### Three separate environment contexts in init script
The init script (`scripts/init-openclaw.sh`) has three env contexts that do NOT share variables:

1. **Gateway env block** — `su -s /bin/bash openclaw -c "..."` starting the gateway
2. **PTY server env block** — `su -s /bin/bash openclaw -c "..."` starting `agent --pty-server`
3. **`/etc/profile.d/` scripts** — only sourced by login shells, not direct command execution

Env vars needed by multiple contexts must be added to each independently. PTY-spawned commands (`openclaw tui`, `tail -f`) inherit the PTY server process env, NOT the shell profile.

---

## Gateway

### Startup takes ~55 seconds
Node.js module loading is the bottleneck. Quick-start (`ocm_quick_start=1`) only skips sidecars. Tests must use 90-second timeout for `waitForGateway`.

### Config source lifecycle
When `OCM_CONFIG_SOURCE=metadata`, `loadConfig()` returns `{}` if the metadata endpoint is not wired correctly. All 62+ runtime callers (WebSocket handler, TUI, channels, agents) get empty config, causing cascading failures.

### Auth proxy Host header rewriting
The auth proxy (`proxy.go`) overwrites the `Host` header when proxying to the gateway. With `dangerouslyAllowHostHeaderOriginFallback=true`, the gateway constructs allowed origins from the Host header. If the auth proxy rewrites it to `127.0.0.1:18789`, browser origin checks fail with "origin not allowed."

### Gateway logs location
Logs go to `/tmp/openclaw/openclaw-YYYY-MM-DD.log` (date-based), NOT `/var/log/openclaw-gateway.log`. Use `find` to locate.

---

## Native Config Mode — Architectural Patterns

### Nonce-as-universal-key eliminates the chicken-and-egg problem
The exec secret provider (`ocm-secrets`) can't authenticate to the metadata service to fetch secrets, because it *is* the thing that provides the auth token. Calling the metadata HTTP endpoint would require nonce-based auth — but the nonce is what we're trying to return. Solution: write the nonce to a local file (`/run/ocm-nonce`) at boot. The binary does a file read, not an HTTP call. This makes it fast, dependency-free, and avoids the circular auth problem entirely.

### All provider keys resolve to the same value — the proxy does the routing
A non-obvious design choice: `ocm-secrets` returns the same nonce for every requested provider key (Anthropic, OpenAI, Google, etc.). The API proxy identifies the VM by `(source IP, nonce)` and looks up the real key per provider from the `LLMKeys` metadata map. This means the secret provider doesn't need to know *which* provider is being queried — it just proves "I am this VM." If per-provider secrets are ever needed (e.g., a plugin needing a real API key directly), `ocm-secrets` can be extended with a fallback to `/v1/secrets` for unrecognized IDs.

### Exec endpoint is the universal control channel
Config push, pairing approval, extension install, diagnostics — all flow through the same `POST /exec` primitive. The backend sends `["openclaw", "gateway", "call", "config.patch", "--params", ...]` via exec rather than implementing per-feature RPC endpoints. This means every new gateway capability is automatically available to the control plane without backend code changes. The exec endpoint replaced the need for the fork's custom RPC methods.

### JSON merge patch preserves user intent
Dashboard config pushes use OpenClaw's `config.patch` RPC (JSON merge patch semantics) instead of overwriting `openclaw.json`. Objects merge recursively, `null` deletes keys, arrays replace. This means if the dashboard pushes a model change, user's custom plugin config survives. The `baseHash` optimistic concurrency prevents lost updates when user and dashboard edit simultaneously.

### Seed-then-own: first-boot-only config
The platform writes `openclaw.json` only if the file doesn't exist. After first boot, the user (and dashboard via `config.patch`) owns the config. This is the core of the "managed runtime" model — the platform assists but doesn't control. The `/data` volume persists across reboots, so user customizations survive. This contrasts with fork mode where the platform rebuilt the full config on every start.

### Disk config is source of truth after first boot
After first boot, the on-disk `openclaw.json` is canonical — not the DB's `assembled_config`. The orchestrator writes config into `config-current/openclaw.json` with a stable symlink at `~/.openclaw/openclaw.json`. The init script migrates old direct-file layouts to the symlink layout on upgrade. Live updates should use scoped `config.patch` operations, not full DB reassembly. See `docs/designs/disk-config-source-of-truth.md`.

### Dual RuntimeConfig construction is a divergence risk
The server has two startup paths — worker mode and API mode — each constructing `machines.RuntimeConfig{}` independently in `cmd/server/main.go`. When new fields are added to one path, they're easily missed in the other (the `OpikAPIKey` bug). Go doesn't warn about missing fields in struct literals. Watch for this pattern when adding new RuntimeConfig fields — always check both paths.

### Native mode must write auth-profiles.json for model discovery
The gateway's ModelRegistry discovers available models from `auth-profiles.json`, which maps provider names to auth credentials. Fork mode writes this file during `start_gateway()` (line ~887), but native mode returned early (line 837) before reaching that code. Without `auth-profiles.json`, the ModelRegistry discovers zero models, and every model — including the default `deepseek-ai/DeepSeek-V3-0324` — is rejected as "Unknown model." The fix: native mode now fetches `/v1/providers` from metadata and writes `auth-profiles.json` before starting the gateway, using the same nonce-as-key pattern as fork mode.

### Native mode seed config must include agents.defaults.models
The gateway uses `agents.defaults.models` as the model catalog — if a model isn't listed there, it's "Unknown." Fork mode's `AssembleConfig` populated this (line 395), but `AssembleSeedConfig` was missing it. The seed config set `agents.defaults.model.primary` (which model to use) but not `agents.defaults.models` (which models exist). Both are required.

### Three things needed for a model to work in the gateway
1. **auth-profiles.json** — provider entry so ModelRegistry can discover the model
2. **agents.defaults.models** — model listed in the catalog so the gateway accepts it
3. **models.providers.\<name\>** — provider config with baseUrl and apiKey so requests route correctly
4. **models.providers.\<name\>.models** — for non-built-in providers only: explicit model entries with `api` field

Missing any one of these causes different errors: "Unknown model" (1, 2, or 4 missing), or proxy/auth failures (3 missing). All four are independently generated and easy to miss when adding a new config mode.

### Non-built-in providers need explicit model entries and `api` field
The gateway has built-in providers (anthropic, openai, google) with hardcoded model lists. For these, an empty `models: []` array works — the gateway discovers models from its internal catalog. For non-built-in providers (nebius, qianfan, modelstudio), the `models` array in `models.providers.<name>` IS the model list. Each entry needs at minimum: `id`, `name`, and `api` (e.g., `"openai-completions"` for OpenAI-compatible APIs). Without `api`, the gateway doesn't know the wire protocol.

### Model ID mapping creates a debugging trap
`deepseek/deepseek-v3` (user-facing) maps to `deepseek-ai/DeepSeek-V3-0324` (Nebius API). The mapping happens in `AssembleSeedConfig` via `platformModelMap`. When the error says "Unknown model: deepseek-ai/DeepSeek-V3-0324", it's not obvious that the seed config code is involved — you'd naturally look for that exact string in the codebase and find only the map definition. The mapped ID appears nowhere in the init script or gateway config assembly, making it hard to trace.

---

## Credentials & Auth

### Credential type determines auth header
- Anthropic `api_key` -> `x-api-key` header
- Anthropic `subscription_key` -> `Authorization: Bearer` header
- OpenAI `api_key` -> `Authorization: Bearer` header

### Key prefix patterns
- Anthropic API keys: `sk-ant-api03-*`
- Anthropic subscription keys: `sk-ant-oat01-*` or `k-ant-oat01-*`
- OpenAI keys: `sk-proj-*`

### OAuth tokens need control-plane refresh
Refresh tokens stay on the control plane (has DB, encryption key, client credentials). The proxy/agent only gets the current access token + expiry. The control plane pushes fresh tokens before expiry.

### Public endpoints that take an identity field from the caller are IDOR vulnerabilities
The `/api/composio/*` proxy routes originally accepted `user_id` from the query string / request body and forwarded it to Composio with a single platform key. No middleware validated the caller. Result: anyone on the internet who could guess a machine UUID could act as that machine's connected Gmail/Slack/etc. accounts. The comment in `server.go:312-315` saying "Safe: only proxies to Composio REST API using the platform key, no secrets exposed" was wrong — the leaked authority *was* the platform key plus the caller-controlled user_id. Fix (composiofix branch): per-machine HS256 token, signed at config assembly, validated by the handler; the user_id is forced from the token's `machine_id` claim and any caller-supplied user_id is ignored. **Rule:** any public endpoint whose behaviour depends on an identity field is only safe if that field comes from an authenticated source (session cookie, machine token, agent token), never from the body or query. If you have a route that "looks like" anyone can call it but it's "safe because no secrets are exposed," re-read it — the secret might be the authority to act as someone else.

### Composio holds the platform OAuth tokens, not OCM
End-user OAuth tokens for Gmail / Slack / GitHub / etc. live in Composio's cloud, not OCM's database. OCM only holds the platform `ak_` key (one per OCM deployment) in GCP Secret Manager under `COMPOSIO_CONSUMER_KEY` (legacy name). Implication: a Composio-side breach is a real exposure path for OCM users; key rotation matters; see [`docs/runbooks/composio-key-rotation.md`](runbooks/composio-key-rotation.md). The `composio-plugin.tgz` source is at [`mathaix/ocm-openclaw-composio-plugin`](https://github.com/mathaix/ocm-openclaw-composio-plugin); the npm package isn't published — `scripts/build-composio-plugin.sh` rebuilds the tarball from a local peer checkout (`~/ocm-openclaw-composio-plugin`).

---

## Process

### Analyze before implementing
Understand the big picture before writing code. Show understanding, get approval, then implement. Don't jump ahead.

### Always use make targets
Never bypass the build/deploy pipeline with custom scripts. All builds and deploys go through `make`.

### Commit = push
When asked to commit, always push to remote too unless told otherwise.

### Verify before marking complete
Do not mark tasks as done without evidence (test output, curl response, or log line). "It should work" is not verification.
