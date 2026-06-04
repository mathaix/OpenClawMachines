-- Add OpenAI Codex 5.5 subscription model.
--
-- These are ChatGPT subscription-backed Codex models, so pricing is recorded
-- as zero while usage/token counts still flow through the normal paths.
INSERT INTO model_catalog (
  id,
  label,
  description,
  source,
  tier,
  input_price_per_m,
  output_price_per_m,
  gateway_model_id,
  provider,
  enabled,
  sort_order
) VALUES (
  'openai-codex/gpt-5.5',
  'GPT-5.5',
  'ChatGPT subscription',
  'subscription',
  NULL,
  0,
  0,
  'openai/gpt-5.5',
  'openai-codex',
  true,
  19
)
ON CONFLICT (id) DO UPDATE SET
  label = EXCLUDED.label,
  description = EXCLUDED.description,
  source = EXCLUDED.source,
  tier = EXCLUDED.tier,
  input_price_per_m = EXCLUDED.input_price_per_m,
  output_price_per_m = EXCLUDED.output_price_per_m,
  gateway_model_id = EXCLUDED.gateway_model_id,
  provider = EXCLUDED.provider,
  enabled = EXCLUDED.enabled,
  sort_order = EXCLUDED.sort_order;
