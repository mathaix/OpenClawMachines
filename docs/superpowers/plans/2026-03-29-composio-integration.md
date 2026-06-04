# Composio Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable OCM machines to connect to third-party apps (Gmail, Slack, GitHub, etc.) via Composio with per-machine isolation, a backend proxy layer, and dashboard UI.

**Architecture:** Go backend wraps Composio REST API for connect links, connection status, and disconnect. Consumer key delivered to VMs via ocm-secrets (not plain text config). Forked plugin adds userId support for per-machine MCP scoping. Frontend IntegrationsTab shows a category-grouped grid with connect/disconnect buttons.

**Tech Stack:** Go (backend), TypeScript (frontend React), PostgreSQL (Neon), Composio REST API v3, MCP protocol

**Spec:** `docs/superpowers/specs/2026-03-29-composio-integration-design.md`

---

## File Structure

### New Files

| File | Responsibility |
|------|---------------|
| `backend/internal/composio/client.go` | HTTP client wrapping Composio REST API (list connections, create connect link, delete connection) |
| `backend/internal/composio/client_test.go` | Unit tests with mock HTTP server |
| `backend/internal/api/integrations.go` | API handlers: list integrations, create connect link, delete integration, callback page, admin CRUD |
| `backend/internal/api/integrations_test.go` | Handler tests with mock composio client |
| `backend/migrations/054_composio_integration.sql` | integration_catalog + integration_events tables, composio plugin catalog seed |

### Modified Files

| File | Change |
|------|--------|
| `backend/internal/api/server.go` | Add composio.Client field to Server, register integration routes |
| `backend/internal/api/machines.go:103` | Auto-enable composio plugin on machine creation |
| `backend/internal/api/agent_auth.go:433` | Add COMPOSIO_CONSUMER_KEY to platform secrets |
| `backend/internal/configassembly/assembler.go:329,582,878` | Add ComposioConsumerKey to AssemblyParams, inject composio config in both AssembleConfig and AssembleSeedConfig |
| `backend/internal/configassembly/assembler_test.go` | Add Composio injection test |
| `backend/cmd/server/main.go:281` | Read COMPOSIO_CONSUMER_KEY env var, pass to Server |
| `backend/internal/store/store.go:763` | Add IntegrationCatalogRepo and IntegrationEventRepo interfaces |
| `backend/internal/store/postgres.go` | Implement integration catalog and events store methods |
| `frontend/src/lib/api.ts:459` | Add integration API functions |
| `frontend/src/pages/machine-tabs/IntegrationsTab.tsx` | Replace placeholder with real integration management UI |
| `rootfs/Dockerfile.openclaw:191` | Add composio plugin installation |
| `scripts/deploy-cloud-run.sh:153` | Add COMPOSIO_CONSUMER_KEY secret mapping |

### Plugin Fork (separate repo)

| File | Change |
|------|--------|
| `~/ocm-openclaw-composio-plugin/src/types.ts` | Add userId to ComposioConfig |
| `~/ocm-openclaw-composio-plugin/src/config.ts` | Add userId parsing, env var resolution for consumerKey |
| `~/ocm-openclaw-composio-plugin/index.ts` | Pass userId on MCP calls |
| `~/ocm-openclaw-composio-plugin/openclaw.plugin.json` | Add userId to config schema |
| `~/ocm-openclaw-composio-plugin/package.json` | Update name to @mathaix scope |

---

## Task 1: Plugin Fork — Add userId Support

**Files:**
- Modify: `~/ocm-openclaw-composio-plugin/src/types.ts`
- Modify: `~/ocm-openclaw-composio-plugin/src/config.ts`
- Modify: `~/ocm-openclaw-composio-plugin/index.ts`
- Modify: `~/ocm-openclaw-composio-plugin/openclaw.plugin.json`
- Modify: `~/ocm-openclaw-composio-plugin/package.json`

- [ ] **Step 1: Update the types**

In `~/ocm-openclaw-composio-plugin/src/types.ts`:
```typescript
export interface ComposioConfig {
  enabled: boolean;
  consumerKey: string;
  mcpUrl: string;
  userId: string;
}
```

- [ ] **Step 2: Update config parsing with userId and env var resolution**

In `~/ocm-openclaw-composio-plugin/src/config.ts`, update the schema and parser:

```typescript
export const ComposioConfigSchema = z.object({
  enabled: z.boolean().default(true),
  consumerKey: z.string().default(""),
  mcpUrl: z.string().default("https://connect.composio.dev/mcp"),
  userId: z.string().default("default"),
});

export function parseComposioConfig(value: unknown): ComposioConfig {
  const raw =
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};

  const configObj = raw.config as Record<string, unknown> | undefined;

  let consumerKey =
    (typeof configObj?.consumerKey === "string" && configObj.consumerKey.trim()) ||
    (typeof raw.consumerKey === "string" && raw.consumerKey.trim()) ||
    process.env.COMPOSIO_CONSUMER_KEY ||
    "";

  // If consumerKey looks like an env var name (ALL_CAPS_UNDERSCORE), resolve it
  if (consumerKey && /^[A-Z_][A-Z0-9_]*$/.test(consumerKey)) {
    consumerKey = process.env[consumerKey] || "";
  }

  const mcpUrl =
    (typeof configObj?.mcpUrl === "string" && configObj.mcpUrl.trim()) ||
    (typeof raw.mcpUrl === "string" && raw.mcpUrl.trim()) ||
    "https://connect.composio.dev/mcp";

  const userId =
    (typeof configObj?.userId === "string" && configObj.userId.trim()) ||
    (typeof raw.userId === "string" && raw.userId.trim()) ||
    "default";

  return ComposioConfigSchema.parse({ ...raw, consumerKey, mcpUrl, userId });
}
```

- [ ] **Step 3: Pass userId on MCP calls**

In `~/ocm-openclaw-composio-plugin/index.ts`, update `fetchToolsSync` to append `userId` as a query param:

```typescript
function fetchToolsSync(mcpUrl: string, consumerKey: string, userId: string) {
  const url = new URL(mcpUrl);
  if (userId && userId !== "default") {
    url.searchParams.set("user_id", userId);
  }
  const body = JSON.stringify({ jsonrpc: "2.0", id: "1", method: "tools/list" });
  const raw = execFileSync("curl", [
    url.toString(), "-s", "-X", "POST",
    "-H", "Content-Type: application/json",
    "-H", "Accept: application/json, text/event-stream",
    "-H", `x-consumer-api-key: ${consumerKey}`,
    "-d", body,
  ], { encoding: "utf-8", timeout: 15_000 });
  // ... rest unchanged
```

