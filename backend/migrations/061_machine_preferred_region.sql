-- Add preferred region to machine placement intent.
-- Null means use account/system defaults.
ALTER TABLE machines
ADD COLUMN IF NOT EXISTS preferred_region TEXT;

CREATE INDEX IF NOT EXISTS idx_machines_preferred_region
ON machines(preferred_region)
WHERE preferred_region IS NOT NULL;
