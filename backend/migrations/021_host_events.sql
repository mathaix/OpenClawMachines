-- Host event log: tracks all changes to host VMs (IP changes, upgrades, status transitions, heartbeats)
CREATE TABLE IF NOT EXISTS host_events (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL DEFAULT gen_random_uuid()::text,
    event_type TEXT NOT NULL,        -- heartbeat.ip_changed, agent.upgraded, rootfs.upgraded, status.changed, host.provisioned, host.destroyed
    host_id INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    metadata JSONB,                  -- arbitrary key-value data (old_ip, new_ip, old_version, new_version, etc.)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_host_events_host_id ON host_events(host_id);
CREATE INDEX IF NOT EXISTS idx_host_events_type ON host_events(event_type);
CREATE INDEX IF NOT EXISTS idx_host_events_created_at ON host_events(created_at);

-- Heartbeat tracking on hosts table
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS last_heartbeat TIMESTAMPTZ;
