-- Allow Hermes rootfs artifacts to be tracked separately from OpenClaw rootfs.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'artifact_releases'::regclass
      AND conname = 'artifact_releases_kind_check'
  ) THEN
    ALTER TABLE artifact_releases
      DROP CONSTRAINT artifact_releases_kind_check;
  END IF;

  ALTER TABLE artifact_releases
    ADD CONSTRAINT artifact_releases_kind_check
    CHECK (kind IN ('rootfs', 'openclaw', 'hermes-rootfs'));

  IF EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'host_artifact_state'::regclass
      AND conname = 'host_artifact_state_kind_check'
  ) THEN
    ALTER TABLE host_artifact_state
      DROP CONSTRAINT host_artifact_state_kind_check;
  END IF;

  ALTER TABLE host_artifact_state
    ADD CONSTRAINT host_artifact_state_kind_check
    CHECK (kind IN ('rootfs', 'openclaw', 'hermes-rootfs'));
END $$;
