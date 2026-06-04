-- 047_usage_dashboard.sql

-- Session snapshots from gateway polling
CREATE TABLE session_snapshots (
  id BIGSERIAL PRIMARY KEY,
  machine_id TEXT NOT NULL,
  session_key TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT 'unknown',
  channel TEXT NOT NULL DEFAULT 'chat',
  input_tokens INT NOT NULL DEFAULT 0,
  output_tokens INT NOT NULL DEFAULT 0,
  total_tokens INT NOT NULL DEFAULT 0,
  polled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_session_snapshots_machine ON session_snapshots(machine_id, polled_at);
CREATE INDEX idx_session_snapshots_agent ON session_snapshots(machine_id, agent_id, polled_at);

-- Future-ready category columns on token_usage
ALTER TABLE token_usage ADD COLUMN IF NOT EXISTS category TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE token_usage ADD COLUMN IF NOT EXISTS category_detail TEXT;
