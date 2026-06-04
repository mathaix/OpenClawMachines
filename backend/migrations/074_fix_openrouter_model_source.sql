-- Fix OpenRouter models incorrectly seeded as 'platform' source.
--
-- Platform models bypass credential gating in AvailableCatalogIDs, so
-- OpenRouter models were showing up for all machines even without an
-- OpenRouter API key. They should be 'byok' — only visible when the
-- machine has an OpenRouter credential configured.

BEGIN;

UPDATE model_catalog
   SET source = 'byok'
 WHERE provider = 'openrouter'
   AND source = 'platform';

COMMIT;
