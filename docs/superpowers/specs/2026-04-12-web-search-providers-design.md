# Web Search Provider Configuration

## Problem

OpenClaw machines have no way to configure web search providers. The gateway supports 11 search providers (Brave, Tavily, Exa, Gemini, Firecrawl, etc.) but openclawmachines generates no `tools.web.search` config. Users are stuck with the keyless DuckDuckGo fallback, which has lower quality results and no structured data.

Additionally, the current credential pipeline is hardcoded per-provider across 6+ files (`credentials.go`, `machine_config.go`, `assembler.go`, `providers.go`, `ocm-secrets/main.go`, `init-openclaw.sh`). Adding any new provider — LLM, search, or automation — requires code changes in all of them plus a rootfs rebuild.

## Solution

Two changes, one builds on the other:

1. **Data-driven provider catalog** — a `provider_catalog` table that describes all provider behavior as data. Adding a new provider (LLM, search, or tool) becomes an INSERT, not a code change.
2. **Web search provider configuration** — a new "Web Search" tab backed by the catalog, with per-machine search provider selection and credential management.

## Part 1: Data-Driven Provider Catalog

### Schema

```sql
CREATE TABLE provider_catalog (
  id               TEXT PRIMARY KEY,      -- "brave", "anthropic", "discord"
  label            TEXT NOT NULL,         -- "Brave Search"
  category         TEXT NOT NULL,         -- "ai", "search", "automation"
  upstream_host    TEXT,                  -- "api.search.brave.com"
  upstream_path_prefix TEXT DEFAULT '',   -- "/api" for OpenRouter (proxy prepends before forwarding)
  scheme           TEXT DEFAULT 'https',
  auth_method      TEXT,                  -- "bearer_header", "api_key_header", "query_param", "path_token", "none"
  auth_header      TEXT,                  -- "X-Subscription-Token" (null = default for method)
  allowed_hosts    TEXT[],               -- {"api.search.brave.com"}
  env_var          TEXT,                  -- "BRAVE_API_KEY"
  credential_type  TEXT DEFAULT 'api_key', -- "api_key", "token", "oauth"
  placeholder      TEXT,                  -- "BSA..." (UI input hint)
  icon_letter      TEXT,                  -- "B"
  icon_bg          TEXT,                  -- "bg-orange-600"
  validation       JSONB,                -- see below
  exec_secret_id      TEXT,              -- "anthropic-key" (default: id || '-key')
  runtime_provider_id TEXT,              -- "gemini" for gemini-search (default: id)
  plugin_id           TEXT,              -- "google" for gemini-search (default: id)
  autodetect_order INT,                  -- 10 (lower = higher priority, null = no auto-detect)
  sort_order       INT DEFAULT 0,
  enabled          BOOLEAN DEFAULT true,
  created_at       TIMESTAMPTZ DEFAULT now()
);
```

**Computed defaults:** most providers need zero config for these columns. Application code uses:
- `exec_secret_id` -- defaults to `id || '-key'` when NULL
- `runtime_provider_id` -- defaults to `id` when NULL
- `plugin_id` -- defaults to `id` when NULL

Only providers where OCM's ID diverges from the runtime's ID need explicit values (e.g., `gemini-search`).

### What each component reads from this table

| Component | Reads | Currently hardcoded in |
|-----------|-------|----------------------|
| API proxy | `upstream_host`, `upstream_path_prefix`, `auth_method`, `auth_header`, `allowed_hosts` | `providers.go` (per-provider functions) |
| Config assembly | `env_var`, `category`, `autodetect_order` | `providerExecIDs`, `llmProviderEnvVars` maps |
| Credential validation | `validation` JSONB | `validateBraveKey()` etc. |
| Credential push | `category` (ai/search → proxied, automation → channel) | `llmProviderSet`, `searchProviderSet` |
| Frontend | `label`, `placeholder`, `icon_letter`, `icon_bg`, `category`, `credential_type` | `CREDENTIAL_PROVIDERS` array in `types.ts` |
| ocm-secrets | `category` (via convention-based allowlist derived from catalog) | `proxyKeyIDs` map |
| Init script | Nothing -- `.models.providers` config is iterated generically | provider-specific case arms |

