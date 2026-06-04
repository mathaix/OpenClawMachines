CREATE TABLE IF NOT EXISTS machine_placements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    host_id INT NOT NULL REFERENCES hosts(id),
    vm_ip TEXT,
    state TEXT NOT NULL DEFAULT 'reserved',
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    created_by_operation_id UUID REFERENCES machine_operations(id),
    CONSTRAINT machine_placements_state_check CHECK (state IN ('reserved', 'active', 'released'))
);

-- One active placement per machine
CREATE UNIQUE INDEX IF NOT EXISTS idx_placements_active_machine
ON machine_placements(machine_id) WHERE released_at IS NULL;

-- One active (host_id, vm_ip) pair
CREATE UNIQUE INDEX IF NOT EXISTS idx_placements_active_host_ip
ON machine_placements(host_id, vm_ip) WHERE released_at IS NULL;

-- Fast lookup by host for reconciler
CREATE INDEX IF NOT EXISTS idx_placements_host ON machine_placements(host_id) WHERE released_at IS NULL;
