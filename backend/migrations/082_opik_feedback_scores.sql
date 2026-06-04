-- Feedback scores for Opik traces and spans.
--
-- Scores are account-scoped so reviews stay tenant-isolated. span_id is
-- optional: a score can describe the whole trace or one specific span.
CREATE TABLE IF NOT EXISTS opik_feedback_scores (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    trace_id UUID NOT NULL REFERENCES opik_traces(id) ON DELETE CASCADE,
    span_id UUID REFERENCES opik_spans(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    value DOUBLE PRECISION NOT NULL CHECK (value >= 0 AND value <= 1),
    reason TEXT,
    source TEXT NOT NULL DEFAULT 'human',
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_opik_feedback_account_trace_created
    ON opik_feedback_scores(account_id, trace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_opik_feedback_account_trace_span
    ON opik_feedback_scores(account_id, trace_id, span_id);

CREATE INDEX IF NOT EXISTS idx_opik_feedback_account_name_value
    ON opik_feedback_scores(account_id, name, value);
