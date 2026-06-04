-- 036: Add machine_agents table for control-plane-managed OpenClaw personas

CREATE TABLE IF NOT EXISTS machine_agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    agent_id        TEXT NOT NULL,
    name            TEXT NOT NULL,
    model           TEXT,
    identity_emoji  TEXT,
    identity_avatar TEXT,
    soul            TEXT,
    is_default      BOOLEAN NOT NULL DEFAULT false,
    sort_order      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(machine_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_machine_agents_machine_id ON machine_agents(machine_id);
