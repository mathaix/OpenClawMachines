-- Indexes for the account-scoped machine trace panel.
--
-- The panel lists traces by account + machine + time and also includes traces
-- where the selected machine emitted spans into an account-owned trace.
CREATE INDEX IF NOT EXISTS idx_opik_traces_account_machine_start
    ON opik_traces(account_id, machine_id, start_time DESC);

CREATE INDEX IF NOT EXISTS idx_opik_spans_account_machine_trace
    ON opik_spans(account_id, machine_id, trace_id);

CREATE INDEX IF NOT EXISTS idx_opik_spans_account_trace_start
    ON opik_spans(account_id, trace_id, start_time ASC, created_at ASC);
