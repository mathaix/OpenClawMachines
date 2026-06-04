# Model Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace all hardcoded model definitions with a single `model_catalog` DB table that both frontend and backend read from.

**Architecture:** New `model_catalog` table seeded via migration. Backend store methods expose the catalog. New `GET /api/models` endpoint serves the catalog to the frontend. Backend validation, config assembly, and pricing all read from the catalog instead of hardcoded maps.

**Tech Stack:** PostgreSQL migration, Go store/API layer, TypeScript frontend API call + ModelPicker refactor.

**Spec:** `docs/superpowers/specs/2026-03-24-model-catalog-design.md`

---

### Task 1: Migration — Create and seed `model_catalog` table

**Files:**
- Create: `backend/migrations/041_model_catalog.sql`

- [ ] **Step 1: Write the migration**

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

INSERT INTO model_catalog (id, label, description, source, tier, input_price_per_m, output_price_per_m, cost_input_microcents, cost_output_microcents, gateway_model_id, provider, sort_order) VALUES
  ('qwen/qwen3.5-397b', 'Smart', 'Qwen 3.5 397B — Deep reasoning', 'platform', 'smart', 0.60, 3.60, 400000, 2400000, 'nebius/Qwen/Qwen3.5-397B-A17B', 'nebius', 1),
  ('minimax/minimax-m2.5', 'Balanced', 'MiniMax M2.5 — Agentic coding', 'platform', 'balanced', 0.30, 1.20, 200000, 800000, 'nebius/MiniMaxAI/MiniMax-M2.5', 'nebius', 2),
  ('openai/gpt-oss-120b', 'Fast', 'GPT OSS 120B — Fast and capable', 'platform', 'fast', 0.15, 0.60, 100000, 400000, 'nebius/openai/gpt-oss-120b', 'nebius', 3),
  ('anthropic/claude-sonnet-4-6', 'Claude Sonnet 4.6', 'Anthropic', 'byok', NULL, 3.00, 15.00, 3000000, 15000000, NULL, 'anthropic', 10),
  ('anthropic/claude-opus-4-6', 'Claude Opus 4.6', 'Anthropic', 'byok', NULL, 15.00, 75.00, 15000000, 75000000, NULL, 'anthropic', 11),
  ('openai/gpt-4o', 'GPT-4o', 'OpenAI', 'byok', NULL, 2.50, 10.00, 2500000, 10000000, NULL, 'openai', 12),
  ('openai/gpt-4o-mini', 'GPT-4o Mini', 'OpenAI', 'byok', NULL, 0.15, 0.60, 150000, 600000, NULL, 'openai', 13),
  ('google/gemini-2.5-flash-preview-05-20', 'Gemini 2.5 Flash', 'Google', 'byok', NULL, 0.15, 0.60, 150000, 600000, NULL, 'google', 14),
  ('google/gemini-2.5-pro-preview-05-06', 'Gemini 2.5 Pro', 'Google', 'byok', NULL, 1.25, 10.00, 1250000, 10000000, NULL, 'google', 15),
  ('google/gemini-2.0-flash', 'Gemini 2.0 Flash', 'Google', 'byok', NULL, 0.075, 0.30, 75000, 300000, NULL, 'google', 16),
  ('openai-codex/gpt-5.4', 'GPT-5.4', 'ChatGPT subscription', 'subscription', NULL, 0, 0, 0, 0, NULL, 'openai-codex', 20);
```

- [ ] **Step 2: Commit**

```bash
git add backend/migrations/041_model_catalog.sql
git commit -m "feat: add model_catalog migration (041)"
```

---

### Task 2: Store layer — `ModelCatalogRepo` interface and Postgres implementation

**Files:**
- Modify: `backend/internal/store/store.go` — add `ModelCatalogEntry` struct and `ModelCatalogRepo` interface, add to `Store` composite
- Modify: `backend/internal/store/postgres.go` — implement `ListModelCatalog` and `GetModelCatalogEntry`
- Create: `backend/internal/store/model_catalog_test.go` — test both methods

- [ ] **Step 1: Write the failing tests**

In `model_catalog_test.go`, write table-driven tests:
- `TestListModelCatalog` — seeds the table, calls `ListModelCatalog`, asserts all enabled models returned in sort_order, disabled models excluded
- `TestGetModelCatalogEntry` — asserts correct fields returned for a known model, returns nil for unknown model, returns nil for disabled model

These tests should use the same test DB setup pattern as other store tests in the project.

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test-go`
Expected: FAIL — `ModelCatalogRepo` not defined, methods not implemented.

- [ ] **Step 3: Add the struct and interface to `store.go`**

