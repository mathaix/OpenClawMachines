CREATE TABLE IF NOT EXISTS capacity_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_ref TEXT,
    cpu_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    memory_overcommit_ratio NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    reserve_vcpus INT NOT NULL DEFAULT 0,
    reserve_memory_mb INT NOT NULL DEFAULT 0,
    max_machine_count INT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT capacity_policies_scope_check CHECK (scope_type IN ('global', 'host_pool', 'host')),
    CONSTRAINT capacity_policies_cpu_ratio CHECK (cpu_overcommit_ratio BETWEEN 1.0 AND 3.0),
    CONSTRAINT capacity_policies_mem_ratio CHECK (memory_overcommit_ratio BETWEEN 1.0 AND 1.2),
    CONSTRAINT capacity_policies_reserve_vcpus CHECK (reserve_vcpus >= 0),
    CONSTRAINT capacity_policies_reserve_mem CHECK (reserve_memory_mb >= 0)
);

-- One enabled policy per scope
CREATE UNIQUE INDEX IF NOT EXISTS idx_capacity_policies_scope_unique
ON capacity_policies(scope_type, scope_ref) WHERE enabled = true;

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS host_pool TEXT NOT NULL DEFAULT 'default';
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS capacity_policy_id UUID REFERENCES capacity_policies(id);
ALTER TABLE machines ADD COLUMN IF NOT EXISTS home_host_id INT REFERENCES hosts(id) ON DELETE SET NULL;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS storage_mode TEXT NOT NULL DEFAULT 'host_local';

-- Default global policy: 2x CPU overcommit (matches current implicit behavior), 1x memory
INSERT INTO capacity_policies (name, scope_type, cpu_overcommit_ratio, memory_overcommit_ratio)
SELECT 'default', 'global', 2.0, 1.0
WHERE NOT EXISTS (
    SELECT 1 FROM capacity_policies WHERE scope_type = 'global' AND name = 'default' AND enabled = true
);
