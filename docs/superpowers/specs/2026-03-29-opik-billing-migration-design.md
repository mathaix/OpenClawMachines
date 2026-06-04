# Migrate Billing to Opik Spans

## Problem

The current billing pipeline (`token_usage` table populated by API proxy interception) is not working. Meanwhile, the Opik tracing pipeline is live and capturing all LLM usage data in `opik_spans` with per-request token counts, model, and provider.

The frontend usage dashboards (OverviewTab, UsageTab, UsageDashboard) are functional but show no data because the underlying `token_usage` table is empty.

## Solution

Replace the billing data source from `token_usage` to `opik_spans`. Keep the same API response shapes so the frontend requires zero changes.

- **Cost computation**: Only Nebius platform models get cost calculated (via `model_pricing_history`). All other providers show token counts with `cost_microcents = 0`.
- **Full Nebius catalog**: Add all ~35 Nebius models to `model_catalog` and `model_pricing_history`, but only enable 3 platform models (Smart, Balanced, Fast).

## Data Flow

```
opik_spans (prompt_tokens, completion_tokens, model, provider, account_id, machine_id)
    ↓ LEFT JOIN on model (catalog ID format, e.g. "openai/gpt-oss-120b")
model_pricing_history (cost_input_microcents, cost_output_microcents, margin)
    ↓
billing store methods (same return types: LLMUsage, UsageBucket, spend totals)
    ↓
billing.go handlers (same response JSON shapes)
    ↓
frontend dashboards (zero changes)
```

## Model Name Mapping (Verified)

**Verified from production data** (`SELECT DISTINCT model, provider FROM opik_spans`):

The Opik SDK records the **catalog model ID** (user-facing format), not the gateway model ID:
- `openai/gpt-oss-120b` with `provider = "nebius"` (for Nebius platform models)
- `anthropic/claude-sonnet-4-6` with `provider = "anthropic"` (for BYOK models)

This means the existing `model_pricing_history` rows (seeded from `model_catalog.id` in migration 048) work **directly** — no dual-key strategy needed. The JOIN is simply:
```sql
opik_spans.model = model_pricing_history.model_id
```

## Migration: Full Nebius Catalog

### model_catalog additions (all disabled except existing 3)

Source: https://nebius.com/token-factory/prices

New models inserted with `enabled = false`. The 3 existing platform models (`qwen/qwen3.5-397b`, `minimax/minimax-m2.5`, `openai/gpt-oss-120b`) are already in the catalog and remain enabled.

For each new `model_catalog` entry, a corresponding `model_pricing_history` row is inserted keyed by the catalog `model_id`, backdated to epoch.

**Text-to-Text Models (new):**

