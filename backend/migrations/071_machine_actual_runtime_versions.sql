ALTER TABLE machines
  ADD COLUMN IF NOT EXISTS actual_rootfs_version TEXT,
  ADD COLUMN IF NOT EXISTS actual_openclaw_version TEXT;

UPDATE machines
SET actual_rootfs_version = COALESCE(actual_rootfs_version, rootfs_snapshot),
    actual_openclaw_version = COALESCE(actual_openclaw_version, openclaw_version)
WHERE actual_rootfs_version IS NULL
   OR actual_openclaw_version IS NULL;
