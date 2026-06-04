# Runner Architecture: Gateway RPC for Channel Operations

**Status:** On hold — pending gateway RPC surface discovery
**Date:** 2026-03-05

## Context

Current channel setup (Telegram, Discord) works through OCM's config assembly pipeline: UI collects token → backend stores credential → config assembly injects it → gateway hot-reloads. Pairing approval uses `gatewayRPC("node.pair.approve", ...)` over WebSocket.

## Proposed Direction

Replace config-assembly-driven channel setup with **gateway WebSocket RPC calls**. The gateway manages its own channel state natively — we stop reimplementing it.

```
UI → gatewayRPC(machineId, "channels.add", {provider:"telegram", token:"..."})
   → existing proxy chain → gateway handles it
```

## Decision: Option A (Gateway RPC) with Option B fallback

- **Primary:** Use gateway's WebSocket RPC for all channel operations
- **Fallback:** Agent exec-into-VM endpoint for operations that have no RPC method (e.g., WhatsApp QR flow)

See full options analysis in conversation (2026-03-05).

## Discovered CLI Commands

`openclaw channels login --channel <name>` exists in v2026.2.26. Confirmed:
- `openclaw channels login --channel whatsapp` → "Unsupported channel: whatsapp" (not supported yet)
- Telegram and Discord are likely supported channels

### Next Step

Probe the full `openclaw channels` CLI surface:
- `openclaw channels --help`
- `openclaw channels login --channel telegram`
- `openclaw channels list`
- Check upstream openclaw docs/source for RPC methods
- Specifically need: channel add/remove, token update, group config

## Affected Components

- `frontend/src/components/ChannelSetup.tsx` — would call `gatewayRPC()` instead of backend credential API
- `frontend/src/components/onboarding/StepChannels.tsx` — same
- `backend/internal/configassembly/` — channel credential injection could be simplified
- `backend/internal/metadata/server_linux.go` — `injectChannelCredentials()` may become unnecessary
