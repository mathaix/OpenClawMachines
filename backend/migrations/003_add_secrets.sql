-- Per-machine secret storage for API keys, tokens, etc.
CREATE TABLE IF NOT EXISTS secrets (
    id              SERIAL PRIMARY KEY,
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    encrypted_value TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(machine_id, key)
);

CREATE INDEX IF NOT EXISTS idx_secrets_machine_id ON secrets(machine_id);
