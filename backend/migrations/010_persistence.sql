-- Add data volume size column for persistent data volumes.
-- Default 5GB for all existing and new machines.
ALTER TABLE machines ADD COLUMN IF NOT EXISTS data_volume_gb INT NOT NULL DEFAULT 5;
