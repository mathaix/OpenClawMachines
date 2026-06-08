-- Restore per-machine LLM budget limit. NULL means no budget cap.
ALTER TABLE machines ADD COLUMN IF NOT EXISTS budget_microcents BIGINT;
