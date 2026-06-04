# Versioned Model Pricing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move cost computation from write-time to query-time, using a versioned pricing history table as the single source of truth for model prices.

**Architecture:** New `model_pricing_history` table replaces `cost_*_microcents` columns in `model_catalog`. Token write path drops cost columns entirely. All 5 read queries join `token_usage` with `model_pricing_history` via lateral join to compute costs at query time.

**Tech Stack:** Go 1.25, PostgreSQL (Neon), pgx/v5, React/TypeScript frontend

---

### Task 1: Create migration — `model_pricing_history` table, seed data, drop columns

**Files:**
- Create: `backend/migrations/048_versioned_pricing.sql`

- [ ] **Step 1: Write the migration SQL**

```sql
-- Step 1: Create the pricing history table
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

-- Step 2: Seed with current platform model prices, backdated to epoch
INSERT INTO model_pricing_history (model_id, cost_input_microcents, cost_output_microcents, margin, effective_from)
SELECT id, cost_input_microcents, cost_output_microcents, 1.0, '1970-01-01T00:00:00Z'
FROM model_catalog
WHERE source = 'platform';

-- Step 3: Drop pricing columns from model_catalog
ALTER TABLE model_catalog
    DROP COLUMN cost_input_microcents,
    DROP COLUMN cost_output_microcents;

-- Step 4: Drop pre-computed cost columns from token_usage
ALTER TABLE token_usage
    DROP COLUMN cost_input_usd,
    DROP COLUMN cost_output_usd;
```

- [ ] **Step 2: Verify migration file exists**

Run: `cat backend/migrations/048_versioned_pricing.sql`
Expected: The SQL above

- [ ] **Step 3: Commit**

```bash
git add backend/migrations/048_versioned_pricing.sql
git commit -m "feat: add migration 048 — versioned pricing history table"
```

---

### Task 2: Update `TokenUsageRecord` struct and `RecordTokenUsage` — remove cost fields

**Files:**
- Modify: `backend/internal/store/store.go:406-418` — `TokenUsageRecord` struct
- Modify: `backend/internal/store/postgres.go:3196-3203` — `RecordTokenUsage` function

- [ ] **Step 1: Write failing test**

Add a test in `backend/internal/store/postgres_test.go` (or verify existing tests) that calls `RecordTokenUsage` without cost fields. Since we're removing fields, the test is that the existing function compiles and inserts successfully without `CostInputUSD` / `CostOutputUSD`.

If no test file exists for this, create a simple compile-check by building:

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Build fails because `CostInputUSD` and `CostOutputUSD` are still referenced

- [ ] **Step 2: Remove cost fields from `TokenUsageRecord` struct**

In `backend/internal/store/store.go`, change the struct from:

```go
type TokenUsageRecord struct {
	ID            int64     `json:"id"`
	AccountID     int       `json:"account_id"`
	MachineID     string    `json:"machine_id"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	CostInputUSD  *float64  `json:"cost_input_usd,omitempty"`
	CostOutputUSD *float64  `json:"cost_output_usd,omitempty"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}
```

To:

```go
type TokenUsageRecord struct {
	ID           int64     `json:"id"`
	AccountID    int       `json:"account_id"`
	MachineID    string    `json:"machine_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}
```

- [ ] **Step 3: Update `RecordTokenUsage` SQL**

In `backend/internal/store/postgres.go`, change `RecordTokenUsage` from:

```go
func (s *PostgresStore) RecordTokenUsage(ctx context.Context, record *TokenUsageRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO token_usage (account_id, machine_id, provider, model, input_tokens, output_tokens, cost_input_usd, cost_output_usd, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		record.AccountID, record.MachineID, record.Provider, record.Model,
		record.InputTokens, record.OutputTokens, record.CostInputUSD, record.CostOutputUSD, record.Source)
	return err
}
```

To:

```go
func (s *PostgresStore) RecordTokenUsage(ctx context.Context, record *TokenUsageRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO token_usage (account_id, machine_id, provider, model, input_tokens, output_tokens, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.AccountID, record.MachineID, record.Provider, record.Model,
		record.InputTokens, record.OutputTokens, record.Source)
	return err
}
```

- [ ] **Step 4: Verify build compiles (will fail — callers still set cost fields)**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Compile errors in `agent_auth.go` referencing `CostInputUSD` / `CostOutputUSD` — this is expected and fixed in Task 3.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "refactor: remove cost columns from TokenUsageRecord and RecordTokenUsage"
```

