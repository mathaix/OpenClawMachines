-- Migration 070: Remove plugins.installs from Composio catalog template.
--
-- The rootfs no longer seeds plugins (DR-201). Composio is bundled in the
-- artifact's dist/extensions/ and discovered via OPENCLAW_BUNDLED_PLUGINS_DIR.
-- The old installPath pointed to /home/openclaw/.openclaw/extensions/composio
-- which no longer exists on new VMs.
--
-- Rollback: re-add plugins.installs to the Composio template.

UPDATE plugin_catalog
SET config_template = config_template::jsonb #- '{plugins,installs}'
WHERE id = 'composio'
  AND config_template::jsonb ? 'plugins'
  AND (config_template::jsonb -> 'plugins') ? 'installs';
