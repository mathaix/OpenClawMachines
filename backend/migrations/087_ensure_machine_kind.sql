-- Repair migration for environments where migration number 085 was already
-- consumed before 085_machine_kind.sql was introduced.

ALTER TABLE machines
  ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'openclaw';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conrelid = 'machines'::regclass
      AND conname = 'chk_machines_kind'
  ) THEN
    ALTER TABLE machines
      ADD CONSTRAINT chk_machines_kind
      CHECK (kind IN ('openclaw', 'hermes'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_machines_kind ON machines(kind);