### Generic validation via JSONB

```json
{
  "url": "https://api.search.brave.com/res/v1/web/search?q=test&count=1",
  "method": "GET",
  "auth_method": "header",
  "auth_header": "X-Subscription-Token",
  "success": [200, 429],
  "fail": [401, 403]
}
```

One generic `validateFromCatalog()` function replaces all provider-specific validators. The function:
1. Builds an HTTP request from `url` + `method`
2. Injects the key using `auth_method` + `auth_header`
3. Checks response status against `success` / `fail` lists

Providers with complex validation (e.g., Tavily puts the key in the POST body) use a `body_template` field:

```json
{
  "url": "https://api.tavily.com/search",
  "method": "POST",
  "body_template": "{\"api_key\":\"{{key}}\",\"query\":\"test\",\"max_results\":1}",
  "content_type": "application/json",
  "success": [200, 429],
  "fail": [401, 403]
}
```

**Recognized validation JSONB fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `url` | string | yes | Endpoint to hit for validation |
| `method` | string | yes | HTTP method (GET, POST, etc.) |
| `auth_method` | string | no | How to inject the key: `"header"`, `"bearer"`, `"query_param"` |
| `auth_header` | string | no | Header name for header/bearer methods (e.g., `"X-Subscription-Token"`) |
| `extra_headers` | object | no | Additional headers to include (e.g., `{"anthropic-version": "2023-06-01"}`) |
| `body_template` | string | no | Request body with `{{key}}` placeholder for key injection |
| `content_type` | string | no | Content-Type header when `body_template` is used |
| `success` | int[] | no | HTTP status codes indicating valid key |
| `fail` | int[] | no | HTTP status codes indicating invalid key |

### Generic proxy provider from config

The proxy already has `CustomProviderToProxy()` + `makeInjectKey()` that build a `Provider` struct from data. All catalog providers use this same pattern.

**Note:** the proxy cannot read from the DB directly -- it runs inside the VM. The backend reads the catalog, serializes provider definitions into the machine config payload, and the agent feeds them to the proxy. `LoadProvidersFromConfig()` is called with data the agent already pushes, not from a DB query.

```go
func (p *Proxy) LoadProvidersFromConfig(catalog []ProviderCatalogEntry) {
    for _, entry := range catalog {
        if entry.UpstreamHost == "" {
            continue // automation providers (discord, telegram) keep custom code
        }
        p.providers[entry.ID] = &Provider{
            Name:               entry.ID,
            UpstreamHost:       entry.UpstreamHost,
            Scheme:             entry.Scheme,
            PathPrefix:         "/" + entry.ID,
            UpstreamPathPrefix: entry.UpstreamPathPrefix,
            AllowedHosts:       entry.AllowedHosts,
            InjectKey:          makeInjectKey(entry.AuthMethod, entry.AuthHeader),
            ExtractToken: func(req *http.Request) string {
                return req.Header.Get("x-api-key")
            },
        }
    }
}
```

Providers with non-standard behavior (Anthropic subscription identity injection, Telegram path-based auth, OpenAI Codex OAuth) keep their hand-written `Provider` definitions. The catalog `auth_method` field can be set to `"custom"` to skip auto-generation.

### Generic credential push

The metadata server stores a generic `Credentials map[string]CredentialEntry` (renamed from `LLMKeys`). The backend pushes all proxied credentials (category `ai` or `search`) into this map. The metadata server and proxy don't need to know what type of provider it is.

