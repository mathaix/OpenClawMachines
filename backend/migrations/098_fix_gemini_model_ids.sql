-- Update the Google Gemini 2.5 model catalog IDs to their current, non-preview
-- names. The preview IDs seeded in 041_model_catalog.sql
-- (gemini-2.5-flash-preview-05-20, gemini-2.5-pro-preview-05-06) were retired
-- by Google on 2025-11-18; the OpenClaw runtime now rejects them at config
-- validation ("Unknown model … Use google/gemini-2.5-flash."), so a machine
-- pinned to one fails its live config push. Map them to the stable IDs.
--
-- Guarded so it's a no-op if a row with the target ID already exists (e.g. a
-- later reseed) — model_catalog.id is the primary key.

UPDATE model_catalog
SET id = 'google/gemini-2.5-flash',
    label = 'Gemini 2.5 Flash'
WHERE id = 'google/gemini-2.5-flash-preview-05-20'
  AND NOT EXISTS (SELECT 1 FROM model_catalog WHERE id = 'google/gemini-2.5-flash');

UPDATE model_catalog
SET id = 'google/gemini-2.5-pro',
    label = 'Gemini 2.5 Pro'
WHERE id = 'google/gemini-2.5-pro-preview-05-06'
  AND NOT EXISTS (SELECT 1 FROM model_catalog WHERE id = 'google/gemini-2.5-pro');

-- Renaming the catalog row is not enough: existing machines persist their own
-- model selection in machine_config.platform_overrides ('preferred_model' and
-- the 'model_fallbacks' array). GetMachinePreferredModel reads those directly,
-- and mapModelFromCatalog passes an ID it can't find in the catalog through
-- unchanged — so a machine still pinned to a preview ID keeps generating configs
-- with the rejected model. Rewrite the persisted selections too.

-- preferred_model (single string)
UPDATE machine_config
SET platform_overrides = jsonb_set(platform_overrides, '{preferred_model}', to_jsonb('google/gemini-2.5-flash'::text))
WHERE platform_overrides->>'preferred_model' = 'google/gemini-2.5-flash-preview-05-20';

UPDATE machine_config
SET platform_overrides = jsonb_set(platform_overrides, '{preferred_model}', to_jsonb('google/gemini-2.5-pro'::text))
WHERE platform_overrides->>'preferred_model' = 'google/gemini-2.5-pro-preview-05-06';

-- model_fallbacks (JSON array of strings) — rebuild the array, remapping the
-- retired IDs elementwise. Guarded to arrays that actually contain one, so
-- jsonb_agg never collapses an untouched or empty array to null.
UPDATE machine_config
SET platform_overrides = jsonb_set(
        platform_overrides,
        '{model_fallbacks}',
        (
            SELECT jsonb_agg(
                CASE elem #>> '{}'
                    WHEN 'google/gemini-2.5-flash-preview-05-20' THEN to_jsonb('google/gemini-2.5-flash'::text)
                    WHEN 'google/gemini-2.5-pro-preview-05-06'   THEN to_jsonb('google/gemini-2.5-pro'::text)
                    ELSE elem
                END
            )
            FROM jsonb_array_elements(platform_overrides->'model_fallbacks') AS elem
        )
    )
WHERE jsonb_typeof(platform_overrides->'model_fallbacks') = 'array'
  AND (platform_overrides->'model_fallbacks' @> '["google/gemini-2.5-flash-preview-05-20"]'
    OR platform_overrides->'model_fallbacks' @> '["google/gemini-2.5-pro-preview-05-06"]');

-- assembled_config is a cached blob regenerated from the selections above on the
-- next config push/start, but rewrite the stored copy too so any reuse/display
-- is consistent. These IDs are specific enough that a scoped string replace has
-- no substring-collision risk.
UPDATE machine_config
SET assembled_config = replace(
        replace(assembled_config::text,
            'google/gemini-2.5-flash-preview-05-20', 'google/gemini-2.5-flash'),
        'google/gemini-2.5-pro-preview-05-06', 'google/gemini-2.5-pro')::jsonb
WHERE assembled_config IS NOT NULL
  AND (assembled_config::text LIKE '%gemini-2.5-flash-preview-05-20%'
    OR assembled_config::text LIKE '%gemini-2.5-pro-preview-05-06%');
