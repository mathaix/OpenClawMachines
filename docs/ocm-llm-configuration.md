# LLM Configuration: Requirements

## Problem

The OpenClaw gateway inside MicroVMs cannot authenticate with LLM providers (Anthropic, OpenAI, Google). When a user sends a message, the gateway fails with:

```
No API key found for provider "anthropic".
Auth store: /home/openclaw/.openclaw/agents/main/agent/auth-profiles.json
```

Credentials are stored encrypted in the database, decrypted at machine start, and held in the agent's metadata server — but they never reach the gateway process.

## Current Architecture

### What works

1. **Credential storage**: Users add API keys via CLI (`ocm providers setup`) or frontend. Keys are validated, encrypted (AES-256-GCM), and stored in `account_credentials`.
2. **Credential decryption at machine start**: `startMachineInternal()` loads credentials, decrypts them, classifies them as LLM or channel, and sends them to the agent in the `CreateVM` request.
3. **Metadata server**: The agent registers the machine config including `LLMKeys` and `ChannelKeys` (decrypted, in-memory only). These are accessible internally but NOT served to the MicroVM by default (`json:"-"` tags).
4. **API proxy (port 4000)**: A fully-implemented proxy (`backend/internal/apiproxy/`) that:
   - Listens on the bridge IP (`169.254.169.253:4000`)
   - Intercepts requests like `GET /anthropic/v1/messages`
   - Looks up the real API key from the metadata server
   - Injects the key into the upstream request
   - Forwards to the real provider API
   - Tracks token usage and enforces budgets
   - Supports built-in providers (Anthropic, OpenAI, Google, Telegram, Discord, WhatsApp) and custom providers
5. **Metadata endpoints**: `/v1/providers` returns provider URL maps pointing to port 4000. `/v1/llm` returns provider names and base URLs.

### What's broken

1. **Init script injection disabled**: All LLM env var injection in `scripts/init-openclaw.sh` is commented out (3 separate sections, ~80 lines). Comments say: "DISABLED — users configure via openclaw setup".
2. **Gateway has no credentials**: The gateway starts with:
   - No `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GOOGLE_API_KEY` env vars
   - Empty `auth-profiles.json`
   - No `baseUrl` config pointing to the API proxy
3. **Gateway doesn't know about the proxy**: Even though the API proxy is running on port 4000, the gateway is not configured to route LLM requests through it.

### Credential flow diagram

```
Backend DB (encrypted)
    ↓ decrypt at machine start
Agent CreateVM request (plaintext, in-memory)
    ↓ register
Metadata server (in-memory, keyed by VM IP)
    ↓
    ├── /v1/providers → { "llm": { "anthropic": "http://169.254.169.253:4000/anthropic" } }
    ├── /v1/llm → { "providers": [{ "name": "anthropic", "base_url": "..." }] }
    ├── API proxy (port 4000) → intercepts requests, injects real keys, proxies upstream
    │
    └── Gateway process → ??? (no credentials arrive here)
```

## Two Possible Approaches

### Approach A: Proxy model (gateway routes through API proxy)

The gateway sends LLM API calls to `http://169.254.169.253:4000/{provider}/...` instead of directly to `api.anthropic.com`. The API proxy injects the real key and forwards upstream.

**How it would work:**
- Init script fetches `/v1/providers` from metadata
- Sets `ANTHROPIC_BASE_URL=http://169.254.169.253:4000/anthropic` (and similar for other providers)
- Sets a dummy API key (the proxy authenticates via source IP + nonce, not API key)
- Gateway makes LLM calls to the proxy URL → proxy adds real key → upstream

**Pros:**
- API keys never enter the MicroVM at all (better security isolation)
- Budget enforcement and usage tracking are built-in (already implemented in API proxy)
- Custom provider support already works
- Single point of key management

