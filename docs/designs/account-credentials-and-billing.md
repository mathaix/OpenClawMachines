# Design: Account Credentials & Billing

**Status:** Proposed
**Branch:** openclaworking

## What We're Building

Users bring their own API keys (Anthropic, OpenAI, etc.) and manage them at the account level. Keys are shared across all machines in the account. Each machine can have a spending budget. A billing dashboard shows token usage and cost per machine.

## User Stories

1. **As an account owner**, I want to add my Anthropic API key once and have it available to all my machines, so I don't have to configure each machine separately.
2. **As an account owner**, I want to validate my API key when I add it, so I know it works before starting a machine.
3. **As an account owner**, I want to set a spending limit per machine, so one runaway machine doesn't drain my API balance.
4. **As an account owner**, I want to see how much each machine has spent (tokens + cost), so I can manage my budget.
5. **As an account owner**, I want to replace or rotate a key without stopping my machines (future).

## User Flows

### Flow 1: Add an API Key

```
Settings page → "API Keys" tab → "Add Key" button
  → Select provider (Anthropic / OpenAI / Google)
  → Paste API key
  → Click "Validate & Save"
  → Backend validates key against provider API
    → Success: key encrypted, stored, shows "✓ Validated" with last 4 chars
    → Failure: shows error "Invalid key" — not saved
```

### Flow 2: Start a Machine (key resolution)

```
User clicks "Start" on a machine
  → Backend fetches account credentials for the account
  → Decrypts the LLM API key
  → Passes to agent via LLMKey field (never enters the VM directly)
  → Agent registers key in metadata service for the VM's LLM proxy
  → VM makes LLM calls through proxy on bridge IP
  → Proxy forwards to provider with the real key
```

The user doesn't see any of this. They just click Start and the machine has LLM access.

### Flow 3: Set a Machine Budget

```
Machine settings → "Budget" section
  → Enter monthly limit in dollars (e.g., $10.00)
  → Save
  → Backend stores as microcents on the machine record
  → When machine's accumulated spend reaches limit, LLM proxy returns 402
  → Machine logs show "Budget exhausted"
  → User can increase limit or wait for monthly reset
```

### Flow 4: View Billing Dashboard

```
Account settings → "Usage & Billing" tab
  → Shows total account spend (current month)
  → Table of machines with columns:
    | Machine | Provider | Tokens In | Tokens Out | Cost | Budget | % Used |
  → Click a machine row → detailed view:
    → Per-day cost chart
    → Per-model breakdown
    → Recent requests list
```

### Flow 5: Key Rotation

```
Settings → "API Keys" → click existing key → "Replace Key"
  → Paste new key
  → Validate against provider
  → Success: old key overwritten (UPSERT), running machines pick up new key on next cold start
  → Machines currently running continue with the old key until restart
```

## Data Model

### What Exists (ready to use)

**`account_credentials`** — one key per provider per account:
```
account_id | provider    | credential_type | encrypted_value | label | last_validated | last_four
1          | anthropic   | api_key         | <AES-256-GCM>   | Prod  | 2026-02-07     | Xk9m
1          | openai      | api_key         | <AES-256-GCM>   | null  | null           | 7f2B
```

**`llm_usage`** — per-request usage records:
```
account_id | machine_id | provider  | model              | input_tokens | output_tokens | cost_microcents
1          | abc-123    | anthropic | claude-sonnet-4    | 1500         | 800           | 3200
```

**Store methods** — `SetAccountCredential`, `ListAccountCredentials`, `GetAccountCredentialWithValue`, `DeleteAccountCredential`, `UpdateAccountCredentialValidation`, `CreateLLMUsage`, `GetLLMSpendByAccount` — all implemented.

### What's New

**Add `budget_microcents` to `machines` table:**
```sql
ALTER TABLE machines ADD COLUMN budget_microcents BIGINT;
-- NULL = no limit, 1000000 = $10.00
```

**Add per-machine spend query to store:**
```go
GetLLMSpendByMachine(ctx, machineID string) (int64, error)
GetLLMUsageByMachine(ctx, machineID string, since time.Time, limit int) ([]LLMUsage, error)
GetLLMUsageByAccount(ctx, accountID int, since time.Time, limit int) ([]LLMUsage, error)
```

## API Design

### Credential Endpoints

```
GET    /api/accounts/{id}/credentials
  → [{provider, credential_type, label, last_validated, last_four, created_at}]
  → Never returns the key value

PUT    /api/accounts/{id}/credentials/{provider}
  → Body: {value, credential_type, label?}
  → Validates key against provider API
  → Encrypts and stores (UPSERT)
  → Returns: {provider, last_four, last_validated}

DELETE /api/accounts/{id}/credentials/{provider}
  → Deletes the credential
  → Running machines keep their current key until restart
```

### Budget Endpoints

```
PUT    /api/accounts/{id}/machines/{machineId}/budget
  → Body: {limit_cents: 1000}  (integer cents, $10.00)
  → Stores as budget_microcents = limit_cents * 10000

DELETE /api/accounts/{id}/machines/{machineId}/budget
  → Removes budget limit (NULL)
```

### Usage/Billing Endpoints

```
GET    /api/accounts/{id}/usage?since=2026-02-01
  → {total_cost_microcents, machines: [{machine_id, name, provider, input_tokens, output_tokens, cost_microcents}]}

GET    /api/accounts/{id}/machines/{machineId}/usage?since=2026-02-01
  → {cost_microcents, records: [{provider, model, input_tokens, output_tokens, cost_microcents, created_at}]}
```

