-- Workspace-scoped integrations: parallel MCP/API-native integration model.
-- This intentionally does not replace or mutate the existing Composio-backed
-- integration tables or plugin catalog entry.

CREATE TABLE IF NOT EXISTS workspaces (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    slug       TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(account_id, slug),
    UNIQUE(account_id, id)
);

ALTER TABLE machines ADD COLUMN IF NOT EXISTS workspace_id UUID;

INSERT INTO workspaces (account_id, slug, name)
SELECT a.id, 'default', 'Default'
FROM accounts a
ON CONFLICT (account_id, slug) DO NOTHING;

UPDATE machines m
SET workspace_id = w.id
FROM workspaces w
WHERE w.account_id = m.account_id
  AND w.slug = 'default'
  AND m.workspace_id IS NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'machines_account_workspace_fk'
    ) THEN
        ALTER TABLE machines
            ADD CONSTRAINT machines_account_workspace_fk
            FOREIGN KEY (account_id, workspace_id)
            REFERENCES workspaces(account_id, id)
            ON DELETE RESTRICT;
    END IF;
END
$$;

ALTER TABLE machines ALTER COLUMN workspace_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS idx_machines_workspace_id ON machines(workspace_id);

CREATE TABLE IF NOT EXISTS workspace_integrations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug          TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    kind          TEXT NOT NULL,
    transport     TEXT NOT NULL,
    endpoint      TEXT,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    tool_manifest JSONB NOT NULL DEFAULT '[]'::jsonb,
    config        JSONB NOT NULL DEFAULT '{}'::jsonb,
    allowed_tools TEXT[],
    denied_tools  TEXT[],
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_workspace_integrations_workspace_enabled
    ON workspace_integrations(workspace_id, enabled);

CREATE TABLE IF NOT EXISTS workspace_integration_credentials (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id UUID NOT NULL REFERENCES workspace_integrations(id) ON DELETE CASCADE,
    secret_enc     TEXT NOT NULL,
    refresh_enc    TEXT,
    token_type     TEXT,
    expires_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(integration_id)
);

-- Legacy plugin catalog entry. Runtime delivery is native mcp.servers.ocm; this
-- row remains as the existing per-machine capability flag while config assembly
-- keeps the REST plugin disabled.
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template, status, sort_order)
VALUES (
    'ocm-integrations',
    'OCM Integrations',
    'Legacy REST fallback for workspace integrations; native MCP is canonical',
    'workspace-integrations',
    '1',
    'bundled',
    '{"plugins":{"entries":{"ocm-integrations":{"enabled":false,"config":{"enabled":false}}}}}',
    'active',
    21
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    slot = EXCLUDED.slot,
    version = EXCLUDED.version,
    install_kind = EXCLUDED.install_kind,
    config_template = EXCLUDED.config_template,
    status = EXCLUDED.status,
    sort_order = EXCLUDED.sort_order,
    updated_at = NOW();
