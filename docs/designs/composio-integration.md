# Composio Integration

> **Current state (2026-05):** Option B (backend REST proxy) is implemented and deployed.
> The "Integration Approach" section below is preserved as historical context.
> For what's actually running today, see [Current state](#current-state-2026-05) below.

## Current state (2026-05)

OCM runs a backend REST proxy in front of Composio. The in-VM plugin calls
OCM's backend, which forwards to Composio's REST API using a single platform
`ak_` key. Per-machine isolation is enforced by passing the machine's UUID
as the Composio `user_id`. As of the `composiofix` branch (2026-05),
the proxy also requires a per-machine bearer token to prevent IDOR.

### Deployed architecture

```
Agent (in OpenClaw gateway, in Firecracker VM)
  → composio plugin (TypeScript, in gateway)
    → HTTPS to OCM backend /api/composio/*
       headers: Authorization: Bearer <per-machine JWT>
      → backend validates token, forces user_id from token's machine_id claim
        → Composio REST API (https://backend.composio.dev/api/v3)
           headers: x-api-key: <platform ak_ key from GCP Secret Manager>
          → Composio credential store
            → External API (Gmail, Slack, etc.)
```

### Key pieces

| Component | File / location |
|---|---|
| Backend proxy handlers | `backend/internal/api/composio_proxy.go` |
| Composio REST client | `backend/internal/composio/client.go` |
| Per-machine bearer token | `backend/internal/auth/composio_proxy_token.go` (HS256, signed with `JWT_SECRET`, 24 h TTL, issuer `ocm-composio-proxy`) |
| Config assembly | `backend/internal/configassembly/assembler.go` — injects `apiUrl`, `userId`, and `machineToken` into `plugins.entries.composio.config` |
| Plugin source | [`mathaix/ocm-openclaw-composio-plugin`](https://github.com/mathaix/ocm-openclaw-composio-plugin) — npm package `@mathaix/ocm-openclaw-composio-plugin` (not published; built locally) |
| Plugin tarball | `plugins/composio-plugin.tgz` (committed binary; rebuild via `scripts/build-composio-plugin.sh`) |
| Platform key | `COMPOSIO_CONSUMER_KEY` in GCP Secret Manager (name is legacy; value is now an `ak_` platform key) |
| Backend env wiring | `backend/cmd/server/main.go` — reads `COMPOSIO_CONSUMER_KEY`, calls `SetComposioClient` |
| Config URL | `COMPOSIO_API_URL` or `PUBLIC_URL` + `/api/composio` |

### What the bearer token protects against

Before the `composiofix` work, the public `/api/composio/*` routes took
`user_id` directly from the caller's query/body and forwarded it to Composio
without authenticating the caller. Anyone with network reach and a machine
UUID could act as that machine's connected accounts.

After: every call must carry a valid Composio proxy JWT in the
`Authorization` header. The handler extracts `machine_id` from the token's
claims and uses that as the Composio `user_id`. The body/query `user_id`
is ignored. The token is signed with `JWT_SECRET`, has issuer
`ocm-composio-proxy` (so user-session tokens signed with the same key
can't be reused here), and expires after 24 hours.

### Operations

- **Key rotation:** [`docs/runbooks/composio-key-rotation.md`](../runbooks/composio-key-rotation.md)
- **Plugin rebuild:** edit `~/ocm-openclaw-composio-plugin`, run
  `scripts/build-composio-plugin.sh`, commit the new `plugins/composio-plugin.tgz`,
  then `make build-upload-openclaw` to bundle it into a new runtime artifact.
- **Plugin source repo:** `mathaix/ocm-openclaw-composio-plugin` (peer of
  this repo, expected at `~/ocm-openclaw-composio-plugin`).

---

## Historical: original design

The sections below are preserved as the original design doc and pre-date the
REST proxy + IDOR fix. Read for context, not as current-state truth.

## Overview

[Composio](https://composio.dev) is an integration platform for AI agents that provides 850+ app integrations (Gmail, Notion, Slack, GitHub, Salesforce, etc.) behind a unified SDK. It handles OAuth flows, token refresh, and credential storage so agents can use external tools without manual API key configuration.

Composio publishes an official OpenClaw plugin (`@composio/openclaw-plugin`) that bridges the gateway to Composio's managed MCP server.

## How Composio Works

### Architecture

```
Agent (in OpenClaw gateway)
  → Composio OpenClaw Plugin
    → MCP connection to connect.composio.dev/mcp
      → Composio credential store (encrypted, SOC2)
        → External API (Gmail, Slack, etc.)
```

The LLM never sees raw credentials. Composio retrieves tokens from encrypted storage, makes API calls on the agent's behalf, and returns only the result.

### Meta-Tools Pattern

Instead of loading hundreds of tool definitions, Composio provides ~5 meta-tools:
- `COMPOSIO_SEARCH_TOOLS` — discover relevant tools by task description
- `COMPOSIO_GET_TOOL_SCHEMAS` — retrieve input specs for specific tools
- `COMPOSIO_MANAGE_CONNECTIONS` — handle auth flows at runtime
- `COMPOSIO_MULTI_EXECUTE_TOOL` — execute tools using session credentials
- `COMPOSIO_REMOTE_WORKBENCH` — sandboxed environment for bulk operations

### Supported Integrations

- **Productivity**: Gmail, Google Drive, Google Sheets, Notion, Slack, Discord, Trello, Asana
- **Dev Tools**: GitHub, Jira, Linear
- **CRM**: Salesforce, HubSpot, Pipedrive
- **Finance/Support**: Freshdesk, others
- Custom integrations via OpenAPI specs (OAuth1, OAuth2, API-KEY, BASIC auth)

### Pricing

| Tier | Cost | Tool Calls/Month |
|------|------|-----------------|
| Free | $0 | 20,000 |
| Tier 1 | $29/mo | 200,000 |
| Tier 2 | $229/mo | 2,000,000 |
| Enterprise | Custom | Custom |

### SDK Availability

- **Python**: `composio` (Python 3.10+)
- **TypeScript**: `@composio/core` (Node.js + browser)
- **Go**: None — use REST API or MCP
- **CLI**: Available for toolkit management

## Existing OpenClaw Plugin

### Package

- **npm**: `@composio/openclaw-plugin`
- **GitHub**: [ComposioHQ/openclaw-composio-plugin](https://github.com/ComposioHQ/openclaw-composio-plugin)

### How It Works

1. At gateway startup, the plugin calls Composio's MCP server (`tools/list`) with a consumer key
2. **All tools are registered** regardless of whether the user has connected that app
3. Tool list is frozen from that point (no re-discovery, no polling, no webhooks)
4. At execution time, if the user hasn't connected the app, Composio returns an auth error
5. The plugin's system prompt tells the agent to direct the user to `dashboard.composio.dev` to connect

### Configuration

```json
{
  "plugins": {
    "entries": {
      "composio": {
        "enabled": true,
        "config": {
          "consumerKey": "ck_...",
          "mcpUrl": "https://connect.composio.dev/mcp"
        }
      }
    }
  }
}
```

### Limitations for Multi-Tenant Use

The plugin was designed for **single-user self-hosted OpenClaw**, not a managed multi-tenant platform:

1. **No per-user identity** — sends only the consumer key, Composio maps to `user_id=default`
2. **No per-machine isolation** — two machines with the same consumer key share OAuth connections
3. **No proactive status** — no way for the dashboard to show connection status without separate API calls
4. **No Connect Link generation** — relies on user visiting `dashboard.composio.dev` manually
5. **Static tool list** — tools registered once at startup, no dynamic updates

## Integration with OCM

### What Already Exists in OCM

| System | How It Helps |
|--------|-------------|
| Plugin catalog (`plugin_catalog` table) | Add Composio as a catalog entry |
| Per-machine plugins (`machine_plugins` table) | Enable/disable per machine with config overrides |
| Config assembly (`assembler.go`) | Merge Composio config template into gateway config |
| Credential system (`machine_credentials`) | Store consumer keys (platform-wide or per-machine) |
| Frontend plugin UI (`MachinePlugins.tsx`) | Enable/disable toggle already works |
| IntegrationsTab (placeholder) | Ready to be built out with real connection status |

### Integration Approach

#### Option A: Use Plugin As-Is (MVP)

Minimal effort, single-user per machine:

1. Add `@composio/openclaw-plugin` to rootfs build
2. Add `composio` entry to plugin catalog with config template
3. Users enable via MachinePlugins UI, provide their own consumer key
4. Auth happens in-chat (agent directs to dashboard.composio.dev on auth errors)

**Pros**: Fastest path, no plugin fork needed.
**Cons**: No dashboard connection status, no per-machine isolation, user manages their own Composio account.

#### Option B: Backend Proxy Layer (Recommended) — IMPLEMENTED (PR #29, 2026-03)

Build a thin Go backend layer on top of Composio's REST API for proper multi-tenant support:

1. **Identity mapping**: Each machine gets a Composio `user_id` (use machine ID)
2. **Connect Links**: `POST /api/machines/{id}/integrations/{app}/connect` generates a per-machine OAuth URL via Composio API
3. **Connection status**: `GET /api/machines/{id}/integrations` calls Composio API (`GET /api/v3/connected_accounts?user_id={machineId}`) to list connected apps
4. **Frontend IntegrationsTab**: Grid of apps with Connect/Disconnect buttons and live status
5. **Plugin config**: Inject machine-specific `user_id` into plugin config (requires plugin fork or upstream PR)

**Pros**: Per-machine isolation, dashboard shows real status, branded OAuth (your app name).
**Cons**: More work, need to fork plugin or get upstream changes for `user_id` support.

### Composio REST API Endpoints Needed (Option B)

| Purpose | Composio API |
|---------|-------------|
| List connected accounts | `GET /api/v3/connected_accounts?user_id={machineId}` |
| Initiate connection | `POST /api/v3/connected_accounts` with redirect URL |
| Get connection status | `GET /api/v3/connected_accounts/{id}` |
| Delete connection | `DELETE /api/v3/connected_accounts/{id}` |
| List available toolkits | `GET /api/v3/toolkits` |

### Frontend UX

**IntegrationsTab** would show:
- Grid of available apps (Gmail, Slack, Notion, GitHub, etc.)
- Connection status per app (Connected / Not connected)
- **Connect** button → opens Composio OAuth popup
- **Disconnect** button for connected apps
- Connected account info (e.g., email address)

### Consumer Key Strategy

| Approach | Description |
|----------|-------------|
| **Platform-wide key** | OCM provides one Composio API key for all machines. Simpler, you pay for usage. |
| **BYOK** | Users provide their own consumer key via machine config overrides. They pay, full control. |
| **Hybrid** | Platform key as default, users can override with their own. |

## Open Questions

1. Should we fork the plugin to add `user_id` support, or contribute upstream?
2. Platform-wide vs BYOK consumer key strategy?
3. Which apps to surface in the IntegrationsTab initially? (Gmail, Slack, Notion, GitHub, Sheets?)
4. Do we want to support Composio's "managed credentials" (Composio's OAuth app) or require custom OAuth apps (our brand on consent screens)?
5. Pricing pass-through: if platform-wide key, do we meter Composio usage per machine?

## References

- [Composio Documentation](https://docs.composio.dev/docs)
- [Composio OpenClaw Integration Guide](https://composio.dev/toolkits/composio/framework/openclaw)
- [ComposioHQ/openclaw-composio-plugin](https://github.com/ComposioHQ/openclaw-composio-plugin)
- [Composio User Management](https://docs.composio.dev/docs/user-management)
- [Composio Connected Accounts API](https://docs.composio.dev/auth/connection)
- [Composio Pricing](https://composio.dev/pricing)