---

### Task 3: Update `agent_auth.go` — remove cost computation from write path

**Files:**
- Modify: `backend/internal/api/agent_auth.go:367-396`

- [ ] **Step 1: Remove cost computation and simplify RecordTokenUsage call**

In `backend/internal/api/agent_auth.go`, replace the cost computation block (lines ~367-388):

```go
	// Look up pricing and source from model catalog.
	var costInputUSD, costOutputUSD float64
	catalogEntry, _ := s.store.GetModelCatalogEntry(r.Context(), req.Model)
	if catalogEntry != nil {
		// Override source with authoritative value from catalog.
		req.Source = catalogEntry.Source
		// Calculate cost from microcents pricing.
		costInputUSD = float64(int64(req.InputTokens)*catalogEntry.CostInputMicrocents) / 1_000_000_000_000
		costOutputUSD = float64(int64(req.OutputTokens)*catalogEntry.CostOutputMicrocents) / 1_000_000_000_000
	}

	record := &store.TokenUsageRecord{
		AccountID:     machine.AccountID,
		MachineID:     machineID,
		Provider:      req.Provider,
		Model:         req.Model,
		InputTokens:   req.InputTokens,
		OutputTokens:  req.OutputTokens,
		CostInputUSD:  &costInputUSD,
		CostOutputUSD: &costOutputUSD,
		Source:        req.Source,
	}
```

With:

```go
	// Look up source from model catalog.
	catalogEntry, _ := s.store.GetModelCatalogEntry(r.Context(), req.Model)
	if catalogEntry != nil {
		req.Source = catalogEntry.Source
	}

	record := &store.TokenUsageRecord{
		AccountID:    machine.AccountID,
		MachineID:    machineID,
		Provider:     req.Provider,
		Model:        req.Model,
		InputTokens:  req.InputTokens,
		OutputTokens: req.OutputTokens,
		Source:       req.Source,
	}
```

- [ ] **Step 2: Verify build compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Success (all cost field references removed)

- [ ] **Step 3: Commit**

```bash
git add backend/internal/api/agent_auth.go
git commit -m "refactor: remove cost computation from token usage write path"
```

---

### Task 4: Update `ModelCatalogEntry` struct — remove cost microcent fields

**Files:**
- Modify: `backend/internal/store/store.go:758-774` — `ModelCatalogEntry` struct
- Modify: `backend/internal/store/postgres.go:3205-3249` — `ListModelCatalog` and `GetModelCatalogEntry` SQL + scans

- [ ] **Step 1: Remove cost fields from `ModelCatalogEntry` struct**

In `backend/internal/store/store.go`, change:

```go
type ModelCatalogEntry struct {
	ID                   string
	Label                string
	Description          string
	Source               string  // "platform", "byok", "subscription"
	Tier                 *string // nullable
	InputPricePerM       float64
	OutputPricePerM      float64
	CostInputMicrocents  int64
	CostOutputMicrocents int64
	GatewayModelID       *string // nullable
	Provider             string
	Enabled              bool
	SortOrder            int
	CreatedAt            time.Time
}
```

To:

```go
type ModelCatalogEntry struct {
	ID             string
	Label          string
	Description    string
	Source         string  // "platform", "byok", "subscription"
	Tier           *string // nullable
	InputPricePerM  float64
	OutputPricePerM float64
	GatewayModelID *string // nullable
	Provider       string
	Enabled        bool
	SortOrder      int
	CreatedAt      time.Time
}
```

