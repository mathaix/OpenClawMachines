# OpenClaw Version Management

## Overview

OpenClaw (formerly Clawdbot) is the core AI gateway running inside every MicroVM. It is an upstream open-source project with a rapid release cadence that we integrate as a binary dependency baked into our rootfs snapshots.

This document defines how we track, test, upgrade, and roll back OpenClaw versions.

## Current State

| Item | Value |
|------|-------|
| Installed package | `clawdbot@2026.1.24-3` (STALE) |
| Active upstream package | `openclaw` (npm) |
| Latest upstream | `openclaw@2026.2.9` (as of 2026-02-09) |
| Installed via | `pnpm install -g` during rootfs build |
| Binary name | `openclaw` (shims exist for `clawdbot` and `moltbot`) |
| Node.js required | `>= 22.12.0` |

## Package History

The project has been renamed twice:

```
moltbot → clawdbot (2025) → openclaw (2026.1.29)
```

Each rename changed: npm package name, binary name, config directory, env var prefixes. The latest version auto-migrates from older directory names and maintains shims for old binary names.

## Upstream Release Cadence

OpenClaw releases **near-daily**, often with multiple pre-releases per day. A typical month sees 15-25 releases.

### Breaking Changes (v2026.1.24 → v2026.2.9)

| Version | Change | Impact |
|---------|--------|--------|
| 2026.1.29 | Package renamed `clawdbot` → `openclaw` | Binary name, config paths, env vars |
| 2026.1.29 | CVE-2026-25253 patched (`gatewayUrl` RCE) | Security-critical |
| 2026.1.29 | Auth mode `"none"` permanently removed | Must use token/password auth |
| 2026.1.29 | `gateway.token` deprecated | Use `gateway.auth.token` |
| 2026.1.29 | Config auto-migrates `.clawdbot/` → `.openclaw/` | Symlink created for backward compat |
| 2026.2.1 | `cacheControlTtl` renamed to `cacheRetention` | Backward-compat mapping provided |
| 2026.2.3 | Legacy cron `atMs` dropped | Use ISO 8601 `schedule.at` |
| 2026.2.3 | One-shot cron jobs auto-delete after success | Use `--keep-after-run` to retain |

## Config Loading (v2026.2.x)

### Paths

| Item | Default | Override |
|------|---------|----------|
| State directory | `~/.openclaw/` | `OPENCLAW_STATE_DIR` env var |
| Config file | `~/.openclaw/openclaw.json` | `OPENCLAW_CONFIG_PATH` env var |
| Legacy fallback | Also checks for `clawdbot.json` in state dir | `CLAWDBOT_CONFIG_PATH` env var |
| Profile mode | `~/.openclaw-<name>/` | `--profile <name>` CLI flag |
| Dev mode | `~/.openclaw-dev/` | `--dev` CLI flag |

### Config precedence (highest to lowest)

1. CLI flags (`--port 3000`)
2. Environment variables (`OPENCLAW_GATEWAY_PORT`)
3. Config file (`~/.openclaw/openclaw.json`)
4. Hardcoded defaults

### Config format (JSON5)

```json5
{
  "gateway": {
    "port": 3000,
    "bind": "lan",
    "mode": "local",
    "auth": {
      "token": "..."        // replaces deprecated "gateway.token"
    },
    "controlUi": {
      "enabled": true,
      "allowInsecureAuth": true,              // token-only, skip device pairing
      "dangerouslyDisableDeviceAuth": true     // break-glass, disable device identity
    }
  },
  "agents": {
    "defaults": {
      "model": { "primary": "anthropic/claude-sonnet-4" },
      "workspace": "${OPENCLAW_STATE_DIR}/workspace"
    }
  },
  "logging": { "level": "info" }
}
```

Config strings support `${VAR_NAME}` env var substitution at load time.

### Gateway CLI flags (v2026.2.x)

```
openclaw gateway [options]

  --auth <mode>           "token" or "password"
  --bind <mode>           "loopback", "lan", "tailnet", "auto", "custom"
  --port <port>           WebSocket port
  --token <token>         Shared token (default: OPENCLAW_GATEWAY_TOKEN env)
  --password <password>   For password auth mode
  --verbose               Verbose logging
  --allow-unconfigured    Start without gateway.mode in config
  --force                 Kill existing listener on target port
  --ws-log <style>        "auto", "full", "compact"
```

## Environment Variables

### Active (OPENCLAW_*)

