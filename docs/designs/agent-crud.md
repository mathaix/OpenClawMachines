# Agent CRUD — Design Document

## Status: Draft (2026-03-15)

## Problem

OCM machines run OpenClaw, which supports multiple **agents** (AI personas that handle conversations). Today, agents can only be created via the OpenClaw CLI inside the VM (`openclaw agents add`). This has three problems:

1. **Split-brain**: The control plane doesn't know about agents created inside the VM. When config is pushed via the metadata server, locally-created agents are invisible to the platform.
2. **No dashboard management**: Users must SSH into a terminal to manage agents. There's no UI for creating, updating, or deleting agents.
3. **No programmatic self-configuration**: The AI itself cannot create or manage agents — a user asking "create a specialist agent for customer support" has no path to fulfillment.

## Product Decision

OCM machines are **managed appliances** (see `docs/configuration_architecture.md`). The control plane is the source of truth for agent definitions. Agents are created, updated, and deleted via the OCM backend API. The config assembly pipeline renders them into the gateway config served by the metadata server.

## Terminology

To avoid confusion with the OCM **worker agent** (`backend/cmd/agent/`, the Go process on GCE hosts that manages Firecracker VMs), this document uses:

- **"persona"** or **"OpenClaw agent"** — an AI persona within a machine that handles conversations
- **"worker agent"** or **"host agent"** — the OCM Go process that manages VMs

In the codebase, the new feature uses `machine_agents` (DB table), `handleMachineAgent*` (API handlers), and `/agents` (API routes nested under `/machines/{id}/`), keeping it clearly scoped to per-machine personas.

## Architecture

### Config Source Constraint

When `OCM_CONFIG_SOURCE=metadata` (set on all OCM VMs), the OpenClaw gateway reads config **exclusively** from the metadata server (`http://192.168.100.1/v1/config` — the bridge gateway IP). The disk config file (`~/.openclaw/openclaw.json5`) is completely ignored. There is no merge between the two sources.

This means:
- Agent definitions **must** be included in the assembled config served by the metadata server
- Soul files (`SOUL.md`) are stored on disk at the agent's workspace directory — the gateway reads these from the filesystem, not from config
- The control plane stores both in the database, pushes agent config via metadata server and soul files to disk

### Data Flow

```
Dashboard / Self-Config Skill
    │
    ▼
Backend API (CRUD on machine_agents table)
    │
    ▼
Config Assembly (renders agents.list into assembled config)
    │
    ▼
Config Push → metadata server → gateway reloads
    │
    ▼ (separately)
Soul file push → /write-file endpoint on PTY server → /data disk
    │
    ▼ (on cold start)
Init script fetches souls from metadata server → writes to disk before gateway starts
```

### Three Access Paths

```
┌──────────────┐     ┌────────────────┐     ┌──────────────────────┐
│  Dashboard   │     │ Self-Config    │     │  Future: Solution    │
│  (human UI)  │     │ Skill (AI)     │     │  Templates           │
└──────┬───────┘     └───────┬────────┘     └──────────┬───────────┘
       │                     │                         │
       │    ┌────────────────┘                         │
       │    │  (via metadata server /v1/admin/*)       │
       ▼    ▼                                          ▼
┌─────────────────────────────────────────────────────────────────┐
│  Backend API (or metadata-server-proxied agent routes)          │
│  POST   /machines/{id}/agents                                   │
│  GET    /machines/{id}/agents                                   │
│  PUT    /machines/{id}/agents/{agentId}                         │
│  DELETE /machines/{id}/agents/{agentId}                         │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                    PostgreSQL                                    │
│  machine_agents table (source of truth)                         │
└──────────────────────────┬──────────────────────────────────────┘
                           │
              ┌────────────┴────────────┐
              ▼                         ▼
┌──────────────────────┐  ┌──────────────────────────┐
│  Config Assembly     │  │  Soul File Push           │
│  renders agents.list │  │  via /write-file endpoint │
│  into metadata       │  │  on PTY server            │
│  server config       │  │  writes to /data disk     │
└──────────────────────┘  └──────────────────────────┘
```

## Data Model

### New Table: `machine_agents`

```sql
CREATE TABLE machine_agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    agent_id        TEXT NOT NULL,           -- openclaw agent id ("support", "creative")
    name            TEXT NOT NULL,           -- display name ("Support Bot")
    model           TEXT,                    -- per-agent model override (null = use machine default)
    identity_emoji  TEXT,                    -- emoji shown in chat UI
    identity_avatar TEXT,                    -- avatar identifier (opaque string, e.g. "robot", emoji, or URL)
    soul            TEXT,                    -- system prompt / personality (pushed to disk as SOUL.md)
    is_default      BOOLEAN NOT NULL DEFAULT false,
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, agent_id)
);
```