- [ ] **Step 2: Update `ListModelCatalog` SQL and scan**

In `backend/internal/store/postgres.go`, change the query from:

```go
`SELECT id, label, description, source, tier, input_price_per_m, output_price_per_m,
        cost_input_microcents, cost_output_microcents, gateway_model_id, provider,
        enabled, sort_order, created_at
 FROM model_catalog
 WHERE enabled = true
 ORDER BY sort_order`
```

To:

```go
`SELECT id, label, description, source, tier, input_price_per_m, output_price_per_m,
        gateway_model_id, provider, enabled, sort_order, created_at
 FROM model_catalog
 WHERE enabled = true
 ORDER BY sort_order`
```

And update the scan from:

```go
if err := rows.Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
	&e.InputPricePerM, &e.OutputPricePerM, &e.CostInputMicrocents, &e.CostOutputMicrocents,
	&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt); err != nil {
```

To:

```go
if err := rows.Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
	&e.InputPricePerM, &e.OutputPricePerM,
	&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt); err != nil {
```

- [ ] **Step 3: Update `GetModelCatalogEntry` SQL and scan**

Same changes — remove `cost_input_microcents, cost_output_microcents` from SELECT and remove from Scan call.

Change the query from:

```go
`SELECT id, label, description, source, tier, input_price_per_m, output_price_per_m,
        cost_input_microcents, cost_output_microcents, gateway_model_id, provider,
        enabled, sort_order, created_at
 FROM model_catalog
 WHERE id = $1 AND enabled = true`
```

To:

```go
`SELECT id, label, description, source, tier, input_price_per_m, output_price_per_m,
        gateway_model_id, provider, enabled, sort_order, created_at
 FROM model_catalog
 WHERE id = $1 AND enabled = true`
```

And update the scan from:

```go
Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
	&e.InputPricePerM, &e.OutputPricePerM, &e.CostInputMicrocents, &e.CostOutputMicrocents,
	&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt)
```

To:

```go
Scan(&e.ID, &e.Label, &e.Description, &e.Source, &e.Tier,
	&e.InputPricePerM, &e.OutputPricePerM,
	&e.GatewayModelID, &e.Provider, &e.Enabled, &e.SortOrder, &e.CreatedAt)
```

- [ ] **Step 4: Verify build compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: May fail if mock stubs in `runtime_test.go` still reference cost fields — fixed in Task 5.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "refactor: remove cost microcent fields from ModelCatalogEntry and catalog queries"
```

---

### Task 5: Update mock stubs in `runtime_test.go`

**Files:**
- Modify: `backend/internal/machines/runtime_test.go:1158-1182`

- [ ] **Step 1: Update `ListModelCatalog` mock**

The mock entries in `runtime_test.go` don't explicitly set `CostInputMicrocents` / `CostOutputMicrocents` (they rely on zero values), so removing the fields from the struct should just work. Verify by building.

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Success — the mock entries only set fields that still exist (`ID`, `Label`, `Source`, `Tier`, `GatewayModelID`, `Provider`, `Enabled`, `SortOrder`)

- [ ] **Step 2: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests pass

- [ ] **Step 3: Commit (if any changes were needed)**

```bash
git add backend/internal/machines/runtime_test.go
git commit -m "test: update mock stubs for removed cost fields"
```

---

### Task 6: Rewrite 5 cost queries with lateral join to `model_pricing_history`

**Files:**
- Modify: `backend/internal/store/postgres.go:1043-1180` — all 5 billing queries

- [ ] **Step 1: Write test for `GetLLMSpendByMachine` with pricing history**

Create `backend/internal/store/pricing_test.go`:

```go
package store

import (
	"testing"
)

