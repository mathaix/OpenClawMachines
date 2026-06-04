# Composio Integration Design

## Goal

Enable OCM machines to connect to third-party apps (Gmail, Slack, Notion, GitHub, etc.) via Composio, with per-machine isolation, a dashboard IntegrationsTab for managing OAuth connections, and a forked plugin that passes machine identity to Composio's MCP server.

## Architecture

```
┌─ Frontend (IntegrationsTab) ──────────────────────────┐
│  Category-grouped app grid from integration_catalog    │
│  Connect/Disconnect buttons + live connection status   │
│  Poll-on-focus status refresh                          │
└──────────────┬────────────────────────────────────────┘
               │ OCM API calls
               ▼
┌─ OCM Backend ─────────────────────────────────────────┐
│  integrations.go handlers (user + admin endpoints)     │
│  composio.Client: wraps Composio REST API v3           │
│  integration_catalog: dynamic curated app list         │
│  integration_events: tracks connect/disconnect actions  │
│  Config assembly: injects plugin config per machine    │
│  Consumer key via ocm-secrets (not plain text)         │
└──────────────┬────────────────────────────────────────┘
               │ Composio REST API (consumer key)
               ▼
┌─ Composio API (backend.composio.dev) ─────────────────┐
│  user_id = machineId for per-machine isolation         │
│  OAuth token storage, credential management            │
└───────���───────────────────────────────────────────────┘

┌─ Gateway (inside VM) ─────────────────────────────────┐
│  composio plugin (forked, id: "composio")              │
│    → MCP connection to composio MCP server             │
│    → userId passed via header or query param           │
│    → Consumer key resolved at runtime via ocm-secrets  │
│    → Tool execution scoped to machine's connections    │
└───────────────────────────────────────────────────────┘
```

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Integration approach | Option B: backend proxy | Per-machine isolation, secrets stay server-side, single integration point |
| Consumer key strategy | Platform-wide | OCM provides one key, machines isolated by user_id (machine ID) |
| Identity mapping | Machine ID as Composio user_id | Each machine gets its own isolated connections |
| Connection flow | Dashboard-only (IntegrationsTab) | Users connect integrations proactively before working |
| Status polling | Poll-on-focus | Refetch when user returns to tab or popup closes; no webhooks |
| App catalog | DB-backed (integration_catalog) | Add/remove integrations without redeploying |
| Event tracking | integration_events table | Record every connect/disconnect for analytics |
| Plugin source | Fork at `~/ocm-openclaw-composio-plugin` | Adds userId config passthrough; published to npm as `@mathaix/ocm-openclaw-composio-plugin` |
| Plugin ID | `composio` (matches fork manifest) | Must match `openclaw.plugin.json` id field for gateway discovery |
| Composio credentials | One key (consumer key, `ck_...`) | Start with consumer key for both REST API and MCP; add separate API key only if REST API rejects it |
| Key delivery to VM | ocm-secrets exec SecretRef | Consumer key must NOT appear in assembled config (exposed via `/assembled-config`); use same pattern as LLM provider keys |
| Key rotation | Accept the gap | Deploy new key → running VMs cycle naturally → revoke old key. Live push can be added later. |
| Connections per toolkit | One per machine (MVP) | Composio supports multiple; we show most recent only. Document limitation. |

## Component 1: Plugin Fork

**Repo:** `~/ocm-openclaw-composio-plugin` → published to npm as `@mathaix/ocm-openclaw-composio-plugin`

**Plugin ID:** `composio` (matches existing `openclaw.plugin.json` `"id": "composio"`)

**Current plugin behavior:**
- Reads `consumerKey` and `mcpUrl` from config
- Sends `x-consumer-api-key` header to Composio MCP server
- All tool executions map to `user_id=default` (no isolation)

