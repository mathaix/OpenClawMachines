-- Migration 046: Unified activity log for business-level event tracking.
-- Every event is self-contained with denormalized context (no joins needed).

CREATE TABLE IF NOT EXISTS activity_log (
    id             BIGSERIAL PRIMARY KEY,
    event_id       UUID DEFAULT gen_random_uuid() NOT NULL,

    -- What happened
    category       TEXT NOT NULL,
    action         TEXT NOT NULL,
    status         TEXT NOT NULL,

    -- Who did it (denormalized)
    actor_type     TEXT NOT NULL,
    actor_id       INT,
    actor_name     TEXT,

    -- Which account (denormalized)
    account_id     INT REFERENCES accounts(id),
    account_name   TEXT,

    -- Which machine (denormalized)
    machine_id     TEXT,
    machine_name   TEXT,

    -- Which host (denormalized)
    host_id        INT,
    host_name      TEXT,

    -- Versions at time of event
    agent_version  TEXT,
    rootfs_version TEXT,

    -- Human-readable summary
    summary        TEXT NOT NULL,

    -- Structured detail
    detail         JSONB,

    -- Timing
    started_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms    INT,

    -- Failure info
    error_code     TEXT,
    error_message  TEXT
);

CREATE INDEX idx_activity_account   ON activity_log (account_id, started_at DESC, id DESC);
CREATE INDEX idx_activity_machine   ON activity_log (machine_id, started_at DESC, id DESC) WHERE machine_id IS NOT NULL;
CREATE INDEX idx_activity_actor     ON activity_log (actor_type, actor_id, started_at DESC, id DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX idx_activity_category  ON activity_log (category, started_at DESC, id DESC);
CREATE INDEX idx_activity_host      ON activity_log (host_id, started_at DESC, id DESC) WHERE host_id IS NOT NULL;
CREATE INDEX idx_activity_status    ON activity_log (status, started_at DESC, id DESC) WHERE status = 'failure';
CREATE INDEX idx_activity_action    ON activity_log (action, started_at DESC, id DESC);