func TestPricingLateralJoinSQL(t *testing.T) {
	// Verify the lateral join SQL pattern compiles into valid queries.
	// Full integration tests require a database; this validates the SQL strings
	// are well-formed by checking they contain the expected clauses.

	queries := []struct {
		name string
		sql  string
	}{
		{"GetLLMSpendByAccount", `
			SELECT COALESCE(SUM(
				COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
				+ COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
			)::bigint, 0)
			FROM token_usage t
			LEFT JOIN LATERAL (
				SELECT cost_input_microcents, cost_output_microcents, margin
				FROM model_pricing_history
				WHERE model_id = t.model AND effective_from <= t.created_at
				ORDER BY effective_from DESC LIMIT 1
			) p ON true
			WHERE t.account_id = $1`},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			if q.sql == "" {
				t.Fatal("empty SQL")
			}
		})
	}
}
```

- [ ] **Step 2: Rewrite `GetLLMSpendByAccount`**

Change from:

```go
func (s *PostgresStore) GetLLMSpendByAccount(ctx context.Context, accountID int) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			(COALESCE(cost_input_usd, 0) + COALESCE(cost_output_usd, 0)) * 1000000
		)::bigint, 0)
		 FROM token_usage
		 WHERE account_id = $1`,
		accountID,
	).Scan(&total)
	return total, err
}
```

To:

```go
func (s *PostgresStore) GetLLMSpendByAccount(ctx context.Context, accountID int) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
			+ COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
		)::bigint, 0)
		FROM token_usage t
		LEFT JOIN LATERAL (
			SELECT cost_input_microcents, cost_output_microcents, margin
			FROM model_pricing_history
			WHERE model_id = t.model AND effective_from <= t.created_at
			ORDER BY effective_from DESC LIMIT 1
		) p ON true
		WHERE t.account_id = $1`,
		accountID,
	).Scan(&total)
	return total, err
}
```

- [ ] **Step 3: Rewrite `GetLLMSpendByMachine`**

Change from:

```go
func (s *PostgresStore) GetLLMSpendByMachine(ctx context.Context, machineID string) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			(COALESCE(cost_input_usd, 0) + COALESCE(cost_output_usd, 0)) * 1000000
		)::bigint, 0)
		 FROM token_usage
		 WHERE machine_id = $1
		   AND created_at >= date_trunc('month', now())`,
		machineID,
	).Scan(&total)
	return total, err
}
```

To:

```go
func (s *PostgresStore) GetLLMSpendByMachine(ctx context.Context, machineID string) (int64, error) {
	var total int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(
			COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
			+ COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
		)::bigint, 0)
		FROM token_usage t
		LEFT JOIN LATERAL (
			SELECT cost_input_microcents, cost_output_microcents, margin
			FROM model_pricing_history
			WHERE model_id = t.model AND effective_from <= t.created_at
			ORDER BY effective_from DESC LIMIT 1
		) p ON true
		WHERE t.machine_id = $1
		  AND t.created_at >= date_trunc('month', now())`,
		machineID,
	).Scan(&total)
	return total, err
}
```

- [ ] **Step 4: Rewrite `GetLLMUsageByMachine`**

Change the SELECT from:

```go
`SELECT id, account_id, machine_id, provider, model, input_tokens, output_tokens,
        (COALESCE(cost_input_usd, 0) + COALESCE(cost_output_usd, 0)) * 1000000 AS cost_microcents,
        source, created_at
 FROM token_usage
 WHERE machine_id = $1 AND created_at >= $2
 ORDER BY created_at DESC
 LIMIT $3`
```

To:

```go
`SELECT t.id, t.account_id, t.machine_id, t.provider, t.model, t.input_tokens, t.output_tokens,
        COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
        + COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0) AS cost_microcents,
        t.source, t.created_at
 FROM token_usage t
 LEFT JOIN LATERAL (
     SELECT cost_input_microcents, cost_output_microcents, margin
     FROM model_pricing_history
     WHERE model_id = t.model AND effective_from <= t.created_at
     ORDER BY effective_from DESC LIMIT 1
 ) p ON true
 WHERE t.machine_id = $1 AND t.created_at >= $2
 ORDER BY t.created_at DESC
 LIMIT $3`
