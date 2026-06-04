-- 049_opik_tracing.sql
-- Opik-compatible tracing tables for LLM observability

-- Projects (workspace maps to account)
CREATE TABLE opik_projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    account_id INTEGER NOT NULL,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(account_id, name)
);

-- Traces (one per agent conversation turn)
CREATE TABLE opik_traces (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES opik_projects(id),
    account_id INTEGER NOT NULL,
    machine_id TEXT NOT NULL,
    name TEXT,
    thread_id TEXT,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    input JSONB,
    output JSONB,
    metadata JSONB,
    tags TEXT[],
    error_info JSONB,
    source TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ
);

CREATE INDEX idx_opik_traces_project ON opik_traces(project_id, start_time DESC);
CREATE INDEX idx_opik_traces_machine ON opik_traces(machine_id, start_time DESC);
CREATE INDEX idx_opik_traces_account ON opik_traces(account_id, start_time DESC);
CREATE INDEX idx_opik_traces_thread ON opik_traces(thread_id);

-- Spans (LLM calls, tool calls, subagent calls)
CREATE TABLE opik_spans (
    id UUID PRIMARY KEY,
    trace_id UUID NOT NULL REFERENCES opik_traces(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES opik_projects(id),
    account_id INTEGER NOT NULL,
    machine_id TEXT NOT NULL,
    parent_span_id UUID,
    name TEXT,
    type TEXT NOT NULL DEFAULT 'general',
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ,
    input JSONB,
    output JSONB,
    metadata JSONB,
    model TEXT,
    provider TEXT,
    tags TEXT[],
    usage JSONB,
    error_info JSONB,
    total_estimated_cost NUMERIC(12,8),
    duration DOUBLE PRECISION,
    ttft DOUBLE PRECISION,
    source TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ
);

CREATE INDEX idx_opik_spans_trace ON opik_spans(trace_id);
CREATE INDEX idx_opik_spans_project ON opik_spans(project_id, start_time DESC);
CREATE INDEX idx_opik_spans_machine_llm ON opik_spans(machine_id, start_time DESC) WHERE type = 'llm';
CREATE INDEX idx_opik_spans_account_llm ON opik_spans(account_id, start_time DESC) WHERE type = 'llm';
CREATE INDEX idx_opik_spans_model ON opik_spans(model, start_time DESC) WHERE type = 'llm';

-- Add opik-openclaw to plugin catalog in the "observability" slot.
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template, status, sort_order)
VALUES (
    'opik-openclaw',
    'Opik Observability',
    'OpenClaw observability plugin — traces LLM calls, tool executions, and agent interactions',
    'observability',
    '1',
    'bundled',
    '{"plugins": {"slots": {"observability": "opik-openclaw"}, "entries": {"opik-openclaw": {"enabled": true, "config": {"projectName": "default", "workspaceName": "default", "tags": ["ocm"]}}}}}',
    'active',
    10
) ON CONFLICT (id) DO UPDATE SET
    config_template = EXCLUDED.config_template,
    slot = EXCLUDED.slot,
    description = EXCLUDED.description;

-- Auto-enable for all existing machines that don't already have an observability plugin.
INSERT INTO machine_plugins (machine_id, plugin_id, slot, enabled, install_status, installed_version)
SELECT m.id, 'opik-openclaw', 'observability', true, 'installed', '1'
FROM machines m
WHERE NOT EXISTS (
    SELECT 1 FROM machine_plugins mp
    WHERE mp.machine_id = m.id AND mp.slot = 'observability' AND mp.enabled = true
)
ON CONFLICT (machine_id, plugin_id) DO NOTHING;
