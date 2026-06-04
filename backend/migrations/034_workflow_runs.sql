CREATE TABLE IF NOT EXISTS workflow_runs (
    id              TEXT PRIMARY KEY,
    kind            TEXT NOT NULL,
    scope_type      TEXT NOT NULL,
    scope_id        TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('queued', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'manual_action_required')),
    current_phase   TEXT,
    requested_by    INT REFERENCES users(id) ON DELETE SET NULL,
    account_id      INT REFERENCES accounts(id) ON DELETE CASCADE,
    priority        TEXT NOT NULL DEFAULT 'normal',
    input_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_json     JSONB,
    summary_json    JSONB,
    error_code      TEXT,
    error_message   TEXT,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_scope ON workflow_runs(scope_type, scope_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_account ON workflow_runs(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_status ON workflow_runs(status, created_at DESC);

CREATE TABLE IF NOT EXISTS workflow_events (
    id              BIGSERIAL PRIMARY KEY,
    workflow_id     TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    phase           TEXT,
    level           TEXT NOT NULL CHECK (level IN ('info', 'warn', 'error')),
    event_type      TEXT NOT NULL,
    message         TEXT NOT NULL,
    details_json    JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_workflow_events_workflow ON workflow_events(workflow_id, created_at ASC);

CREATE TABLE IF NOT EXISTS workflow_locks (
    resource_type      TEXT NOT NULL,
    resource_id        TEXT NOT NULL,
    lock_kind          TEXT NOT NULL,
    workflow_id        TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    lease_expires_at   TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (resource_type, resource_id, lock_kind)
);

CREATE INDEX IF NOT EXISTS idx_workflow_locks_workflow ON workflow_locks(workflow_id);
