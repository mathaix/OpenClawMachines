-- Add per-VM tunnel tracking columns
ALTER TABLE machines ADD COLUMN IF NOT EXISTS tunnel_id TEXT;
ALTER TABLE machines ADD COLUMN IF NOT EXISTS signing_key TEXT;
