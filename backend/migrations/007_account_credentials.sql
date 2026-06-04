-- Account-level credentials for API keys and bot tokens.
-- Keys are shared across all machines in the account.
-- LLM keys (e.g. anthropic) are proxied via the host LLM proxy and never enter the VM.
-- Non-LLM tokens (e.g. telegram) are delivered directly to the VM via the metadata service.
CREATE TABLE IF NOT EXISTS account_credentials (
    id              SERIAL PRIMARY KEY,
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,         -- 'anthropic', 'telegram', 'openai', etc.
    credential_type TEXT NOT NULL,         -- 'api_key', 'bot_token'
    encrypted_value TEXT NOT NULL,         -- AES-256-GCM (same as secrets table)
    label           TEXT,                  -- user label, e.g. "Production key"
    last_validated  TIMESTAMPTZ,           -- last successful validation
    last_four       TEXT,                  -- last 4 chars for display
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(account_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_account_credentials_account_id ON account_credentials(account_id);
