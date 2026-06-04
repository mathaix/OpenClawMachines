# Opik-Compatible Tracing Backend

## Problem

OCM currently tracks LLM usage through two incomplete data paths:

1. **API proxy interception** (`agent_auth.go` → `token_usage`) — catches requests routed through the proxy, but misses subscription models (e.g., ChatGPT 5.4), has model name mismatches between `model_catalog.id` and raw API response names, and requires per-provider SSE parsing (e.g., `stream_options.include_usage` for Nebius/OpenAI).

2. **Session polling** (`poller.go` → `session_snapshots`) — polls gateway WebSocket for aggregate session totals every 5 minutes, but provides no per-model breakdown and runs on Cloud Run which scales to zero before the ticker fires.

Meanwhile, the OpenClaw gateway emits rich diagnostic events (`model.usage`, `llm_input`, `llm_output`, `tool_call`, `agent_end`) that contain per-request, per-model data for **all** providers. The open-source Opik plugin (`@opik/opik-openclaw`) already captures these events and sends them as structured traces and spans to an Opik-compatible API.

## Solution

Implement an Opik-compatible REST API on the OCM backend. Point the existing `@opik/opik-openclaw` plugin at our endpoint. No new plugin to build.

This gives OCM:
- **Complete usage tracking** — every LLM request regardless of provider, model, or routing path
- **Full trace data** — conversations, tool calls, subagent spans, latency, errors
- **Opik UI compatibility** — the stored data model matches Opik's, enabling future adoption of their open-source trace visualization UI

## Architecture

### Data Flow

```
Gateway emits hook events (llm_input, llm_output, tool_call, agent_end, model.usage)
  → @opik/opik-openclaw plugin captures events, builds traces + spans
  → Plugin batches and POSTs to configured apiUrl (OCM backend)
  → Backend authenticates via API key, resolves machine_id + account_id
  → Backend writes to opik_traces / opik_spans tables (Postgres)
  → Billing queries read from opik_spans WHERE type='llm'
  → Usage tab and dashboards display data
```

### What the Plugin Captures

The `@opik/opik-openclaw` plugin listens for these gateway events:

| Event | Data Captured | Opik Entity |
|-------|--------------|-------------|
| `llm_input` | model, provider, prompt, system prompt, session key | Creates Trace + LLM Span |
| `llm_output` | response, per-request token usage (input, output, cache_read, cache_write, total) | Updates LLM Span with output + usage |
| `before_tool_call` / `after_tool_call` | tool name, input, output | Tool Span (child of trace) |
| Subagent events | subagent session, parent session | General Span (child of trace) |
| `agent_end` | success/error, duration, messages | Finalizes Trace with output + metadata |
| `model.usage` (diagnostic) | costUsd, context used/limit, aggregated usage, duration | Accumulated in trace metadata |

### Plugin Configuration

Assembled by the OCM config assembly pipeline for each machine:

```json
{
  "plugins": {
    "entries": {
      "opik-openclaw": {
        "enabled": true,
        "config": {
          "apiUrl": "https://ocm-backend.run.app/api/opik",
          "apiKey": {
            "source": "exec",
            "provider": "ocm",
            "id": "OPIK_API_KEY"
          },
          "projectName": "default",
          "workspaceName": "default",
          "tags": ["ocm"]
        }
      }
    }
  }
}
```

- `apiUrl` points to the OCM backend's Opik-compatible API
- `apiKey` is injected via SecretRef (same pattern as existing Opik → Comet setup)
- The plugin is bundled in the rootfs (already installed as `@opik/opik-openclaw`)
- Plugin slot: `"observability"` (same slot it already uses)

### Authentication

The plugin authenticates with a machine-scoped API key. On each request, the backend:

1. Validates the API key from the `Authorization` header
2. Resolves the key to `machine_id` and `account_id`
3. Injects `account_id` and `machine_id` into every trace and span row

The plugin sends standard Opik payloads — it does not know about OCM-specific fields. The backend enriches data on ingest.

## API Endpoints

Minimum viable Opik-compatible surface. Mounted at `/api/opik/v1/private/`.

### Write Endpoints (Phase 1 — required for plugin)

| Method | Path | Request Body | Response | Purpose |
|--------|------|-------------|----------|---------|
| `POST` | `/traces` | `Trace` (single) | `201 Created` + Location header | Create one trace |
| `POST` | `/traces/batch` | `{traces: Trace[]}` (max 1000) | `204 No Content` | Create traces in bulk |
| `PATCH` | `/traces/{id}` | Partial `Trace` | `204 No Content` | Update trace (end_time, output, metadata, tags, error_info) |
| `POST` | `/spans` | `Span` (single) | `201 Created` + Location header | Create one span |
| `POST` | `/spans/batch` | `{spans: Span[]}` (max 1000) | `204 No Content` | Create spans in bulk |
| `PATCH` | `/spans/{id}` | Partial `Span` | `204 No Content` | Update span (end_time, output, usage, metadata) |