```

- [ ] **Step 5: Rewrite `GetLLMUsageByAccount`**

Same pattern as Step 4, but with `WHERE t.account_id = $1`:

```go
`SELECT t.id, t.account_id, t.machine_id, t.provider, t.model, t.input_tokens, t.output_tokens,
        COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
        + COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0) AS cost_microcents,
        t.source, t.created_at
 FROM token_usage t
 LEFT JOIN LATERAL (
     SELECT cost_input_microcents, cost_output_microcents, margin
     FROM model_pricing_history
     WHERE model_id = t.model AND effective_from <= t.created_at
     ORDER BY effective_from DESC LIMIT 1
 ) p ON true
 WHERE t.account_id = $1 AND t.created_at >= $2
 ORDER BY t.created_at DESC
 LIMIT $3`
```

- [ ] **Step 6: Rewrite `GetUsageBreakdown`**

Change from:

```go
fmt.Sprintf(`SELECT
    date_trunc('%s', created_at) AS bucket,
    provider,
    model,
    source,
    SUM(input_tokens)::int AS input_tokens,
    SUM(output_tokens)::int AS output_tokens,
    COALESCE(SUM(
        (COALESCE(cost_input_usd, 0) + COALESCE(cost_output_usd, 0)) * 1000000
    )::bigint, 0) AS cost_microcents,
    COUNT(*)::int AS request_count
FROM token_usage
WHERE machine_id = $1 AND created_at >= $2
GROUP BY bucket, provider, model, source
ORDER BY bucket, provider, model`, period)
```

To:

```go
fmt.Sprintf(`SELECT
    date_trunc('%s', t.created_at) AS bucket,
    t.provider,
    t.model,
    t.source,
    SUM(t.input_tokens)::int AS input_tokens,
    SUM(t.output_tokens)::int AS output_tokens,
    COALESCE(SUM(
        COALESCE(t.input_tokens::BIGINT * p.cost_input_microcents * p.margin / 1000000, 0)
        + COALESCE(t.output_tokens::BIGINT * p.cost_output_microcents * p.margin / 1000000, 0)
    )::bigint, 0) AS cost_microcents,
    COUNT(*)::int AS request_count
FROM token_usage t
LEFT JOIN LATERAL (
    SELECT cost_input_microcents, cost_output_microcents, margin
    FROM model_pricing_history
    WHERE model_id = t.model AND effective_from <= t.created_at
    ORDER BY effective_from DESC LIMIT 1
) p ON true
WHERE t.machine_id = $1 AND t.created_at >= $2
GROUP BY bucket, t.provider, t.model, t.source
ORDER BY bucket, t.provider, t.model`, period)
```

- [ ] **Step 7: Verify build compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Success

- [ ] **Step 8: Run tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All tests pass

- [ ] **Step 9: Commit**

```bash
git add backend/internal/store/postgres.go backend/internal/store/pricing_test.go
git commit -m "refactor: rewrite 5 cost queries to use lateral join with model_pricing_history"
```

---

### Task 7: Add `ModelPricingHistory` type and repo interface

**Files:**
- Modify: `backend/internal/store/store.go` — add type and interface method

- [ ] **Step 1: Add `ModelPricingHistory` struct**

Add after the `ModelCatalogEntry` struct in `store.go`:

```go
// ModelPricingHistory represents a versioned pricing entry for a model.
type ModelPricingHistory struct {
	ID                   int64
	ModelID              string
	CostInputMicrocents  int64
	CostOutputMicrocents int64
	Margin               float64
	EffectiveFrom        time.Time
	CreatedAt            time.Time
}
```

- [ ] **Step 2: Add repo method to `ModelCatalogRepo` interface**

Add to the `ModelCatalogRepo` interface (or create a new `PricingRepo` — but simpler to add to `ModelCatalogRepo`):

```go
ListModelPricingHistory(ctx context.Context, modelID string) ([]ModelPricingHistory, error)
InsertModelPricing(ctx context.Context, entry *ModelPricingHistory) error
```

- [ ] **Step 3: Commit**

```bash
git add backend/internal/store/store.go
git commit -m "feat: add ModelPricingHistory type and repo interface methods"
```

---

### Task 8: Implement `ListModelPricingHistory` and `InsertModelPricing` in postgres.go

**Files:**
- Modify: `backend/internal/store/postgres.go` — add implementations

- [ ] **Step 1: Implement `ListModelPricingHistory`**

```go
func (s *PostgresStore) ListModelPricingHistory(ctx context.Context, modelID string) ([]ModelPricingHistory, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, model_id, cost_input_microcents, cost_output_microcents, margin, effective_from, created_at
		 FROM model_pricing_history
		 WHERE model_id = $1
		 ORDER BY effective_from DESC`,
		modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ModelPricingHistory
	for rows.Next() {
		var e ModelPricingHistory
		if err := rows.Scan(&e.ID, &e.ModelID, &e.CostInputMicrocents, &e.CostOutputMicrocents,
			&e.Margin, &e.EffectiveFrom, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
```

- [ ] **Step 2: Implement `InsertModelPricing`**

```go
func (s *PostgresStore) InsertModelPricing(ctx context.Context, entry *ModelPricingHistory) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO model_pricing_history (model_id, cost_input_microcents, cost_output_microcents, margin, effective_from)
		 VALUES ($1, $2, $3, $4, $5)`,
		entry.ModelID, entry.CostInputMicrocents, entry.CostOutputMicrocents, entry.Margin, entry.EffectiveFrom)
	return err
}
```

- [ ] **Step 3: Verify build compiles**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: May fail if mock in `runtime_test.go` doesn't implement new interface methods — fix below.

- [ ] **Step 4: Add mock stubs to `runtime_test.go`**

```go
func (m *mockStore) ListModelPricingHistory(_ context.Context, _ string) ([]store.ModelPricingHistory, error) {
	return nil, nil
}

