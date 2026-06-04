# LLM Usage Dashboard Design

## Goal

Replace the broken zero-count usage display with a working usage dashboard that shows token counts and costs broken down by model, provider, and time period (hourly/daily). Add a background poller that collects per-session token data from the gateway for future agent-level attribution.

## Problem

The API proxy writes usage records to `token_usage` (migration 040), but the read queries (`GetLLMSpendByMachine`, `GetLLMUsageByMachine`) read from the legacy `llm_usage` table (migration 001), which has 0 records. Result: the frontend always shows zero for all token counts and spend.

Additionally:
- Nebius provider records have `input_tokens=0, output_tokens=0` across all 364 records — the SSE parser isn't extracting tokens
- No time-bucketed aggregation endpoint exists (only raw record list)
- No gateway session data is collected (agent/channel context)

## Architecture

```
                    WRITE PATH (existing, working)
┌─────────┐  HTTP   ┌───────────┐  INSERT   ┌──────────────┐
│ Gateway  │───────►│ API Proxy  │─────────►│ token_usage  │
└─────────┘         └───────────┘           └──────────────┘

                    READ PATH (broken → fix)
┌──────────┐  GET   ┌──────────┐  SELECT   ┌──────────────┐
│ Frontend  │──────►│ Backend   │─────────►│ token_usage  │  (was: llm_usage)
└──────────┘        └──────────┘           └──────────────┘

                    SESSION POLLER (new)
┌──────────┐  WS: sessions.list  ┌─────────┐  INSERT   ┌───────────────────┐
│ Backend   │───────────────────►│ Gateway  │          │ session_snapshots  │
│ (poller)  │◄───────────────────┘         │          └───────────────────┘
└──────────┘   response: sessions          │
      │        with token counts           │
      └────────────────────────────────────┘
```

## Data Sources

### Source 1: `token_usage` (proxy writes) — per-request granularity

| Field | Type | Description |
|-------|------|-------------|
| provider | text | anthropic, openai, nebius, etc. |
| model | text | claude-opus-4-6, Qwen3.5-397B, etc. |
| input_tokens | int | Tokens in request |
| output_tokens | int | Tokens in response |
| cost_input_usd | numeric | Per-token input cost from model catalog |
| cost_output_usd | numeric | Per-token output cost from model catalog |
| source | text | byok, platform, subscription |
| created_at | timestamptz | Request timestamp |

437 records exist today. This is the source of truth for cost and model-level breakdown.

### Source 2: `session_snapshots` (new, from gateway polls) — per-session granularity

| Field | Type | Description |
|-------|------|-------------|
| machine_id | text | Which machine |
| session_key | text | e.g. `agent:default:main`, `agent:coder:telegram:group:123` |
| agent_id | text | Parsed from session key |
| channel | text | chat, telegram, discord, etc. |
| input_tokens | int | Cumulative input tokens (from gateway) |
| output_tokens | int | Cumulative output tokens (from gateway) |
| total_tokens | int | Cumulative total tokens |
| polled_at | timestamptz | When this snapshot was taken |

The poller computes **deltas** between consecutive snapshots to derive per-window usage.

## Backend Changes

### 1. Fix read queries

Switch `GetLLMSpendByMachine` and `GetLLMUsageByMachine` to read from `token_usage` instead of `llm_usage`.

**Cost calculation change:**
- Old (`llm_usage`): `SUM(cost_microcents)`
- New (`token_usage`): `SUM((input_tokens * cost_input_usd + output_tokens * cost_output_usd) * 100000000)` (USD → microcents)

Update the `LLMUsage` struct and scan to match `token_usage` columns.

### 2. New aggregation endpoint

`GET /api/accounts/{accountId}/machines/{machineId}/usage/breakdown`

Query params:
- `period` — `hour` or `day` (default: `hour`)
- `since` — RFC3339 timestamp (default: start of today for hour, start of month for day)

Response:
```json
{
  "period": "hour",
  "since": "2026-03-28T00:00:00Z",
  "buckets": [
    {
      "timestamp": "2026-03-28T05:00:00Z",
      "entries": [
        {
          "provider": "anthropic",
          "model": "claude-opus-4-6",
          "source": "byok",
          "input_tokens": 1200,
          "output_tokens": 4500,
          "cost_microcents": 34000,
          "request_count": 8
        }
      ]
    }
  ],
  "totals": {
    "input_tokens": 50000,
    "output_tokens": 120000,
    "cost_microcents": 890000,
    "request_count": 437
  }
}
```

