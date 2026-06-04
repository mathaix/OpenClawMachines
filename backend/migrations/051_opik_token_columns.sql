-- 051_opik_token_columns.sql
-- Extract token counts from JSONB usage into dedicated columns for easy aggregation.

ALTER TABLE opik_spans ADD COLUMN IF NOT EXISTS prompt_tokens INTEGER;
ALTER TABLE opik_spans ADD COLUMN IF NOT EXISTS completion_tokens INTEGER;
ALTER TABLE opik_spans ADD COLUMN IF NOT EXISTS total_tokens INTEGER;

-- Backfill from existing usage JSONB
UPDATE opik_spans SET
    prompt_tokens = (usage->>'prompt_tokens')::integer,
    completion_tokens = (usage->>'completion_tokens')::integer,
    total_tokens = (usage->>'total_tokens')::integer
WHERE usage IS NOT NULL AND total_tokens IS NULL;

-- Index for aggregation queries by account and machine
CREATE INDEX IF NOT EXISTS idx_opik_spans_account_tokens
    ON opik_spans(account_id, start_time DESC)
    WHERE total_tokens IS NOT NULL;