| model_id | label | gateway_model_id | input $/M | output $/M |
|----------|-------|-------------------|-----------|------------|
| openai/gpt-oss-20b | GPT OSS 20B | nebius/openai/gpt-oss-20b | 0.05 | 0.20 |
| moonshot/kimi-k2-instruct | Kimi K2 Instruct | nebius/moonshotai/Kimi-K2-Instruct | 0.50 | 2.40 |
| qwen/qwen3-coder-480b | Qwen3 Coder 480B | nebius/Qwen/Qwen3-Coder-480B | 0.40 | 1.80 |
| qwen/qwen3-235b-a22b-thinking | Qwen3 235B Thinking | nebius/Qwen/Qwen3-235B-A22B-Thinking | 0.20 | 0.80 |
| qwen/qwen3-235b-a22b-instruct | Qwen3 235B Instruct | nebius/Qwen/Qwen3-235B-A22B-Instruct | 0.20 | 0.60 |
| qwen/qwen3-30b-a3b-thinking | Qwen3 30B Thinking | nebius/Qwen/Qwen3-30B-A3B-Thinking | 0.10 | 0.30 |
| qwen/qwen3-30b-a3b-instruct | Qwen3 30B Instruct | nebius/Qwen/Qwen3-30B-A3B-Instruct | 0.10 | 0.30 |
| qwen/qwen3-coder-30b-a3b | Qwen3 Coder 30B | nebius/Qwen/Qwen3-Coder-30B-A3B | 0.10 | 0.30 |
| qwen/qwen3-30b-a3b | Qwen3 30B | nebius/Qwen/Qwen3-30B-A3B | 0.10 | 0.30 |
| qwen/qwen3-32b | Qwen3 32B | nebius/Qwen/Qwen3-32B | 0.10 | 0.30 |
| qwen/qwen3-14b | Qwen3 14B | nebius/Qwen/Qwen3-14B | 0.08 | 0.24 |
| qwen/qwen2.5-coder-7b | Qwen2.5 Coder 7B | nebius/Qwen/Qwen2.5-Coder-7B | 0.03 | 0.09 |
| qwen/qwen2.5-72b-instruct | Qwen2.5 72B Instruct | nebius/Qwen/Qwen2.5-72B-Instruct | 0.13 | 0.40 |
| qwen/qwq-32b | QwQ 32B | nebius/Qwen/QwQ-32B | 0.15 | 0.45 |
| zhipu/glm-4.5 | GLM 4.5 | nebius/THUDM/GLM-4.5 | 0.60 | 2.20 |
| zhipu/glm-4.5-air | GLM 4.5 Air | nebius/THUDM/GLM-4.5-Air | 0.20 | 1.20 |
| deepseek/deepseek-r1-0528 | DeepSeek R1 | nebius/deepseek-ai/DeepSeek-R1-0528 | 0.80 | 2.40 |
| deepseek/deepseek-v3-0324 | DeepSeek V3 | nebius/deepseek-ai/DeepSeek-V3-0324 | 0.50 | 1.50 |
| deepseek/deepseek-v3 | DeepSeek V3 (prev) | nebius/deepseek-ai/DeepSeek-V3 | 0.50 | 1.50 |
| meta/llama-3.3-70b-instruct | Llama 3.3 70B | nebius/Meta/Llama-3.3-70B-Instruct | 0.13 | 0.40 |
| meta/llama-3.1-8b-instruct | Llama 3.1 8B | nebius/Meta/Llama-3.1-8B-Instruct | 0.02 | 0.06 |
| meta/llama-3.1-405b-instruct | Llama 3.1 405B | nebius/Meta/Llama-3.1-405B-Instruct | 1.00 | 3.00 |
| nvidia/llama-3.1-nemotron-ultra-253b | Nemotron Ultra 253B | nebius/nvidia/Llama-3.1-Nemotron-Ultra-253B | 0.60 | 1.80 |
| google/gemma-2-2b-it | Gemma 2 2B | nebius/google/gemma-2-2b-it | 0.02 | 0.06 |
| google/gemma-2-9b-it | Gemma 2 9B | nebius/google/gemma-2-9b-it | 0.03 | 0.09 |
| mistral/devstral-small-2505 | Devstral Small | nebius/mistralai/Devstral-Small-2505 | 0.08 | 0.24 |
| nous/hermes-4-405b | Hermes 4 405B | nebius/NousResearch/Hermes-4-405B | 1.00 | 3.00 |
| nous/hermes-4-70b | Hermes 4 70B | nebius/NousResearch/Hermes-4-70B | 0.13 | 0.40 |
| nous/hermes-3-llama-3.1-405b | Hermes 3 405B | nebius/NousResearch/Hermes-3-Llama-3.1-405B | 1.00 | 3.00 |

**Vision Models (new):**

| model_id | label | gateway_model_id | input $/M | output $/M |
|----------|-------|-------------------|-----------|------------|
| google/gemma-3-27b-it | Gemma 3 27B Vision | nebius/google/gemma-3-27b-it | 0.10 | 0.30 |
| qwen/qwen2-vl-72b | Qwen2 VL 72B | nebius/Qwen/Qwen2-VL-72B | 0.13 | 0.40 |

**Embedding Models (new):**

| model_id | label | gateway_model_id | input $/M | output $/M |
|----------|-------|-------------------|-----------|------------|
| baai/bge-en-icl | BGE EN ICL | nebius/BAAI/bge-en-icl | 0.01 | 0.00 |
| intfloat/multilingual-e5-large-instruct | E5 Large Multilingual | nebius/intfloat/multilingual-e5-large-instruct | 0.01 | 0.00 |
| baai/bge-m3 | BGE M3 | nebius/BAAI/bge-m3 | 0.01 | 0.00 |

**Guardrails (new):**

| model_id | label | gateway_model_id | input $/M | output $/M |
|----------|-------|-------------------|-----------|------------|
| meta/llama-guard-3-8b | Llama Guard 3 8B | nebius/meta-llama/Llama-Guard-3-8B | 0.02 | 0.06 |

### Pricing conversion

