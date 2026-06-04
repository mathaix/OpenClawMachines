-- Provider catalog: data-driven provider definitions.
--
-- Replaces hardcoded provider maps across the codebase (proxy providers.go,
-- config assembly providerExecIDs/llmProviderEnvVars, credential validators,
-- frontend CREDENTIAL_PROVIDERS array) with a single source of truth.

BEGIN;

CREATE TABLE provider_catalog (
  id                   TEXT PRIMARY KEY,
  label                TEXT NOT NULL,
  category             TEXT NOT NULL,
  upstream_host        TEXT,
  upstream_path_prefix TEXT DEFAULT '',
  scheme               TEXT DEFAULT 'https',
  auth_method          TEXT,
  auth_header          TEXT,
  allowed_hosts        TEXT[],
  env_var              TEXT,
  credential_type      TEXT DEFAULT 'api_key',
  placeholder          TEXT,
  icon_letter          TEXT,
  icon_bg              TEXT,
  validation           JSONB,
  autodetect_order     INT,
  sort_order           INT DEFAULT 0,
  enabled              BOOLEAN DEFAULT true,
  -- Identifier mapping columns (most providers use defaults derived from id)
  exec_secret_id       TEXT,   -- secret ID in ocm-secrets, default: id || '-key'
  runtime_provider_id  TEXT,   -- gateway provider name, default: id
  plugin_id            TEXT,   -- gateway plugin ID, default: id
  created_at           TIMESTAMPTZ DEFAULT now()
);

-- ---- AI providers ----

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'anthropic',
  'Anthropic',
  'ai',
  'api.anthropic.com',
  'api_key_header',
  'x-api-key',
  'ANTHROPIC_API_KEY',
  '{"api.anthropic.com"}',
  1,
  'sk-ant-...',
  'A',
  'bg-amber-600',
  '{"url":"https://api.anthropic.com/v1/models","method":"GET","auth_method":"header","auth_header":"x-api-key","extra_headers":{"anthropic-version":"2023-06-01"},"success":[200,429],"fail":[401]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, env_var, allowed_hosts, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'openai',
  'OpenAI',
  'ai',
  'api.openai.com',
  'bearer_header',
  'OPENAI_API_KEY',
  '{"api.openai.com"}',
  2,
  'sk-...',
  'O',
  'bg-emerald-600',
  '{"url":"https://api.openai.com/v1/models","method":"GET","auth_method":"bearer","success":[200,429],"fail":[401]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'google',
  'Google AI',
  'ai',
  'generativelanguage.googleapis.com',
  'query_param',
  'key',
  'GOOGLE_API_KEY',
  '{"generativelanguage.googleapis.com"}',
  3,
  'AIza...',
  'G',
  'bg-blue-600',
  '{"url":"https://generativelanguage.googleapis.com/v1beta/models","method":"GET","auth_method":"header","auth_header":"x-goog-api-key","success":[200,429],"fail":[400,403]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, upstream_host, upstream_path_prefix, auth_method, env_var, allowed_hosts, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'openrouter',
  'OpenRouter',
  'ai',
  'openrouter.ai',
  '/api',
  'bearer_header',
  'OPENROUTER_API_KEY',
  '{"openrouter.ai"}',
  4,
  'sk-or-v1-...',
  'R',
  'bg-purple-600',
  '{"url":"https://openrouter.ai/api/v1/models","method":"GET","auth_method":"bearer","success":[200,429],"fail":[401]}'::jsonb
);

