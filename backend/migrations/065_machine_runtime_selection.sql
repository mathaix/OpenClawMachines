-- Phase 0: machine-level desired/resolved runtime selection.
-- Backward compatible: legacy rootfs_snapshot/openclaw_version remain in place.

ALTER TABLE machines
  ADD COLUMN IF NOT EXISTS desired_rootfs_version TEXT,
  ADD COLUMN IF NOT EXISTS desired_openclaw_version TEXT,
  ADD COLUMN IF NOT EXISTS desired_channel TEXT,
  ADD COLUMN IF NOT EXISTS resolved_rootfs_version TEXT,
  ADD COLUMN IF NOT EXISTS resolved_openclaw_version TEXT,
  ADD COLUMN IF NOT EXISTS version_source TEXT,
  ADD COLUMN IF NOT EXISTS runtime_source TEXT NOT NULL DEFAULT 'auto';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'chk_machines_desired_channel'
  ) THEN
    ALTER TABLE machines
      ADD CONSTRAINT chk_machines_desired_channel
      CHECK (
        desired_channel IS NULL
        OR desired_channel IN ('stable', 'rc', 'dev')
      );
  END IF;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'chk_machines_runtime_source'
  ) THEN
    ALTER TABLE machines
      ADD CONSTRAINT chk_machines_runtime_source
      CHECK (runtime_source IN ('auto', 'artifact', 'legacy_baked'));
  END IF;
END $$;

-- Backfill resolved values from existing legacy version fields when present.
UPDATE machines
SET
  resolved_rootfs_version = COALESCE(resolved_rootfs_version, rootfs_snapshot),
  resolved_openclaw_version = COALESCE(resolved_openclaw_version, openclaw_version),
  version_source = COALESCE(
    version_source,
    CASE
      WHEN rootfs_snapshot IS NOT NULL OR openclaw_version IS NOT NULL THEN 'legacy'
      ELSE 'default'
    END
  )
WHERE
  resolved_rootfs_version IS NULL
  OR resolved_openclaw_version IS NULL
  OR version_source IS NULL;
