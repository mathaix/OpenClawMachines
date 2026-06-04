-- Custom API providers: user-registered providers beyond the built-in set
CREATE TABLE IF NOT EXISTS custom_providers (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    account_id      INTEGER NOT NULL REFERENCES accounts(id),
    name            VARCHAR(100) NOT NULL,
    upstream_host   VARCHAR(255) NOT NULL,
    scheme          VARCHAR(10) DEFAULT 'https',
    auth_method     VARCHAR(50) NOT NULL,
    auth_header     VARCHAR(100),
    path_prefix     VARCHAR(255) DEFAULT '',
    allowed_hosts   TEXT[],
    is_llm          BOOLEAN DEFAULT false,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(account_id, name)
);

CREATE INDEX IF NOT EXISTS custom_providers_account_id_idx ON custom_providers (account_id);
