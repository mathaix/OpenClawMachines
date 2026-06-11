-- Allow the 'experimental' release channel for artifact releases and machine
-- runtime selection (used by experimental rootfs uploads via
-- ROOTFS_SKIP_LATEST_MANIFEST=1 / ROOTFS_CHANNEL=experimental).

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'artifact_releases'::regclass
      AND conname = 'artifact_releases_channel_check'
  ) THEN
    ALTER TABLE artifact_releases
      DROP CONSTRAINT artifact_releases_channel_check;
  END IF;

  ALTER TABLE artifact_releases
    ADD CONSTRAINT artifact_releases_channel_check
    CHECK (channel IN ('stable', 'rc', 'dev', 'experimental'));

  IF EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'machines'::regclass
      AND conname = 'chk_machines_desired_channel'
  ) THEN
    ALTER TABLE machines
      DROP CONSTRAINT chk_machines_desired_channel;
  END IF;

  ALTER TABLE machines
    ADD CONSTRAINT chk_machines_desired_channel
    CHECK (
      desired_channel IS NULL
      OR desired_channel IN ('stable', 'rc', 'dev', 'experimental')
    );
END $$;