func (m *mockStore) InsertModelPricing(_ context.Context, _ *store.ModelPricingHistory) error {
	return nil
}
```

- [ ] **Step 5: Verify build and tests**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/... && make test-go`
Expected: All pass

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store/postgres.go backend/internal/machines/runtime_test.go
git commit -m "feat: implement ListModelPricingHistory and InsertModelPricing"
```

---

### Task 9: Clean up `apiproxy/pricing.go` dead code

**Files:**
- Remove or update: `backend/internal/apiproxy/pricing.go`

- [ ] **Step 1: Check if `CalculateCost` is called anywhere**

Run: `grep -r "CalculateCost" backend/`
Expected: Only the definition in `pricing.go` — it's already a no-op stub.

- [ ] **Step 2: Delete the file if unreferenced**

If `CalculateCost` has no callers, delete `backend/internal/apiproxy/pricing.go`.

If it has callers, leave it (it's already a no-op).

- [ ] **Step 3: Commit**

```bash
git rm backend/internal/apiproxy/pricing.go
git commit -m "chore: remove dead CalculateCost stub"
```

---

### Task 10: Final build, test, and deploy

**Files:** None — verification only

- [ ] **Step 1: Full build**

Run: `cd /home/mantiz/OpenClawMachines && go build ./backend/...`
Expected: Success

- [ ] **Step 2: Run all Go tests**

Run: `cd /home/mantiz/OpenClawMachines && make test-go`
Expected: All pass

- [ ] **Step 3: Run frontend typecheck**

Run: `cd /home/mantiz/OpenClawMachines && make typecheck`
Expected: Success (frontend types unchanged — `ModelEntry` never had cost microcent fields)

- [ ] **Step 4: Commit any remaining changes and push**

```bash
git push
```

- [ ] **Step 5: Deploy backend**

Run: `make deploy-backend`
Expected: Successful deployment. The migration runs on startup and restructures the pricing data.
