-- 053_host_maintenance_mode.sql
-- Add explicit maintenance mode toggle for hosts.
-- When true, the reconciler skips heartbeat staleness checks for this host.

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS maintenance_mode BOOLEAN NOT NULL DEFAULT false;
