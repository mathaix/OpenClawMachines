# Versioned Model Pricing — Design Spec

## Goal

Decouple cost computation from the token write path. Store raw token counts as the source of truth, track versioned model pricing in a separate history table, and compute costs at query time by joining tokens with the pricing that was active at the time of the request.

## Context

Currently, `agent_auth.go` computes `cost_input_usd` and `cost_output_usd` at write time using prices from `model_catalog`, and stores them alongside token counts in `token_usage`. Read queries convert these stored USD values to microcents. This makes price changes impossible to apply retroactively and couples the write path to pricing logic.

Only platform models (Nebius, `source = 'platform'`) are priced. BYOK and subscription models are pass-through — we track token counts but don't compute cost.

## Architecture

### New Table: `model_pricing_history`

```sql
CREATE TABLE model_pricing_history (
    id BIGSERIAL PRIMARY KEY,
    model_id TEXT NOT NULL,
    cost_input_microcents BIGINT NOT NULL,
    cost_output_microcents BIGINT NOT NULL,
    margin NUMERIC(6,4) NOT NULL DEFAULT 1.0,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_model_pricing_history_lookup
    ON model_pricing_history (model_id, effective_from DESC);
```

- `model_id` — references `model_catalog(id)`, e.g. `qwen/qwen3.5-397b`
- `cost_input_microcents` / `cost_output_microcents` — actual provider cost per 1M tokens
- `margin` — multiplier applied on top of actual cost. `1.0` = pass-through (default), `1.3` = 30% markup
- `effective_from` — when this price takes effect. Query-time lookup finds the latest row where `effective_from <= usage.created_at`
- Only platform models get rows. BYOK/subscription models have no pricing history (cost = 0 in queries).

### Changes to `model_catalog`

Remove pricing columns — pricing now lives exclusively in `model_pricing_history`:

```sql
ALTER TABLE model_catalog
    DROP COLUMN cost_input_microcents,
    DROP COLUMN cost_output_microcents;
```

`input_price_per_m` and `output_price_per_m` stay — they're human-readable display values ($/M tokens) used in the frontend model picker. They are not used in cost computation.

### Changes to `token_usage`

Drop the pre-computed cost columns:

```sql
ALTER TABLE token_usage
    DROP COLUMN cost_input_usd,
    DROP COLUMN cost_output_usd;
```

Token counts (`input_tokens`, `output_tokens`) are the sole source of truth.

### Migration Seed Data

Seed `model_pricing_history` with current platform model prices, backdated to epoch so all historical `token_usage` rows get accurate pricing:

```sql
INSERT INTO model_pricing_history (model_id, cost_input_microcents, cost_output_microcents, margin, effective_from)
SELECT id, cost_input_microcents, cost_output_microcents, 1.0, '1970-01-01T00:00:00Z'
FROM model_catalog
WHERE source = 'platform';
```

This runs before the `model_catalog` columns are dropped.

### Write Path Changes

**`agent_auth.go`** — Remove cost computation. The INSERT becomes:

```sql
INSERT INTO token_usage (account_id, machine_id, provider, model, input_tokens, output_tokens, source)
VALUES ($1, $2, $3, $4, $5, $6, $7)
```

Remove the `catalogEntry.CostInputMicrocents` / `CostOutputMicrocents` lookup and multiplication. The `GetModelCatalogEntry` call may still be needed for other purposes (validation, gateway model ID), but cost fields are gone.

### Read Path Changes

The 5 SQL queries in `postgres.go` that currently compute cost as:

```sql
(COALESCE(cost_input_usd, 0) + COALESCE(cost_output_usd, 0)) * 1000000
```

Change to a lateral join against `model_pricing_history`:

```sql
LEFT JOIN LATERAL (
    SELECT cost_input_microcents, cost_output_microcents, margin
    FROM model_pricing_history
    WHERE model_id = t.model
      AND effective_from <= t.created_at
    ORDER BY effective_from DESC
    LIMIT 1
) p ON true
```

Cost microcents per row:

```sql
COALESCE(
    (t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000)
    + (t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000),
    0
)
```

This returns 0 for BYOK/subscription models (no pricing history row → `p` is NULL → COALESCE to 0).

### `ModelCatalogEntry` Struct

Remove `CostInputMicrocents` and `CostOutputMicrocents` fields. Keep `InputPricePerM` and `OutputPricePerM` (display values).

### Budget Enforcement

The budget check in `agent_auth.go` currently uses `GetMachineUsageMicrocents` which reads stored costs. After this change, it uses the same lateral join, so budget enforcement automatically picks up the correct historical price. No separate change needed.

### Price Updates

To change a platform model's price:

1. Insert a new row into `model_pricing_history` with the new prices and `effective_from = now()`
2. Update `input_price_per_m` / `output_price_per_m` in `model_catalog` for display consistency

No API endpoint is needed initially — this is a direct database operation. An admin API can be added later.

## What Changes

| Component | Change |
|-----------|--------|
| `model_pricing_history` table | **Create** — new table with seed data |
| `model_catalog` table | **Drop** `cost_input_microcents`, `cost_output_microcents` |
| `token_usage` table | **Drop** `cost_input_usd`, `cost_output_usd` |
| `agent_auth.go` | **Remove** cost computation from write path |
| `postgres.go` (5 queries) | **Replace** stored cost reads with lateral join |
| `store.go` `ModelCatalogEntry` | **Remove** cost microcent fields |
| `store.go` | **Add** `ModelPricingHistory` type and repo methods |
| `models.go` API handler | **Update** to not return dropped fields |
| Frontend model display | **No change** — uses `input_price_per_m` / `output_price_per_m` |
| Frontend usage tab | **No change** — receives microcents from API as before |

## What Stays the Same

- `token_usage` table structure (minus dropped columns)
- Frontend `UsageTab` — still receives microcents from the API
- Session poller — tracks tokens, not costs
- `input_price_per_m` / `output_price_per_m` in `model_catalog` — display-only
- Budget enforcement flow — same query, different cost source

## Out of Scope

- Admin API for managing pricing versions (database-direct for now)
- Scheduled/future-dated pricing (immediate only)
- Per-user or per-machine pricing overrides
- BYOK/subscription model pricing