Update the async MCP client in the `register` function to also append `userId`:

```typescript
const mcpReady = (async () => {
  const { Client } = await import("@modelcontextprotocol/sdk/client/index.js");
  const { StreamableHTTPClientTransport } = await import(
    "@modelcontextprotocol/sdk/client/streamableHttp.js"
  );
  const client = new Client({ name: "openclaw", version: "1.0" });
  const mcpUrlWithUser = new URL(config.mcpUrl);
  if (config.userId && config.userId !== "default") {
    mcpUrlWithUser.searchParams.set("user_id", config.userId);
  }
  await client.connect(
    new StreamableHTTPClientTransport(mcpUrlWithUser, {
      requestInit: {
        headers: { "x-consumer-api-key": config.consumerKey },
      },
    })
  );
  mcpClient = client;
  api.logger.info("[composio] MCP client connected");
})().catch((err) => {
  api.logger.error(`[composio] MCP client connection failed: ${err instanceof Error ? err.message : String(err)}`);
});
```

And update the call site in the `try` block:

```typescript
const tools = fetchToolsSync(config.mcpUrl, config.consumerKey, config.userId);
```

- [ ] **Step 4: Update plugin manifest**

In `~/ocm-openclaw-composio-plugin/openclaw.plugin.json`, add userId to configSchema.properties:

```json
"userId": {
  "type": "string",
  "description": "Machine-specific user ID for per-machine connection isolation (injected by OCM, do not set manually)"
}
```

- [ ] **Step 5: Update package.json**

In `~/ocm-openclaw-composio-plugin/package.json`, change name:

```json
"name": "@mathaix/ocm-openclaw-composio-plugin",
```

And update repository URL:

```json
"repository": {
  "type": "git",
  "url": "https://github.com/mathaix/ocm-openclaw-composio-plugin.git"
}
```

- [ ] **Step 6: Commit**

```bash
cd ~/ocm-openclaw-composio-plugin
git add -A
git commit -m "feat: add userId support for per-machine Composio isolation

- Add userId to config schema, parsed from plugin config
- Append user_id query param to MCP URL for both sync and async clients
- Resolve env var names as consumerKey values (for ocm-secrets pattern)
- Update package name to @mathaix scope

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
git push
```

---

## Task 2: Database Migration — Integration Tables + Plugin Catalog Seed

**Files:**
- Create: `backend/migrations/054_composio_integration.sql`

- [ ] **Step 1: Write the migration**

Create `backend/migrations/054_composio_integration.sql`:

```sql
-- Composio integration: catalog of curated integrations, event tracking, and plugin catalog entry.

-- 1. Integration catalog: admin-managed list of integrations shown in dashboard
CREATE TABLE IF NOT EXISTS integration_catalog (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    icon           TEXT NOT NULL,
    toolkit        TEXT NOT NULL,
    auth_config_id TEXT,
    category       TEXT NOT NULL DEFAULT 'other',
    sort_order     INT NOT NULL DEFAULT 0,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. Integration events: audit trail for connect/disconnect actions
CREATE TABLE IF NOT EXISTS integration_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     INTEGER NOT NULL REFERENCES accounts(id),
    machine_id     UUID REFERENCES machines(id) ON DELETE SET NULL,
    integration_id TEXT NOT NULL,
    event          TEXT NOT NULL,
    metadata       JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_integration_events_machine ON integration_events(machine_id, created_at);
CREATE INDEX IF NOT EXISTS idx_integration_events_integration ON integration_events(integration_id, created_at);

-- 3. Seed integration catalog
INSERT INTO integration_catalog (id, name, icon, toolkit, category, sort_order) VALUES
    ('gmail',           'Gmail',           'gmail',           'gmail',           'google',       1),
    ('google-calendar', 'Google Calendar', 'google-calendar', 'googlecalendar',  'google',       2),
    ('google-drive',    'Google Drive',    'google-drive',    'googledrive',     'google',       3),
    ('google-sheets',   'Google Sheets',   'google-sheets',   'googlesheets',    'google',       4),
    ('google-docs',     'Google Docs',     'google-docs',     'googledocs',      'google',       5),
    ('youtube',         'YouTube',         'youtube',         'youtube',         'google',       6),
    ('notion',          'Notion',          'notion',          'notion',          'productivity', 10),
    ('slack',           'Slack',           'slack',           'slack',           'productivity', 11),
    ('github',          'GitHub',          'github',          'github',          'dev',          20),
    ('jira',            'Jira',            'jira',            'jira',            'dev',          21),
    ('linkedin',        'LinkedIn',        'linkedin',        'linkedin',        'social',       30),
    ('x',              'X',               'x',               'twitter',         'social',       31),
    ('hubspot',         'HubSpot',         'hubspot',         'hubspot',         'crm',          40)
ON CONFLICT (id) DO NOTHING;

-- 4. Composio plugin catalog entry
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

- [ ] **Step 2: Run the migration**

```bash
make migrate
```

Expected: migration applies without errors.

- [ ] **Step 3: Verify**

```bash
make migrate-status
```

Expected: 054_composio_integration.sql shows as applied.

- [ ] **Step 4: Commit**

```bash
cd /home/mantiz/OpenClawMachines
git add backend/migrations/054_composio_integration.sql
git commit -m "feat: add integration_catalog, integration_events tables and composio plugin seed

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 3: Backend — Composio REST API Client

**Files:**
- Create: `backend/internal/composio/client.go`
- Create: `backend/internal/composio/client_test.go`

- [ ] **Step 1: Write failing tests**

Create `backend/internal/composio/client_test.go`:

```go
package composio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListConnections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/connected_accounts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("user_id") != "machine-123" {
			t.Errorf("missing or wrong user_id query param: %s", r.URL.Query().Get("user_id"))
		}
		if r.Header.Get("x-api-key") != "ck_test" {
			t.Errorf("missing or wrong x-api-key header: %s", r.Header.Get("x-api-key"))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{
				{
					"id":         "ca_abc",
					"status":     "ACTIVE",
					"created_at": "2026-03-28T12:00:00Z",
					"toolkit":    map[string]interface{}{"slug": "gmail"},
				},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	conns, err := c.ListConnections(context.Background(), "machine-123")
	if err != nil {
		t.Fatalf("ListConnections error: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("got %d connections, want 1", len(conns))
	}
	if conns[0].ID != "ca_abc" {
		t.Errorf("ID = %q, want ca_abc", conns[0].ID)
	}
	if conns[0].Toolkit != "gmail" {
		t.Errorf("Toolkit = %q, want gmail", conns[0].Toolkit)
	}
	if conns[0].Status != "active" {
		t.Errorf("Status = %q, want active", conns[0].Status)
	}
}

func TestListConnectionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"items": []interface{}{}})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	conns, err := c.ListConnections(context.Background(), "machine-123")
	if err != nil {
		t.Fatalf("ListConnections error: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("got %d connections, want 0", len(conns))
	}
}

func TestListConnectionsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_bad")
	_, err := c.ListConnections(context.Background(), "machine-123")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestCreateConnectLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v3/connected_accounts/link" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["user_id"] != "machine-456" {
			t.Errorf("user_id = %v, want machine-456", body["user_id"])
		}
		if body["auth_config_id"] != "ac_gmail" {
			t.Errorf("auth_config_id = %v, want ac_gmail", body["auth_config_id"])
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"redirect_url": "https://accounts.google.com/o/oauth2/auth?...",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	resp, err := c.CreateConnectLink(context.Background(), "machine-456", "ac_gmail", "https://example.com/callback")
	if err != nil {
		t.Fatalf("CreateConnectLink error: %v", err)
	}
	if resp.URL == "" {
		t.Error("redirect URL is empty")
	}
}

func TestDeleteConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/v3/connected_accounts/ca_xyz" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	err := c.DeleteConnection(context.Background(), "ca_xyz")
	if err != nil {
		t.Fatalf("DeleteConnection error: %v", err)
	}
}

func TestDeleteAllConnections(t *testing.T) {
	listCalled := false
	deleteCalled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/connected_accounts":
			listCalled = true
			json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{
					{"id": "ca_1", "status": "ACTIVE", "toolkit": map[string]interface{}{"slug": "gmail"}},
					{"id": "ca_2", "status": "ACTIVE", "toolkit": map[string]interface{}{"slug": "slack"}},
				},
			})
		case r.Method == http.MethodDelete:
			deleteCalled++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ck_test")
	err := c.DeleteAllConnections(context.Background(), "machine-789")
	if err != nil {
		t.Fatalf("DeleteAllConnections error: %v", err)
	}
	if !listCalled {
		t.Error("ListConnections was not called")
	}
	if deleteCalled != 2 {
		t.Errorf("DeleteConnection called %d times, want 2", deleteCalled)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /home/mantiz/OpenClawMachines && go test ./backend/internal/composio/... -v -count=1
```

Expected: compilation errors (package doesn't exist yet).

- [ ] **Step 3: Implement the client**

Create `backend/internal/composio/client.go`:

```go
package composio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps the Composio REST API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Connection represents a Composio connected account (normalized from API response).
type Connection struct {
	ID        string `json:"id"`
	Toolkit   string `json:"toolkit"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ConnectLinkResponse is the response from creating a connect link.
type ConnectLinkResponse struct {
	URL string `json:"url"`
}

// NewClient creates a new Composio API client.
// baseURL is typically "https://backend.composio.dev" for production.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Enabled returns true if the client has an API key configured.
func (c *Client) Enabled() bool {
	return c != nil && c.apiKey != ""
}

// ListConnections returns all connected accounts for a machine.
func (c *Client) ListConnections(ctx context.Context, machineID string) ([]Connection, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v3/connected_accounts?user_id="+machineID, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: list connections: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.apiError("list connections", resp)
	}

	var result struct {
		Items []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
			Toolkit   struct {
				Slug string `json:"slug"`
			} `json:"toolkit"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode list response: %w", err)
	}

	conns := make([]Connection, 0, len(result.Items))
	for _, item := range result.Items {
		conns = append(conns, Connection{
			ID:        item.ID,
			Toolkit:   item.Toolkit.Slug,
			Status:    strings.ToLower(item.Status),
			CreatedAt: item.CreatedAt,
		})
	}
	return conns, nil
}

// CreateConnectLink generates an OAuth connect URL for a specific integration.
func (c *Client) CreateConnectLink(ctx context.Context, machineID, authConfigID, callbackURL string) (*ConnectLinkResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"user_id":        machineID,
		"auth_config_id": authConfigID,
		"callback_url":   callbackURL,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v3/connected_accounts/link",
		strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("composio: create connect link: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.apiError("create connect link", resp)
	}

	var result struct {
		RedirectURL string `json:"redirect_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("composio: decode connect link response: %w", err)
	}

	return &ConnectLinkResponse{URL: result.RedirectURL}, nil
}

// DeleteConnection removes a single connected account by ID.
func (c *Client) DeleteConnection(ctx context.Context, connectionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/api/v3/connected_accounts/"+connectionID, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("composio: delete connection: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.apiError("delete connection", resp)
	}
	return nil
}

// DeleteAllConnections removes all connected accounts for a machine (used on machine delete).
func (c *Client) DeleteAllConnections(ctx context.Context, machineID string) error {
	conns, err := c.ListConnections(ctx, machineID)
	if err != nil {
		return fmt.Errorf("composio: list for cleanup: %w", err)
	}
	for _, conn := range conns {
		if delErr := c.DeleteConnection(ctx, conn.ID); delErr != nil {
			return fmt.Errorf("composio: delete %s: %w", conn.ID, delErr)
		}
	}
	return nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
}

func (c *Client) apiError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("composio: %s: HTTP %d: %s", op, resp.StatusCode, string(body))
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /home/mantiz/OpenClawMachines && go test ./backend/internal/composio/... -v -count=1
```

Expected: all 6 tests pass.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/composio/
git commit -m "feat: add Composio REST API client with tests

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 4: Backend — Store Layer (Integration Catalog + Events)

**Files:**
- Modify: `backend/internal/store/store.go`
- Modify: `backend/internal/store/postgres.go`

- [ ] **Step 1: Add types and interfaces to store.go**

In `backend/internal/store/store.go`, add after the existing plugin types (around line 760):

```go
// ---- Integration types ----

type IntegrationCatalogEntry struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Icon         string    `json:"icon"`
	Toolkit      string    `json:"toolkit"`
	AuthConfigID *string   `json:"auth_config_id,omitempty"`
	Category     string    `json:"category"`
	SortOrder    int       `json:"sort_order"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type IntegrationEvent struct {
	ID            string          `json:"id"`
	AccountID     int             `json:"account_id"`
	MachineID     *string         `json:"machine_id,omitempty"`
	IntegrationID string          `json:"integration_id"`
	Event         string          `json:"event"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}