### Project Endpoint (Phase 1 — plugin validates project on startup)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/projects` | List projects (filtered by workspace) |
| `POST` | `/projects` | Create project |
| `GET` | `/projects/{id}` | Get project by ID |

The plugin calls `projects.retrieveProject({name})` on startup to validate the target project exists. If the project doesn't exist, it logs a warning but continues operating.

### Read Endpoints (Phase 3 — for trace visualization UI)

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/traces` | List traces by project with filtering, sorting, pagination |
| `GET` | `/traces/{id}` | Get single trace with spans |
| `GET` | `/spans` | List spans by project/trace with filtering |
| `GET` | `/spans/{id}` | Get single span |

These are not needed for Phase 1 (usage tracking) but follow the same Opik API contract for future UI compatibility.

## Database Schema

### Migration 049: `opik_projects`, `opik_traces`, `opik_spans`

```sql
-- Projects (workspace maps to account)
CREATE TABLE opik_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    account_id INTEGER NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(account_id, name)
);

-- Traces (one per agent conversation turn)
CREATE TABLE opik_traces (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES opik_projects(id),
    account_id INTEGER NOT NULL,
    machine_id TEXT NOT NULL,
    name TEXT,
    thread_id TEXT,                    -- gateway sessionKey
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    input JSONB,
    output JSONB,
    metadata JSONB,
    tags TEXT[],
    error_info JSONB,
    source TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ
);

CREATE INDEX idx_opik_traces_project ON opik_traces(project_id, start_time DESC);
CREATE INDEX idx_opik_traces_machine ON opik_traces(machine_id, start_time DESC);
CREATE INDEX idx_opik_traces_account ON opik_traces(account_id, start_time DESC);
CREATE INDEX idx_opik_traces_thread ON opik_traces(thread_id);

-- Spans (LLM calls, tool calls, subagent calls — children of traces)
CREATE TABLE opik_spans (
    id UUID PRIMARY KEY,
    trace_id UUID NOT NULL REFERENCES opik_traces(id),
    project_id UUID NOT NULL REFERENCES opik_projects(id),
    account_id INTEGER NOT NULL,
    machine_id TEXT NOT NULL,
    parent_span_id UUID,
    name TEXT,
    type TEXT NOT NULL,                -- 'llm', 'tool', 'general'
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    input JSONB,
    output JSONB,
    metadata JSONB,
    model TEXT,
    provider TEXT,
    tags TEXT[],
    usage JSONB,                       -- {prompt_tokens, completion_tokens, total_tokens, cache_read_tokens, cache_write_tokens}
    error_info JSONB,
    total_estimated_cost NUMERIC(12,8),
    duration DOUBLE PRECISION,         -- milliseconds
    ttft DOUBLE PRECISION,             -- time to first token, milliseconds
    source TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ
);

