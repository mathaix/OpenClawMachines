-- Fix opik-openclaw config_template: remove invalid "slots.observability" key.
-- The gateway's Zod schema only allows known slot names (e.g. "memory").
-- "observability" is not a valid slot and causes config validation failure:
--   "plugins.slots: Unrecognized key: observability"
-- The plugin only needs an entry in plugins.entries + plugins.allow for trust-listing.

UPDATE plugin_catalog
SET config_template = '{"plugins": {"allow": ["opik-openclaw"], "entries": {"opik-openclaw": {"enabled": true, "config": {"projectName": "default", "workspaceName": "default", "tags": ["ocm"]}}}}}'
WHERE id = 'opik-openclaw';
