-- Add per-machine budget limit (NULL = no limit, 1000000 = $10.00)
ALTER TABLE machines ADD COLUMN IF NOT EXISTS budget_microcents BIGINT;
