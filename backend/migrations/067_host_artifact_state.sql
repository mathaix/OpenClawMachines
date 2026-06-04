-- Per-host artifact staging state. Tracks which release version is staged,
-- active (currently booting VMs), and the host-level default.
-- One row per (host_id, kind) pair.
CREATE TABLE IF NOT EXISTS host_artifact_state (
    id                  SERIAL PRIMARY KEY,
    host_id             INT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL CHECK (kind IN ('rootfs', 'openclaw')),
    staged_version      TEXT,
    active_version      TEXT,
    default_version     TEXT,
    last_staged_at      TIMESTAMPTZ,
    last_activated_at   TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (host_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_host_artifact_state_host
    ON host_artifact_state (host_id);
