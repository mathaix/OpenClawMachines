-- 052_nebius_full_catalog.sql
-- Add full Nebius model catalog and pricing keyed by catalog model IDs (what opik spans record).

-- ============================================================
-- 1. Insert new Nebius models into model_catalog (all disabled)
-- ============================================================
-- Note: The 3 existing platform models (qwen/qwen3.5-397b, minimax/minimax-m2.5, openai/gpt-oss-120b)
-- are already in the catalog. We add the rest as disabled.

INSERT INTO model_catalog (id, label, description, source, tier, input_price_per_m, output_price_per_m, gateway_model_id, provider, enabled, sort_order) VALUES
  -- Text-to-Text
  ('openai/gpt-oss-20b',                    'GPT OSS 20B',           'Fast & cheap',                    'platform', NULL, 0.05, 0.20, 'nebius/openai/gpt-oss-20b',                    'nebius', false, 100),
  ('moonshot/kimi-k2-instruct',             'Kimi K2 Instruct',      'Moonshot reasoning',              'platform', NULL, 0.50, 2.40, 'nebius/moonshotai/Kimi-K2-Instruct',           'nebius', false, 101),
  ('qwen/qwen3-coder-480b',                 'Qwen3 Coder 480B',      'Large coding model',              'platform', NULL, 0.40, 1.80, 'nebius/Qwen/Qwen3-Coder-480B',                 'nebius', false, 102),
  ('qwen/qwen3-235b-a22b-thinking',         'Qwen3 235B Thinking',   'Reasoning model',                 'platform', NULL, 0.20, 0.80, 'nebius/Qwen/Qwen3-235B-A22B-Thinking',         'nebius', false, 103),
  ('qwen/qwen3-235b-a22b-instruct',         'Qwen3 235B Instruct',   'Instruction-tuned',               'platform', NULL, 0.20, 0.60, 'nebius/Qwen/Qwen3-235B-A22B-Instruct',         'nebius', false, 104),
  ('qwen/qwen3-30b-a3b-thinking',           'Qwen3 30B Thinking',    'Small reasoning',                 'platform', NULL, 0.10, 0.30, 'nebius/Qwen/Qwen3-30B-A3B-Thinking',           'nebius', false, 105),
  ('qwen/qwen3-30b-a3b-instruct',           'Qwen3 30B Instruct',    'Small instruction-tuned',         'platform', NULL, 0.10, 0.30, 'nebius/Qwen/Qwen3-30B-A3B-Instruct',           'nebius', false, 106),
  ('qwen/qwen3-coder-30b-a3b',              'Qwen3 Coder 30B',       'Small coding model',              'platform', NULL, 0.10, 0.30, 'nebius/Qwen/Qwen3-Coder-30B-A3B',              'nebius', false, 107),
  ('qwen/qwen3-30b-a3b',                    'Qwen3 30B',             'Small general purpose',           'platform', NULL, 0.10, 0.30, 'nebius/Qwen/Qwen3-30B-A3B',                    'nebius', false, 108),
  ('qwen/qwen3-32b',                        'Qwen3 32B',             'Medium general purpose',          'platform', NULL, 0.10, 0.30, 'nebius/Qwen/Qwen3-32B',                        'nebius', false, 109),
  ('qwen/qwen3-14b',                        'Qwen3 14B',             'Compact model',                   'platform', NULL, 0.08, 0.24, 'nebius/Qwen/Qwen3-14B',                        'nebius', false, 110),
  ('qwen/qwen2.5-coder-7b',                 'Qwen2.5 Coder 7B',      'Tiny coding model',               'platform', NULL, 0.03, 0.09, 'nebius/Qwen/Qwen2.5-Coder-7B',                 'nebius', false, 111),
  ('qwen/qwen2.5-72b-instruct',             'Qwen2.5 72B Instruct',  'Previous gen instruction-tuned',  'platform', NULL, 0.13, 0.40, 'nebius/Qwen/Qwen2.5-72B-Instruct',             'nebius', false, 112),
  ('qwen/qwq-32b',                          'QwQ 32B',               'Reasoning model',                 'platform', NULL, 0.15, 0.45, 'nebius/Qwen/QwQ-32B',                          'nebius', false, 113),
  ('zhipu/glm-4.5',                         'GLM 4.5',               'Zhipu large model',               'platform', NULL, 0.60, 2.20, 'nebius/THUDM/GLM-4.5',                         'nebius', false, 114),
  ('zhipu/glm-4.5-air',                     'GLM 4.5 Air',           'Zhipu efficient model',           'platform', NULL, 0.20, 1.20, 'nebius/THUDM/GLM-4.5-Air',                     'nebius', false, 115),
  ('deepseek/deepseek-r1-0528',             'DeepSeek R1',           'Reasoning model',                 'platform', NULL, 0.80, 2.40, 'nebius/deepseek-ai/DeepSeek-R1-0528',          'nebius', false, 116),
  ('deepseek/deepseek-v3-0324',             'DeepSeek V3',           'General purpose',                 'platform', NULL, 0.50, 1.50, 'nebius/deepseek-ai/DeepSeek-V3-0324',          'nebius', false, 117),
  ('deepseek/deepseek-v3',                  'DeepSeek V3 (prev)',    'Previous version',                'platform', NULL, 0.50, 1.50, 'nebius/deepseek-ai/DeepSeek-V3',               'nebius', false, 118),
  ('meta/llama-3.3-70b-instruct',           'Llama 3.3 70B',        'Meta instruction-tuned',          'platform', NULL, 0.13, 0.40, 'nebius/Meta/Llama-3.3-70B-Instruct',           'nebius', false, 119),
  ('meta/llama-3.1-8b-instruct',            'Llama 3.1 8B',         'Meta small model',                'platform', NULL, 0.02, 0.06, 'nebius/Meta/Llama-3.1-8B-Instruct',            'nebius', false, 120),
  ('meta/llama-3.1-405b-instruct',          'Llama 3.1 405B',       'Meta large model',                'platform', NULL, 1.00, 3.00, 'nebius/Meta/Llama-3.1-405B-Instruct',          'nebius', false, 121),
  ('nvidia/llama-3.1-nemotron-ultra-253b',  'Nemotron Ultra 253B',   'NVIDIA fine-tuned',               'platform', NULL, 0.60, 1.80, 'nebius/nvidia/Llama-3.1-Nemotron-Ultra-253B',  'nebius', false, 122),
  ('google/gemma-2-2b-it',                  'Gemma 2 2B',            'Google tiny model',               'platform', NULL, 0.02, 0.06, 'nebius/google/gemma-2-2b-it',                  'nebius', false, 123),
  ('google/gemma-2-9b-it',                  'Gemma 2 9B',            'Google small model',              'platform', NULL, 0.03, 0.09, 'nebius/google/gemma-2-9b-it',                  'nebius', false, 124),
  ('mistral/devstral-small-2505',           'Devstral Small',        'Mistral coding model',            'platform', NULL, 0.08, 0.24, 'nebius/mistralai/Devstral-Small-2505',         'nebius', false, 125),
  ('nous/hermes-4-405b',                    'Hermes 4 405B',         'NousResearch large',              'platform', NULL, 1.00, 3.00, 'nebius/NousResearch/Hermes-4-405B',            'nebius', false, 126),
  ('nous/hermes-4-70b',                     'Hermes 4 70B',          'NousResearch medium',             'platform', NULL, 0.13, 0.40, 'nebius/NousResearch/Hermes-4-70B',             'nebius', false, 127),
  ('nous/hermes-3-llama-3.1-405b',          'Hermes 3 405B',         'NousResearch previous',           'platform', NULL, 1.00, 3.00, 'nebius/NousResearch/Hermes-3-Llama-3.1-405B',  'nebius', false, 128),
  -- Vision
  ('google/gemma-3-27b-it',                 'Gemma 3 27B Vision',    'Google vision model',             'platform', NULL, 0.10, 0.30, 'nebius/google/gemma-3-27b-it',                 'nebius', false, 130),
  ('qwen/qwen2-vl-72b',                     'Qwen2 VL 72B',          'Qwen vision-language',            'platform', NULL, 0.13, 0.40, 'nebius/Qwen/Qwen2-VL-72B',                    'nebius', false, 131),
  -- Embeddings
  ('baai/bge-en-icl',                       'BGE EN ICL',            'English embeddings',              'platform', NULL, 0.01, 0.00, 'nebius/BAAI/bge-en-icl',                       'nebius', false, 140),
  ('intfloat/multilingual-e5-large-instruct','E5 Large Multilingual', 'Multilingual embeddings',         'platform', NULL, 0.01, 0.00, 'nebius/intfloat/multilingual-e5-large-instruct','nebius', false, 141),
  ('baai/bge-m3',                           'BGE M3',                'Multilingual embeddings',         'platform', NULL, 0.01, 0.00, 'nebius/BAAI/bge-m3',                           'nebius', false, 142),
  -- Guardrails
  ('meta/llama-guard-3-8b',                 'Llama Guard 3 8B',      'Content safety',                  'platform', NULL, 0.02, 0.06, 'nebius/meta-llama/Llama-Guard-3-8B',           'nebius', false, 150)