```go
// pushCredentialsToVM — generic, reads category from catalog
func (s *Server) pushCredentialsToVM(ctx context.Context, host *store.Host, machine *store.Machine) error {
    creds, _ := s.store.ListMachineCredentialsWithValues(ctx, machine.ID)
    catalog := s.providerCatalog // loaded at startup, refreshed periodically

    keys := make(map[string]metadata.CredentialEntry)
    for _, cred := range creds {
        cat := catalog[cred.Provider]
        if cat.Category == "ai" || cat.Category == "search" {
            val, _ := crypto.Decrypt(cred.EncryptedValue, s.secretKey)
            keys[cred.Provider] = metadata.CredentialEntry{Value: val, CredentialType: cred.CredentialType}
        }
    }
    return s.agentClient.ReplaceVMCredentials(ctx, host, machine.ID, keys)
}
```

#### Replace vs. merge semantics

Two distinct operations exist for updating credentials on a running VM:

- **`UpdateMachineLLMKeys`** (existing, PATCH) -- merge semantics. Keys present in the payload get updated; absent keys are left alone. Used by the OAuth refresh path in the proxy, which updates a single key at a time.
- **`ReplaceMachineLLMKeys`** (new, PUT) -- surgical replace of ONLY the credentials map. The rest of the MachineConfig is untouched. Used by the credential push from the backend, which always builds the full credential set from DB.

The delete credential handler calls `pushCredentialsToVM`, which rebuilds the full set from DB (minus the deleted key), then calls `ReplaceVMCredentials` (PUT) instead of `UpdateVMCredentials` (PATCH). The metadata server does `cfg.LLMKeys = newMap`, not a merge loop.

The full chain is:

```
backend → agentclient.ReplaceVMCredentials (PUT /vms/{id}/credentials)
        → orchestrator.ReplaceLLMKeys
        → metadata.ReplaceMachineLLMKeys (sets cfg.LLMKeys = newMap)
```

The set credential handler also uses replace semantics (same `pushCredentialsToVM` function), since it builds the full set anyway.

### Generic ocm-secrets: convention-based allowlist (one-time rootfs change)

Replace the hardcoded `proxyKeyIDs` map with a convention-based rule. Do NOT make ocm-secrets a blind passthrough -- it still returns the nonce locally for proxy keys and real values for platform secrets, but derives the allowlist from convention instead of a hardcoded map.

**Convention:** any credential ID matching `{provider}-key` where that provider exists in the catalog with category `ai` or `search` gets the nonce. The metadata server already knows which credentials are proxied (it has the full credential map).

```go
// Before: hardcoded allowlist
var proxyKeyIDs = map[string]bool{
    "anthropic-key": true,
    "openai-key":    true,
    // ... one entry per provider
}
if proxyKeyIDs[id] { out.Values[id] = nonce }

// After: convention-based check
func isProxyCredential(id string) bool {
    base := strings.TrimSuffix(id, "-key")
    if base == id {
        return false // no "-key" suffix, not a proxy credential
    }
    cat, ok := catalogByID[base]
    return ok && (cat.Category == "ai" || cat.Category == "search")
}

// In the handler:
if isProxyCredential(id) {
    out.Values[id] = nonce
}
```

No network call, no scope expansion, and new providers still work without rootfs changes -- just adding a catalog row is enough. This is the last rootfs change needed for the credential pipeline.

### Search providers use .models.providers (no new config concept)

Search providers use the exact same pattern as LLM providers -- they go into `.models.providers.{name}` in the gateway config with exec secret refs. The gateway resolves them the same way. The init script already iterates `.models.providers` generically for env var export. No `runtime.env` block or new config concept is needed.

**Config assembly output for a search provider:**
```yaml
models:
  providers:
    brave:
      apiKey: "exec:brave-key"
tools:
  web:
    search:
      enabled: true
      provider: "brave"
```

The init script's existing generic loop over `.models.providers` handles env var export for both LLM and search providers without any changes.

### Seed data