**Design decisions:**
- `agent_id` is the OpenClaw identifier (lowercase, used in routing and sessions). Separate from the UUID `id` which is the OCM primary key.
- `model` is nullable — null means inherit the machine's default model.
- `soul` stored in DB as source of truth. Pushed to disk as `SOUL.md` (OpenClaw's default soul filename) at the agent's workspace directory.
- `is_default` — exactly one agent per machine should be default. Enforced in application logic (not DB constraint, to allow transactional swaps).
- No routing bindings table in v1 — agents handle all channels by default. Routing can be added as a follow-on.

### Config Assembly Output

The `machine_agents` rows are rendered into the `agents` section of assembled config. The `agents.defaults.model` uses the object shape with `primary` + `models` map (matching the existing assembler pattern at `assembler.go:337-345`):

```json
{
  "agents": {
    "defaults": {
      "model": {
        "primary": "anthropic/claude-sonnet-4-6"
      },
      "models": {
        "anthropic/claude-sonnet-4-6": {},
        "anthropic/claude-opus-4-6": {}
      },
      "workspace": "/home/openclaw/.openclaw/workspace"
    },
    "list": [
      {
        "id": "support",
        "name": "Support Bot",
        "default": true,
        "model": { "primary": "anthropic/claude-sonnet-4-6" },
        "identity": { "emoji": "🤖" },
        "workspace": "/home/openclaw/.openclaw/workspace"
      },
      {
        "id": "creative",
        "name": "Creative Writer",
        "model": { "primary": "anthropic/claude-opus-4-6" },
        "identity": { "emoji": "✍️" },
        "workspace": "/home/openclaw/.openclaw/workspace-creative"
      }
    ]
  }
}
```

**Workspace path logic** (from OpenClaw `agent-scope.ts:270-271`):
- Default agent: uses `agents.defaults.workspace` (currently `/home/openclaw/.openclaw/workspace`)
- Non-default agents: uses `<stateDir>/workspace-<agentId>` where stateDir = `/home/openclaw/.openclaw`
- OpenClaw auto-creates workspace dirs via `ensureAgentWorkspace()` — no init script changes needed

### Config Assembly Changes

The existing `AssemblyParams` struct needs a new field:

```go
type AssemblyParams struct {
    // ... existing fields ...
    Agents []AgentDefinition  // NEW — per-machine agent definitions
}

type AgentDefinition struct {
    AgentID        string
    Name           string
    Model          string  // empty = inherit default
    IdentityEmoji  string
    IdentityAvatar string
    IsDefault      bool
    SortOrder      int
}
```

New assembly step (after step 5c which sets `agents.defaults`):

```
5d. If Agents is non-empty AND the agents map exists (i.e., DefaultModel was set
    in step 5c), build agents.list array from AgentDefinition slice.
    For each agent:
      - Set id, name, default flag
      - If agent has model override, validate it at API time (create/update):
        - Must appear in the machine's allowed models list (same allowlist used
          by machine-level model validation in machine_config.go:494-527)
        - Must have a provider with wired credentials (the override model's
          provider must exist in the machine's credential entries)
      - If agent has model override, set model as { "primary": "<model>" }
      - Add all per-agent override models to agents.defaults.models map
        (OpenClaw expects override models to appear in the available-model map):
        agents.defaults.models["<override_model>"] = {}
      - If agent has identity_emoji, set identity.emoji
      - If agent has identity_avatar, set identity.avatar
      - Set workspace path based on default status:
        - Default agent: use agents.defaults.workspace value
        - Non-default: /home/openclaw/.openclaw/workspace-<agent_id>
    If Agents is non-empty but DefaultModel is empty:
      - This is detected in assembleConfigForMachine() (pre-assembly), not
        in AssembleConfig() itself. assembleConfigForMachine already accumulates
        warnings before calling AssembleConfig (see machine_config.go:246,364).
      - Add warning to the existing warnings list: "agents defined but no
        default model — agents.list skipped"
      - Pass an empty Agents slice to AssembleConfig to skip rendering
```

Note: `agents` is in `ProtectedConfigKeys` — this is correct because agents should only be set by config assembly (step 5d), never by capability overrides. The assembly step itself bypasses the protection since it writes directly to the result map.

## Backend API

### Endpoints

All endpoints require JWT auth + account ownership. Agent mutation endpoints (create, update, delete) require owner/admin role (inside the existing `requireRole("owner", "admin")` group).

Routes are nested under the existing machine route `/{id}` (matching the codebase pattern where the URL param is `{id}`, not `{machineId}`):

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/{id}/agents` | List agents on this machine |
| `POST` | `/{id}/agents` | Create agent |
| `GET` | `/{id}/agents/{agentId}` | Get agent |
| `PUT` | `/{id}/agents/{agentId}` | Update agent |
| `DELETE` | `/{id}/agents/{agentId}` | Delete agent |

### Request/Response

**Create agent:**
```json
// POST /api/accounts/{accountId}/machines/{id}/agents
{
  "agent_id": "support",
  "name": "Support Bot",
  "model": "anthropic/claude-sonnet-4-6",
  "identity_emoji": "🤖",
  "identity_avatar": "robot",
  "soul": "You are a helpful customer support agent...",
  "is_default": false,
  "sort_order": 1
}

// Response: 201
{
  "id": "uuid",
  "agent_id": "support",
  "name": "Support Bot",
  "model": "anthropic/claude-sonnet-4-6",
  "identity_emoji": "🤖",
  "identity_avatar": "robot",
  "is_default": false,
  "sort_order": 1,
  "created_at": "2026-03-15T..."
}
```

**Update agent:**
```json
// PUT /api/accounts/{accountId}/machines/{id}/agents/support
{
  "name": "Support Bot v2",
  "model": "anthropic/claude-opus-4-6",
  "soul": "Updated personality..."
}

// Response: 200
{ "status": "ok" }
```

**Delete agent:**
```
// DELETE /api/accounts/{accountId}/machines/{id}/agents/support
// Response: 200
{ "status": "ok" }
```

### Push Behavior

Agent CRUD does **not** auto-push config (matching the existing codebase pattern where capability mutations don't auto-push). Users call the existing `POST /{id}/config/push` endpoint to push after making changes. This keeps the push explicit and allows batching multiple changes before pushing.

The `assembleConfigForMachine()` function is extended to:
1. Load agents from `store.ListMachineAgents(ctx, machineID)`
2. Map them to `AgentDefinition` slice
3. Pass to `AssembleConfig()` via the new `Agents` field

The push flow also writes soul files to disk for any agents that have a non-empty `soul` field, using a new `/write-file` endpoint added to the PTY server as part of this feature (only when the machine is running — see Soul File Management for the cold start path).

## Soul File Management

OpenClaw reads agent personality from `SOUL.md` in the agent's workspace directory (`resolveAgentWorkspaceDir()` in `agent-scope.ts`). This file is NOT part of the config JSON — it lives on the filesystem.

### Two Delivery Paths

Soul files need to reach the VM in two scenarios:

**Path 1: Hot push (machine is running)**

The config push handler writes soul files via the new `/write-file` PTY server endpoint:

```
handlePushMachineConfig
    ├── 1. Assemble config (includes agents.list, NOT soul text)
    ├── 2. Write soul files FIRST (before config push):
    │       For each agent with non-empty soul:
    │         POST /write-file → agent proxy → PTY server
    │         Path depends on default status:
    │           - Default agent → /home/openclaw/.openclaw/workspace/SOUL.md
    │           - Non-default   → /home/openclaw/.openclaw/workspace-<agent_id>/SOUL.md
    └── 3. Push config to VM metadata server (bumps config-version → gateway reload)
```

**Order matters:** Souls must be written before the config push because the config push bumps the metadata config-version, which triggers the VM's config-watcher to send SIGUSR1, which restarts the gateway. If souls were written after, the gateway could reload into a new agent list before the soul files land. Writing souls first ensures they are on disk before the gateway restart.

If a write-file call fails (e.g., VM not ready), log a warning but don't fail the push. The soul file will be written on next push or next cold start.

**Path 2: Cold start (machine was stopped when agent was created/updated)**

New metadata server endpoint: `GET /v1/souls`

Returns all soul files for the machine as a JSON array:

```json
[
  { "agent_id": "support", "path": "/home/openclaw/.openclaw/workspace/SOUL.md", "content": "You are...", "is_default": true },
  { "agent_id": "creative", "path": "/home/openclaw/.openclaw/workspace-creative/SOUL.md", "content": "Creative writer...", "is_default": false }
]
```

Note: the `path` is computed by the backend using the same workspace logic as config assembly — default agent uses `workspace/` (no suffix), non-default uses `workspace-<agent_id>/`.

The init script (`scripts/init-openclaw.sh`) fetches this endpoint and writes each soul file to disk **before starting the gateway**:

```bash
# Fetch and write soul files from metadata server
SOULS=$(curl -s -H "X-Metadata-Nonce: $NONCE" "$METADATA_URL/v1/souls" 2>/dev/null)
if [ -n "$SOULS" ] && [ "$SOULS" != "null" ]; then
    echo "$SOULS" | python3 -c "
import json, sys, os
for s in json.load(sys.stdin):
    os.makedirs(os.path.dirname(s['path']), exist_ok=True)
    with open(s['path'], 'w') as f:
        f.write(s['content'])
    os.chown(s['path'], 1000, 1000)  # openclaw user
" 2>/dev/null
fi
```

**How souls reach the metadata server on cold start:**

The host agent/orchestrator never talks to Postgres — it only receives a VM-create payload from the backend. Souls must be included in that payload:

1. Backend `handleStartMachine` → `runtime.Start()` already fetches machine data from Postgres
2. Add: `runtime.Start()` also fetches `store.ListMachineAgents(ctx, machineID)` to get soul data
3. Soul data is added to `VMRequest` as a new field `Souls []SoulEntry` (agent_id + path + content)
4. Agent `handleCreateVM` passes souls through to `orchestrator.VMConfig`
5. Orchestrator `RegisterMachine` stores souls in `MachineConfig.Souls`
6. Metadata server serves `/v1/souls` from the in-memory `MachineConfig.Souls` field

```go
// Added to agentclient.VMRequest
Souls []SoulEntry `json:"souls,omitempty"`

// Added to metadata.MachineConfig
Souls []SoulEntry `json:"-"` // served via /v1/souls, not /v1/config

type SoulEntry struct {
    AgentID string `json:"agent_id"`
    Path    string `json:"path"`
    Content string `json:"content"`
}
```

This follows the exact same pattern as `OpenClawConf`, `LLMKeys`, `ChannelKeys`, and `Secrets` — all are fetched by the backend from Postgres and included in the `CreateVM` payload.

### /write-file Security Model

The `/write-file` endpoint is new (added as part of this feature) on the PTY server (port 7681). It needs careful security:

```go
// Allowed exact filenames within workspace directories
var allowedWriteFiles = map[string]bool{
    "SOUL.md": true,
}

// allowedWriteBase is the workspace parent directory
const allowedWriteBase = "/home/openclaw/.openclaw/"

func handleWriteFile(w http.ResponseWriter, r *http.Request) {
    // 1. Parse request
    var req struct {
        Path    string `json:"path"`
        Content string `json:"content"`
    }

    // 2. Normalize path: filepath.Clean, then filepath.Abs
    cleanPath := filepath.Clean(req.Path)
    absPath, _ := filepath.Abs(cleanPath)

    // 3. Resolve symlinks to prevent symlink escape
    // Use filepath.EvalSymlinks on the parent directory
    parentDir := filepath.Dir(absPath)
    realParent, err := filepath.EvalSymlinks(parentDir)
    if err != nil {
        // Parent doesn't exist yet (new workspace-<agent> dir).
        // Walk up to find the nearest existing ancestor and verify it's
        // within the allowed base. This handles first-write to a new agent.
        ancestor := parentDir
        for ancestor != "/" {
            if rp, e := filepath.EvalSymlinks(ancestor); e == nil {
                if !strings.HasPrefix(rp+"/", allowedWriteBase) {
                    writeError(w, 403, "path outside allowed directory")
                    return
                }
                break
            }
            ancestor = filepath.Dir(ancestor)
        }
        realParent = parentDir  // use the original (no symlinks to resolve)
    }
    realPath := filepath.Join(realParent, filepath.Base(absPath))

    // 4. Verify resolved path starts with allowed base
    if !strings.HasPrefix(realPath, allowedWriteBase) {
        writeError(w, 403, "path outside allowed directory")
        return
    }

    // 5. Verify filename is in the allowlist
    if !allowedWriteFiles[filepath.Base(realPath)] {
        writeError(w, 403, "filename not allowed")
        return
    }

    // 6. Enforce size limit (1MB max for soul files)
    if len(req.Content) > 1024*1024 {
        writeError(w, 400, "content too large (max 1MB)")
        return
    }

    // 7. Create parent dirs and write file
    os.MkdirAll(filepath.Dir(realPath), 0755)
    os.WriteFile(realPath, []byte(req.Content), 0644)
    // chown to openclaw user
}
```

**Security properties:**
- Only `SOUL.md` filename allowed (exact match)
- Path must resolve to within `/home/openclaw/.openclaw/` after symlink resolution
- Symlink escape blocked via `filepath.EvalSymlinks`
- 1MB size limit
- `../` traversal blocked via `filepath.Clean` + prefix check on resolved path

## Self-Config Skill

### Overview

An OpenClaw skill that lets the AI manage its own machine's agents and capabilities by calling back to the control plane through the metadata server.

### Callback Path: Metadata Server Admin Endpoints

The VM cannot reach the host agent proxy ports (9090/9091) — they are blocked by iptables rules in `bridge_linux.go`. However, the VM **can** reach the metadata server on port 80 at the bridge gateway IP (192.168.100.1). The metadata server runs inside the host agent process, which has:

- The machine ID for each VM (via `MachineConfig.MachineID`)
- The account ID (via `MachineConfig.AccountID`)
- Access to the backend API (the agent process can make HTTP calls to Cloud Run)
- The agent token for authenticating to backend API routes

```
OpenClaw Agent (in VM)
    │ uses "machine-admin" tool
    ▼
HTTP call to metadata server (http://192.168.100.1/v1/admin/agents)
    │ authenticated via metadata nonce (same as /v1/config)
    ▼
Metadata server (runs inside host agent process)
    │ resolves machine ID from caller's VM IP
    │ proxies to backend API using agent token
    ▼
Backend API (/api/agent/machines/{machineID}/agents)
    │ new agent-authenticated machine routes
    ▼
PostgreSQL (machine_agents table)
```

### New Backend API Routes (Agent-Authenticated)

Alongside the existing `/api/agent/heartbeat` and `/api/agent/register` routes, add machine-scoped agent-authenticated routes:

```go
// Agent-authenticated machine operations (called by host agent's metadata server)
r.Post("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthCreateAgent)
r.Get("/api/agent/machines/{machineID}/agents", srv.handleAgentAuthListAgents)
r.Put("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthUpdateAgent)
r.Delete("/api/agent/machines/{machineID}/agents/{agentId}", srv.handleAgentAuthDeleteAgent)
r.Post("/api/agent/machines/{machineID}/config/push", srv.handleAgentAuthPushConfig)
```

**Auth model:** There is no `AgentAuthMiddleware` in the codebase. The existing agent endpoints (`/api/agent/heartbeat`, etc.) authenticate inline via `authenticateAgent(ctx, r, hostID)`.

For the new machine-scoped routes, each handler authenticates using `store.GetHostByAgentToken()` (already exists at `postgres.go:2652`), which resolves the host directly from the bearer token — no caller-supplied host ID needed:

1. Extract bearer token from `Authorization` header
2. Call `store.GetHostByAgentToken(ctx, token)` to resolve the host
3. If no host found, check fleet-wide `s.agentToken` fallback
4. Validate the machine ID from the URL belongs to this host (via placement check)
5. Reuse the same store operations as dashboard API handlers

This is more secure than a caller-supplied `X-Host-ID` header since the host identity is derived from the token, not from untrusted input.

### New Metadata Server Admin Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/admin/agents` | List agents on this machine |
| `POST` | `/v1/admin/agents` | Create agent |
| `PUT` | `/v1/admin/agents/{agentId}` | Update agent |
| `DELETE` | `/v1/admin/agents/{agentId}` | Delete agent |
| `GET` | `/v1/admin/capabilities` | List available capabilities |
| `POST` | `/v1/admin/capabilities` | Enable capability |
| `DELETE` | `/v1/admin/capabilities/{entryId}` | Disable capability |
| `POST` | `/v1/admin/config/push` | Push config to apply changes |

These endpoints:
1. Authenticate the caller via metadata nonce (same as `/v1/config`)
2. Resolve machine ID from the caller's VM IP (already available in `Server.configs`)
3. Proxy the request to the backend API at `/api/agent/machines/{machineID}/...`
4. Use the agent token for authentication

The metadata server struct gains two new fields:

```go
type Server struct {
    // ... existing fields ...
    BackendURL  string // e.g., "https://api.openclawmachines.com"
    AgentToken  string // for authenticating to backend API (per-host or fleet-wide)
}
```

### Skill Registration

The self-config skill is a registry entry (`type = "skill"`). When enabled on a machine, config assembly includes the tool definition in the gateway config. The tool definition tells the gateway to make HTTP calls to `http://192.168.100.1/v1/admin/...` with the metadata nonce for auth.

The skill reads the metadata URL and nonce from environment variables already set inside the VM:
- `OCM_METADATA_URL` — set in the gateway env block in `init-openclaw.sh:837`
- `OCM_METADATA_NONCE` — set in the gateway env block in `init-openclaw.sh:838`
- The nonce is also persisted at `/run/ocm-nonce` (init-openclaw.sh:222)

No config assembly injection is needed — the nonce is already available at runtime inside the gateway process environment. The skill definition just documents that it reads these env vars.

### Permission Restrictions (Deferred)

v1: no restrictions — if the skill is enabled, the agent can do anything the API allows.

Future: per-machine permission config on the skill capability (e.g., max agents, allowed models, deny delete, read-only mode).

## Template Readiness

The architecture supports future solution templates by design:

- Agent definitions are **declarative and serializable** — a template is a JSON array of agent definitions
- API supports **idempotent creation** — a template engine can PUT desired state
- Agent definitions are **machine-independent** — same definition can be stamped onto multiple machines
- `sort_order` preserves agent ordering from templates

Future template structure (not built now):
```json
{
  "name": "E-commerce Support Suite",
  "machines": [
    {
      "name": "Customer Support",
      "agents": [
        { "agent_id": "tier1", "name": "Tier 1 Bot", "model": "sonnet", "soul": "..." },
        { "agent_id": "escalation", "name": "Escalation", "model": "opus", "soul": "..." }
      ],
      "capabilities": ["telegram", "email", "web-search"]
    }
  ]
}
```

## Scope Summary

### In v1
- `machine_agents` table + migration
- Store methods: CRUD for machine agents
- Backend API: CRUD endpoints for agents (owner/admin gated), with model validation against allowed models
- Backend API: agent-authenticated machine routes (`/api/agent/machines/{machineID}/agents`) using `GetHostByAgentToken()` for auth
- Config assembly: extend `AssemblyParams` with agents, render `agents.list` (including avatar in identity)
- PTY server: new `/write-file` endpoint for soul files (restricted to `SOUL.md` in workspace dirs)
- Agent proxy: `/write-file` proxy route
- VM create payload: add `Souls []SoulEntry` to `VMRequest` for cold-start soul delivery
- Metadata server: `/v1/souls` endpoint serving souls from `MachineConfig.Souls`
- Metadata server: `/v1/admin/*` endpoints for self-config skill callback
- Init script: fetch and write soul files before gateway start
- Config push: write soul files via /write-file after config push (hot path), with correct workspace paths for default vs non-default agents
- Self-config skill: registry entry + tool definition
- Dashboard UI: list, create, edit, delete agents on a machine

### Deferred
- Routing bindings (which agent handles which channel)
- Permission restrictions on self-config skill
- Solution templates
- Agent workspace cleanup on delete
- Bulk operations API

## Test Plan

### Unit Tests

#### Database / Store Layer
- [ ] `TestCreateMachineAgent` — insert agent, verify all fields stored correctly
- [ ] `TestCreateMachineAgent_DuplicateAgentID` — same machine + agent_id → unique constraint error
- [ ] `TestCreateMachineAgent_InvalidMachineID` — foreign key violation
- [ ] `TestListMachineAgents` — returns agents for correct machine only, ordered by sort_order
- [ ] `TestListMachineAgents_Empty` — returns empty slice, not nil
- [ ] `TestGetMachineAgent` — by machine_id + agent_id
- [ ] `TestGetMachineAgent_NotFound` — returns appropriate error
- [ ] `TestUpdateMachineAgent` — partial update (only name), verify other fields unchanged
- [ ] `TestUpdateMachineAgent_Soul` — update soul text, verify stored
- [ ] `TestUpdateMachineAgent_NotFound` — returns error
- [ ] `TestDeleteMachineAgent` — verify removed, verify other agents unaffected
- [ ] `TestDeleteMachineAgent_NotFound` — returns error
- [ ] `TestDeleteMachineAgent_CascadeOnMachineDelete` — deleting machine deletes agents
- [ ] `TestMachineAgent_IsDefaultConstraint` — verify application logic enforces single default

#### Config Assembly
- [ ] `TestAssembleConfig_WithAgents` — agents rendered into `agents.list` with correct shape
- [ ] `TestAssembleConfig_AgentModelOverride` — agent with model uses its model; agent without model inherits default
- [ ] `TestAssembleConfig_AgentOverrideModelInDefaultsModels` — per-agent override models added to agents.defaults.models map
- [ ] `TestAssembleConfig_AgentModelObjectShape` — model rendered as `{ "primary": "..." }` not bare string
- [ ] `TestAssembleConfig_AgentIdentity` — emoji and avatar rendered in identity object
- [ ] `TestAssembleConfig_AgentWorkspacePaths` — default agent uses defaults.workspace; non-default gets `/home/openclaw/.openclaw/workspace-<id>`
- [ ] `TestAssembleConfig_AgentDefaultFlag` — `"default": true` set on exactly the default agent
- [ ] `TestAssembleConfig_AgentSortOrder` — agents ordered by sort_order in output
- [ ] `TestAssembleConfig_NoAgents` — no agents → no `agents.list` key (backward compatible)
- [ ] `TestAssembleConfig_AgentsSoulNotInConfig` — soul text NOT included in assembled config JSON
- [ ] `TestAssembleConfig_ProtectedKeysStillWork` — capability overrides still can't touch `agents` key
- [ ] `TestAssembleConfig_AgentsWithoutDefaultModel` — agents present but DefaultModel empty → warning added in assembleConfigForMachine, agents.list skipped
- [ ] `TestHandleCreateAgent_InvalidModel` — model not in machine's allowed models list → 400
- [ ] `TestAssembleConfig_AgentsSchemaValidation` — assembled config with agents passes OpenClaw config schema validation

#### API Handlers (Dashboard — JWT auth)
- [ ] `TestHandleCreateAgent_Success` — valid request → 201, agent stored
- [ ] `TestHandleCreateAgent_MachineNotFound` — 404
- [ ] `TestHandleCreateAgent_WrongAccount` — 403
- [ ] `TestHandleCreateAgent_DuplicateAgentID` — 409 conflict
- [ ] `TestHandleCreateAgent_MissingRequiredFields` — 400 (no agent_id or name)
- [ ] `TestHandleCreateAgent_InvalidAgentID` — 400 (uppercase, spaces, special chars)
- [ ] `TestHandleCreateAgent_RBACMemberDenied` — member role → 403 (owner/admin required)
- [ ] `TestHandleListAgents_Success` — returns all agents for machine
- [ ] `TestHandleListAgents_Empty` — returns empty array
- [ ] `TestHandleListAgents_WrongAccount` — 403
- [ ] `TestHandleGetAgent_Success` — returns single agent with all fields
- [ ] `TestHandleGetAgent_NotFound` — 404
- [ ] `TestHandleUpdateAgent_Success` — partial update works
- [ ] `TestHandleUpdateAgent_NotFound` — 404
- [ ] `TestHandleUpdateAgent_WrongAccount` — 403
- [ ] `TestHandleDeleteAgent_Success` — agent removed
- [ ] `TestHandleDeleteAgent_NotFound` — 404
- [ ] `TestHandleDeleteAgent_CannotDeleteDefault` — 400 (must assign new default first)

#### API Handlers (Agent-authenticated — agent token)
- [ ] `TestAgentAuthCreateAgent_Success` — valid agent token → 201
- [ ] `TestAgentAuthCreateAgent_InvalidToken` — 401
- [ ] `TestAgentAuthCreateAgent_PerHostToken` — per-host enrolled token authenticates via GetHostByAgentToken
- [ ] `TestAgentAuthCreateAgent_FleetToken` — fleet-wide token authenticates when per-host not found
- [ ] `TestAgentAuthCreateAgent_MachineBelongsToHost` — machine not on this host → 403
- [ ] `TestAgentAuthListAgents_Success` — returns agents for the machine
- [ ] `TestAgentAuthUpdateAgent_Success` — update works
- [ ] `TestAgentAuthDeleteAgent_Success` — delete works
- [ ] `TestAgentAuthPushConfig_Success` — triggers config assembly + push

#### PTY Server /write-file Endpoint
- [ ] `TestWriteFile_Success` — writes SOUL.md to allowed workspace path
- [ ] `TestWriteFile_DisallowedPath` — path outside `/home/openclaw/.openclaw/` → 403
- [ ] `TestWriteFile_PathTraversal` — `../` in path → 403 after filepath.Clean
- [ ] `TestWriteFile_SymlinkEscape` — symlink pointing outside allowed base → 403
- [ ] `TestWriteFile_DisallowedFilename` — filename other than SOUL.md → 403
- [ ] `TestWriteFile_MethodNotAllowed` — GET → 405
- [ ] `TestWriteFile_MissingFields` — no path or content → 400
- [ ] `TestWriteFile_ContentTooLarge` — over 1MB → 400
- [ ] `TestWriteFile_CreatesParentDirs` — workspace dir doesn't exist → created automatically
- [ ] `TestWriteFile_CorrectOwnership` — written file owned by openclaw user (uid 1000)

#### Metadata Server
- [ ] `TestMetadataSouls_ReturnsSoulFiles` — GET /v1/souls returns soul array from MachineConfig.Souls
- [ ] `TestMetadataSouls_DefaultAgentWorkspacePath` — default agent soul path uses `/home/openclaw/.openclaw/workspace/SOUL.md` (no agent_id suffix)
- [ ] `TestMetadataSouls_NonDefaultAgentWorkspacePath` — non-default agent soul path uses `/home/openclaw/.openclaw/workspace-<agent_id>/SOUL.md`
- [ ] `TestMetadataSouls_EmptyWhenNoAgents` — returns empty array
- [ ] `TestMetadataSouls_SkipsAgentsWithoutSoul` — only includes agents with non-empty soul
- [ ] `TestMetadataSouls_RequiresNonce` — missing/wrong nonce → 403
- [ ] `TestMetadataAdmin_ProxiesToBackend` — POST /v1/admin/agents proxies to backend API
- [ ] `TestMetadataAdmin_RequiresNonce` — missing nonce → 403
- [ ] `TestMetadataAdmin_ResolvesCorrectMachineID` — uses machine ID from VM IP lookup

#### Agent Proxy Routes
- [ ] `TestAgentProxy_WriteFile` — proxies POST to PTY server /write-file
- [ ] `TestAgentProxy_WriteFile_MissingProxyToken` — 401
- [ ] `TestAgentProxy_WriteFile_InvalidProxyToken` — 403
- [ ] `TestAgentProxy_WriteFile_MachineNotFound` — 404

#### Config Push with Agents
- [ ] `TestConfigPush_IncludesAgents` — assembled config contains agents.list
- [ ] `TestConfigPush_WritesSoulFiles` — soul files written via /write-file for each agent with soul
- [ ] `TestConfigPush_SkipsSoulForAgentsWithoutSoul` — agents with empty soul → no /write-file call
- [ ] `TestConfigPush_SoulWriteFailureNonFatal` — /write-file failure → warning in response, not error
- [ ] `TestConfigPush_SoulWriteSkippedWhenNotRunning` — machine not running → soul write skipped (cold start handles it)
- [ ] `TestConfigPush_SoulsWrittenBeforeConfigPush` — verify soul /write-file calls happen before metadata config-version bump
- [ ] `TestConfigPush_DefaultAgentSoulPath` — default agent soul written to `/home/openclaw/.openclaw/workspace/SOUL.md`
- [ ] `TestConfigPush_NonDefaultAgentSoulPath` — non-default agent soul written to `/home/openclaw/.openclaw/workspace-<agent_id>/SOUL.md`

#### VM Create Payload (Soul Delivery Chain)
- [ ] `TestVMRequest_IncludesSouls` — CreateVM payload contains souls from store.ListMachineAgents
- [ ] `TestVMRequest_SoulPathsCorrect` — default vs non-default workspace paths correct in payload
- [ ] `TestVMRequest_NoAgents_NoSouls` — machine with no agents → Souls field empty

### Integration Tests (if VM available)
- [ ] `TestE2E_CreateAgentAndPush` — create agent via API → push config → verify gateway loads agent
- [ ] `TestE2E_SoulFileOnDisk` — after push, SOUL.md exists at correct workspace path with correct content
- [ ] `TestE2E_ColdStartSoulDelivery` — create agent while stopped → start machine → verify SOUL.md written before gateway starts
- [ ] `TestE2E_DeleteAgentAndPush` — delete agent → push → gateway no longer has agent
- [ ] `TestE2E_SelfConfigSkill` — agent uses self-config tool → creates second agent → push → both live
- [ ] `TestE2E_MultipleAgents` — create 3 agents with different models → push → all active with correct models

### Frontend Tests
- [ ] `TestAgentList_RendersAgents` — component shows agent cards with name, emoji, model
- [ ] `TestAgentList_EmptyState` — shows "No agents" message with create button
- [ ] `TestCreateAgentForm_Validation` — required fields (agent_id, name) enforced
- [ ] `TestCreateAgentForm_AgentIDFormat` — rejects uppercase, spaces, special chars
- [ ] `TestCreateAgentForm_Submit` — calls API, shows success toast
- [ ] `TestEditAgent_PrePopulates` — form filled with existing values including soul
- [ ] `TestDeleteAgent_Confirmation` — shows confirmation dialog before delete
- [ ] `TestDeleteAgent_DefaultBlocked` — cannot delete default agent, shows explanation
