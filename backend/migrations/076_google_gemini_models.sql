-- Update Google Gemini models in the model catalog.
--
-- 1. Replace old preview-versioned model IDs with stable GA IDs.
-- 2. Add new Gemini 3.x preview models.
-- 3. Update the provider_catalog upstream_path_prefix for Google to reflect
--    the switch to Google's OpenAI-compatible endpoint (/v1beta/openai).

BEGIN;

-- ============================================================
-- 1. Replace old preview IDs with stable GA model IDs
-- ============================================================
-- The old preview-dated IDs (gemini-2.5-flash-preview-05-20, gemini-2.5-pro-preview-05-06)
-- no longer resolve. Replace with the stable model IDs.

DELETE FROM model_catalog WHERE id IN (
  'google/gemini-2.5-flash-preview-05-20',
  'google/gemini-2.5-pro-preview-05-06'
);

-- ============================================================
-- 2. Insert/update Google Gemini models
-- ============================================================
-- Pricing from https://ai.google.dev/pricing (paid tier, April 2026).
-- gemini-2.0-flash already exists (sort_order=16), upsert to refresh pricing.

INSERT INTO model_catalog (id, label, description, source, tier, input_price_per_m, output_price_per_m, gateway_model_id, provider, enabled, sort_order) VALUES
  ('google/gemini-2.5-flash',        'Gemini 2.5 Flash',        'Google — fast and capable',             'byok', NULL, 0.30,  2.50, NULL, 'google', true,  14),
  ('google/gemini-2.5-pro',          'Gemini 2.5 Pro',          'Google — advanced reasoning',           'byok', NULL, 1.25, 10.00, NULL, 'google', true,  15),
  ('google/gemini-2.0-flash',        'Gemini 2.0 Flash',        'Google — deprecated June 2026',         'byok', NULL, 0.10,  0.40, NULL, 'google', true,  16),
  ('google/gemini-3-flash-preview',  'Gemini 3 Flash (Preview)','Google — next-gen fast model',          'byok', NULL, 0.50,  3.00, NULL, 'google', true,  17),
  ('google/gemini-3.1-pro-preview',  'Gemini 3.1 Pro (Preview)','Google — next-gen advanced reasoning',  'byok', NULL, 2.00, 12.00, NULL, 'google', true,  18)
ON CONFLICT (id) DO UPDATE SET
  label = EXCLUDED.label,
  description = EXCLUDED.description,
  input_price_per_m = EXCLUDED.input_price_per_m,
  output_price_per_m = EXCLUDED.output_price_per_m,
  enabled = EXCLUDED.enabled,
  sort_order = EXCLUDED.sort_order;

-- ============================================================
-- 3. Update provider_catalog: set upstream_path_prefix for Google
-- ============================================================
-- Google AI now uses OpenAI-compatible endpoint at /v1beta/openai/.
-- The proxy prepends this prefix to the remaining path before forwarding.

UPDATE provider_catalog
   SET upstream_path_prefix = '/v1beta/openai'
 WHERE id = 'google';

COMMIT;