```

Add new repo interfaces (near the PluginCatalogRepo interface):

```go
type IntegrationCatalogRepo interface {
	ListIntegrationCatalog(ctx context.Context) ([]IntegrationCatalogEntry, error)
	ListConfiguredIntegrations(ctx context.Context) ([]IntegrationCatalogEntry, error) // enabled + auth_config_id IS NOT NULL
	GetIntegrationCatalogEntry(ctx context.Context, id string) (*IntegrationCatalogEntry, error)
	CreateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error
	UpdateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error
	DeleteIntegrationCatalogEntry(ctx context.Context, id string) error
}

type IntegrationEventRepo interface {
	LogIntegrationEvent(ctx context.Context, accountID int, machineID, integrationID, event string, metadata json.RawMessage) error
	HasIntegrationEvent(ctx context.Context, machineID, integrationID, event string) (bool, error)
}
```

Embed both in the Store interface (around line 918):

```go
IntegrationCatalogRepo
IntegrationEventRepo
```

- [ ] **Step 2: Implement in postgres.go**

Add at the end of `backend/internal/store/postgres.go`:

```go
// ---- Integration Catalog ----

func (s *PostgresStore) ListIntegrationCatalog(ctx context.Context) ([]IntegrationCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []IntegrationCatalogEntry
	for rows.Next() {
		var e IntegrationCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) ListConfiguredIntegrations(ctx context.Context) ([]IntegrationCatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog WHERE enabled = true AND auth_config_id IS NOT NULL
		 ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []IntegrationCatalogEntry
	for rows.Next() {
		var e IntegrationCatalogEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) GetIntegrationCatalogEntry(ctx context.Context, id string) (*IntegrationCatalogEntry, error) {
	var e IntegrationCatalogEntry
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, icon, toolkit, auth_config_id, category, sort_order, enabled, created_at, updated_at
		 FROM integration_catalog WHERE id = $1`, id).
		Scan(&e.ID, &e.Name, &e.Icon, &e.Toolkit, &e.AuthConfigID,
			&e.Category, &e.SortOrder, &e.Enabled, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *PostgresStore) CreateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO integration_catalog (id, name, icon, toolkit, auth_config_id, category, sort_order, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.ID, e.Name, e.Icon, e.Toolkit, e.AuthConfigID, e.Category, e.SortOrder, e.Enabled)
	return err
}

func (s *PostgresStore) UpdateIntegrationCatalogEntry(ctx context.Context, e IntegrationCatalogEntry) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE integration_catalog SET name=$2, icon=$3, toolkit=$4, auth_config_id=$5,
		 category=$6, sort_order=$7, enabled=$8, updated_at=NOW() WHERE id=$1`,
		e.ID, e.Name, e.Icon, e.Toolkit, e.AuthConfigID, e.Category, e.SortOrder, e.Enabled)
	return err
}

func (s *PostgresStore) DeleteIntegrationCatalogEntry(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM integration_catalog WHERE id = $1`, id)
	return err
}

// ---- Integration Events ----

func (s *PostgresStore) LogIntegrationEvent(ctx context.Context, accountID int, machineID, integrationID, event string, metadata json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO integration_events (account_id, machine_id, integration_id, event, metadata)
		 VALUES ($1, $2, $3, $4, $5)`,
		accountID, machineID, integrationID, event, metadata)
	return err
}

func (s *PostgresStore) HasIntegrationEvent(ctx context.Context, machineID, integrationID, event string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM integration_events WHERE machine_id=$1 AND integration_id=$2 AND event=$3)`,
		machineID, integrationID, event).Scan(&exists)
	return exists, err
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd /home/mantiz/OpenClawMachines && go build ./backend/...
```

Expected: compiles without errors.

- [ ] **Step 4: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat: add integration catalog and events store layer

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 5: Backend — API Handlers (Integrations)

**Files:**
- Create: `backend/internal/api/integrations.go`
- Modify: `backend/internal/api/server.go`

- [ ] **Step 1: Add composio client to Server struct**

In `backend/internal/api/server.go`, add to the Server struct (around line 40):

```go
composioClient *composio.Client
```

Add import:
```go
"github.com/mathaix/openclawmachines/backend/internal/composio"
```

- [ ] **Step 2: Register routes**

In `backend/internal/api/server.go`, inside the machine routes block (after the existing plugins routes around line 472), add:

```go
// Integrations (Composio)
r.Get("/integrations", srv.handleListIntegrations)
r.Post("/integrations/{integration}/connect", srv.handleCreateConnectLink)
r.Delete("/integrations/{connId}", srv.handleDeleteIntegration)
```

In the admin routes block (around line 490), add:

```go
// Integration catalog
r.Get("/integrations", srv.handleListIntegrationCatalog)
r.Post("/integrations", srv.handleCreateIntegrationCatalogEntry)
r.Put("/integrations/{integrationId}", srv.handleUpdateIntegrationCatalogEntry)
r.Delete("/integrations/{integrationId}", srv.handleDeleteIntegrationCatalogEntry)
```

Add a public callback route (around line 314, in the public routes section):

```go
r.Get("/api/integrations/callback", srv.handleIntegrationCallback)
```

- [ ] **Step 3: Implement handlers**

Create `backend/internal/api/integrations.go`:

```go
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mathaix/openclawmachines/backend/internal/store"
)

// ---- User-facing Integration Handlers ----

type integrationResponse struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Icon               string  `json:"icon"`
	Category           string  `json:"category"`
	Connected          bool    `json:"connected"`
	ConnectedAccountID *string `json:"connected_account_id,omitempty"`
	ConnectedAt        *string `json:"connected_at,omitempty"`
}

func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	catalog, err := s.store.ListConfiguredIntegrations(r.Context())
	if err != nil {
		slog.Error("integrations.list_catalog_failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list integrations")
		return
	}

	// Build response from catalog
	result := make([]integrationResponse, 0, len(catalog))
	for _, entry := range catalog {
		result = append(result, integrationResponse{
			ID:       entry.ID,
			Name:     entry.Name,
			Icon:     entry.Icon,
			Category: entry.Category,
		})
	}

	// If composio client is configured, fetch live connection status
	if s.composioClient != nil && s.composioClient.Enabled() {
		conns, err := s.composioClient.ListConnections(r.Context(), machineID)
		if err != nil {
			slog.Warn("integrations.composio_list_failed", "machine_id", machineID, "error", err)
			// Return catalog without connection status rather than failing
		} else {
			// Build toolkit -> most recent active connection map
			connByToolkit := map[string]struct {
				id        string
				createdAt string
			}{}
			for _, conn := range conns {
				if conn.Status != "active" {
					continue
				}
				existing, ok := connByToolkit[conn.Toolkit]
				if !ok || conn.CreatedAt > existing.createdAt {
					connByToolkit[conn.Toolkit] = struct {
						id        string
						createdAt string
					}{conn.ID, conn.CreatedAt}
				}
			}

			// Match connections to catalog entries
			for i := range result {
				entry := catalog[i]
				if conn, ok := connByToolkit[entry.Toolkit]; ok {
					result[i].Connected = true
					result[i].ConnectedAccountID = &conn.id
					result[i].ConnectedAt = &conn.createdAt

					// Log connect_completed if not already logged
					hasEvent, _ := s.store.HasIntegrationEvent(r.Context(), machineID, entry.ID, "connect_completed")
					if !hasEvent {
						meta, _ := json.Marshal(map[string]string{"connected_account_id": conn.id})
						_ = s.store.LogIntegrationEvent(r.Context(), accountID, machineID, entry.ID, "connect_completed", meta)
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCreateConnectLink(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	integrationID := chi.URLParam(r, "integration")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "Composio integration not configured")
		return
	}

	entry, err := s.store.GetIntegrationCatalogEntry(r.Context(), integrationID)
	if err != nil {
		writeError(w, http.StatusNotFound, "integration not found")
		return
	}
	if entry.AuthConfigID == nil || *entry.AuthConfigID == "" {
		writeError(w, http.StatusBadRequest, "integration not yet configured (missing auth_config_id)")
		return
	}

	// Log connect_started event
	meta, _ := json.Marshal(map[string]string{"machine_slug": machine.Slug})
	_ = s.store.LogIntegrationEvent(r.Context(), accountID, machineID, integrationID, "connect_started", meta)

	callbackURL := s.publicURL() + "/api/integrations/callback"
	resp, err := s.composioClient.CreateConnectLink(r.Context(), machineID, *entry.AuthConfigID, callbackURL)
	if err != nil {
		slog.Error("integrations.create_connect_link_failed", "machine_id", machineID, "integration", integrationID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to create connect link")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"url": resp.URL})
}

func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	machineID := chi.URLParam(r, "id")
	connID := chi.URLParam(r, "connId")
	accountID := accountIDFromContext(r.Context())

	machine, err := s.store.GetMachine(r.Context(), machineID)
	if err != nil || machine.AccountID != accountID {
		writeError(w, http.StatusNotFound, "machine not found")
		return
	}

	if s.composioClient == nil || !s.composioClient.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "Composio integration not configured")
		return
	}

	// Ownership check: verify connID belongs to this machine
	conns, err := s.composioClient.ListConnections(r.Context(), machineID)
	if err != nil {
		slog.Error("integrations.verify_ownership_failed", "machine_id", machineID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to verify connection ownership")
		return
	}
	found := false
	toolkit := ""
	for _, conn := range conns {
		if conn.ID == connID {
			found = true
			toolkit = conn.Toolkit
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "connection not found for this machine")
		return
	}

	// Log disconnected event — find integration ID from toolkit name
	integrationID := ""
	catalog, _ := s.store.ListConfiguredIntegrations(r.Context())
	for _, entry := range catalog {
		if entry.Toolkit == toolkit {
			integrationID = entry.ID
			break
		}
	}
	meta, _ := json.Marshal(map[string]string{"connected_account_id": connID, "toolkit": toolkit, "machine_slug": machine.Slug})
	_ = s.store.LogIntegrationEvent(r.Context(), accountID, machineID, integrationID, "disconnected", meta)

	if err := s.composioClient.DeleteConnection(r.Context(), connID); err != nil {
		slog.Error("integrations.delete_failed", "conn_id", connID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to disconnect")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIntegrationCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Connected</title></head>
<body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#eee">
<div style="text-align:center">
<h2>Connection successful</h2>
<p>You can close this window.</p>
</div>
<script>
try { window.opener.postMessage({type:"composio-connected"}, "*"); } catch(e) {}
setTimeout(function(){ window.close(); }, 1500);
</script>
</body></html>`)
}

// publicURL returns the backend's public URL (e.g. https://ocm-backend-xxx.run.app).
func (s *Server) publicURL() string {
	if s.frontendBaseURL != "" {
		return s.frontendBaseURL
	}
	return "http://localhost:8080"
}

// ---- Admin Integration Catalog Handlers ----

