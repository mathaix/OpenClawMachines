# Platform Models via Nebius Token Factory + Universal Usage Metering

## Summary

OCM provides open-source AI models as a platform service via Nebius Token Factory. Users see "platform models" that are always available without bringing their own API keys. Separately, all LLM usage (platform and BYOK) is metered for future billing.

## Product Model

Two ways to use LLMs on OCM:

1. **BYOK** (existing) — user provides API keys for Anthropic, OpenAI, Google, OpenRouter
2. **Platform models** (new) — OCM provides open-source models, user pays per token at 1.5x markup

Users never see "Nebius" — they see model names like "DeepSeek V3" and "Qwen 3 235B". The provider is an implementation detail.

## Curated Model List

| User-facing ID | Nebius Model ID | Category | Input/M (Nebius) | Output/M (Nebius) | Input/M (User @ 1.5x) | Output/M (User @ 1.5x) |
|---------------|-----------------|----------|-----------------|-------------------|----------------------|------------------------|
| deepseek/deepseek-r1 | deepseek-ai/DeepSeek-R1-0528 | Reasoning | $2.00 | $6.00 | $3.00 | $9.00 |
| deepseek/deepseek-v3 | deepseek-ai/DeepSeek-V3-0324 | General | $0.75 | $2.25 | $1.13 | $3.38 |
| qwen/qwen3-235b | Qwen/Qwen3-235B-Instruct | General | $0.20 | $0.60 | $0.30 | $0.90 |
| qwen/qwen3-coder-480b | Qwen/Qwen3-Coder-480B | Coding | $0.40 | $1.80 | $0.60 | $2.70 |
| qwen/qwen3-30b | Qwen/Qwen3-30B-Instruct | Fast | $0.10 | $0.30 | $0.15 | $0.45 |
| meta/llama-3.3-70b | meta-llama/Llama-3.3-70B-Instruct | General | $0.25 | $0.75 | $0.38 | $1.13 |
| meta/llama-3.1-8b | meta-llama/Llama-3.1-8B-Instruct | Cheap | $0.03 | $0.09 | $0.05 | $0.14 |
| mistral/devstral-small | mistralai/Devstral-Small-2505 | Coding | $0.08 | $0.24 | $0.12 | $0.36 |

## Architecture

### Data Flow

```
User selects platform model (e.g., "DeepSeek V3")
  → config assembly injects nebius proxy URL (no user credential needed)
  → gateway sends request to proxy at 169.254.169.253:4000/nebius
  → proxy injects platform Nebius API key (from metadata server)
  → proxy forwards to https://api.tokenfactory.nebius.com/v1/
  → response streams back through proxy
  → proxy parses token counts from response
  → proxy POSTs usage to backend: POST /api/agent/machines/{machineID}/usage
  → backend writes to token_usage table
```

### Platform Key Management

- `NEBIUS_API_KEY` stored in GCP Secret Manager
- Injected as env var to Cloud Run backend
- Backend passes to agent in `VMRequest` as a platform-level LLM key
- Metadata server serves it to API proxy alongside user credentials
- Never exposed to users or stored in `account_credentials`

### Usage Metering

The API proxy reports usage for ALL LLM requests (both platform and BYOK):

```
proxy completes LLM request
  → parses token counts from response (existing ParseJSONUsage / ParseSSEEvent)
  → POST /api/agent/machines/{machineID}/usage
    {
      "provider": "nebius",
      "model": "deepseek-ai/DeepSeek-V3-0324",
      "input_tokens": 1500,
      "output_tokens": 800,
      "source": "platform"  // or "byok"
    }
  → backend calculates cost from pricing table, writes to token_usage
```

## Database

### Migration: `token_usage` table

```sql
CREATE TABLE token_usage (
  id BIGSERIAL PRIMARY KEY,
  account_id INTEGER NOT NULL REFERENCES accounts(id),
  machine_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_input_usd NUMERIC(12,8),
  cost_output_usd NUMERIC(12,8),
  source TEXT NOT NULL DEFAULT 'byok',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_token_usage_account ON token_usage(account_id, created_at);
CREATE INDEX idx_token_usage_machine ON token_usage(machine_id, created_at);
```

### Pricing Table (in-code, not DB)

Backend holds a `map[string]ModelPricing` with per-model input/output cost per million tokens. Cost is calculated at write time. This keeps pricing easy to update without migrations.

## API Changes

### New Internal Endpoint

`POST /api/agent/machines/{machineID}/usage` (agent-auth)

```json
// Request
{
  "provider": "nebius",
  "model": "deepseek-ai/DeepSeek-V3-0324",
  "input_tokens": 1500,
  "output_tokens": 800,
  "source": "platform"
}

// Response
{ "status": "ok" }
```

### Model Selection Updates

`GET /api/accounts/{accountId}/machines/{machineId}/model` — response now includes `available_models` with platform models always present.

`PUT /api/accounts/{accountId}/machines/{machineId}/model` — accepts platform model IDs (e.g., `deepseek/deepseek-v3`).

## Config Assembly Changes

- Platform models always inject `nebius` provider proxy URL
- If user has no LLM credentials and no preferred model, default to `deepseek/deepseek-v3`
- Allowed models list expanded with all 8 platform models
- Model ID mapping: user-facing ID (e.g., `deepseek/deepseek-v3`) → Nebius model ID (e.g., `deepseek-ai/DeepSeek-V3-0324`)

## API Proxy Changes

- New `nebius` provider in `apiproxy/providers.go` (OpenAI-compatible, bearer auth, host: `api.tokenfactory.nebius.com`)
- After every LLM response (all providers), POST usage to backend internal API
- Usage reporting is fire-and-forget (don't block the response)

## Frontend Changes

- Model picker groups models: "Platform Models" (always available) and "Your Models" (require BYOK keys)
- Platform models show as available even with no credentials
- Usage tab (future) — placeholder for now

## Security

- Platform Nebius key never exposed to users
- Usage endpoint is agent-authenticated (same as plugin status)
- Proxy reports usage asynchronously, doesn't block responses
- Cost calculation happens server-side only

## Implementation Order

1. Database migration (`token_usage` table)
2. Backend: usage reporting endpoint + store methods + pricing table
3. API proxy: nebius provider + usage reporting for all providers
4. Backend: platform key injection in VMRequest + metadata server
5. Config assembly: platform model support + model ID mapping
6. Backend: model selection updates (allowed models, defaults)
7. Frontend: model picker with platform models group
