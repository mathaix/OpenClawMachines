-- Phase 3 Hermes scaffold: make machines explicitly payload-kind aware.
-- Existing machines are OpenClaw machines by default. Browser VMs remain a
-- separate resource in browser_vms and are not represented by this column.

ALTER TABLE machines
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'openclaw';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'chk_machines_kind'
  ) THEN
    ALTER TABLE machines
      ADD CONSTRAINT chk_machines_kind
      CHECK (kind IN ('openclaw', 'hermes'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_machines_kind ON machines(kind);