```sql
INSERT INTO provider_catalog (id, label, category, upstream_host, upstream_path_prefix, auth_method, auth_header, allowed_hosts, env_var, exec_secret_id, runtime_provider_id, plugin_id, placeholder, icon_letter, icon_bg, validation, autodetect_order, sort_order) VALUES
-- AI providers (exec_secret_id, runtime_provider_id, plugin_id all NULL = use defaults derived from id)
('anthropic',     'Anthropic Claude', 'ai', 'api.anthropic.com',                    '',     'api_key_header', 'x-api-key',              '{"api.anthropic.com"}',                    'ANTHROPIC_API_KEY',  NULL, NULL, NULL, 'sk-ant-...',  'A', 'bg-amber-600',  '{"url":"https://api.anthropic.com/v1/models","method":"GET","auth_method":"header","auth_header":"x-api-key","extra_headers":{"anthropic-version":"2023-06-01"},"success":[200,429],"fail":[401]}', NULL, 1),
('openai',        'OpenAI',           'ai', 'api.openai.com',                       '',     'bearer_header',  NULL,                     '{"api.openai.com"}',                       'OPENAI_API_KEY',     NULL, NULL, NULL, 'sk-...',      'O', 'bg-emerald-600','{"url":"https://api.openai.com/v1/models","method":"GET","auth_method":"bearer","success":[200,429],"fail":[401]}', NULL, 2),
('google',        'Google AI',        'ai', 'generativelanguage.googleapis.com',     '',     'query_param',    'key',                    '{"generativelanguage.googleapis.com"}',     'GOOGLE_API_KEY',     NULL, NULL, NULL, 'AIza...',     'G', 'bg-blue-600',   '{"url":"https://generativelanguage.googleapis.com/v1beta/models","method":"GET","auth_method":"query_param","auth_header":"key","success":[200,429],"fail":[400,403]}', NULL, 3),
('openrouter',    'OpenRouter',       'ai', 'openrouter.ai',                        '/api', 'bearer_header',  NULL,                     '{"openrouter.ai"}',                        'OPENROUTER_API_KEY', NULL, NULL, NULL, 'sk-or-v1-...','R', 'bg-purple-600', '{"url":"https://openrouter.ai/api/v1/models","method":"GET","auth_method":"bearer","success":[200,429],"fail":[401]}', NULL, 4),
-- Search providers (gemini-search needs explicit IDs; others use defaults)
('brave',         'Brave Search',     'search', 'api.search.brave.com',              '',     'api_key_header', 'X-Subscription-Token',   '{"api.search.brave.com"}',                 'BRAVE_API_KEY',      NULL, NULL, NULL, 'BSA...',      'B', 'bg-orange-600', '{"url":"https://api.search.brave.com/res/v1/web/search?q=test&count=1","method":"GET","auth_method":"header","auth_header":"X-Subscription-Token","success":[200,429],"fail":[401,403]}', 10, 10),
('tavily',        'Tavily',           'search', 'api.tavily.com',                    '',     'api_key_header', 'x-api-key',              '{"api.tavily.com"}',                       'TAVILY_API_KEY',     NULL, NULL, NULL, 'tvly-...',    'T', 'bg-teal-600',   '{"url":"https://api.tavily.com/search","method":"POST","body_template":"{\"api_key\":\"{{key}}\",\"query\":\"test\",\"max_results\":1}","content_type":"application/json","success":[200,429],"fail":[401,403]}', 70, 11),
('exa',           'Exa',              'search', 'api.exa.ai',                        '',     'api_key_header', 'x-api-key',              '{"api.exa.ai"}',                           'EXA_API_KEY',        NULL, NULL, NULL, 'exa-...',     'E', 'bg-violet-600', '{"url":"https://api.exa.ai/search","method":"POST","body_template":"{\"query\":\"test\",\"numResults\":1}","content_type":"application/json","auth_method":"header","auth_header":"x-api-key","success":[200,429],"fail":[401,403]}', 65, 12),
('gemini-search', 'Gemini Search',    'search', 'generativelanguage.googleapis.com', '',     'query_param',    'key',                    '{"generativelanguage.googleapis.com"}',     'GEMINI_API_KEY',     'gemini-search-key', 'gemini', 'google', 'AIza...',     'G', 'bg-blue-500',   '{"url":"https://generativelanguage.googleapis.com/v1beta/models","method":"GET","auth_method":"query_param","auth_header":"key","success":[200,429],"fail":[400,403]}', 20, 13),
('firecrawl',     'Firecrawl',        'search', 'api.firecrawl.dev',                 '',     'bearer_header',  NULL,                     '{"api.firecrawl.dev"}',                    'FIRECRAWL_API_KEY',  NULL, NULL, NULL, 'fc-...',      'F', 'bg-red-600',    '{"url":"https://api.firecrawl.dev/v1/crawl","method":"GET","auth_method":"bearer","success":[200,405,429],"fail":[401,403]}', 60, 14),
-- Automation providers (no upstream_host — custom proxy code)
('discord',       'Discord Bot',      'automation', NULL,                             '',     'custom',         NULL,                     NULL,                                        NULL,                 NULL, NULL, NULL, 'Bot token...','D', 'bg-indigo-600', NULL, NULL, 20),
('telegram',      'Telegram Bot',     'automation', NULL,                             '',     'custom',         NULL,                     NULL,                                        NULL,                 NULL, NULL, NULL, 'Bot token...','T', 'bg-sky-600',    NULL, NULL, 21),
('whatsapp',      'WhatsApp Business', 'automation', NULL,                            '',     'custom',         NULL,                     NULL,                                        NULL,                 NULL, NULL, NULL, 'Access token...','W','bg-green-600', NULL, NULL, 22),
('slack',         'Slack Bot',        'automation', NULL,                             '',     'custom',         NULL,                     NULL,                                        NULL,                 NULL, NULL, NULL, 'xoxb-...',    'S', 'bg-purple-700', NULL, NULL, 23);
```

