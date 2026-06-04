-- Immutable catalog of published artifact releases.
-- Each row represents one published version of a rootfs or openclaw artifact.
CREATE TABLE IF NOT EXISTS artifact_releases (
    id          SERIAL PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('rootfs', 'openclaw')),
    version     TEXT NOT NULL,
    channel     TEXT NOT NULL DEFAULT 'stable' CHECK (channel IN ('stable', 'rc', 'dev')),
    url         TEXT NOT NULL,
    sha256      TEXT NOT NULL,
    size_bytes  BIGINT,
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_artifact_releases_kind_version
    ON artifact_releases (kind, version);

CREATE INDEX IF NOT EXISTS idx_artifact_releases_kind_channel
    ON artifact_releases (kind, channel, created_at DESC);