## Key Validation

When a user submits a key, the backend makes a lightweight API call to verify it works:

| Provider | Validation Call | Success |
|----------|----------------|---------|
| Anthropic | `POST /v1/messages` with `max_tokens: 1`, tiny prompt | 200 response |
| OpenAI | `GET /v1/models` | 200 response |
| Google | `GET /v1beta/models` | 200 response |

On success: encrypt, store, set `last_validated = now()`, extract `last_four`.
On failure: return 400 with provider error message. Don't store.

## How Keys Reach Machines

```
Account credential (encrypted in Postgres)
  ↓ decrypted at machine start
Control Plane → Agent API (POST /vms, LLMKey field)
  ↓
Agent registers in metadata service (keyed by VM IP + nonce)
  ↓
VM init script fetches from metadata service
  ↓
Exported as ANTHROPIC_API_KEY (or equivalent) env var
  ↓
OpenClaw gateway uses it for LLM calls
```

LLM keys are decrypted only in the control plane and agent memory. They transit over HTTPS (Cloudflare Tunnel) and are served to the specific VM via the metadata service on the host-local bridge network.

## Budget Enforcement

Budget checking happens at the LLM proxy layer (not yet ported, but the design is):

```
VM sends LLM request to proxy (192.168.100.1:4000)
  ↓
Proxy looks up machine's accumulated spend:
  SELECT COALESCE(SUM(cost_microcents), 0) FROM llm_usage
  WHERE machine_id = $1 AND created_at >= date_trunc('month', now())
  ↓
Compare against machine.budget_microcents
  ↓
Over budget? → Return 402 Payment Required
Under budget? → Forward request, record usage after response
```

## Cost Calculation

The LLM proxy callback receives token counts from the provider response. Cost is computed using a static pricing table:

| Provider | Model | Input $/1M | Output $/1M | Microcents/1K In | Microcents/1K Out |
|----------|-------|-----------|-------------|-----------------|-------------------|
| Anthropic | claude-sonnet-4 | $3.00 | $15.00 | 300 | 1500 |
| Anthropic | claude-haiku-3.5 | $0.80 | $4.00 | 80 | 400 |
| OpenAI | gpt-4o | $2.50 | $10.00 | 250 | 1000 |
| OpenAI | gpt-4o-mini | $0.15 | $0.60 | 15 | 60 |

Pricing is hardcoded initially. Can move to a DB table later if providers change prices frequently.

## Frontend Pages

### Settings → API Keys Tab

```
┌─────────────────────────────────────────────┐
│  API Keys                                    │
│                                              │
│  Anthropic    ✓ Validated    ····Xk9m       │
│               Label: Production key          │
│               Added Feb 7, 2026              │
│               [Replace] [Delete]             │
│                                              │
│  OpenAI       Not configured                 │
│               [Add Key]                      │
│                                              │
│  Google AI    Not configured                 │
│               [Add Key]                      │
└─────────────────────────────────────────────┘
```

### Settings → Usage & Billing Tab

```
┌─────────────────────────────────────────────┐
│  Usage & Billing              February 2026  │
│                                              │
│  Total Spend: $4.23                          │
│                                              │
│  Machine         Provider   Cost    Budget   │
│  ─────────────────────────────────────────── │
│  my-bot          Anthropic  $3.10   $10/mo   │
│  test-agent      Anthropic  $1.13   No limit │
│                                              │
│  [View detailed breakdown →]                 │
└─────────────────────────────────────────────┘
```

### Machine Settings → Budget

```
┌─────────────────────────────────────────────┐
│  Monthly Budget                              │
│                                              │
│  Limit: [$10.00        ]                     │
│  Spent this month: $3.10 (31%)               │
│  ████████░░░░░░░░░░░░░░░░░░░░░░             │
│                                              │
│  [Save] [Remove limit]                       │
└─────────────────────────────────────────────┘
```

## Implementation Order

### Phase 1: Credential CRUD + Validation (this branch)

1. API endpoints for credentials (PUT, GET, DELETE)
2. Key validation against provider APIs
3. Wire credentials into machine start flow (pass LLMKey to agent)
4. Frontend: API Keys settings tab

### Phase 2: Budget + Usage Visibility

5. Add `budget_microcents` column to machines
6. Usage query endpoints (per-machine, per-account)
7. Frontend: Usage & Billing tab, machine budget UI

### Phase 3: Budget Enforcement (requires LLM proxy)

8. LLM proxy integration (LiteLLM on bridge IP)
9. Budget check on each proxied request
10. 402 response when over budget

## Infrastructure Dependencies

These changes require supporting infrastructure work:

- **Encryption key from GCP Secret Manager** — see [`designs/gcp-secret-manager.md`](gcp-secret-manager.md). Credentials are encrypted at rest; the master key must be available at startup.
- **Metadata nonce authentication** — see [`designs/security-hardening.md`](security-hardening.md) (H3). Keys delivered to VMs via metadata service must be authenticated per-VM.
- **File proxy token fix** — see [`designs/security-hardening.md`](security-hardening.md) (H5). Bug fix, not directly related but ships together.

## What Does NOT Change

- Per-machine secrets (`secrets` table) — still exist for machine-specific env vars (not API keys)
- Machine CRUD, start/stop flow — unchanged (credentials are additive)
- Authentication (JWT, OAuth) — unchanged
- Database schema for existing tables — unchanged (only new column on machines)
