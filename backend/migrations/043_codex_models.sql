-- Add additional OpenAI Codex subscription models
INSERT INTO model_catalog (id, label, description, source, tier, input_price_per_m, output_price_per_m, cost_input_microcents, cost_output_microcents, gateway_model_id, provider, sort_order) VALUES
  ('openai-codex/gpt-5.3-codex', 'GPT-5.3 Codex', 'ChatGPT subscription', 'subscription', NULL, 0, 0, 0, 0, NULL, 'openai-codex', 21),
  ('openai-codex/gpt-5.3-codex-spark', 'GPT-5.3 Codex Spark', 'ChatGPT subscription (fast)', 'subscription', NULL, 0, 0, 0, 0, NULL, 'openai-codex', 22)
ON CONFLICT (id) DO NOTHING;