**Fork changes:**
1. Add `userId` field to `ComposioConfig` interface and `ComposioConfigSchema`
2. In `parseComposioConfig`, read `userId` from `configObj?.userId` or `raw.userId`, default to `"default"`
3. Pass `userId` on MCP calls — either as `x-composio-user-id` header or `?user_id={userId}` query param (verify which Composio accepts during implementation)
4. Update `openclaw.plugin.json` config schema to include `userId` property
5. Change `consumerKey` resolution to support exec SecretRef: if `consumerKey` matches `/^[A-Z_]+$/` (e.g. `COMPOSIO_CONSUMER_KEY`), treat it as an env var name and resolve via `process.env[consumerKey]`
6. Update `package.json` name to `@mathaix/ocm-openclaw-composio-plugin`

**Config schema addition:**
```json
{
  "userId": {
    "type": "string",
    "description": "Machine-specific user ID for per-machine connection isolation (injected by OCM, do not set manually)"
  }
}
```

## Component 2: Backend — Composio Client Package

**New package:** `backend/internal/composio/`

```go
// client.go
type Client struct {
    consumerKey string
    baseURL     string // https://backend.composio.dev/api
    httpClient  *http.Client
}

// Connection represents a Composio connected account.
// Field names are OCM-internal; the client maps from Composio's actual response format.
type Connection struct {
    ID        string `json:"id"`
    Toolkit   string `json:"toolkit"`   // Composio toolkit slug (e.g. "gmail", "slack")
    Status    string `json:"status"`    // normalized to lowercase: "active", "initiated", "failed"
    CreatedAt string `json:"created_at"`
}

type ConnectLinkResponse struct {
    URL string `json:"url"` // redirect URL for OAuth flow
}

func NewClient(consumerKey string) *Client

// ListConnections returns all connected accounts for a machine.
// Calls Composio REST API, maps response fields to Connection struct.
func (c *Client) ListConnections(ctx context.Context, machineID string) ([]Connection, error)

// CreateConnectLink generates an OAuth URL for connecting an app.
// Calls Composio REST API with user_id, auth_config_id, and callback_url.
func (c *Client) CreateConnectLink(ctx context.Context, machineID, authConfigID, callbackURL string) (*ConnectLinkResponse, error)

// DeleteConnection removes a connected account.
// IMPORTANT: Caller must verify the connection belongs to the target machine
// before calling this method (see handleDeleteIntegration).
func (c *Client) DeleteConnection(ctx context.Context, connectionID string) error

// DeleteAllConnections removes all connections for a machine.
// Called on machine delete to prevent orphaned Composio accounts.
func (c *Client) DeleteAllConnections(ctx context.Context, machineID string) error
```

- `consumerKey` sourced from `COMPOSIO_CONSUMER_KEY` environment variable (GCP Secret Manager → Cloud Run env var)
- `machineID` maps to Composio's `user_id` parameter — this is the isolation boundary
- No local DB state for connections — always fetched live from Composio's API (single source of truth)
- `DeleteAllConnections` called during machine deletion to clean up orphaned accounts
- If the consumer key doesn't work for REST API calls, add a separate `COMPOSIO_API_KEY` (`ak_...`) env var
- Field mapping from Composio response format to OCM types is an implementation detail of the client

## Component 3: Backend — Database Tables

### integration_catalog

Dynamic curated list of integrations shown in the dashboard. Admin-managed, no deploy needed to add/remove.