CREATE INDEX idx_opik_spans_trace ON opik_spans(trace_id);
CREATE INDEX idx_opik_spans_project ON opik_spans(project_id, start_time DESC);
CREATE INDEX idx_opik_spans_machine_llm ON opik_spans(machine_id, start_time DESC) WHERE type = 'llm';
CREATE INDEX idx_opik_spans_account_llm ON opik_spans(account_id, start_time DESC) WHERE type = 'llm';
CREATE INDEX idx_opik_spans_model ON opik_spans(model, start_time DESC) WHERE type = 'llm';
```

### Key Design Decisions

- **`account_id` and `machine_id` denormalized onto both tables** — avoids trace joins for billing queries
- **Partial indexes on `type = 'llm'`** — billing only queries LLM spans, so indexes are filtered
- **`usage` as JSONB** — matches Opik's `Map<String, Integer>` contract. Keys: `prompt_tokens`, `completion_tokens`, `total_tokens`, `cache_read_tokens`, `cache_write_tokens`
- **`input`/`output`/`metadata` as JSONB** — stores full trace context for Phase 3 visualization
- **UUIDs as primary keys** — Opik plugin generates UUIDs client-side

## Billing Query Adaptation

Billing queries read from `opik_spans WHERE type = 'llm'` instead of `token_usage`. The `model_pricing_history` lateral join pattern is unchanged.

### Example: GetLLMSpendByMachine

```sql
SELECT COALESCE(SUM(
    COALESCE((s.usage->>'prompt_tokens')::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
    + COALESCE((s.usage->>'completion_tokens')::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
)::bigint, 0)
FROM opik_spans s
LEFT JOIN LATERAL (
    SELECT cost_input_microcents, cost_output_microcents, margin
    FROM model_pricing_history
    WHERE model_id = s.model AND effective_from <= s.start_time
    ORDER BY effective_from DESC LIMIT 1
) p ON true
WHERE s.type = 'llm'
AND s.machine_id = $1
```

### Example: GetLLMUsageByMachine (with breakdown)

```sql
SELECT
    s.model,
    s.provider,
    (s.usage->>'prompt_tokens')::INT AS input_tokens,
    (s.usage->>'completion_tokens')::INT AS output_tokens,
    (s.usage->>'cache_read_tokens')::INT AS cache_read_tokens,
    COALESCE(
        (s.usage->>'prompt_tokens')::BIGINT * p.cost_input_microcents * p.margin / 1000000
        + (s.usage->>'completion_tokens')::BIGINT * p.cost_output_microcents * p.margin / 1000000
    , 0) AS cost_microcents,
    s.start_time AS created_at
FROM opik_spans s
LEFT JOIN LATERAL (
    SELECT cost_input_microcents, cost_output_microcents, margin
    FROM model_pricing_history
    WHERE model_id = s.model AND effective_from <= s.start_time
    ORDER BY effective_from DESC LIMIT 1
) p ON true
WHERE s.type = 'llm'
AND s.machine_id = $1
AND s.start_time >= $2
ORDER BY s.start_time DESC
LIMIT $3
```

## Coexistence with Existing Systems

### Proxy Tracking (Nebius)

The `agent_auth.go` write path stays for Nebius (platform) models. It continues writing to `token_usage`. This serves as a billing verification layer: if the plugin reports fewer Nebius tokens than the proxy sees, it signals tampering or data loss.

### Session Poller

Deprecated. The plugin provides strictly more data (per-request, per-model) than the session poller (aggregate per-session). The poller can be removed once the plugin is proven reliable in production.

### `token_usage` Table

Retained for Nebius proxy writes. Not deleted. Eventually the billing queries can fall back to `token_usage` if `opik_spans` has gaps (e.g., plugin was disabled).

## Product Tiers

All tiers use the same data pipeline. The plugin always captures everything. Tiers control what the frontend exposes.

| Tier | What's Shown | Backend Surface |
|------|-------------|----------------|
| **Base** | Aggregated cost and total tokens per machine | `GetLLMSpendByMachine`, `GetLLMSpendByAccount` |
| **Pro** | Per-model cost breakdown, usage charts, Usage tab | `GetLLMUsageByMachine`, `GetUsageBreakdown` |
| **Max** | Full trace viewer — conversations, tool calls, latency, debugging | Trace/span read endpoints + adapted Opik UI |

## Implementation Phases

### Phase 1: Opik-Compatible Ingest + Billing

- Migration 049: `opik_projects`, `opik_traces`, `opik_spans` tables
- Opik-compatible write endpoints (traces batch, spans batch, trace/span update)
- Project endpoint (list/create for plugin startup validation)
- API key authentication resolving machine_id + account_id
- Rewrite billing queries to read from `opik_spans`
- Config assembly: auto-configure `opik-openclaw` plugin with OCM backend `apiUrl`
- Generate `OPIK_API_KEY` per machine: reuse the existing `gateway_token` (already a machine-scoped secret stored via `SetSecret`). The Opik endpoint validates this token the same way `handleAgentAuthRecordUsage` validates agent tokens — by looking up the machine from the token. No new secret type needed.

### Phase 2: Usage Dashboard Enhancements (Pro tier)

- Adapt Usage tab to show richer data (cache tokens, duration, context window)
- Per-model, per-provider breakdown from span data
- Cost reconciliation: compare plugin data vs proxy data for Nebius

### Phase 3: Trace Visualization (Max tier)

- Implement Opik-compatible read endpoints (GET traces, GET spans with filtering/pagination)
- Adapt Opik's open-source frontend UI to connect to OCM backend
- Trace detail view: conversation flow, tool calls, subagent spans, timing

## Model Name Mapping

The plugin captures the `model` field from `llm_input`/`llm_output` events — this is the model name as the gateway sees it (e.g., `claude-sonnet-4-20250514`, `Qwen/Qwen3.5-397B-A17B`). This is the same value that ends up in the `opik_spans.model` column.

The `model_pricing_history.model_id` must match these gateway-reported model names (not the catalog IDs). This is the same mapping issue we already fixed for `token_usage` — the fix carries over.

## Security Considerations

- The plugin runs inside the user's Firecracker VM. A user could disable or tamper with it.
- Proxy-based tracking for Nebius provides a tamper-resistant verification layer for the models where OCM charges money.
- For BYOK and subscription models, usage tracking is best-effort — users aren't charged, so tampering has no financial impact.
- API keys are machine-scoped and injected via SecretRef — no plaintext credentials in config.

## Reference Implementation

- Opik plugin source: `@opik/opik-openclaw` (npm, Apache-2.0)
- Opik server source: `github.com/comet-ml/opik` (Java/ClickHouse)
- Our implementation: Go + Postgres, implementing the same REST API contract
- Plugin already bundled in rootfs at `Dockerfile.openclaw` lines 179-184
