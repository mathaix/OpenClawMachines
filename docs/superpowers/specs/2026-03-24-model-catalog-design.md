# Model Catalog — DB-Driven Single Source of Truth

## Problem

Model definitions are hardcoded in 4+ places (frontend types.ts, backend allowedModels, platformModelMap, pricing.go). Adding or changing a model requires code changes and redeployment.

## Solution

Single `model_catalog` DB table that both frontend and backend read from.

## Schema (Migration 041)

```sql
CREATE TABLE model_catalog (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  description TEXT NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('platform', 'byok', 'subscription')),
  tier TEXT CHECK (tier IN ('smart', 'balanced', 'fast')),
  input_price_per_m NUMERIC(10,4) NOT NULL DEFAULT 0,
  output_price_per_m NUMERIC(10,4) NOT NULL DEFAULT 0,
  cost_input_microcents BIGINT NOT NULL DEFAULT 0,
  cost_output_microcents BIGINT NOT NULL DEFAULT 0,
  gateway_model_id TEXT,
  provider TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  sort_order INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Seeded with all 11 current models (3 platform, 7 BYOK, 1 subscription).

## Backend

**Store layer:**
- `ListModelCatalog(ctx) ([]ModelCatalogEntry, error)` — all enabled models, ordered by sort_order
- `GetModelCatalogEntry(ctx, modelID) (*ModelCatalogEntry, error)` — single model lookup

**API endpoint:** `GET /api/models` (JWT-authenticated) — returns catalog for frontend (omits backend-only fields like microcent costs and gateway_model_id).

**Replaces:**
- `allowedModels` map → `GetModelCatalogEntry()` nil check
- `platformModelMap` in assembler.go → `entry.GatewayModelID`
- `pricingTable` in pricing.go → `entry.CostInputMicrocents/CostOutputMicrocents`
- Nebius model definitions in assembler.go → catalog entries where provider="nebius"

## Frontend

- Remove `PLATFORM_TIERS`, `BYOK_MODELS`, `SUBSCRIPTION_MODELS`, `ALL_MODELS` from types.ts
- Add `listModels()` API call
- ModelPicker receives `models: ModelEntry[]` as prop (fetched by MachineView)
- Grouping/rendering logic unchanged — just data source moves from imports to API

## Management

Models managed via SQL migrations. Admin UI deferred to later.