Add `ModelCatalogEntry` struct with fields matching the DB schema. Add `ModelCatalogRepo` interface with:
```go
type ModelCatalogRepo interface {
    ListModelCatalog(ctx context.Context) ([]ModelCatalogEntry, error)
    GetModelCatalogEntry(ctx context.Context, modelID string) (*ModelCatalogEntry, error)
}
```
Add `ModelCatalogRepo` to the `Store` composite interface.

- [ ] **Step 4: Implement in `postgres.go`**

`ListModelCatalog`: `SELECT * FROM model_catalog WHERE enabled = true ORDER BY sort_order`
`GetModelCatalogEntry`: `SELECT * FROM model_catalog WHERE id = $1 AND enabled = true`

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test-go`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go backend/internal/store/model_catalog_test.go
git commit -m "feat: add ModelCatalogRepo store interface and Postgres implementation"
```

---

### Task 3: API endpoint — `GET /api/models`

**Files:**
- Create: `backend/internal/api/models.go` — `handleListModels` handler
- Modify: `backend/internal/api/server.go` — register route
- Create: `backend/internal/api/models_test.go` — handler test

- [ ] **Step 1: Write the failing test**

In `models_test.go`, test that `GET /api/models` returns the seeded models as JSON array. Each entry should have: `id`, `label`, `description`, `source`, `tier`, `input_price_per_m`, `output_price_per_m`, `sort_order`. Should NOT include backend-only fields: `cost_input_microcents`, `cost_output_microcents`, `gateway_model_id`, `provider`.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-go`
Expected: FAIL — handler not defined.

- [ ] **Step 3: Implement `handleListModels` in `models.go`**

```go
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
    models, err := s.store.ListModelCatalog(r.Context())
    if err != nil {
        writeError(w, http.StatusInternalServerError, "failed to list models")
        return
    }

    type modelResponse struct {
        ID              string   `json:"id"`
        Label           string   `json:"label"`
        Description     string   `json:"description"`
        Source          string   `json:"source"`
        Tier            *string  `json:"tier,omitempty"`
        InputPricePerM  float64  `json:"input_price_per_m"`
        OutputPricePerM float64  `json:"output_price_per_m"`
        SortOrder       int      `json:"sort_order"`
    }

    resp := make([]modelResponse, len(models))
    for i, m := range models {
        resp[i] = modelResponse{
            ID:              m.ID,
            Label:           m.Label,
            Description:     m.Description,
            Source:          m.Source,
            Tier:            m.Tier,
            InputPricePerM:  m.InputPricePerM,
            OutputPricePerM: m.OutputPricePerM,
            SortOrder:       m.SortOrder,
        }
    }
    writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 4: Register route in `server.go`**