**Cons:**
- Extra network hop for every LLM request (bridge network, ~0.1ms)
- Gateway needs `baseUrl` override per LLM provider
- Streaming responses must pass through proxy (already implemented with SSE support)
- Subscription tokens (setup-tokens / OAT) may need Bearer auth instead of x-api-key — proxy currently injects x-api-key for Anthropic

### Approach B: Direct injection (env vars to gateway)

The init script fetches decrypted keys from a metadata endpoint and exports them as environment variables before starting the gateway.

**How it would work:**
- Add/enable a `/v1/credentials` metadata endpoint that returns `{ "llm": { "anthropic": "sk-..." }, "channels": { ... } }`
- Init script fetches this endpoint, exports `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.
- Gateway reads keys from env vars as it normally would

**Pros:**
- Simpler — gateway works exactly like a standard OpenClaw installation
- No extra network hop
- No proxy complexity for streaming

**Cons:**
- Decrypted keys exist inside the MicroVM (in env vars / process memory)
- No built-in usage tracking or budget enforcement
- Need to handle credential refresh (if keys rotate or OAuth tokens expire)
- Custom providers need additional env var mapping

## Requirements

Regardless of which approach is chosen:

1. **LLM calls must work**: When a user sends a message via the gateway UI, the gateway must be able to call the configured LLM provider (Anthropic, OpenAI, Google) and return a response.
2. **Multiple providers**: A machine may have credentials for multiple LLM providers. All configured providers must work.
3. **Custom providers**: User-defined custom providers (stored in `custom_providers` table) must also work.
4. **Channel credentials**: Channel providers (Telegram, Discord, WhatsApp) also need credentials. The same mechanism should handle both LLM and channel credentials.
5. **Credential types**: Must support both `api_key` (pay-per-use) and `subscription_key` / setup-token (Claude Pro/Max, ChatGPT Plus) credential types.
6. **Hot reload**: When credentials change (user updates a key), the running gateway should pick up the new credentials without a full VM restart. The config watcher (`config_watcher()` in init script) already watches for config version changes.
7. **Security**: Decrypted keys should have minimal exposure. Prefer approaches where keys don't persist to disk inside the MicroVM.
8. **Budget enforcement**: If a machine has a budget (`budget_microcents`), LLM spend must be tracked and capped.

## Components Involved

| Component | File(s) | Role |
|-----------|---------|------|
| Metadata server | `backend/internal/metadata/server_linux.go` | Serves config + credentials to MicroVM |
| API proxy | `backend/internal/apiproxy/handler.go`, `providers.go`, `proxy.go` | Proxies LLM/channel requests with real keys |
| Init script | `scripts/init-openclaw.sh` | Boots gateway with env vars + config |
| Config assembly | `backend/internal/configassembly/assembler.go` | Builds OpenClaw gateway JSON config |
| Machine startup | `backend/internal/api/server.go` (`startMachineInternal`) | Decrypts credentials, sends to agent |
| Agent handlers | `backend/internal/agentapi/handlers.go` | Receives credentials, registers in metadata |
| CLI setup | `cli/internal/commands/providers_setup.go` | User-facing credential management |

## Open Questions

1. **Which approach?** Proxy model (A) or direct injection (B)? Proxy is more secure and has usage tracking built-in; direct injection is simpler and more compatible with standard OpenClaw.
2. **Subscription tokens (OAT)**: Anthropic setup-tokens use Bearer auth, not x-api-key. The API proxy's Anthropic provider currently injects `x-api-key`. If using the proxy model, the proxy needs to detect OAT tokens and use Bearer auth instead.
3. **Hot reload for credentials**: If a user changes their API key while a machine is running, how does the gateway pick it up? The config watcher handles config changes, but credentials live in env vars (approach B) or the metadata server (approach A).
4. **Channel credential delivery**: Channels (Telegram, Discord) need bot tokens in their gateway config (`botToken` field). The current config assembly doesn't inject these. Should channels use the same delivery mechanism as LLM providers?
