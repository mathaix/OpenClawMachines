-- Add version tracking to hosts
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS agent_version TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS openclaw_version TEXT;
