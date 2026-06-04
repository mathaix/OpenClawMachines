-- Add source_image column to track which snapshot a host was created from
-- This enables the scheduler to only use hosts with matching image versions
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS source_image TEXT;

-- Index for fast lookups by source_image
CREATE INDEX IF NOT EXISTS idx_hosts_source_image ON hosts(source_image);
