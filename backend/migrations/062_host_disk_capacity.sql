-- Add disk capacity tracking to hosts table.
-- Mirrors existing vCPU and memory pattern: capacity vs used counters.
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS capacity_disk_mb BIGINT NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS used_disk_mb BIGINT NOT NULL DEFAULT 0;
