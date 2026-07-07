-- Native mcp.servers.ocm is the product runtime path for workspace
-- integrations. Keep the catalog row so existing machine_plugins rows can
-- continue acting as a per-machine capability flag, but stop defaulting the
-- legacy REST plugin into assembled OpenClaw runtime config.

UPDATE plugin_catalog
   SET description = 'Legacy REST fallback for workspace integrations; native MCP is canonical',
       config_template = '{"plugins":{"entries":{"ocm-integrations":{"enabled":false,"config":{"enabled":false}}}}}'::jsonb,
       updated_at = NOW()
 WHERE id = 'ocm-integrations';