Single SQL query:
```sql
SELECT
  date_trunc($1, created_at) AS bucket,
  provider,
  model,
  source,
  SUM(input_tokens) AS input_tokens,
  SUM(output_tokens) AS output_tokens,
  SUM((input_tokens * COALESCE(cost_input_usd, 0) + output_tokens * COALESCE(cost_output_usd, 0)) * 100000000)::bigint AS cost_microcents,
  COUNT(*) AS request_count
FROM token_usage
WHERE machine_id = $2 AND created_at >= $3
GROUP BY bucket, provider, model, source
ORDER BY bucket, provider, model
```

### 3. Session snapshot poller

New `SessionPoller` in `backend/internal/usage/` (or similar):

- Runs as a background goroutine (like the Projector)
- Configurable interval: 5 minutes (default), adjustable to 1 hour
- For each running machine:
  1. Connect to gateway via agent proxy: `ws://{hostIP}:9091/proxy/{machineID}/gateway/ws`
  2. Authenticate with gateway token
  3. Send `sessions.list` request
  4. Parse response: extract session keys, token counts
  5. Parse agent ID from session key format: `agent:<agentId>:<rest>`
  6. INSERT snapshot into `session_snapshots`
- Graceful: skip machines where gateway is unreachable, log warning, continue

### 4. Session snapshots migration

```sql
CREATE TABLE session_snapshots (
  id BIGSERIAL PRIMARY KEY,
  machine_id TEXT NOT NULL,
  session_key TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT 'unknown',
  channel TEXT NOT NULL DEFAULT 'chat',
  input_tokens INT NOT NULL DEFAULT 0,
  output_tokens INT NOT NULL DEFAULT 0,
  total_tokens INT NOT NULL DEFAULT 0,
  polled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_session_snapshots_machine ON session_snapshots(machine_id, polled_at);
CREATE INDEX idx_session_snapshots_agent ON session_snapshots(machine_id, agent_id, polled_at);
```

### 5. Fix Nebius token parsing

Investigate why `input_tokens=0` for all Nebius records. The Nebius provider in `providers.go` uses OpenAI-compatible `usage.prompt_tokens` / `usage.completion_tokens` parsing. Either:
- The Nebius API returns tokens in a different location
- The SSE event format differs from what we parse
- Nebius doesn't return usage in streaming responses (common for some providers)

### 6. Add `category` column to `token_usage` (future-ready)

```sql
ALTER TABLE token_usage ADD COLUMN category TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE token_usage ADD COLUMN category_detail TEXT;
```

Values: `main`, `agent`, `tool`, `system`, `unknown`. Not populated yet — reserved for when we can correlate proxy requests with gateway session context (Phase 2).

## Frontend Changes

### Usage section in OverviewTab

Replace the current broken token/spend cards with working data from the fixed endpoint.

**Summary cards row:**
- Total spend (this month)
- Total input tokens
- Total output tokens
- Request count
- Top model (by cost)

### New "Usage" view (either a sub-tab or expanded section)

**"Today" view (hourly):**
- Stacked bar chart: X-axis = hours, Y-axis = tokens or cost, segments = models
- Table below: model, provider, input tokens, output tokens, cost, request count

**"This Month" view (daily):**
- Stacked bar chart: X-axis = days, Y-axis = tokens or cost, segments = models
- Table below: same columns, daily aggregates

**"By Agent" view (Phase 2):**
- Uses `session_snapshots` data once enough is collected
- Per-agent token totals with channel breakdown
- Not in Phase 1 scope — just accumulate data

### API integration

New API function:
```typescript
export const getMachineUsageBreakdown = (
  accountId: number,
  machineId: string,
  period: "hour" | "day",
  since?: string
) => {
  const params = new URLSearchParams({ period });
  if (since) params.set("since", since);
  return request<UsageBreakdown>(`/accounts/${accountId}/machines/${machineId}/usage/breakdown?${params}`);
};
```

## Phase 1 Scope (this PR)

1. Fix `GetLLMSpendByMachine` / `GetLLMUsageByMachine` → read from `token_usage`
2. New `/usage/breakdown` aggregation endpoint
3. Migration: `session_snapshots` table + `category` columns on `token_usage`
4. Session snapshot poller (background goroutine, 5-min interval)
5. Fix Nebius zero-token bug (investigate + fix)
6. Frontend: fix OverviewTab summary cards, add hourly/daily breakdown view

## Phase 2 (future)

1. Correlate session snapshots with proxy records by time window
2. Populate `category` / `category_detail` on `token_usage`
3. Frontend "By Agent" view using session data
4. Explore gateway extension points for `X-OCM-Category` header

## Not in scope

- Dropping `llm_usage` table (separate cleanup migration)
- Real-time streaming usage updates (polling is sufficient)
- Budget alerts/notifications (existing budget check in proxy is sufficient)
- Per-user attribution (would need auth context in proxy)