| Env Var | Purpose |
|---------|---------|
| `OPENCLAW_STATE_DIR` | Override state directory (`~/.openclaw`) |
| `OPENCLAW_CONFIG_PATH` | Override config file path |
| `OPENCLAW_GATEWAY_TOKEN` | Gateway auth token |
| `OPENCLAW_GATEWAY_PASSWORD` | Gateway auth password |
| `OPENCLAW_GATEWAY_PORT` | Override gateway port |
| `OPENCLAW_GATEWAY_BIND` | Override bind mode |
| `OPENCLAW_HOME` | Override home directory for path resolution |
| `OPENCLAW_AGENT_DIR` | Override agent directory root |
| `OPENCLAW_SKIP_CHANNELS` | Skip channel initialization (set to `1`) |
| `OPENCLAW_SKIP_CANVAS_HOST` | Disable Canvas Host (set to `1`) |
| `OPENCLAW_DISABLE_BONJOUR` | Disable mDNS broadcasting (set to `1`) |
| `OPENCLAW_LOAD_SHELL_ENV` | Enable shell environment sourcing |
| `OPENCLAW_RAW_STREAM` | Enable raw stream logging |
| `OPENCLAW_PROFILE` | Named profile |

### Legacy (still recognized)

| Env Var | Maps To |
|---------|---------|
| `CLAWDBOT_CONFIG_PATH` | `OPENCLAW_CONFIG_PATH` |
| `CLAWDBOT_GATEWAY_TOKEN` | `OPENCLAW_GATEWAY_TOKEN` |
| `CLAWDBOT_GATEWAY_PASSWORD` | `OPENCLAW_GATEWAY_PASSWORD` |
| `CLAWDBOT_SKIP_CHANNELS` | `OPENCLAW_SKIP_CHANNELS` |
| `CLAWDBOT_SHELL` | Shell override |

## WebSocket Protocol

Protocol version **3**. Three-step handshake:

1. **Gateway → Client**: `{type: "event", event: "connect.challenge", payload: {nonce, ts}}`
2. **Client → Gateway**: `{type: "req", id: "<uuid>", method: "connect", params: {minProtocol: 3, maxProtocol: 3, client: {id, version, platform, mode}, role, scopes, device, auth, ...}}`
3. **Gateway → Client**: `{type: "res", id: "<uuid>", ok: true, payload: {type: "hello-ok", protocol: 3, ...}}`

General message formats:
- Request: `{type: "req", id, method, params}`
- Response: `{type: "res", id, ok, payload|error}`
- Event: `{type: "event", event, payload, seq?}`

The protocol format has not changed across the clawdbot → openclaw rebrand.

## Installation Strategy

### Option A: npm install (current)

```bash
OPENCLAW_VERSION="2026.2.9"
pnpm install -g "openclaw@${OPENCLAW_VERSION}"
```

**Pros**: Simple, versioned, quick to update.
**Cons**: Large install (~150MB+ with deps), includes unused assets (Chrome extension, DMG backgrounds), depends on npm registry at build time.

### Option B: Vendored tarball

```bash
# During development: download and commit the tarball
npm pack openclaw@2026.2.9
mv openclaw-2026.2.9.tgz vendor/

# During rootfs build: install from local tarball
pnpm install -g ./vendor/openclaw-2026.2.9.tgz
```

**Pros**: Reproducible (no registry dependency), auditable, works offline.
**Cons**: Large file in git (use Git LFS), manual update process.

### Option C: Build from source

```bash
git clone --depth 1 --branch v2026.2.9 https://github.com/openclaw/openclaw.git
cd openclaw && pnpm install && pnpm build
# Copy dist/ to rootfs
```

**Pros**: Full control, can strip unused features, can patch.
**Cons**: Complex build (63 dependencies), fragile, significant maintenance burden, build may require secrets or specific environment.

### Recommendation

**Use Option A (npm install) with pinned versions.** The package is pre-built and bundled. Building from source adds complexity with minimal benefit since we treat OpenClaw as a black-box dependency. If npm registry availability is a concern, use Option B (vendored tarball).

## Version Pinning

### Rule: Always pin an exact version. Never use `@latest`.

The pinned version is tracked in the repository:

```json
// versions.json
{
  "openclaw": "2026.2.9",
  "openclaw_updated": "2026-02-09",
  "openclaw_notes": "Rebrand release, config path migration, CVE fix"
}
```

This file is committed to git and checked during snapshot builds.

## Upgrade Cadence

### Monthly scheduled upgrades

- **When**: First week of each month
- **Who**: Whoever is on infra duty
- **What**: Evaluate all releases since last pin, test, upgrade

### Out-of-band upgrades

Trigger immediately for:
- Security advisories (CVEs)
- Bugs that affect our users (gateway crashes, auth failures)
- Breaking changes in upstream infrastructure

### Skip criteria

Do NOT upgrade if:
- Mid-feature and the snapshot is in flux
- The release is a pre-release (`-1`, `-2`, `-3`) unless it fixes a critical issue
- The changelog mentions only features we don't use

## Upgrade Process

### 1. Check what changed

```bash
CURRENT=$(jq -r .openclaw versions.json)
LATEST=$(npm view openclaw version)
echo "$CURRENT → $LATEST"

# Review releases: https://github.com/openclaw/openclaw/releases
# Check the 148KB CHANGELOG.md in the package
```

### 2. Check for breaking changes