ON CONFLICT (id) DO UPDATE SET
  label = EXCLUDED.label,
  description = EXCLUDED.description,
  input_price_per_m = EXCLUDED.input_price_per_m,
  output_price_per_m = EXCLUDED.output_price_per_m,
  gateway_model_id = EXCLUDED.gateway_model_id;

-- ============================================================
-- 2. Insert pricing rows keyed by CATALOG model IDs
--    (opik spans record catalog IDs, so pricing must match)
-- ============================================================
-- Formula: cost_microcents = price_per_m_dollars * 1_000_000
-- Verified: gpt-4o-mini has $0.15/M → 150000 in migration 041 (line 25).
-- Note: The original 3 Nebius models had WRONG values (e.g., $0.15 → 100000
-- instead of 150000). We insert correct values keyed by catalog model IDs.

INSERT INTO model_pricing_history (model_id, cost_input_microcents, cost_output_microcents, margin, effective_from)
VALUES
  -- Existing 3 platform models: correct pricing keyed by catalog ID (what opik spans record)
  ('qwen/qwen3.5-397b',            600000,  3600000, 1.0, '1970-01-01T00:00:00Z'),
  ('minimax/minimax-m2.5',         300000,  1200000, 1.0, '1970-01-01T00:00:00Z'),
  ('openai/gpt-oss-120b',          150000,   600000, 1.0, '1970-01-01T00:00:00Z'),
  -- New text-to-text
  ('openai/gpt-oss-20b',            50000,   200000, 1.0, '1970-01-01T00:00:00Z'),
  ('moonshot/kimi-k2-instruct',    500000,  2400000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-coder-480b',        400000,  1800000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-235b-a22b-thinking',200000,   800000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-235b-a22b-instruct',200000,   600000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-30b-a3b-thinking',  100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-30b-a3b-instruct',  100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-coder-30b-a3b',     100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-30b-a3b',           100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-32b',               100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen3-14b',                80000,   240000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen2.5-coder-7b',         30000,    90000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen2.5-72b-instruct',    130000,   400000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwq-32b',                 150000,   450000, 1.0, '1970-01-01T00:00:00Z'),
  ('zhipu/glm-4.5',                600000,  2200000, 1.0, '1970-01-01T00:00:00Z'),
  ('zhipu/glm-4.5-air',            200000,  1200000, 1.0, '1970-01-01T00:00:00Z'),
  ('deepseek/deepseek-r1-0528',    800000,  2400000, 1.0, '1970-01-01T00:00:00Z'),
  ('deepseek/deepseek-v3-0324',    500000,  1500000, 1.0, '1970-01-01T00:00:00Z'),
  ('deepseek/deepseek-v3',         500000,  1500000, 1.0, '1970-01-01T00:00:00Z'),
  ('meta/llama-3.3-70b-instruct',  130000,   400000, 1.0, '1970-01-01T00:00:00Z'),
  ('meta/llama-3.1-8b-instruct',    20000,    60000, 1.0, '1970-01-01T00:00:00Z'),
  ('meta/llama-3.1-405b-instruct',1000000,  3000000, 1.0, '1970-01-01T00:00:00Z'),
  ('nvidia/llama-3.1-nemotron-ultra-253b', 600000, 1800000, 1.0, '1970-01-01T00:00:00Z'),
  ('google/gemma-2-2b-it',          20000,    60000, 1.0, '1970-01-01T00:00:00Z'),
  ('google/gemma-2-9b-it',          30000,    90000, 1.0, '1970-01-01T00:00:00Z'),
  ('mistral/devstral-small-2505',   80000,   240000, 1.0, '1970-01-01T00:00:00Z'),
  ('nous/hermes-4-405b',          1000000,  3000000, 1.0, '1970-01-01T00:00:00Z'),
  ('nous/hermes-4-70b',            130000,   400000, 1.0, '1970-01-01T00:00:00Z'),
  ('nous/hermes-3-llama-3.1-405b',1000000,  3000000, 1.0, '1970-01-01T00:00:00Z'),
  -- Vision
  ('google/gemma-3-27b-it',        100000,   300000, 1.0, '1970-01-01T00:00:00Z'),
  ('qwen/qwen2-vl-72b',            130000,   400000, 1.0, '1970-01-01T00:00:00Z'),
  -- Embeddings (output price = 0 for embedding models)
  ('baai/bge-en-icl',               10000,        0, 1.0, '1970-01-01T00:00:00Z'),
  ('intfloat/multilingual-e5-large-instruct', 10000, 0, 1.0, '1970-01-01T00:00:00Z'),
  ('baai/bge-m3',                    10000,        0, 1.0, '1970-01-01T00:00:00Z'),
  -- Guardrails
  ('meta/llama-guard-3-8b',          20000,    60000, 1.0, '1970-01-01T00:00:00Z')
ON CONFLICT DO NOTHING;
