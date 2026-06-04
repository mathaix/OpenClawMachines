-- Composio integration: catalog of curated integrations, event tracking, and plugin catalog entry.

-- 1. Integration catalog: admin-managed list of integrations shown in dashboard
CREATE TABLE IF NOT EXISTS integration_catalog (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    icon           TEXT NOT NULL,
    toolkit        TEXT NOT NULL,
    auth_config_id TEXT,
    category       TEXT NOT NULL DEFAULT 'other',
    sort_order     INT NOT NULL DEFAULT 0,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 2. Integration events: audit trail for connect/disconnect actions
CREATE TABLE IF NOT EXISTS integration_events (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     INTEGER NOT NULL REFERENCES accounts(id),
    machine_id     UUID REFERENCES machines(id) ON DELETE SET NULL,
    integration_id TEXT NOT NULL,
    event          TEXT NOT NULL,
    metadata       JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_integration_events_machine ON integration_events(machine_id, created_at);
CREATE INDEX IF NOT EXISTS idx_integration_events_integration ON integration_events(integration_id, created_at);

-- 3. Seed integration catalog
-- auth_config_id values from Composio dashboard (claramap workspace, mmathew_workspace_first_project)
INSERT INTO integration_catalog (id, name, icon, toolkit, auth_config_id, category, sort_order) VALUES
    ('gmail',           'Gmail',           'gmail',           'gmail',           'ac_Jtq3FFwstUFC', 'google',       1),
    ('google-calendar', 'Google Calendar', 'google-calendar', 'googlecalendar',  'ac_pyzmb6irstck', 'google',       2),
    ('google-drive',    'Google Drive',    'google-drive',    'googledrive',     'ac_zTGrROq9Cerx', 'google',       3),
    ('google-sheets',   'Google Sheets',   'google-sheets',   'googlesheets',    'ac_OOPQIEOAdzBl', 'google',       4),
    ('google-docs',     'Google Docs',     'google-docs',     'googledocs',      'ac_qtkLL4VKwTGv', 'google',       5),
    ('youtube',         'YouTube',         'youtube',         'youtube',         NULL,              'google',       6),
    ('notion',          'Notion',          'notion',          'notion',          'ac_XBnGuEuqf1y0', 'productivity', 10),
    ('slack',           'Slack',           'slack',           'slack',           'ac_ADOvEFhw59kd', 'productivity', 11),
    ('github',          'GitHub',          'github',          'github',          'ac_RX2VHEmUyPWa', 'dev',          20),
    ('jira',            'Jira',            'jira',            'jira',            NULL,              'dev',          21),
    ('linkedin',        'LinkedIn',        'linkedin',        'linkedin',        NULL,              'social',       30),
    ('x',              'X',               'x',               'twitter',         NULL,              'social',       31),
    ('hubspot',         'HubSpot',         'hubspot',         'hubspot',         NULL,              'crm',          40)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    icon = EXCLUDED.icon,
    toolkit = EXCLUDED.toolkit,
    auth_config_id = COALESCE(EXCLUDED.auth_config_id, integration_catalog.auth_config_id),
    category = EXCLUDED.category,
    sort_order = EXCLUDED.sort_order,
    updated_at = NOW();

-- 4. Composio plugin catalog entry
INSERT INTO plugin_catalog (id, name, description, slot, version, install_kind, config_template, status, sort_order)
VALUES (
    'composio',
    'Composio',
    'Connect third-party apps (Gmail, Slack, Notion, GitHub, etc.) to your agent via OAuth',
    'integrations',
    '1',
    'bundled',
    '{"plugins":{"allow":["composio"],"entries":{"composio":{"enabled":true,"config":{"mcpUrl":"https://connect.composio.dev/mcp"}}},"installs":{"composio":{"source":"archive","installPath":"/home/openclaw/.openclaw/extensions/composio"}}}}',
    'active',
    20
)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    config_template = EXCLUDED.config_template,
    updated_at = NOW();

-- 5. Auto-enable composio plugin for all existing machines (same pattern as opik in 049)
INSERT INTO machine_plugins (machine_id, plugin_id, slot, enabled, install_status, installed_version)
SELECT m.id, 'composio', 'integrations', true, 'installed', '1'
FROM machines m
WHERE NOT EXISTS (
    SELECT 1 FROM machine_plugins mp
    WHERE mp.machine_id = m.id AND mp.plugin_id = 'composio'
)
ON CONFLICT (machine_id, plugin_id) DO NOTHING;
