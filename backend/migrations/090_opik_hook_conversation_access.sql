-- OpenClaw 2026.5.x requires explicit conversation access for non-bundled
-- hooks that inspect LLM input/output. Opik traces depend on those hooks.

UPDATE plugin_catalog
SET config_template = '{"plugins": {"allow": ["opik-openclaw"], "entries": {"opik-openclaw": {"enabled": true, "hooks": {"allowConversationAccess": true}, "config": {"projectName": "default", "workspaceName": "default", "tags": ["ocm"]}}}}}'::jsonb
WHERE id = 'opik-openclaw';
