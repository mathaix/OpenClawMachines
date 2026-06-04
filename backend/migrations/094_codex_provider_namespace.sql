-- OpenClaw 2026.5.28's Codex app-server harness owns the "codex" provider
-- namespace. Mapping the subscription Codex model to openai/gpt-5.5 leaves
-- persisted codex harness sessions with provider=openai, which the harness
-- rejects before reply.
UPDATE model_catalog
SET gateway_model_id = 'codex/gpt-5.5'
WHERE id = 'openai-codex/gpt-5.5';