### Adding a new provider

```sql
INSERT INTO provider_catalog (id, label, category, upstream_host, auth_method, env_var, validation, autodetect_order, sort_order, placeholder, icon_letter, icon_bg)
VALUES ('perplexity', 'Perplexity', 'search', 'api.perplexity.ai', 'bearer_header', 'PERPLEXITY_API_KEY',
  '{"url":"https://api.perplexity.ai/chat/completions","method":"POST","auth_method":"bearer","success":[200,429],"fail":[401]}',
  15, 15, 'pplx-...', 'P', 'bg-cyan-600');
```

**Zero code changes. Zero deploys. Zero rootfs rebuilds.**

### Migration path

The existing hardcoded providers (Anthropic, OpenAI, etc.) keep their hand-written `Provider` definitions for now — they have complex behaviors (OAuth refresh, subscription identity injection, usage parsing) that can't be described in the catalog. The catalog handles:
- Simple proxy routing (search providers, future tool providers)
- Credential validation
- Frontend display metadata
- Config assembly env var mapping

Over time, the hand-written providers can be migrated as the catalog schema gains fields for usage parsing, body mutation, etc.

## Part 2: Web Search Configuration

### openclaw search provider resolution (source-verified)

Each provider has two resolution paths (checked in order):

1. **Config path** — provider reads its key from the config object at runtime
2. **Env var fallback** — provider reads from an environment variable

**Source references:**

| Provider | Config read (runtime) | Env var | Source file | Line |
|----------|----------------------|---------|-------------|------|
| brave | `searchConfig.apiKey` (top-level) | `BRAVE_API_KEY` | `extensions/brave/src/brave-web-search-provider.ts` | 129, 122 |
| tavily | `searchConfig.tavily.apiKey` (scoped) | `TAVILY_API_KEY` | `extensions/tavily/src/tavily-search-provider.ts` | 40, 33 |
| exa | `searchConfig.exa.apiKey` (scoped) | `EXA_API_KEY` | `extensions/exa/src/exa-web-search-provider.ts` | 607, 600 |
| gemini | `searchConfig.gemini.apiKey` (scoped) | `GEMINI_API_KEY` | `extensions/google/src/gemini-web-search-provider.ts` | 260, 253 |
| firecrawl | `searchConfig.firecrawl.apiKey` (scoped) | `FIRECRAWL_API_KEY` | `extensions/firecrawl/src/firecrawl-search-provider.ts` | 40, 33 |

