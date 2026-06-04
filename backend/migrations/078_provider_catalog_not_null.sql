-- Tighten provider_catalog: defaulted columns become NOT NULL so the Go
-- scan targets can stay non-pointer (string/int/bool/time.Time). Without
-- NOT NULL, a future INSERT ... NULL would crash every read path.
--
-- We backfill any NULLs to the column default first (defensive — current
-- inserts in 075 always rely on the DEFAULT, but a hand-written UPDATE
-- could have introduced one).

BEGIN;

UPDATE provider_catalog SET upstream_path_prefix = '' WHERE upstream_path_prefix IS NULL;
UPDATE provider_catalog SET scheme = 'https' WHERE scheme IS NULL;
UPDATE provider_catalog SET credential_type = 'api_key' WHERE credential_type IS NULL;
UPDATE provider_catalog SET sort_order = 0 WHERE sort_order IS NULL;
UPDATE provider_catalog SET enabled = true WHERE enabled IS NULL;
UPDATE provider_catalog SET created_at = now() WHERE created_at IS NULL;

ALTER TABLE provider_catalog
  ALTER COLUMN upstream_path_prefix SET NOT NULL,
  ALTER COLUMN scheme               SET NOT NULL,
  ALTER COLUMN credential_type      SET NOT NULL,
  ALTER COLUMN sort_order           SET NOT NULL,
  ALTER COLUMN enabled              SET NOT NULL,
  ALTER COLUMN created_at           SET NOT NULL;

COMMIT;
