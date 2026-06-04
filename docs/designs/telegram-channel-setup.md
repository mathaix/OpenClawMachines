# Telegram Channel Setup — Design Document

**Date:** 2026-03-27
**Status:** Investigation complete — fix identified
**Problem:** Telegram channel pairing is broken after OpenClaw upgrade to v2026.3.22+

## Problem Statement

Users cannot connect to the Telegram bot via DM. The bot responds with "pairing required" and generates a setup link pointing to `127.0.0.1`, which is unreachable from external devices. The in-VM agent misdiagnoses this as a network binding issue and suggests `openclaw config set gateway.bind lan`, which is wrong for the OCM platform (gateway is behind a Cloudflare Tunnel).

## Timeline

| Date | Event | Impact |
|------|-------|--------|
| Feb 25 | `7b5ad5c` — Gateway bind changed from `lan` to `127.0.0.1` | No impact — Telegram uses polling, not webhooks |
| ~Feb 28 | Telegram confirmed working with loopback bind | Pairing was functional |
| Mar 11 | `3a2c5f1` — OpenClaw upgraded from v2026.3.8 to v2026.3.12 | Unknown impact |
| Mar 23 | `8311dec` — OpenClaw upgraded to v2026.3.22 | **12 breaking changes** — pairing security hardened |
| Mar 24 | Upgraded to v2026.3.23-2, then v2026.3.24 | Additional pairing hardening |
| Mar 26 | `45a0cdf` — Channel tokens switched to plaintext injection | Fixed crashloop but pairing still broken |

## Root Cause Analysis

### OpenClaw v2026.3.22 Breaking Changes (Pairing-Related)

1. **Pairing codes bound to intended node profile** — setup codes now encode the expected role/scope, preventing escalation during first-use bootstrap
2. **DM warning without allowlist** — new warning when Telegram DMs arrive without a configured `allowFrom` list
3. **`channels.telegram.groups["*"].requireMention=true` seeded by default** — fresh setups require `@mention` in groups
4. **Channel ID validation hardened** — prototype-chain and control-character abuse blocked

### v2026.3.24 Additional Changes

