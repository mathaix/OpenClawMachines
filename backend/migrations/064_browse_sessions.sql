-- Browse sessions track active `ocm machines browse` CLI connections.
-- The backend janitor auto-cleans expired sessions (ungraceful CLI exit).

CREATE TABLE IF NOT EXISTS browse_sessions (
    id          TEXT PRIMARY KEY DEFAULT 'bs_' || gen_random_uuid(),
    machine_id  UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    account_id  INTEGER NOT NULL,
    cdp_target  TEXT NOT NULL DEFAULT '127.0.0.1:9222',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id)
);
