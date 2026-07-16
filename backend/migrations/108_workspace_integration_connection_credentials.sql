-- Store credentials on normalized workspace integration connections.
--
-- Legacy workspace_integration_credentials remain for v1-backed rows while
-- provider flows migrate. This table is keyed by connection_id so normalized
-- connections can execute without a workspace_integrations runtime row.

CREATE TABLE IF NOT EXISTS workspace_integration_connection_credentials (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id  UUID NOT NULL REFERENCES workspace_integration_connections(id) ON DELETE CASCADE,
    secret_enc     TEXT NOT NULL,
    refresh_enc    TEXT,
    token_type     TEXT,
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(connection_id)
);

INSERT INTO workspace_integration_connection_credentials (
    connection_id, secret_enc, refresh_enc, token_type, expires_at, created_at, updated_at
)
SELECT
    conn.id,
    cred.secret_enc,
    cred.refresh_enc,
    cred.token_type,
    cred.expires_at,
    cred.created_at,
    cred.updated_at
FROM workspace_integration_connections conn
JOIN workspace_integration_credentials cred
  ON cred.integration_id = conn.legacy_integration_id
ON CONFLICT (connection_id) DO NOTHING;