-- ---- Search providers ----

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, autodetect_order, sort_order, placeholder, icon_letter, icon_bg, validation, exec_secret_id)
VALUES (
  'brave',
  'Brave Search',
  'search',
  'api.search.brave.com',
  'api_key_header',
  'X-Subscription-Token',
  'BRAVE_API_KEY',
  '{"api.search.brave.com"}',
  10,
  10,
  'BSA...',
  'B',
  'bg-orange-600',
  '{"url":"https://api.search.brave.com/res/v1/web/search?q=test&count=1","method":"GET","auth_method":"header","auth_header":"X-Subscription-Token","success":[200,429],"fail":[401,403]}'::jsonb,
  'search-brave-api-key'
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, autodetect_order, sort_order, placeholder, icon_letter, icon_bg, validation, exec_secret_id)
VALUES (
  'tavily',
  'Tavily',
  'search',
  'api.tavily.com',
  'api_key_header',
  'x-api-key',
  'TAVILY_API_KEY',
  '{"api.tavily.com"}',
  70,
  11,
  'tvly-...',
  'T',
  'bg-teal-600',
  '{"url":"https://api.tavily.com/search","method":"POST","body_template":"{\"api_key\":\"{{key}}\",\"query\":\"test\",\"max_results\":1}","content_type":"application/json","success":[200,429],"fail":[401,403]}'::jsonb,
  'search-tavily-api-key'
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, autodetect_order, sort_order, placeholder, icon_letter, icon_bg, validation, exec_secret_id)
VALUES (
  'exa',
  'Exa',
  'search',
  'api.exa.ai',
  'api_key_header',
  'x-api-key',
  'EXA_API_KEY',
  '{"api.exa.ai"}',
  65,
  12,
  'exa-...',
  'E',
  'bg-violet-600',
  '{"url":"https://api.exa.ai/search","method":"POST","body_template":"{\"query\":\"test\",\"numResults\":1}","content_type":"application/json","auth_method":"header","auth_header":"x-api-key","success":[200,429],"fail":[401,403]}'::jsonb,
  'search-exa-api-key'
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, auth_header, env_var, allowed_hosts, autodetect_order, sort_order, placeholder, icon_letter, icon_bg, validation, exec_secret_id, runtime_provider_id, plugin_id)
VALUES (
  'gemini-search',
  'Gemini Search',
  'search',
  'generativelanguage.googleapis.com',
  'query_param',
  'key',
  'GEMINI_API_KEY',
  '{"generativelanguage.googleapis.com"}',
  20,
  13,
  'AIza...',
  'G',
  'bg-blue-500',
  '{"url":"https://generativelanguage.googleapis.com/v1beta/models","method":"GET","auth_method":"header","auth_header":"x-goog-api-key","success":[200,429],"fail":[400,403]}'::jsonb,
  'search-gemini-api-key',
  'gemini',
  'google'
);

INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, env_var, allowed_hosts, autodetect_order, sort_order, placeholder, icon_letter, icon_bg, validation, exec_secret_id)
VALUES (
  'firecrawl',
  'Firecrawl',
  'search',
  'api.firecrawl.dev',
  'bearer_header',
  'FIRECRAWL_API_KEY',
  '{"api.firecrawl.dev"}',
  60,
  14,
  'fc-...',
  'F',
  'bg-red-600',
  '{"url":"https://api.firecrawl.dev/v1/crawl","method":"GET","auth_method":"bearer","success":[200,405,429],"fail":[401,403]}'::jsonb,
  'search-firecrawl-api-key'
);

-- ---- Automation providers (no upstream_host, custom proxy code) ----

INSERT INTO provider_catalog (id, label, category, auth_method, credential_type, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'discord',
  'Discord',
  'automation',
  'custom',
  'bot_token',
  20,
  'Bot token...',
  'D',
  'bg-indigo-600',
  '{"url":"https://discord.com/api/v10/users/@me","method":"GET","auth_method":"custom","auth_prefix":"Bot ","success":[200,429],"fail":[401]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, auth_method, credential_type, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'telegram',
  'Telegram',
  'automation',
  'custom',
  'bot_token',
  21,
  'Bot token...',
  'T',
  'bg-sky-500',
  '{"url":"https://api.telegram.org/bot{{key}}/getMe","method":"GET","success":[200],"fail":[401]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, auth_method, credential_type, sort_order, placeholder, icon_letter, icon_bg, validation)
VALUES (
  'whatsapp',
  'WhatsApp',
  'automation',
  'custom',
  'access_token',
  22,
  'Access token...',
  'W',
  'bg-green-500',
  '{"url":"https://graph.facebook.com/v18.0/me","method":"GET","auth_method":"bearer","success":[200,429],"fail":[401]}'::jsonb
);

INSERT INTO provider_catalog (id, label, category, auth_method, credential_type, sort_order, placeholder, icon_letter, icon_bg)
VALUES (
  'slack',
  'Slack',
  'automation',
  'custom',
  'oauth_token',
  23,
  'xoxb-...',
  'S',
  'bg-fuchsia-600'
);

COMMIT;
