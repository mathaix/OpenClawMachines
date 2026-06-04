-- Expand integration catalog with new entries and reorganized categories.
-- New categories: google, microsoft, productivity, dev, social, sales
-- New integrations: Outlook, Microsoft Teams, TikTok, Instagram, Trello, Salesforce, Apollo
-- Moved: YouTube from google → social, HubSpot from crm → sales, Jira stays in dev

-- 1. Update existing entries with new categories, sort orders, and auth_config_ids
UPDATE integration_catalog SET category = 'social', sort_order = 53, auth_config_id = 'ac_Pm9YwvnOJJJA' WHERE id = 'youtube';
UPDATE integration_catalog SET category = 'sales', sort_order = 60, auth_config_id = 'ac_nOTXK1FWimMD' WHERE id = 'hubspot';
UPDATE integration_catalog SET sort_order = 41, auth_config_id = 'ac_6lqRMMBbpku9' WHERE id = 'jira';

-- Reorder existing entries within their categories
UPDATE integration_catalog SET sort_order = 10 WHERE id = 'gmail';
UPDATE integration_catalog SET sort_order = 11 WHERE id = 'google-calendar';
UPDATE integration_catalog SET sort_order = 12 WHERE id = 'google-drive';
UPDATE integration_catalog SET sort_order = 13 WHERE id = 'google-sheets';
UPDATE integration_catalog SET sort_order = 14 WHERE id = 'google-docs';
UPDATE integration_catalog SET sort_order = 30 WHERE id = 'notion';
UPDATE integration_catalog SET sort_order = 31 WHERE id = 'slack';
UPDATE integration_catalog SET sort_order = 40 WHERE id = 'github';
UPDATE integration_catalog SET sort_order = 50 WHERE id = 'linkedin';
UPDATE integration_catalog SET sort_order = 51 WHERE id = 'x';

-- 2. Insert new integrations
-- TikTok and X (Twitter) require custom OAuth app credentials — auth_config_id left NULL
INSERT INTO integration_catalog (id, name, icon, toolkit, auth_config_id, category, sort_order) VALUES
    ('outlook',         'Outlook',          'outlook',    'outlook',        'ac_iZwreWyT77XQ', 'microsoft',    20),
    ('microsoft-teams', 'Microsoft Teams',  'msteams',    'microsoftteams', 'ac_7N1CKLjSY15o', 'microsoft',    21),
    ('trello',          'Trello',           'trello',     'trello',         'ac_XNlT_YKdiBf9', 'productivity', 32),
    ('tiktok',          'TikTok',           'tiktok',     'tiktok',         NULL,               'social',       52),
    ('instagram',       'Instagram',        'instagram',  'instagram',      'ac_FaA8KIinsCY9', 'social',       54),
    ('salesforce',      'Salesforce',       'salesforce', 'salesforce',     'ac_rJQc9JmxuNFe', 'sales',        61),
    ('apollo',          'Apollo',           'apollo',     'apollo',         'ac_UILWny5h90J7', 'sales',        62)
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    icon = EXCLUDED.icon,
    toolkit = EXCLUDED.toolkit,
    auth_config_id = EXCLUDED.auth_config_id,
    category = EXCLUDED.category,
    sort_order = EXCLUDED.sort_order,
    updated_at = NOW();
