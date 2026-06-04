-- Add Anthropic subscription models (Claude Pro/Max/Team plans)
-- These use the same provider ("anthropic") as BYOK but with source "subscription".
-- The proxy handles subscription_key credential type with Bearer auth.
INSERT INTO model_catalog (id, label, description, source, tier, input_price_per_m, output_price_per_m, cost_input_microcents, cost_output_microcents, gateway_model_id, provider, sort_order) VALUES
  ('anthropic/claude-opus-4-6-sub', 'Claude Opus 4.6', 'Claude subscription', 'subscription', NULL, 0, 0, 0, 0, 'anthropic/claude-opus-4-6', 'anthropic', 25),
  ('anthropic/claude-sonnet-4-6-sub', 'Claude Sonnet 4.6', 'Claude subscription', 'subscription', NULL, 0, 0, 0, 0, 'anthropic/claude-sonnet-4-6', 'anthropic', 26),
  ('anthropic/claude-haiku-4-5-sub', 'Claude Haiku 4.5', 'Claude subscription', 'subscription', NULL, 0, 0, 0, 0, 'anthropic/claude-haiku-4-5-20251001', 'anthropic', 27)
ON CONFLICT (id) DO NOTHING;