**`autoDetectOrder` values** (lower = higher priority, used when no explicit `provider` is set):
- brave: 10, gemini: 20, firecrawl: 60, exa: 65, tavily: 70

Source: `src/web-search/runtime.ts:116-165` — `resolveWebSearchProviderId()` iterates providers sorted by `autoDetectOrder`, returns the first one with a working credential.

### WebSearchConfig type definition

From `src/config/types.tools.ts:484-516`:

```typescript
tools: {
  web: {
    search: {
      enabled?: boolean;
      provider?: string;         // "brave", "tavily", "exa", "gemini", "firecrawl"
      apiKey?: SecretInput;      // top-level slot (brave reads this)
      maxResults?: number;       // 1-10, default 5
      timeoutSeconds?: number;
      cacheTtlMinutes?: number;
    } & Record<string, unknown>; // scoped provider configs (e.g., .tavily.apiKey)
  };
};
```

### Config assembly output

```yaml
models:
  providers:
    brave:
      apiKey: "exec:brave-key"   # same exec secret pattern as LLM providers
tools:
  web:
    search:
      enabled: true
      provider: "brave"    # openclaw provider ID, resolved from catalog
```

Search providers go into `.models.providers.{name}` just like LLM providers. Config assembly reads `env_var` and the identifier columns (`runtime_provider_id`, `plugin_id`) from the catalog to build the correct provider block. The init script's existing generic iteration over `.models.providers` handles env var export for both LLM and search providers without changes.

The `gemini-search` catalog row has `runtime_provider_id='gemini'` and `plugin_id='google'`, so config assembly emits it under `.models.providers.gemini` (not `gemini-search`). All other search providers use their `id` directly since the defaults match. See the `exec_secret_id`, `runtime_provider_id`, and `plugin_id` columns in the schema above.

Notes:
- DuckDuckGo requires no key and is the out-of-box default. It does not appear in the provider cards -- it is just noted as the fallback.

### Frontend: New "Web Search" tab

Added to the machine detail page tab bar (after "Model", before "Channels").

**Layout:**

