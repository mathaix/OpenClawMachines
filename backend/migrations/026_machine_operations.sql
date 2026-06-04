-- Machine operations track in-flight lifecycle operations for idempotency and race prevention.
CREATE TABLE IF NOT EXISTS machine_operations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,        -- 'start', 'stop', 'delete'
    state TEXT NOT NULL,       -- 'pending', 'in_progress', 'completed', 'failed'
    idempotency_key TEXT,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_machine_operations_machine_id ON machine_operations(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_operations_state ON machine_operations(state) WHERE state IN ('pending', 'in_progress');

-- Only one active (non-completed) operation per machine at a time
CREATE UNIQUE INDEX IF NOT EXISTS idx_machine_operations_active_unique
ON machine_operations(machine_id)
WHERE state IN ('pending', 'in_progress');

-- Add current_operation_id to machines for CAS-style transition ownership
ALTER TABLE machines ADD COLUMN IF NOT EXISTS current_operation_id UUID;
