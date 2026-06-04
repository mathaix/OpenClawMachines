-- Align the subscription Codex catalog with the OpenClaw 2026.5.19 runtime.
-- The gateway rejects the old 5.3 Codex aliases and asks callers to use
-- openai/gpt-5.5 instead.

UPDATE model_catalog
SET
  gateway_model_id = 'openai/gpt-5.5',
  enabled = true,
  sort_order = 19
WHERE id = 'openai-codex/gpt-5.5';

UPDATE model_catalog
SET enabled = false
WHERE id IN (
  'openai-codex/gpt-5.3-codex',
  'openai-codex/gpt-5.3-codex-spark'
);
