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
