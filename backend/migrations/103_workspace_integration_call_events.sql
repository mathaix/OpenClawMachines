-- Durable workspace integration call audit and health telemetry.
--
-- Raw argument values and upstream bodies must never be stored here. Argument
-- evidence is limited to keys and sanitized shape facts.

CREATE TABLE IF NOT EXISTS workspace_integration_call_events (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id            INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    machine_id            UUID REFERENCES machines(id) ON DELETE SET NULL,
    integration_id        UUID REFERENCES workspace_integrations(id) ON DELETE SET NULL,
    integration_slug      TEXT NOT NULL,
    tool_name             TEXT NOT NULL,
    tool_id               TEXT NOT NULL,
    tool_address          TEXT,
    call_mode             TEXT NOT NULL,
    transport             TEXT NOT NULL,
    access                TEXT NOT NULL DEFAULT 'read'
        CHECK (access IN ('read', 'write')),
    status                TEXT NOT NULL
        CHECK (status IN ('success', 'error')),
    failure_class         TEXT,
    upstream_status       INT,
    latency_ms            INT NOT NULL DEFAULT 0,
    ocm_overhead_ms       INT,
    upstream_latency_ms   INT,
    retry_count           INT NOT NULL DEFAULT 0,
    retry_after_ms        INT,
    retryable             BOOLEAN NOT NULL DEFAULT false,
    terminal              BOOLEAN NOT NULL DEFAULT false,
    arg_keys              TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    arg_shape             JSONB NOT NULL DEFAULT '{}'::jsonb,
    sample_rate           DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    detail_level          TEXT NOT NULL DEFAULT 'telemetry'
        CHECK (detail_level IN ('audit', 'telemetry')),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_call_events_workspace_time
    ON workspace_integration_call_events(workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_call_events_tool_time
    ON workspace_integration_call_events(workspace_id, tool_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_integration_call_events_status
    ON workspace_integration_call_events(workspace_id, status, created_at DESC);