Prices above are in $/M tokens. Microcents per token = price_per_m * 1,000,000 / 1,000,000 = price_per_m * 1 (since 1 microcent = $0.00000001, and $/M = $/1,000,000 tokens).

Formula: `cost_input_microcents = input_price_per_m * 1_000_000 / 1_000_000 * 100 * 10_000`
Simplified: `cost_microcents_per_token = price_per_m_dollars * 100_000_000 / 1_000_000`

Example: $0.15/M → 0.15 * 100 * 10_000 = 150,000 microcents per M tokens → stored as 100,000 (per the existing convention where cost = tokens * microcents / 1,000,000).

Using existing convention from migration 041: $0.15/M input → 100,000 microcents, $0.60/M output → 400,000 microcents.

## Store Changes

### New methods on BillingRepo interface and PostgresStore

Replace `token_usage` queries with `opik_spans` queries. Reuse existing return types (`LLMUsage`, `UsageBucket`).

**Interface changes** (`store.go` — `BillingRepo` interface):
- Add: `GetOpikSpendByMachine(ctx, machineID) → (int64, error)`
- Add: `GetOpikUsageByMachine(ctx, machineID, since, limit) → ([]LLMUsage, error)`
- Add: `GetOpikUsageBreakdown(ctx, machineID, period, since) → ([]UsageBucket, error)`

**GetOpikSpendByMachine(ctx, machineID) → int64**
```sql
SELECT COALESCE(SUM(
    COALESCE(s.prompt_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
    + COALESCE(s.completion_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
)::bigint, 0)
FROM opik_spans s
LEFT JOIN LATERAL (
    SELECT cost_input_microcents, cost_output_microcents, margin
    FROM model_pricing_history
    WHERE model_id = s.model AND effective_from <= s.start_time
    ORDER BY effective_from DESC LIMIT 1
) p ON true
WHERE s.machine_id = $1
  AND s.start_time >= date_trunc('month', now())
  AND s.type = 'llm'
```

**GetOpikUsageByMachine(ctx, machineID, since, limit) → []LLMUsage**
Maps opik span fields to LLMUsage:
- `prompt_tokens` → `input_tokens`
- `completion_tokens` → `output_tokens`
- `start_time` → `created_at`
- `model`, `provider` → direct
- `LLMUsage.ID` → 0 (opik span ID is UUID, not int64; ID is not used by frontend)
- `LLMUsage.Source` → empty string (opik span `source` is not the billing classification; field not displayed by frontend)
- cost computed via pricing JOIN (0 for models without pricing)

**GetOpikUsageBreakdown(ctx, machineID, period, since) → []UsageBucket**
Groups by `date_trunc(period, start_time)`, `provider`, `model`.
- `UsageBucketEntry.Source` → empty string (not used in display)
- Same bucket structure as current.

### Filter: type = 'llm'

Only spans with `type = 'llm'` represent actual LLM API calls with token usage. Tool calls (`type = 'tool'`) and general spans have no model/usage data and are excluded from billing.

## API Changes

### billing.go handler updates

**`handleGetAccountUsage`**: Currently loops `ListMachinesByAccount` and calls `GetLLMSpendByMachine` per machine to build `per_machine` array. Change: swap `GetLLMSpendByMachine` → `GetOpikSpendByMachine` inside the loop. Response shape unchanged.

**`handleGetMachineUsage`**: Swap `GetLLMSpendByMachine` → `GetOpikSpendByMachine` and `GetLLMUsageByMachine` → `GetOpikUsageByMachine`.

**`handleGetMachineUsageBreakdown`**: Swap `GetUsageBreakdown` → `GetOpikUsageBreakdown`.

Budget handlers (SetBudget, DeleteBudget) unchanged — they operate on the `machines` table.

### Mock/test updates

- Update `BillingRepo` interface in `store.go` with new method signatures
- Update mock stores in test files (`runtime_test.go`, etc.) to implement new methods
- Add unit tests for new store methods

## Frontend Changes

None. Response shapes are identical. Fields that change semantically (`id`, `source`) are not used by the frontend components.

## Testing

- Unit tests for new store methods using mock data
- Verify `LLMUsage` and `UsageBucket` field mapping from opik spans
- Verify cost = 0 for non-Nebius models
- Gateway E2E tests still pass (unrelated to billing data source)
- Manual verification: start a machine, trigger an LLM call, check dashboard shows data