| Area | What to check | How |
|------|---------------|-----|
| Config format | Key names, nesting, required fields | `openclaw config --help`, changelog |
| Config path | Directory and filename | `openclaw --help` (look for `--profile` docs) |
| CLI flags | `gateway` subcommand flags | `openclaw gateway --help` |
| Env vars | Prefix changes, new vars | `grep OPENCLAW_ dist/entry.js` |
| WebSocket protocol | Protocol version, message format | Changelog, test handshake |
| Node.js version | Minimum engine requirement | `npm view openclaw engines` |

### 3. Test in isolation

```bash
make test-openclaw-upgrade VERSION=2026.2.9
# or test multiple versions:
make test-openclaw-upgrade VERSIONS="2026.3.28 2026.3.29"
# strict opik install requirement (default in script):
E2E_REQUIRE_OPIK=1 make test-openclaw-upgrade VERSION=2026.3.29
```

Smoke tests:
- Gateway starts and listens on port 3000
- Config file loaded from expected path
- Opik plugin installs and plugin/channel config push smoke suite passes
- `allowInsecureAuth` / `dangerouslyDisableDeviceAuth` respected
- WebSocket handshake completes (challenge → connect → hello-ok)
- Control UI HTML loads with correct base path
- Channel/plugin config set/unset does not crash gateway

### 4. Update and deploy

```bash
# Update versions.json and init script
# Commit, build snapshot, deploy
git commit -m "chore: upgrade openclaw to $VERSION"
```

### 5. Verify in production

```bash
# Check gateway version in VM
gcloud compute ssh <host> --tunnel-through-iap --command='
  curl -sf http://192.168.100.10:3000/ | grep -oP "OpenClaw [0-9.]+"'

# Check gateway logs for config loading
# Check WebSocket connections succeed (no 1006/1008 errors)
```

## Config Path Strategy

### Recommended: dual-write + both env vars

Write config to both legacy and new paths so it works regardless of version:

```bash
GATEWAY_CONFIG=$(cat <<'EOF'
{
  "gateway": {
    "port": 3000,
    "bind": "lan",
    "mode": "local",
    "controlUi": {
      "enabled": true,
      "dangerouslyDisableDeviceAuth": true,
      "allowInsecureAuth": true
    }
  }
}
EOF
)

for dir in /home/openclaw/.clawdbot /home/openclaw/.openclaw; do
    mkdir -p "$dir/workspace"
    echo "$GATEWAY_CONFIG" > "$dir/clawdbot.json"
    echo "$GATEWAY_CONFIG" > "$dir/openclaw.json"
    chown -R openclaw:openclaw "$dir"
done
```

Set both env var names in the gateway startup:

```bash
export CLAWDBOT_CONFIG_PATH=/home/openclaw/.clawdbot/clawdbot.json
export OPENCLAW_CONFIG_PATH=/home/openclaw/.openclaw/openclaw.json
export CLAWDBOT_STATE_DIR=/home/openclaw/.clawdbot
export OPENCLAW_STATE_DIR=/home/openclaw/.openclaw
```

## Rollback

Every snapshot is immutable and named with a timestamp:

```
ocm-snapshot-20260210-040840
```

To roll back:

```bash
make set-snapshot NAME=$(cat .snapshot.previous)
make deploy-backend
```

Rules:
- Never delete the previous snapshot until validated (min 24 hours)
- Keep at least 3 recent snapshots in GCP
- If rolling back, also revert init script changes that depend on the newer version

## Integration Points

### Init script (`scripts/init-openclaw.sh`)

Primary integration surface. Version-sensitive areas:
- Config file path and filename (section 10)
- Env var names (OPENCLAW_* vs CLAWDBOT_*)
- CLI flags for `openclaw gateway` subcommand
- Working directory path

### Gateway proxy (`backend/internal/agentapi/proxy.go`)

Mostly version-agnostic (bidirectional byte forwarding). Sensitive to:
- `__CLAWDBOT_CONTROL_UI_BASE_PATH__` global name in HTML rewriting
- If renamed to `__OPENCLAW_CONTROL_UI_BASE_PATH__`, the rewrite in `proxyGatewayRoot` breaks

### Frontend (`frontend/src/pages/GatewayDashboard.tsx`)

Embeds gateway UI in iframe with `gatewayUrl` query param. Stable across versions.

## Monitoring

### Version drift alert

```bash
CURRENT=$(jq -r .openclaw versions.json)
LATEST=$(npm view openclaw version)
if [ "$CURRENT" != "$LATEST" ]; then
    echo "OpenClaw update: $CURRENT → $LATEST"
fi
```

### Gateway health

Agent polls gateway port 3000 every 30s. Reports `gateway_ok: false` if unreachable.

## See Also

- [rootfs-design.md](rootfs-design.md) — Rootfs build process and layout
- [RECOVERY_AND_PERSISTENCE.md](RECOVERY_AND_PERSISTENCE.md) — Recovery mechanisms and data persistence
