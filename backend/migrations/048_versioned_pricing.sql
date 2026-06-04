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