Add inside the authenticated group, alongside other non-account-scoped routes:
```go
r.Get("/api/models", srv.handleListModels)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test-go`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add backend/internal/api/models.go backend/internal/api/models_test.go backend/internal/api/server.go
git commit -m "feat: add GET /api/models endpoint"
```

---

### Task 4: Backend — Replace `allowedModels` with DB lookup

**Files:**
- Modify: `backend/internal/api/machine_config.go` — replace `allowedModels` map with `store.GetModelCatalogEntry()` call
- Modify: `backend/internal/api/machine_config_test.go` — update tests if needed

- [ ] **Step 1: Remove `allowedModels` map**

Delete the `var allowedModels = map[string]bool{...}` block.

- [ ] **Step 2: Update `handleSetMachineModel` validation**

Replace:
```go
if !allowedModels[req.Model] {
    writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not allowed", req.Model))
    return
}
```

With:
```go
entry, err := s.store.GetModelCatalogEntry(r.Context(), req.Model)
if err != nil || entry == nil {
    writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not allowed", req.Model))
    return
}
```

- [ ] **Step 3: Run tests**

Run: `make test-go`
Expected: PASS (existing tests should still pass since the DB is seeded with the same models)

- [ ] **Step 4: Commit**

```bash
git add backend/internal/api/machine_config.go
git commit -m "refactor: replace hardcoded allowedModels with DB lookup"
```

---

### Task 5: Backend — Replace `platformModelMap` with DB lookup in config assembly

**Files:**
- Modify: `backend/internal/configassembly/assembler.go` — replace `platformModelMap`, `MapPlatformModel()`, and `nebiusModelDefs`/`buildNebiusModelsList()` with store-driven lookups
- Modify: `backend/internal/configassembly/assembler_test.go` — update tests
- Modify: `backend/internal/api/machine_config.go` — pass catalog entries to assembler

The assembler is called by the API layer which has store access. The assembler doesn't have direct store access itself.

- [ ] **Step 1: Add catalog entries to `AssembleParams`**

In `assembler.go`, add a field to the params struct that `assembleConfigForMachine` passes in:
```go
ModelCatalog []ModelCatalogEntry // passed from API layer
```

Where `ModelCatalogEntry` is a struct local to configassembly (or imported from store).

- [ ] **Step 2: Replace `MapPlatformModel()` with catalog lookup**

New function that looks up `gateway_model_id` from the catalog entries:
```go
func mapModelFromCatalog(catalog []ModelCatalogEntry, modelID string) string {
    for _, m := range catalog {
        if m.ID == modelID && m.GatewayModelID != nil {
            return *m.GatewayModelID
        }
    }
    return modelID // BYOK models pass through unchanged
}
```

- [ ] **Step 3: Replace `buildNebiusModelsList()` with catalog-driven builder**

Build the Nebius models list from catalog entries where `provider = "nebius"`:
```go
func buildNebiusModelsFromCatalog(catalog []ModelCatalogEntry) []interface{} {
    var models []interface{}
    for _, m := range catalog {
        if m.GatewayModelID == nil {
            continue
        }
        // Strip "nebius/" prefix to get the Nebius model ID
        nebiusID := strings.TrimPrefix(*m.GatewayModelID, "nebius/")
        models = append(models, map[string]interface{}{
            "id":            nebiusID,
            "name":          m.Label,
            "api":           "openai-completions",
            "reasoning":     m.Tier != nil && *m.Tier == "smart",
            "input":         []string{"text"},
            "contextWindow": 131072,
            "maxTokens":     8192,
        })
    }
    return models
}
```

- [ ] **Step 4: Update `assembleConfigForMachine` in `machine_config.go` to pass catalog**

Load catalog from store and pass to assembler:
```go
catalog, err := s.store.ListModelCatalog(ctx)
// ... pass to AssembleConfig via params
```

- [ ] **Step 5: Delete old `platformModelMap`, `MapPlatformModel()`, `nebiusModelDefs`, `buildNebiusModelsList()`**

- [ ] **Step 6: Update tests**

Update assembler tests to pass catalog entries instead of relying on hardcoded maps.

- [ ] **Step 7: Run tests**

Run: `make test-go`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add backend/internal/configassembly/assembler.go backend/internal/configassembly/assembler_test.go backend/internal/api/machine_config.go
git commit -m "refactor: replace hardcoded platformModelMap and nebiusModelDefs with DB catalog"
```

---

### Task 6: Backend — Replace `pricingTable` with DB lookup

**Files:**
- Modify: `backend/internal/apiproxy/pricing.go` — make `CalculateCost` use DB pricing
- Modify: `backend/internal/apiproxy/proxy.go` — inject pricing data at proxy startup
- Modify: `backend/internal/apiproxy/streaming.go` — update calls if signature changes

The proxy runs on the agent (GCE VM), no direct DB access. But the proxy already reports raw token counts to the backend via `reportUsageToBackend`. The backend can calculate cost server-side from the DB.

**Approach:** Move cost calculation to the backend's `handleAgentAuthRecordUsage` handler. The proxy keeps sending `(provider, model, input_tokens, output_tokens, source)` — no change to the proxy-to-backend protocol. The backend looks up pricing from the model catalog and stores cost.

- [ ] **Step 1: Update `handleAgentAuthRecordUsage` in `agent_auth.go`**

After decoding the request, look up pricing from the model catalog and calculate cost:
```go
// Look up pricing from model catalog
var costMicrocents int64
catalogEntry, _ := s.store.GetModelCatalogEntry(r.Context(), req.Model)
if catalogEntry != nil {
    costMicrocents = int64(req.InputTokens)*catalogEntry.CostInputMicrocents/1_000_000 +
        int64(req.OutputTokens)*catalogEntry.CostOutputMicrocents/1_000_000
}
```

Store cost in the `TokenUsageRecord`. Check if `TokenUsageRecord` and `RecordTokenUsage` already handle a cost field — if not, add one.

- [ ] **Step 2: Remove `pricingTable` and `CalculateCost` from `pricing.go`**

Delete the hardcoded map and function. The proxy's log lines can drop the cost or use 0.

- [ ] **Step 3: Clean up proxy logging**

Remove `cost` from proxy log messages in `streaming.go`, or keep it as 0 since it's just cosmetic.

- [ ] **Step 4: Run tests**