```
┌─ Web Search ──────────────────────────────────────────────────┐
│                                                                │
│ ╔═ Active Search Provider ═══════════════════════════════╗    │
│ ║                                                         ║    │
│ ║  Using: DuckDuckGo (default, no key needed)             ║    │
│ ║                                                         ║    │
│ ║  Add a search provider key below for higher quality     ║    │
│ ║  results with structured data and filtering.            ║    │
│ ╚═════════════════════════════════════════════════════════╝    │
│                                                                │
│ ╔═ Search Providers ═════════════════════════════════════╗    │
│ ║                                                         ║    │
│ ║  ┌──────────────────────┐ ┌──────────────────────┐     ║    │
│ ║  │ Brave Search         │ │ Tavily               │     ║    │
│ ║  │ Structured results   │ │ AI-powered search    │     ║    │
│ ║  │                      │ │                      │     ║    │
│ ║  │      [ Add key ]     │ │      [ Add key ]     │     ║    │
│ ║  └──────────────────────┘ └──────────────────────┘     ║    │
│ ║                                                         ║    │
│ ║  ┌──────────────────────┐ ┌──────────────────────┐     ║    │
│ ║  │ Exa                  │ │ Gemini Search        │     ║    │
│ ║  │ AI-native semantic   │ │ Google AI search     │     ║    │
│ ║  │                      │ │                      │     ║    │
│ ║  │      [ Add key ]     │ │      [ Add key ]     │     ║    │
│ ║  └──────────────────────┘ └──────────────────────┘     ║    │
│ ║                                                         ║    │
│ ║  ┌──────────────────────┐                              ║    │
│ ║  │ Firecrawl            │                              ║    │
│ ║  │ Web scraping + search│                              ║    │
│ ║  │                      │                              ║    │
│ ║  │      [ Add key ]     │                              ║    │
│ ║  └──────────────────────┘                              ║    │
│ ╚═════════════════════════════════════════════════════════╝    │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

**Behavior:**
- When no search key is configured: shows "Using: DuckDuckGo (default)" in the status card.
- When user adds a key: that provider becomes the active search provider automatically (highest-priority key wins per `autodetect_order` from catalog).
- If multiple keys are configured: the one with the lowest `autodetect_order` wins. User can override by explicitly selecting.
- Connected providers show a green "Connected" badge and a radio button to select as active.
- Key input flow reuses the existing BYOK pattern from ModelTab (inline password input + Save button).
- After saving a key: pushes config to the running VM via `pushMachineConfig`.
- Provider cards are rendered from the catalog API (`GET /api/catalog/providers?category=search`), not hardcoded in the frontend.

### API

**Provider catalog (new):**
```
GET  /api/catalog/providers                              → ProviderCatalogEntry[]
GET  /api/catalog/providers?category=search              → ProviderCatalogEntry[] (filtered)
```

**Search provider override (new):**
```
GET  /accounts/{id}/machines/{id}/search-provider    → { provider: string, resolved: string }
PUT  /accounts/{id}/machines/{id}/search-provider    → { provider: string }
DELETE /accounts/{id}/machines/{id}/search-provider  → 204 (clear override, revert to auto-detect)
```

`GET` returns both the explicit override (`provider`, may be empty) and the resolved active provider (`resolved`, computed from auto-detect). This addresses the Codex review finding about the weak API contract.

**Credentials (existing):**
```
PUT    /accounts/{id}/machines/{id}/credentials/{provider}
DELETE /accounts/{id}/machines/{id}/credentials/{provider}
GET    /accounts/{id}/machines/{id}/credentials
```

### Backend: credential push on save

The Codex review noted that credential handlers only call `pushCredentialsToVMByID` (LLM keys only). With the generic credential push, this is resolved — `pushCredentialsToVM` pushes all proxied credentials (category `ai` + `search`) from the catalog. No frontend discipline required; the backend always pushes the full set.

Additionally, `pushCredentialsToVMByID` should also trigger a config push when search credentials change, since the `tools.web.search` config block depends on which search credentials exist:

```go
func (s *Server) pushCredentialsToVMByID(machineID string) {
    // ... existing credential push ...
    // Also re-assemble and push config (search provider may have changed)
    go s.pushMachineConfigByID(machineID)
}
```

### Machine config storage

Extend `platform_overrides` JSONB to include `search_provider`:

```json
{
  "preferred_model": "minimax/minimax-m2.5",
  "search_provider": "brave"
}
```

No migration needed — JSONB is schema-flexible.

## Data Flow

```
User adds Brave key in Web Search tab
  → PUT /credentials/brave (encrypted, stored in DB)
  → pushCredentialsToVMByID()
    → pushCredentialsToVM(): pushes all proxied credentials (generic, catalog-driven)
    → pushMachineConfig(): re-assembles config
      → reads provider_catalog for env_var mappings
      → reads platform_overrides.search_provider (or auto-detects from credentials)
      → emits tools.web.search.provider = "brave"
      → emits models.providers.brave.apiKey = "exec:brave-key"
  → Agent receives updated config + credentials
  → Init script iterates .models.providers, exports BRAVE_API_KEY=<nonce>
  → Gateway resolves provider = "brave", reads exec secret ref via ocm-secrets
  → ocm-secrets returns nonce → openclaw hits proxy → proxy swaps nonce for real key → Brave API
