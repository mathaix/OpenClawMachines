-- Drop old credential tables (no users, clean slate)
DROP TABLE IF EXISTS machine_credentials;
DROP TABLE IF EXISTS account_credentials;

-- New credentials table: one credential per provider per machine
CREATE TABLE credentials (
    id SERIAL PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id),
    machine_id UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    credential_type TEXT NOT NULL DEFAULT 'api_key',
    encrypted_value TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    last_validated TIMESTAMPTZ,
    last_four TEXT,
    expires_at TIMESTAMPTZ,
    refresh_token TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    status_detail TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT credentials_machine_provider_unique UNIQUE (machine_id, provider)
);

CREATE INDEX idx_credentials_account ON credentials(account_id);
CREATE INDEX idx_credentials_machine ON credentials(machine_id);