```sql
CREATE TABLE integration_catalog (
    id             TEXT PRIMARY KEY,        -- e.g. "gmail", "slack"
    name           TEXT NOT NULL,           -- "Gmail", "Slack"
    icon           TEXT NOT NULL,           -- icon identifier for frontend
    toolkit        TEXT NOT NULL,           -- Composio toolkit name
    auth_config_id TEXT,                    -- Composio auth_config_id (nullable until configured)
    category       TEXT NOT NULL DEFAULT 'other', -- "google", "dev", "social", "crm", "productivity"
    sort_order     INT NOT NULL DEFAULT 0,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**NOTE:** User-facing endpoints only return entries where `auth_config_id IS NOT NULL`. Entries with null `auth_config_id` are admin-visible only (not yet configured). This prevents users seeing a "Connect" button that would fail.

**Seed data (13 integrations):**

| id | name | category |
|----|------|----------|
| gmail | Gmail | google |
| google-calendar | Google Calendar | google |
| google-drive | Google Drive | google |
| google-sheets | Google Sheets | google |
| google-docs | Google Docs | google |
| notion | Notion | productivity |
| youtube | YouTube | google |
| github | GitHub | dev |
| slack | Slack | productivity |
| jira | Jira | dev |
| linkedin | LinkedIn | social |
| x | X | social |
| hubspot | HubSpot | crm |

`auth_config_id` values are populated by admin after creating auth configs in the Composio dashboard.

### integration_events

Records every connect/disconnect action for analytics and auditing.

```sql
CREATE TABLE integration_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     INTEGER NOT NULL REFERENCES accounts(id),
    machine_id     UUID REFERENCES machines(id) ON DELETE SET NULL,
    integration_id TEXT NOT NULL,           -- integration_catalog.id
    event          TEXT NOT NULL,           -- "connect_started", "connect_completed", "disconnected"
    metadata       JSONB,                   -- e.g. {"connected_account_id": "ca_...", "machine_slug": "my-bot"}
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_integration_events_machine ON integration_events(machine_id, created_at);
CREATE INDEX idx_integration_events_integration ON integration_events(integration_id, created_at);
```

**NOTE:** `machine_id` uses `ON DELETE SET NULL` (not CASCADE) so audit history survives machine deletion. The `metadata` field stores the machine slug for historical reference.

## Component 4: Backend — API Handlers

**New file:** `backend/internal/api/integrations.go`

### User endpoints (account-scoped)

| Endpoint | Handler | Purpose |
|----------|---------|---------|
| `GET .../machines/{id}/integrations` | `handleListIntegrations` | List configured catalog entries + live connection status |
| `POST .../machines/{id}/integrations/{integration}/connect` | `handleCreateConnectLink` | Log event, generate Connect Link, return redirect URL |
| `DELETE .../machines/{id}/integrations/{connId}` | `handleDeleteIntegration` | Verify ownership, log event, call Composio disconnect |

### Admin endpoints

| Endpoint | Handler | Purpose |
|----------|---------|---------|
| `GET /admin/integrations` | `handleListIntegrationCatalog` | List all catalog entries (including unconfigured) |
| `POST /admin/integrations` | `handleCreateIntegrationCatalogEntry` | Add new integration |
| `PUT /admin/integrations/{id}` | `handleUpdateIntegrationCatalogEntry` | Update integration (name, icon, auth_config_id, enable/disable) |
| `DELETE /admin/integrations/{id}` | `handleDeleteIntegrationCatalogEntry` | Remove integration |

### Callback endpoint (public, no auth)

| Endpoint | Handler | Purpose |
|----------|---------|---------|
| `GET /integrations/callback` | `handleIntegrationCallback` | Static HTML page that closes the popup |

### handleListIntegrations

1. Fetch entries from `integration_catalog` where `enabled = true AND auth_config_id IS NOT NULL`
2. Call `composio.Client.ListConnections(machineID)` for live status
3. Match connections to catalog entries by toolkit name
4. For connections with multiple accounts per toolkit, use the most recent active one
5. For any new active connection without a `connect_completed` event, log the event
6. Return merged list:

```json
[
  {
    "id": "gmail",
    "name": "Gmail",
    "icon": "gmail",
    "category": "google",
    "connected": true,
    "connected_account_id": "ca_abc123",
    "connected_at": "2026-03-28T..."
  },
  {
    "id": "slack",
    "name": "Slack",
    "icon": "slack",
    "category": "productivity",
    "connected": false
  }
]
```

### handleCreateConnectLink

1. Look up `auth_config_id` from `integration_catalog` for the given integration
2. Fail with 400 if `auth_config_id` is null
3. Log `connect_started` event to `integration_events`
4. Construct callback URL: `{PUBLIC_URL}/integrations/callback`
5. Call `composio.Client.CreateConnectLink(machineID, authConfigID, callbackURL)`
6. Return `{ "url": "https://..." }`

### handleDeleteIntegration

1. Call `composio.Client.ListConnections(machineID)` to get this machine's connections
2. Verify `connId` appears in the list — reject with 404 if not (prevents cross-machine disconnect)
3. Log `disconnected` event to `integration_events`
4. Call `composio.Client.DeleteConnection(connId)`
5. Return 204

### handleIntegrationCallback

Returns static HTML:
- Displays "Connection successful. You can close this window."
- Calls `window.opener.postMessage({ type: "composio-connected" }, "*")`
- Calls `window.close()`

### Machine deletion cleanup

In the existing machine delete handler, add: `composio.Client.DeleteAllConnections(machineID)` (best-effort, log errors but don't block delete).

## Component 5: Config Assembly & Secret Delivery

### Consumer key delivery (ocm-secrets pattern)

The consumer key must NOT appear as plain text in the assembled config, because:
- Assembled config is persisted in `machine_config.assembled_config` (DB)
- It's exposed via `GET /assembled-config` to account members
- A platform-wide key in plain text there would be extractable

**Instead, use the existing exec SecretRef pattern** (same as LLM provider keys):

1. Config assembly writes an env var reference (e.g. `COMPOSIO_CONSUMER_KEY`) as the `consumerKey` value
2. The plugin fork resolves env var references at runtime (see Component 1, change #5)
3. The actual key is delivered via the metadata server's `/v1/secrets` endpoint
4. Inside the VM, `ocm-secrets` resolves the reference before the gateway starts

### Config assembly changes

In `backend/internal/configassembly/assembler.go`:

**New field on `AssemblyParams`:**
```go
ComposioConsumerKey string // platform consumer key (ck_...) — for metadata server secrets, not config
```

**Injection logic** (after existing plugin merge):

When `ComposioConsumerKey` is non-empty and the `composio` plugin is in the enabled plugins list:

1. Set `plugins.entries.composio.config.consumerKey` to `"COMPOSIO_CONSUMER_KEY"` (env var name, not the actual key)
2. Set `plugins.entries.composio.config.mcpUrl` to `"https://connect.composio.dev/mcp"`
3. Set `plugins.entries.composio.config.userId` to `params.MachineID`
4. Set `plugins.allow` to include `"composio"`
5. Set `plugins.installs.composio` to `{"source": "archive", "installPath": "/home/openclaw/.openclaw/extensions/composio"}`

The actual consumer key is delivered as a platform secret via the metadata server (registered at machine startup, same as `OPIK_API_KEY`).

### Metadata server secret registration

In `backend/internal/api/agent_auth.go` (the secrets endpoint), add:
```go
if srv.composioConsumerKey != "" {
    secrets["COMPOSIO_CONSUMER_KEY"] = srv.composioConsumerKey
}
```

This makes the key available at `/v1/secrets` → `ocm-secrets` resolves it → plugin reads `process.env.COMPOSIO_CONSUMER_KEY`.

## Component 6: Rootfs — Bundling the Plugin

**Publish the fork to npm** as `@mathaix/ocm-openclaw-composio-plugin`, then install in Dockerfile from the registry (same pattern as Opik with `@opik/opik-openclaw`).

The fork at `~/ocm-openclaw-composio-plugin` is outside the Docker build context (`rootfs/`), so it cannot be `COPY`'d or packed locally. Publishing to npm is the correct approach.

**Dockerfile addition** (following the Opik pattern):
```dockerfile
ARG COMPOSIO_PLUGIN_VERSION=0.1.0
RUN cd /tmp \
    && npm pack @mathaix/ocm-openclaw-composio-plugin@${COMPOSIO_PLUGIN_VERSION} --quiet \
    && openclaw plugins install "/tmp/mathaix-ocm-openclaw-composio-plugin-${COMPOSIO_PLUGIN_VERSION}.tgz" \
    && rm -f /tmp/mathaix-ocm-openclaw-composio-plugin-*.tgz
