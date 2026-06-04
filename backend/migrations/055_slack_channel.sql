-- Add Slack channel to registry.
-- Slack uses Socket Mode (two tokens: bot xoxb- + app-level xapp-).
-- Config template follows the Telegram pattern: enabled, dmPolicy, groups.
INSERT INTO registry_entries (id, type, name, description, config_template, required_credentials, status, sort_order)
VALUES
    ('slack', 'channel', 'Slack', 'Slack messaging integration',
     '{"channels":{"slack":{"enabled":true,"mode":"socket","dmPolicy":"pairing","groupPolicy":"open","groups":{"*":{"requireMention":true}}}}}'::jsonb,
     ARRAY['slack'], 'active', 3)
ON CONFLICT (id) DO UPDATE SET
    type = EXCLUDED.type,
    config_template = EXCLUDED.config_template,
    required_credentials = EXCLUDED.required_credentials;
