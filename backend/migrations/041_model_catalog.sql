CREATE TABLE model_catalog (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    description TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('platform', 'byok', 'subscription')),
    tier TEXT CHECK (tier IN ('smart', 'balanced', 'fast')),
    gateway_model_id TEXT,
    provider TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO model_catalog (id, label, description, source, tier, gateway_model_id, provider, sort_order) VALUES
  ('qwen/qwen3.5-397b', 'Smart', 'Qwen 3.5 397B - Deep reasoning', 'platform', 'smart', 'nebius/Qwen/Qwen3.5-397B-A17B', 'nebius', 1),
  ('minimax/minimax-m2.5', 'Balanced', 'MiniMax M2.5 - Agentic coding', 'platform', 'balanced', 'nebius/MiniMaxAI/MiniMax-M2.5', 'nebius', 2),
  ('openai/gpt-oss-120b', 'Fast', 'GPT OSS 120B - Fast and capable', 'platform', 'fast', 'nebius/openai/gpt-oss-120b', 'nebius', 3),
  ('anthropic/claude-sonnet-4-6', 'Claude Sonnet 4.6', 'Anthropic', 'byok', NULL, NULL, 'anthropic', 10),
  ('anthropic/claude-opus-4-6', 'Claude Opus 4.6', 'Anthropic', 'byok', NULL, NULL, 'anthropic', 11),
  ('openai/gpt-4o', 'GPT-4o', 'OpenAI', 'byok', NULL, NULL, 'openai', 12),
  ('openai/gpt-4o-mini', 'GPT-4o Mini', 'OpenAI', 'byok', NULL, NULL, 'openai', 13),
  ('google/gemini-2.5-flash-preview-05-20', 'Gemini 2.5 Flash', 'Google', 'byok', NULL, NULL, 'google', 14),
  ('google/gemini-2.5-pro-preview-05-06', 'Gemini 2.5 Pro', 'Google', 'byok', NULL, NULL, 'google', 15),
  ('google/gemini-2.0-flash', 'Gemini 2.0 Flash', 'Google', 'byok', NULL, NULL, 'google', 16),
  ('openai-codex/gpt-5.4', 'GPT-5.4', 'ChatGPT subscription', 'subscription', NULL, NULL, 'openai-codex', 20);