func (s *Server) handleListIntegrationCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.ListIntegrationCatalog(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list integration catalog")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleCreateIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	var entry store.IntegrationCatalogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if entry.ID == "" || entry.Name == "" || entry.Icon == "" || entry.Toolkit == "" {
		writeError(w, http.StatusBadRequest, "id, name, icon, and toolkit are required")
		return
	}
	if err := s.store.CreateIntegrationCatalogEntry(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create integration")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleUpdateIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "integrationId")
	var entry store.IntegrationCatalogEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	entry.ID = id
	if err := s.store.UpdateIntegrationCatalogEntry(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update integration")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleDeleteIntegrationCatalogEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "integrationId")
	if err := s.store.DeleteIntegrationCatalogEntry(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete integration")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Verify compilation**

```bash
cd /home/mantiz/OpenClawMachines && go build ./backend/...
```

Expected: compiles. There may be issues with mock stores in tests not implementing new interfaces — fix those by adding stub methods.

- [ ] **Step 5: Add stub methods to any mock stores in test files**

Any test file with a mock store will need empty implementations of the new `IntegrationCatalogRepo` and `IntegrationEventRepo` methods. Search for all mock stores:

```bash
grep -rn "type mock.*Store" backend/internal/api/*_test.go backend/internal/configassembly/*_test.go
```

For each mock, add stubs like:

```go
func (m *mockStore) ListIntegrationCatalog(_ context.Context) ([]store.IntegrationCatalogEntry, error) { return nil, nil }
func (m *mockStore) ListConfiguredIntegrations(_ context.Context) ([]store.IntegrationCatalogEntry, error) { return nil, nil }
func (m *mockStore) GetIntegrationCatalogEntry(_ context.Context, _ string) (*store.IntegrationCatalogEntry, error) { return nil, nil }
func (m *mockStore) CreateIntegrationCatalogEntry(_ context.Context, _ store.IntegrationCatalogEntry) error { return nil }
func (m *mockStore) UpdateIntegrationCatalogEntry(_ context.Context, _ store.IntegrationCatalogEntry) error { return nil }
func (m *mockStore) DeleteIntegrationCatalogEntry(_ context.Context, _ string) error { return nil }
func (m *mockStore) LogIntegrationEvent(_ context.Context, _ int, _, _, _ string, _ json.RawMessage) error { return nil }
func (m *mockStore) HasIntegrationEvent(_ context.Context, _, _, _ string) (bool, error) { return false, nil }
```

- [ ] **Step 6: Run tests**

```bash
cd /home/mantiz/OpenClawMachines && make test-go
```

Expected: all existing tests pass (new handlers don't have tests yet).

- [ ] **Step 7: Commit**

```bash
git add backend/internal/api/integrations.go backend/internal/api/server.go backend/internal/api/*_test.go
git commit -m "feat: add Composio integration API handlers and routes

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 6: Backend — Config Assembly + Secret Delivery

**Files:**
- Modify: `backend/internal/configassembly/assembler.go`
- Modify: `backend/internal/api/agent_auth.go`
- Modify: `backend/cmd/server/main.go`
- Modify: `backend/internal/api/machines.go`

- [ ] **Step 1: Add ComposioConsumerKey to AssemblyParams**

In `backend/internal/configassembly/assembler.go`, add to the `AssemblyParams` struct (around line 346):

```go
ComposioConsumerKey string // platform consumer key — for metadata server only, NOT injected as plain text
```

- [ ] **Step 2: Add Composio injection in AssembleConfig**

In `backend/internal/configassembly/assembler.go`, after the Opik injection block (around line 608), add:

```go
// 5e-composio. Inject Composio config: env var ref for consumerKey, mcpUrl, userId.
// The actual consumer key is delivered via metadata server secrets, not config.
if params.ComposioConsumerKey != "" {
	if plugins, ok := result["plugins"].(map[string]interface{}); ok {
		if entries, ok := plugins["entries"].(map[string]interface{}); ok {
			if composioEntry, ok := entries["composio"].(map[string]interface{}); ok {
				config := getOrCreateMap(composioEntry, "config")
				if _, ok := config["enabled"]; !ok {
					config["enabled"] = true
				}
				config["mcpUrl"] = "https://connect.composio.dev/mcp"
				config["consumerKey"] = "COMPOSIO_CONSUMER_KEY" // env var name, resolved by ocm-secrets
				config["userId"] = params.MachineID
			}
		}
		installs := getOrCreateMap(plugins, "installs")
		installs["composio"] = map[string]interface{}{
			"source":      "archive",
			"installPath": "/home/openclaw/.openclaw/extensions/composio",
		}
	}
}
```

- [ ] **Step 3: Add same injection in AssembleSeedConfig**

In the `AssembleSeedConfig` function (after the Opik seed injection block, around line 899), add the same block:

```go
// Inject Composio config into seed config.
if params.ComposioConsumerKey != "" {
	if plugins, ok := result["plugins"].(map[string]interface{}); ok {
		if entries, ok := plugins["entries"].(map[string]interface{}); ok {
			if composioEntry, ok := entries["composio"].(map[string]interface{}); ok {
				config := getOrCreateMap(composioEntry, "config")
				if _, ok := config["enabled"]; !ok {
					config["enabled"] = true
				}
				config["mcpUrl"] = "https://connect.composio.dev/mcp"
				config["consumerKey"] = "COMPOSIO_CONSUMER_KEY"
				config["userId"] = params.MachineID
			}
		}
		installs := getOrCreateMap(plugins, "installs")
		installs["composio"] = map[string]interface{}{
			"source":      "archive",
			"installPath": "/home/openclaw/.openclaw/extensions/composio",
		}
	}
}
```

- [ ] **Step 4: Add COMPOSIO_CONSUMER_KEY to platform secrets**

In `backend/internal/api/agent_auth.go`, after the OPIK_API_KEY block (around line 437), add:

```go
// Include COMPOSIO_CONSUMER_KEY for Composio plugin auth
if s.composioConsumerKey != "" {
	secrets["COMPOSIO_CONSUMER_KEY"] = s.composioConsumerKey
}
```

Add the field to the Server struct in `server.go`:

```go
composioConsumerKey string
```

- [ ] **Step 5: Auto-enable composio plugin on machine creation**

In `backend/internal/api/machines.go`, after the opik-openclaw EnableMachinePlugin call (around line 103), add:

```go
if err := s.store.EnableMachinePlugin(r.Context(), machine.ID, "composio", nil); err != nil {
	slog.Warn("create_machine.enable_composio_failed", "machine_id", machine.ID, "error", err)
}
```

- [ ] **Step 6: Add Composio cleanup on machine delete**

In `backend/internal/api/machines.go`, in `handleDeleteMachine` (around line 312), add before the `s.machines.Delete()` call:

```go
// Best-effort cleanup of Composio connections
if s.composioClient != nil && s.composioClient.Enabled() {
	if err := s.composioClient.DeleteAllConnections(r.Context(), machineID); err != nil {
		slog.Warn("delete_machine.composio_cleanup_failed", "machine_id", machineID, "error", err)
	}
}
```

- [ ] **Step 7: Wire up in main.go**

In `backend/cmd/server/main.go`, read the env var and pass to the server (around line 281 where server is created). Add after server creation:

```go
if key := os.Getenv("COMPOSIO_CONSUMER_KEY"); key != "" {
	srv.SetComposioClient(composio.NewClient("https://backend.composio.dev", key))
	srv.SetComposioConsumerKey(key)
	slog.Info("composio.configured")
}
```

Add setter methods to `server.go`:

```go
func (s *Server) SetComposioClient(c *composio.Client) {
	s.composioClient = c
}

func (s *Server) SetComposioConsumerKey(key string) {
	s.composioConsumerKey = key
}
```

Also pass `ComposioConsumerKey` in the config assembly params — find where `AssemblyParams` is constructed in `machine_config.go` and add:

```go
ComposioConsumerKey: s.composioConsumerKey,
```

- [ ] **Step 8: Verify compilation and tests**

```bash
cd /home/mantiz/OpenClawMachines && go build ./backend/... && make test-go
```

Expected: compiles and all tests pass.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/api/agent_auth.go backend/internal/api/server.go backend/internal/api/machines.go backend/cmd/server/main.go backend/internal/api/machine_config.go
git commit -m "feat: wire Composio config assembly, secret delivery, and auto-enable

- Inject composio plugin config with env var ref (not plain text key)
- Add COMPOSIO_CONSUMER_KEY to metadata server platform secrets
- Auto-enable composio plugin on machine creation
- Cleanup Composio connections on machine delete

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 7: Backend — Config Assembly Test

**Files:**
- Modify: `backend/internal/configassembly/assembler_test.go`

- [ ] **Step 1: Write the test**

Add to `backend/internal/configassembly/assembler_test.go`:

```go
func TestAssembleConfig_ComposioInjection(t *testing.T) {
	params := AssemblyParams{
		MachineID: "machine-uuid-123",
		Plugins: []PluginSelection{
			{
				PluginID: "composio",
				Slot:     "integrations",
				ConfigTemplate: map[string]interface{}{
					"plugins": map[string]interface{}{
						"allow": []interface{}{"composio"},
						"entries": map[string]interface{}{
							"composio": map[string]interface{}{
								"enabled": true,
								"config":  map[string]interface{}{},
							},
						},
						"installs": map[string]interface{}{
							"composio": map[string]interface{}{
								"source":      "archive",
								"installPath": "/home/openclaw/.openclaw/extensions/composio",
							},
						},
					},
				},
			},
		},
		ComposioConsumerKey: "ck_test_key",
	}

	data, err := AssembleConfig(params)
	if err != nil {
		t.Fatalf("AssembleConfig: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	plugins, ok := result["plugins"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins key")
	}

	entries, ok := plugins["entries"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries")
	}

	composioEntry, ok := entries["composio"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries.composio")
	}

	config, ok := composioEntry["config"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.entries.composio.config")
	}

	// Verify env var ref, NOT the actual key
	if config["consumerKey"] != "COMPOSIO_CONSUMER_KEY" {
		t.Errorf("consumerKey = %v, want COMPOSIO_CONSUMER_KEY (env var ref, not actual key)", config["consumerKey"])
	}

	// Verify actual key is NOT in the config anywhere
	configJSON, _ := json.Marshal(result)
	if strings.Contains(string(configJSON), "ck_test_key") {
		t.Error("actual consumer key found in assembled config — security violation")
	}

	if config["userId"] != "machine-uuid-123" {
		t.Errorf("userId = %v, want machine-uuid-123", config["userId"])
	}

	if config["mcpUrl"] != "https://connect.composio.dev/mcp" {
		t.Errorf("mcpUrl = %v, want https://connect.composio.dev/mcp", config["mcpUrl"])
	}

	// Verify installs
	installs, ok := plugins["installs"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.installs")
	}
	composioInstall, ok := installs["composio"].(map[string]interface{})
	if !ok {
		t.Fatal("missing plugins.installs.composio")
	}
	if composioInstall["installPath"] != "/home/openclaw/.openclaw/extensions/composio" {
		t.Errorf("installPath = %v, want /home/openclaw/.openclaw/extensions/composio", composioInstall["installPath"])
	}
}
```

Add `"strings"` to imports if not present.

- [ ] **Step 2: Run the test**

```bash
cd /home/mantiz/OpenClawMachines && go test ./backend/internal/configassembly/... -run TestAssembleConfig_ComposioInjection -v -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/configassembly/assembler_test.go
git commit -m "test: add Composio config assembly injection test

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 8: Frontend — IntegrationsTab

**Files:**
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/pages/machine-tabs/IntegrationsTab.tsx`

- [ ] **Step 1: Add API functions**

In `frontend/src/lib/api.ts`, add after the plugin API functions (around line 459):

```typescript
// ---- Integrations (Composio) ----

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

- [ ] **Step 2: Rewrite IntegrationsTab**

Replace the entire contents of `frontend/src/pages/machine-tabs/IntegrationsTab.tsx`:

```tsx
import { useEffect, useState, useCallback, useRef } from "react";
import type { Machine } from "../../lib/types";
import { listIntegrations, createConnectLink, deleteIntegration } from "../../lib/api";
import type { Integration } from "../../lib/api";
import { useToast } from "../../components/Toast";

interface IntegrationsTabProps {
  machine: Machine;
}

const CATEGORY_LABELS: Record<string, string> = {
  google: "Google",
  productivity: "Productivity",
  dev: "Developer Tools",
  social: "Social",
  crm: "CRM",
  other: "Other",
};

const CATEGORY_ORDER = ["google", "productivity", "dev", "social", "crm", "other"];

export default function IntegrationsTab({ machine }: IntegrationsTabProps) {
  const [integrations, setIntegrations] = useState<Integration[]>([]);
  const [loading, setLoading] = useState(true);
  const [connecting, setConnecting] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState<string | null>(null);
  const popupRef = useRef<Window | null>(null);
  const { toast } = useToast();

  const fetchIntegrations = useCallback(async () => {
    try {
      const data = await listIntegrations(machine.account_id, machine.id);
      setIntegrations(data);
    } catch (err) {
      console.error("Failed to fetch integrations", err);
    } finally {
      setLoading(false);
    }
  }, [machine.account_id, machine.id]);

  // Fetch on mount
  useEffect(() => {
    fetchIntegrations();
  }, [fetchIntegrations]);

  // Poll on focus
  useEffect(() => {
    const handleFocus = () => fetchIntegrations();
    window.addEventListener("focus", handleFocus);
    return () => window.removeEventListener("focus", handleFocus);
  }, [fetchIntegrations]);

  // Listen for postMessage from callback popup
  useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      if (event.data?.type === "composio-connected") {
        fetchIntegrations();
        setConnecting(null);
      }
    };
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [fetchIntegrations]);

  // Watch for popup close
  useEffect(() => {
    if (!connecting || !popupRef.current) return;
    const interval = setInterval(() => {
      if (popupRef.current?.closed) {
        popupRef.current = null;
        setConnecting(null);
        fetchIntegrations();
        clearInterval(interval);
      }
    }, 500);
    return () => clearInterval(interval);
  }, [connecting, fetchIntegrations]);

  const handleConnect = async (integrationId: string) => {
    setConnecting(integrationId);
    try {
      const { url } = await createConnectLink(machine.account_id, machine.id, integrationId);
      popupRef.current = window.open(url, "composio-connect", "width=500,height=700,popup=1");
    } catch (err) {
      setConnecting(null);
      toast({ title: "Failed to start connection", variant: "error" });
    }
  };

  const handleDisconnect = async (integration: Integration) => {
    if (!integration.connected_account_id) return;
    if (!confirm(`Disconnect ${integration.name}?`)) return;

    setDisconnecting(integration.id);
    try {
      await deleteIntegration(machine.account_id, machine.id, integration.connected_account_id);
      await fetchIntegrations();
      toast({ title: `${integration.name} disconnected` });
    } catch (err) {
      toast({ title: "Failed to disconnect", variant: "error" });
    } finally {
      setDisconnecting(null);
    }
  };

  if (loading) {
    return (
      <div className="space-y-4 p-4">
        {[1, 2, 3].map((i) => (
          <div key={i} className="h-16 bg-card border border-border rounded-lg animate-shimmer" />
        ))}
      </div>
    );
  }

  if (integrations.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-text-muted">
        <p className="text-sm">No integrations available yet.</p>
        <p className="text-xs mt-1">Integrations will appear here once configured by the admin.</p>
      </div>
    );
  }

  // Group by category
  const grouped = new Map<string, Integration[]>();
  for (const integration of integrations) {
    const cat = integration.category || "other";
    if (!grouped.has(cat)) grouped.set(cat, []);
    grouped.get(cat)!.push(integration);
  }

  return (
    <div className="space-y-6 p-4">
      {CATEGORY_ORDER.filter((cat) => grouped.has(cat)).map((cat) => (
        <div key={cat}>
          <h3 className="text-xs font-medium text-text-muted uppercase tracking-wider mb-3">
            {CATEGORY_LABELS[cat] || cat}
          </h3>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {grouped.get(cat)!.map((integration) => (
              <div
                key={integration.id}
                className="flex items-center justify-between p-3 bg-card border border-border rounded-lg"
              >
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-md bg-surface flex items-center justify-center text-xs font-semibold text-text-muted shrink-0">
                    {integration.name.charAt(0)}
                  </div>
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-text truncate">{integration.name}</div>
                    {integration.connected && (
                      <div className="text-xs text-green-500">Connected</div>
                    )}
                  </div>
                </div>
                <div className="shrink-0 ml-2">
                  {integration.connected ? (
                    <button
                      onClick={() => handleDisconnect(integration)}
                      disabled={disconnecting === integration.id}
                      className="text-xs px-3 py-1.5 rounded-md border border-border text-text-muted hover:text-red-400 hover:border-red-400/50 transition-colors disabled:opacity-50"
                    >
                      {disconnecting === integration.id ? "..." : "Disconnect"}
                    </button>
                  ) : (
                    <button
                      onClick={() => handleConnect(integration.id)}
                      disabled={connecting === integration.id}
                      className="text-xs px-3 py-1.5 rounded-md bg-brand-500 text-white hover:bg-brand-600 transition-colors disabled:opacity-50"
                    >
                      {connecting === integration.id ? "Connecting..." : "Connect"}
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
```

- [ ] **Step 3: Verify typecheck**

```bash
make typecheck
```

Expected: no type errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/pages/machine-tabs/IntegrationsTab.tsx
git commit -m "feat: replace IntegrationsTab placeholder with real Composio integration UI

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 9: Infrastructure — Deploy Script + Rootfs

**Files:**
- Modify: `scripts/deploy-cloud-run.sh`
- Modify: `rootfs/Dockerfile.openclaw`

- [ ] **Step 1: Add secret to deploy script**

In `scripts/deploy-cloud-run.sh`, add `COMPOSIO_CONSUMER_KEY=COMPOSIO_CONSUMER_KEY:latest` to the `--set-secrets` line (around line 153). Find the existing secrets line and append:

```
,COMPOSIO_CONSUMER_KEY=COMPOSIO_CONSUMER_KEY:latest
```

- [ ] **Step 2: Add plugin to Dockerfile**

In `rootfs/Dockerfile.openclaw`, after the Opik plugin installation block (around line 191), add:

```dockerfile
# Install Composio plugin (forked with userId support)
ARG COMPOSIO_PLUGIN_VERSION=0.1.0
RUN cd /tmp \
    && npm pack @mathaix/ocm-openclaw-composio-plugin@${COMPOSIO_PLUGIN_VERSION} --quiet \
    && openclaw plugins install "/tmp/mathaix-ocm-openclaw-composio-plugin-${COMPOSIO_PLUGIN_VERSION}.tgz" \
    && rm -f /tmp/mathaix-ocm-openclaw-composio-plugin-*.tgz
```

After the Opik relocation block (around line 228), add:

```dockerfile
# Move Composio plugin from root's home to openclaw user's extensions dir
RUN if [ -d /root/.openclaw/extensions/composio ]; then \
      mkdir -p /home/openclaw/.openclaw/extensions \
      && cp -r /root/.openclaw/extensions/composio /home/openclaw/.openclaw/extensions/ \
      && chown -R openclaw:openclaw /home/openclaw/.openclaw/extensions/composio \
      && rm -rf /root/.openclaw/extensions/composio \
      && echo "Composio plugin moved to /home/openclaw/.openclaw/extensions/composio"; \
    fi
```

- [ ] **Step 3: Commit**

```bash
git add scripts/deploy-cloud-run.sh rootfs/Dockerfile.openclaw
git commit -m "infra: add Composio plugin to rootfs and consumer key to Cloud Run secrets

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 10: Gateway E2E Test

**Files:**
- Modify: gateway E2E test file (find with `grep -rn "TestAssembleConfig\|TestGatewayE2E" backend/internal/gatewaye2e/`)

- [ ] **Step 1: Add Composio config assembly E2E test**

Add a test to the gateway E2E suite that verifies:
1. When composio plugin is enabled and ComposioConsumerKey is set, the assembled config contains `plugins.entries.composio.config.consumerKey = "COMPOSIO_CONSUMER_KEY"` (env var ref)
2. The actual key value does NOT appear anywhere in the assembled config
3. `plugins.entries.composio.config.userId` matches the machine ID
4. `plugins.installs.composio.installPath` is set

This test uses the existing gateway E2E pattern — follow `TestOpikConfigAssembly` or similar as a template.

- [ ] **Step 2: Run the test**

```bash
make test-gateway-e2e
```

Expected: passes (~12s).

- [ ] **Step 3: Commit**

```bash
git add backend/internal/gatewaye2e/
git commit -m "test: add Composio config assembly gateway E2E test

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
```

---

## Task 11: Final Integration Test + Push

- [ ] **Step 1: Run full test suite**

```bash
make test
```

Expected: all tests pass.

- [ ] **Step 2: Run typecheck**

```bash
make typecheck
```

Expected: clean.

- [ ] **Step 3: Push all commits**

```bash
git push
```

- [ ] **Step 4: Update CurrentFeature.md**

Update `docs/CurrentFeature.md` with current status.
