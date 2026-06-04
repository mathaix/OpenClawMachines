-- Remove "groups" key from Slack config template.
-- Gateway 2026.3.28 rejects it as an unrecognized key in channels.slack schema.
UPDATE registry_entries
SET config_template = '{"channels":{"slack":{"enabled":true,"mode":"socket","dmPolicy":"pairing","groupPolicy":"open"}},"plugins":{"entries":{"slack":{"enabled":true}},"allow":["slack"]}}'::jsonb
WHERE id = 'slack';