```

## Implementation Phases

### Phase 1: Catalog table + API + generic validation (data model only, zero runtime impact)
- Create `provider_catalog` table + seed migration
- `GET /api/catalog/providers` endpoint
- Generic `validateFromCatalog()` replacing per-provider validation functions
- All existing hardcoded maps read from catalog at startup

### Phase 2: Config assembly reads catalog + credential push reads catalog + search config block (control plane uses catalog, runtime unchanged)
- Config assembly reads `env_var` and identifier columns from catalog, emits `.models.providers.{name}` blocks
- Generic credential push (category-based from catalog, not hardcoded set)
- Search provider resolution logic (override + auto-detect from `autodetect_order`)
- `GET/PUT/DELETE /search-provider` endpoints
- `tools.web.search` config block assembly

### Phase 3: Agent propagates catalog to proxy + metadata replace semantics + ocm-secrets convention-based allowlist (runtime changes, one rootfs rebuild)
- `LoadProvidersFromConfig()` -- agent feeds catalog data to proxy, proxy builds Provider structs
- Metadata server: `ReplaceMachineLLMKeys` (PUT replace semantics) alongside existing PATCH
- `ocm-secrets`: replace `proxyKeyIDs` with convention-based `isProxyCredential()` allowlist
- Metadata server: rename `LLMKeys` -> `Credentials` (generic map)

### Phase 4: Frontend (can start after Phase 1)
- `GET /api/catalog/providers?category=search` drives the UI (no hardcoded provider list)
- New `WebSearchTab` component with provider cards from API
- Search provider override selection
- Existing credential CRUD endpoints for key management

## Files to Create/Modify

| File | Phase | Action | Description |
|------|-------|--------|-------------|
| `backend/migrations/075_provider_catalog.sql` | 1 | Create | Table + seed data |
| `backend/internal/store/postgres.go` | 1 | Edit | CRUD for provider_catalog |
| `backend/internal/store/store.go` | 1 | Edit | ProviderCatalogEntry type |
| `backend/internal/api/credentials.go` | 1 | Edit | Generic `validateFromCatalog()` |
| `backend/internal/api/providers.go` | 1 | Create | `GET /api/catalog/providers` handler |
| `backend/internal/api/server.go` | 1 | Edit | Register catalog routes |
| `backend/internal/configassembly/assembler.go` | 2 | Edit | Catalog-driven config + `.models.providers` + search block |
| `backend/internal/api/machine_config.go` | 2 | Edit | Generic credential push, search provider handlers |
| `backend/internal/api/server.go` | 2 | Edit | Register search provider routes |
| `backend/internal/apiproxy/providers.go` | 3 | Edit | `LoadProvidersFromConfig()` |
| `backend/internal/apiproxy/proxy.go` | 3 | Edit | Call `LoadProvidersFromConfig()` on init |
| `backend/internal/metadata/metadata.go` | 3 | Edit | `ReplaceMachineLLMKeys` + rename LLMKeys -> Credentials |
| `backend/cmd/ocm-secrets/main.go` | 3 | Edit | Replace `proxyKeyIDs` with convention-based allowlist |
| `frontend/src/pages/machine-tabs/WebSearchTab.tsx` | 4 | Create | New tab component |
| `frontend/src/lib/api.ts` | 4 | Edit | Provider catalog + search provider API calls |
| `frontend/src/pages/MachineView.tsx` | 4 | Edit | Add Web Search tab to tab bar |

## Out of Scope

- Multiple simultaneous search providers (openclaw only uses one at a time)
- Provider-specific config (Brave mode, Tavily domain filters, etc.) — can add later
- Search usage tracking / billing
- Account-wide search keys (keeping it per-machine for consistency with LLM keys)
- Migrating complex providers (Anthropic OAuth, Telegram path auth) to catalog — they keep hand-written code
- Admin UI for managing the provider catalog (INSERT via migration or direct SQL for now)
