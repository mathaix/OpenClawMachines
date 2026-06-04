ALTER TABLE hosts
ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'gcp',
ADD COLUMN IF NOT EXISTS provider_class TEXT NOT NULL DEFAULT 'managed',
ADD COLUMN IF NOT EXISTS lifecycle_mode TEXT NOT NULL DEFAULT 'provisioned',
ADD COLUMN IF NOT EXISTS agent_endpoint TEXT,
ADD COLUMN IF NOT EXISTS agent_endpoint_type TEXT NOT NULL DEFAULT 'public_http',
ADD COLUMN IF NOT EXISTS provider_host_id TEXT,
ADD COLUMN IF NOT EXISTS provider_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS agent_token TEXT;

CREATE INDEX IF NOT EXISTS idx_hosts_provider ON hosts(provider);
CREATE INDEX IF NOT EXISTS idx_hosts_lifecycle_mode ON hosts(lifecycle_mode);