Run: `make test-go`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/api/agent_auth.go backend/internal/apiproxy/pricing.go backend/internal/apiproxy/streaming.go
git commit -m "refactor: move cost calculation from proxy to backend using DB pricing"
```

---

### Task 7: Backend — Replace `usageSource()` with DB lookup

**Files:**
- Modify: `backend/internal/apiproxy/streaming.go` — remove hardcoded `usageSource()` switch

The `source` field ("platform"/"byok"/"subscription") is already sent by the proxy to the backend. But the proxy currently derives it from a hardcoded provider→source mapping. Since the proxy doesn't have DB access, we can either:
- Keep the proxy's `usageSource()` as-is (it's simple, rarely changes)
- Or derive source from the model catalog in the backend's `handleAgentAuthRecordUsage`

**Approach:** Derive source in the backend from the catalog entry's `source` field. The proxy can still send a source hint, but the backend overrides it with the authoritative DB value.

- [ ] **Step 1: Update `handleAgentAuthRecordUsage` to derive source from catalog**

```go
if catalogEntry != nil {
    record.Source = catalogEntry.Source
}
```

- [ ] **Step 2: Run tests**

Run: `make test-go`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/agent_auth.go
git commit -m "refactor: derive usage source from model catalog in backend"
```

---

### Task 8: Frontend — Fetch models from API and update ModelPicker

**Files:**
- Modify: `frontend/src/lib/api.ts` — add `listModels()` function
- Modify: `frontend/src/lib/types.ts` — remove hardcoded model arrays, keep `ModelEntry` interface
- Modify: `frontend/src/components/ModelPicker.tsx` — accept `models` prop instead of importing constants
- Modify: `frontend/src/pages/MachineView.tsx` — fetch models on mount, pass to ModelPicker

- [ ] **Step 1: Add `listModels` to `api.ts`**

```typescript
export const listModels = () =>
  request<ModelEntry[]>("/models");
```

- [ ] **Step 2: Update `ModelEntry` interface in `types.ts` to match API response**

Keep the interface, add `sort_order`. Remove `PLATFORM_TIERS`, `BYOK_MODELS`, `SUBSCRIPTION_MODELS`, `ALL_MODELS`.

```typescript
export interface ModelEntry {
  id: string;
  label: string;
  description: string;
  source: "platform" | "byok" | "subscription";
  tier?: "smart" | "balanced" | "fast";
  input_price_per_m: number;
  output_price_per_m: number;
  sort_order: number;
}
```

Note: field names change from camelCase to snake_case to match API response. Update all references.

- [ ] **Step 3: Update `ModelPicker.tsx`**

Change props to accept models:
```typescript
interface ModelPickerProps {
  value: string;
  onChange: (modelId: string) => void;
  disabled?: boolean;
  configuredProviders?: Set<string>;
  models: ModelEntry[];
}
```

Derive groups from the `models` prop:
```typescript
const platformTiers = models.filter(m => m.source === "platform");
const byokModels = models.filter(m => m.source === "byok");
const subscriptionModels = models.filter(m => m.source === "subscription");
```

Update price display references from `m.inputPricePerM` to `m.input_price_per_m` (or use a helper).

Replace `ALL_MODELS.find(...)` for selected display with `models.find(...)`.

- [ ] **Step 4: Update `MachineView.tsx`**

Add state and fetch:
```typescript
const [models, setModels] = useState<ModelEntry[]>([]);

useEffect(() => {
  listModels().then(setModels).catch(() => {});
}, []);
```

Pass to ModelPicker:
```typescript
<ModelPicker
  value={currentModel}
  onChange={handleModelChange}
  disabled={modelSaving}
  configuredProviders={configuredProviders}
  models={models}
/>
```

- [ ] **Step 5: Run frontend typecheck**

Run: `make typecheck`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/lib/types.ts frontend/src/components/ModelPicker.tsx frontend/src/pages/MachineView.tsx
git commit -m "feat: fetch models from API, remove hardcoded model lists"
```

---

### Task 9: Verify end-to-end

- [ ] **Step 1: Run full test suite**

Run: `make test-go && make test-frontend && make typecheck`
Expected: All pass.

- [ ] **Step 2: Run gateway E2E tests**

Run: `make test-gateway-e2e`
Expected: PASS — config assembly still produces valid configs.

- [ ] **Step 3: Grep for leftover hardcoded references**

```bash
grep -r "allowedModels\|platformModelMap\|pricingTable\|PLATFORM_TIERS\|BYOK_MODELS\|SUBSCRIPTION_MODELS\|ALL_MODELS" --include='*.go' --include='*.ts' --include='*.tsx' backend/ frontend/
```

Expected: No matches (except possibly in test fixtures or this plan doc).

- [ ] **Step 4: Commit any final cleanup**
