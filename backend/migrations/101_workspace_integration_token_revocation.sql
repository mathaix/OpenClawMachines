ALTER TABLE machines
    ADD COLUMN IF NOT EXISTS workspace_integration_tokens_valid_after TIMESTAMPTZ;

UPDATE machines
SET workspace_integration_tokens_valid_after = TIMESTAMPTZ '1970-01-01 00:00:00+00'
WHERE workspace_integration_tokens_valid_after IS NULL;

ALTER TABLE machines
    ALTER COLUMN workspace_integration_tokens_valid_after SET DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00';