```

And relocate to openclaw user home:
```dockerfile
RUN if [ -d /root/.openclaw/extensions/composio ]; then \
      mkdir -p /home/openclaw/.openclaw/extensions \
      && cp -r /root/.openclaw/extensions/composio /home/openclaw/.openclaw/extensions/ \
      && chown -R openclaw:openclaw /home/openclaw/.openclaw/extensions/composio \
      && rm -rf /root/.openclaw/extensions/composio; \
    fi
```

**Note:** Extension directory name matches the plugin ID (`composio`), not the npm package name.

**Plugin catalog seed** (in migration):
```sql
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template, status, sort_order)
VALUES (
  'composio',
  'Composio',
  'Connect third-party apps (Gmail, Slack, Notion, GitHub, etc.) to your agent via OAuth',
  'integrations',
  '1',
  'bundled',
  '{"plugins":{"allow":["composio"],"entries":{"composio":{"enabled":true,"config":{"mcpUrl":"https://connect.composio.dev/mcp"}}},"installs":{"composio":{"source":"archive","installPath":"/home/openclaw/.openclaw/extensions/composio"}}}}',
  'active',
  20
)
ON CONFLICT (id) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  config_template = EXCLUDED.config_template,
  updated_at = NOW();
```

**Auto-enable on machine creation:** Add `composio` to the default plugins enabled for new machines (same list as `memory-core` and `opik-openclaw`), so the IntegrationsTab works immediately.

## Component 7: Frontend — IntegrationsTab

**Replace placeholder** in `frontend/src/pages/machine-tabs/IntegrationsTab.tsx`.

**Data fetching:**
- On mount + on window focus: `GET .../machines/{id}/integrations`
- Returns only configured integrations (those with `auth_config_id`) with live connection status

**Layout:**
- Grouped by category (Google, Dev Tools, Social, CRM, Productivity)
- Grid of cards: icon, name, connection status badge
- Connected cards: green indicator + "Disconnect" button
- Disconnected cards: "Connect" button

**Connect flow:**
1. User clicks "Connect" → frontend calls `POST .../integrations/{integration}/connect`
2. Backend returns `{ url: "https://..." }` → frontend opens in popup (~500x700)
3. User completes OAuth in popup
4. Popup redirects to callback endpoint → renders close-window page
5. Callback page sends `postMessage` to opener → frontend refetches status
6. Card updates to show connected state

**Disconnect flow:**
1. User clicks "Disconnect" → confirmation dialog
2. Frontend calls `DELETE .../integrations/{connId}`
3. Card updates to disconnected state

**States:**
- Loading: skeleton cards
- No integrations configured: message explaining admin hasn't set up integrations yet
- Connect in progress: button spinner while popup is open
- Error: toast on failed connect/disconnect

**New API functions** in `frontend/src/lib/api.ts`:
```ts
export interface Integration {
  id: string;
  name: string;
  icon: string;
  category: string;
  connected: boolean;
  connected_account_id?: string;
  connected_at?: string;
}

