-- Adds an optional compatibility floor on openclaw release rows:
-- the minimum rootfs version this openclaw is known to run on.
-- NULL means "no constraint" so all existing rows stay permissive.
-- Only meaningful on rows with kind='openclaw'.
ALTER TABLE artifact_releases
    ADD COLUMN IF NOT EXISTS min_rootfs_version TEXT;

-- Backfill: v2026.4.15-r1 was built on Node 24 and requires a Node-24 rootfs.
-- The older rootfs (cd60435-20260414T143154Z, Node 22) triggers a NODE_MODULE_VERSION
-- mismatch → overlay-init exits non-zero → kernel panic. Pin the floor so that
-- openclaw-only upgrades against a stale-rootfs machine are rejected at the API.
-- Older openclaw rows stay NULL (no constraint) — they were compatible with the
-- rootfs they shipped against. No-op if the row hasn't been inserted yet.
UPDATE artifact_releases
SET min_rootfs_version = '1ae2d37-20260418T234618Z'
WHERE kind = 'openclaw'
  AND version = 'v2026.4.15-r1'
  AND min_rootfs_version IS NULL;