- **Pairing security hardened** — `device.token.rotate` deny handling (GHSA-7jrw-x62h-64p8)
- **Channel startup isolation** — one broken channel no longer blocks others (relevant: a misconfigured channel won't crashloop the gateway)
- **Telegram pairing codes rendered as Telegram-only code blocks**

### Two Distinct Pairing Systems

OpenClaw has two separate pairing mechanisms that are often confused:

#### 1. Device Pairing (Working)

Controls which devices (TUI, CLI, browser) can connect to the gateway.

- **Status:** Bypassed in OCM via `gateway.auth.mode: "token"` + `dangerouslyDisableDeviceAuth: true`
- **How it works:** Token auth makes `sharedAuthOk=true`, enabling `roleCanSkipDeviceIdentity`
- **Not the problem**

#### 2. Channel Pairing (Broken)

Controls which Telegram/Discord users can DM the bot.

- **Status:** Broken since v2026.3.22 upgrade
- **How it works:** When a user DMs the bot, the gateway checks if they're in the channel's `allowFrom` list. If not, it initiates a pairing flow that generates a setup code/link
- **The link contains:** `{ url: "<gateway URL>", bootstrapToken: "<short-lived token>" }`
- **The problem:** The gateway doesn't know its external URL, so it uses the bind address (`127.0.0.1:18789`), generating unreachable links

### Why It Worked Before

Before v2026.3.22, the Telegram channel had a more permissive default — DMs were accepted without requiring channel-level pairing or an explicit `allowFrom` list. The upgrade introduced stricter defaults that require either:
- An explicit `dmPolicy: "open"` setting, OR
- A successful pairing flow (which needs a reachable gateway URL)

## Architecture Context

### How Telegram Connects (Polling, Not Webhooks)

```
Telegram API servers
  ↑ getUpdates (long-polling, outbound from VM)
  |
OpenClaw Gateway (127.0.0.1:18789)
  ↑ proxied via loopback
  |
Auth Proxy (192.168.100.x:8080)
  ↑ Cloudflare Tunnel
  |
Cloudflare CDN → Browser/API clients
```

The gateway **polls** Telegram's API — no inbound webhook URL needed. The `127.0.0.1` bind is correct for the gateway; external access comes through the auth proxy + Cloudflare Tunnel.

### Current Config Assembly Flow

1. **Platform defaults** — `gateway.mode: "local"`, `auth.mode: "token"`, `controlUi` settings
2. **Capability templates** — Channel registry entry sets `channels.telegram.enabled: true`
3. **Capability overrides** — ChannelsTab saves `dmPolicy`, `groups` settings via `updateMachineCapabilityOverrides`
4. **Plaintext token injection** — `assembler.go:580-599` injects `channels.telegram.botToken` directly

### What the Assembled Config Looks Like Today

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "botToken": "123456:ABC-DEF-xyz"
    }
  }
}
```

Missing: `dmPolicy`, `groups`, and any other channel-level settings that v2026.3.22+ requires.

## The Fix

### Option 1: Set `dmPolicy: "open"` (Recommended — Simplest)

Skip channel pairing entirely by setting `dmPolicy: "open"` in the assembled config. This tells the gateway to accept all DMs without requiring the sender to be in an allowlist or go through pairing.

**Implementation:**

The ChannelsTab already has UI for `dmPolicy` and saves it as a capability override. The override flows through config assembly via `deepMerge` at step 2-3. The fix has two parts:

**Part A — Default `dmPolicy` to `"open"` in the channel registry template:**

Update the Telegram channel registry entry's `ConfigTemplate` to include:
```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "dmPolicy": "open",
      "groups": {
        "*": {
          "requireMention": true
        }
      }
    }
  }
}
```

This gives all machines a sensible default: open DMs (no pairing needed), but require `@mention` in groups (security). Users can override via ChannelsTab.

**Part B — Verify capability overrides flow through:**

The ChannelsTab saves `dmPolicy` and `groups` as capability overrides on the Telegram channel entry. These are deep-merged in `assembler.go:377-386`. Need to verify:
1. Overrides are saved correctly (`updateMachineCapabilityOverrides` API)
2. Overrides survive the `StripProtectedKeys` filter (`channels.*` is NOT protected)
3. Bot token injection (step 5e) doesn't clobber the `dmPolicy`/`groups` keys (it only sets `botToken`)

**Pros:**
- No gateway URL configuration needed
- No pairing flow needed
- Works with loopback bind
- ChannelsTab already has the UI
- Users who want pairing can set `dmPolicy: "pairing"` via overrides

**Cons:**
- Open DMs mean any Telegram user who finds the bot can chat with it
- Users need to use `allowFrom` or group-level restrictions for security

### Option 2: Inject `gateway.remote.url` (For Pairing Flow)

If pairing is desired (e.g., for security-conscious users), inject the gateway's external URL so pairing links work:

**Implementation:**

In `assembler.go`, inject `gateway.remote.url` using the existing `VMHostname`:
```go
gw := getOrCreateMap(result, "gateway")
if params.VMHostname != "" {
    remote := getOrCreateMap(gw, "remote")
    remote["url"] = "wss://" + params.VMHostname
}
```

**Pros:**
- Pairing flow works correctly
- Users can control who accesses the bot
- Security-first approach

**Cons:**
- Requires the auth proxy to forward WebSocket pairing traffic correctly
- More complex — pairing state must persist across VM reboots (already handled via `/data` volume)
- Gateway bound to loopback — the pairing URL points to the tunnel, which routes through auth proxy, which forwards to loopback gateway. Need to verify this full chain works for pairing handshake

### Option 3: Both (Recommended Long-Term)

Inject `gateway.remote.url` AND default `dmPolicy: "open"`:
- Pairing links work for users who want the security
- Open DMs work out of the box for quick setup
- ChannelsTab lets users choose their preference

## Remaining Issues from Codex Review

These issues from the `45a0cdf` review are still open and should be addressed alongside this fix:

### 1. Channel Credential Live-Update Gap

**Problem:** Changing a channel credential on a running machine doesn't trigger a config push. The `pushCredentialsToVMByID` path only handles LLM credentials (after the reverts). Channel tokens live in the assembled config, so updating them requires a full `pushMachineConfig`.

**Fix:** In `handleSetMachineCredential` and `handleDeleteMachineCredential`, detect non-LLM providers and trigger `pushMachineConfig` instead of (or in addition to) `pushCredentialsToVMByID`.

### 2. Plaintext Tokens in Logs

**Problem:** The PTY server logs `openclaw config set --batch-json` argv at info level, which includes channel tokens. The assembled config is also logged verbatim in debug mode.

**Fix:**
- Redact `--batch-json` values in PTY server logging (replace token values with `***`)
- Remove or redact assembled config from debug logging in `machine_config.go`

### 3. ChannelsTab Override Round-Trip Safety

**Problem:** Loading overrides in ChannelsTab only updates local state when `dmPolicy` or `groups["*"]` are present. Overrides saved from the old `ChannelSetup.tsx` may have different shapes, causing silent data mutation on save.

**Fix:** On load, initialize all fields from the stored overrides with explicit defaults for missing values, rather than leaving React state from a previous render.

## Files Involved

| File | Change Needed |
|------|--------------|
| `backend/internal/configassembly/assembler.go` | Option 2: inject `gateway.remote.url` from `VMHostname` |
| Telegram channel registry entry (DB seed) | Option 1: add `dmPolicy: "open"` + `groups` defaults to `ConfigTemplate` |
| `backend/internal/api/credentials.go` | Issue #1: trigger config push for non-LLM credential changes |
| `backend/cmd/agent/ptyserver.go` | Issue #2: redact `--batch-json` token values in logs |
| `backend/internal/api/machine_config.go` | Issue #2: redact assembled config in debug logs |
| `frontend/src/pages/machine-tabs/ChannelsTab.tsx` | Issue #3: safe initialization of override fields on load |

## Test Plan

1. **Verify `dmPolicy: "open"` works:** Set in config, restart gateway, DM the bot from an unknown Telegram account — should respond without pairing
2. **Verify capability overrides flow:** Save `dmPolicy: "pairing"` from ChannelsTab, check assembled config includes it
3. **Verify bot token injection doesn't clobber overrides:** Check assembled config has both `botToken` AND `dmPolicy`/`groups`
4. **Verify live credential update:** Change bot token in dashboard while machine is running, verify gateway picks up the new token
5. **E2E test:** `make test-gateway-e2e` should pass with new config shape