export const listIntegrations = (accountId: number, machineId: string) =>
  request<Integration[]>(`/accounts/${accountId}/machines/${machineId}/integrations`);

export const createConnectLink = (accountId: number, machineId: string, integration: string) =>
  request<{ url: string }>(`/accounts/${accountId}/machines/${machineId}/integrations/${integration}/connect`, { method: "POST" });

export const deleteIntegration = (accountId: number, machineId: string, connId: string) =>
  request<void>(`/accounts/${accountId}/machines/${machineId}/integrations/${connId}`, { method: "DELETE" });
```

## Secret Management

**One secret in GCP Secret Manager:** `COMPOSIO_CONSUMER_KEY` (starts with `ck_...`)

**Delivery paths:**
1. **Backend (Cloud Run):** Injected as env var → used by `composio.Client` for REST API calls
2. **VM (metadata server):** Registered as platform secret → `ocm-secrets` resolves → plugin reads from env

**Never stored in:**
- `assembled_config` (DB/API-exposed) — only the env var name appears
- `plugin_catalog.config_template` — deliberately omitted
- `machine_plugins.config_overrides` — never set by users

**Key rotation:**
1. Generate new key in Composio dashboard
2. Update in GCP Secret Manager
3. Redeploy backend (`make deploy-backend`) — new machines get new key via metadata server
4. Running VMs still have old key until gateway restart / machine recreation
5. Revoke old key after all machines have cycled

## Testing

**Backend unit tests:**
- `composio/client_test.go` — mock HTTP server, test all client methods with success and error responses, verify field mapping from Composio response format
- `api/integrations_test.go` — mock composio client interface, test handler auth/ownership, delete ownership check, event logging, callback HTML
- `configassembly/assembler_test.go` — verify Composio config injection produces env var ref (not plain text key), `plugins.allow`, `plugins.installs`, `userId`

**Gateway E2E** (`gatewaye2e/`):
- Verify Composio config appears correctly in assembled config when plugin is enabled
- Verify consumer key does NOT appear in assembled config (only env var name)

**Manual testing:**
- Enable Composio plugin on a machine
- Click "Connect Gmail" in IntegrationsTab → verify OAuth popup opens
- Complete OAuth → verify popup closes, connection shows as "Connected"
- Disconnect → verify connection removed
- Attempt disconnect of another machine's connection → verify 404
- Delete machine → verify Composio connections cleaned up
- Verify agent can use Composio tools in chat
- Verify assembled config does not contain the actual consumer key

## Limitations (MVP)

- One connection per toolkit per machine (Composio supports multiple; we show most recent)
- No webhook-based real-time status updates (poll-on-focus)
- No in-chat agent-driven connection flow
- No BYOK consumer key support (platform-wide only)
- No Composio usage metering/billing pass-through
- No custom OAuth apps (use Composio's managed OAuth)
- No live config push for key rotation

## Files to Create/Modify

| Action | File |
|--------|------|
| Create | `backend/internal/composio/client.go` |
| Create | `backend/internal/composio/client_test.go` |
| Create | `backend/internal/api/integrations.go` |
| Create | `backend/internal/api/integrations_test.go` |
| Create | `backend/migrations/0XX_integration_catalog.sql` |
| Modify | `backend/internal/configassembly/assembler.go` — add Composio injection with env var ref |
| Modify | `backend/internal/configassembly/assembler_test.go` — add Composio test |
| Modify | `backend/internal/api/server.go` — add integration routes |
| Modify | `backend/internal/api/machines.go` — add cleanup on delete, auto-enable composio plugin |
| Modify | `backend/internal/api/agent_auth.go` — add COMPOSIO_CONSUMER_KEY to platform secrets |
| Modify | `backend/cmd/server/main.go` — initialize composio.Client |
| Modify | `frontend/src/lib/api.ts` — add integration API functions |
| Modify | `frontend/src/pages/machine-tabs/IntegrationsTab.tsx` — replace placeholder |
| Modify | `rootfs/Dockerfile.openclaw` — add composio plugin installation |
| Modify | `scripts/deploy-cloud-run.sh` — add COMPOSIO_CONSUMER_KEY env var |
| Modify | `~/ocm-openclaw-composio-plugin/index.ts` — add userId support, env var resolution |
| Modify | `~/ocm-openclaw-composio-plugin/src/config.ts` — add userId to schema |
| Modify | `~/ocm-openclaw-composio-plugin/src/types.ts` — add userId to interface |
| Modify | `~/ocm-openclaw-composio-plugin/openclaw.plugin.json` — add userId to config schema |
| Modify | `~/ocm-openclaw-composio-plugin/package.json` — update name to @mathaix scope |
