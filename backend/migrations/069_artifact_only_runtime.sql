-- Make "artifact" the only valid runtime_source. All machines are now
-- artifact-based; legacy_baked and auto are removed.

-- Convert any remaining legacy_baked machines to artifact.
UPDATE machines
SET runtime_source = 'artifact'
WHERE runtime_source IN ('legacy_baked', 'auto');

-- Drop the old constraint and add a new one that only allows 'artifact'.
ALTER TABLE machines DROP CONSTRAINT IF EXISTS chk_machines_runtime_source;
ALTER TABLE machines
  ADD CONSTRAINT chk_machines_runtime_source
  CHECK (runtime_source = 'artifact');

-- Update the column default from 'auto' to 'artifact'.
ALTER TABLE machines ALTER COLUMN runtime_source SET DEFAULT 'artifact';
