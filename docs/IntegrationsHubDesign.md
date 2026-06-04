# Integrations Hub Redesign — Design Document

## Problem Statement

The current credential system has three fundamental limitations:

1. **One credential per provider per account.** The `UNIQUE(account_id, provider)` constraint means you can't have both a "Production" and "Development" Anthropic key. Teams working across environments are forced to choose one.

2. **All-or-nothing injection.** When a machine starts, every account credential is injected — no way to give Machine A the production key and Machine B the dev key. The `startMachineInternal` function calls `ListAccountCredentialsWithValues(accountID)` and dumps everything in.

3. **Disconnected secret vault.** Per-machine secrets (SecretVault) and account credentials (APIKeys) are two completely separate systems with no relationship. Users manage their AI keys in Settings, but per-machine Discord tokens in the machine's Secrets tab. There's no unified view.

### What This Means for Users

- A user with two Anthropic keys (billing to different projects) must delete and re-add the key every time they switch.
- A machine running a Discord bot and a machine running an AI agent both receive every key — violating least privilege.
- There's no way to see "which credentials does this machine use?" at a glance.

---

## Design Goals

1. **Named credentials**: Multiple credentials per provider, each with a user-chosen label.
2. **Selective association**: Link specific credentials to specific machines.
3. **Unified view**: One "Integrations" surface per machine showing linked credentials + raw secrets.
4. **Backward compatible**: Existing machines with no explicit links fall back to current behavior (all account credentials).
5. **Extensible**: Architecture supports adding OAuth-based providers (GitHub, Gmail) later without schema changes.

---

## User Flows

### Flow 1: Adding a New Credential

**Where:** Settings > Integrations tab

```
User clicks [+ Add Key]
  → Modal opens with Step 1: "Choose a provider"
    → Grid of provider cards (Anthropic, OpenAI, Google AI, Discord Bot)
    → User clicks "Anthropic Claude"

  → Step 2: "Add Anthropic Claude key"
    → "Name" field (required): "Production"
    → "API Key" field (password): "sk-ant-..."
    → [Validate & Save] button

  → Backend validates key against Anthropic API
  → On success: modal closes, new card appears in grid
  → On failure: inline error "key validation failed: invalid API key"
```

**Key decisions:**
- Name (label) is **required**, not optional. This is the core UX change — every credential has a human-readable name.
- The modal is two steps to avoid overloading one screen. Step 1 is provider selection, Step 2 is details.
- For providers with validation (Anthropic, OpenAI, Google, Discord), keys are validated before saving. Unknown/custom providers skip validation.

### Flow 2: Managing Credentials in Settings

**Where:** Settings > Integrations tab

```
┌──────────────────────────────────────────────────────────────────┐
│ Manage your API keys and service connections.                     │
│ Add credentials here, then link them to specific machines.        │
│                                                        [+ Add Key]│
│                                                                   │
│ AI Providers                                                      │
│ ┌─────────────────────┐ ┌─────────────────────┐                  │
│ │ A  Anthropic Claude  │ │ A  Anthropic Claude  │                 │
│ │    "Production"      │ │    "Development"     │                 │
│ │    ····Xk9m          │ │    ····2b4f          │                 │
│ │    ✓ Validated       │ │    ✓ Validated       │                 │
│ │    [Replace] [×]     │ │    [Replace] [×]     │                 │
│ └─────────────────────┘ └─────────────────────┘                  │
│ ┌─────────────────────┐                                          │
│ │ O  OpenAI            │                                         │
│ │    "Main"            │                                         │
│ │    ····k8p2          │                                         │
│ │    ✓ Validated       │                                         │
│ │    [Replace] [×]     │                                         │
│ └─────────────────────┘                                          │
│                                                                   │
│ Automation                                                        │
│ ┌─────────────────────┐                                          │
│ │ D  Discord Bot       │                                         │
│ │    "ClawBot"         │                                         │
│ │    ····abc1          │                                         │
│ │    ✓ Connected       │                                         │
│ │    [Replace] [×]     │                                         │
│ └─────────────────────┘                                          │
└──────────────────────────────────────────────────────────────────┘
```

**Changes from current APIKeys.tsx:**
- Cards displayed in a **responsive grid** (2 columns on desktop, 1 on mobile) instead of a vertical list.
- **Multiple cards can exist** for the same provider.
- Each card shows the user-given **name** prominently (not just the provider name).
- **Single "+ Add Key" button** at the top instead of per-provider "Add Key" links.
- **Delete is by credential ID**, not by provider (since multiple credentials can share a provider).
- "Replace" replaces the API key value but keeps the same name/label (upsert on `account_id, provider, label`).

**Deleting a credential:**
- Click "×" on the card → confirmation prompt: "Delete Production? This will unlink it from all machines using it."
- Cascade: `machine_credentials` rows referencing this credential are auto-deleted by the FK constraint.
- This is potentially destructive if a running machine is using the credential — the credential will be missing on next machine restart.

### Flow 3: Linking Credentials to a Machine

**Where:** Machine View > Integrations tab (replaces current "Secrets" tab)

```
┌──────────────────────────────────────────────────────────────────┐
│ Connected Integrations                            [+ Connect]     │
│                                                                   │
│ ┌───────────────────────────────────────────────────────────────┐ │
│ │ A  Anthropic Claude  "Production"    ····Xk9m           [×]  │ │
│ │ D  Discord Bot       "ClawBot"       ····abc1           [×]  │ │
│ └───────────────────────────────────────────────────────────────┘ │
│                                                                   │
│ No OpenAI or Google AI credentials linked.                        │
│                                                                   │
│ ▸ Environment Variables (advanced)                                │
│   [Collapsible section wrapping existing SecretVault]             │
└──────────────────────────────────────────────────────────────────┘
```

**"+ Connect" flow:**
1. User clicks [+ Connect]
2. Dropdown/popover shows all account credentials **not already linked** to this machine
3. Each option shows: provider icon + name + masked key
4. User clicks one → API call to link → list refreshes

**Unlinking:**
- Click "×" next to a linked credential
- No confirmation needed (this is non-destructive — the credential still exists in the account)
- The credential will no longer be injected when this machine starts

**Empty state (no linked credentials):**
```
No integrations linked to this machine.
When this machine starts, all account credentials will be used automatically.
Link specific credentials to control which keys this machine can access.

[+ Connect Integration]          [Manage credentials in Settings →]
```

The empty state messaging is important: it explains the **fallback behavior** (all account credentials) and guides users to the Settings page if they need to add new credentials.

**Collapsible "Environment Variables" section:**
- Wraps the existing `SecretVault` component unchanged
- Collapsed by default (chevron toggle)
- Label: "Environment Variables (advanced)"
- For power users who need raw key-value env vars beyond structured credentials

### Flow 4: Machine Creation with Named Credentials

**Where:** Create Machine modal (CredentialSelector component)

**Current behavior:**
```
Anthropic: [Account key (…Xk9m)] ▼   ← dropdown with "Account key" / "Custom" / "None"
OpenAI:    [None] ▼
Discord:   [Custom] ▼
           [Bot token input field]
```

**New behavior:**
```
Anthropic: [Production (…Xk9m)] ▼    ← shows credential NAME, not "Account key"
           Options:
             "Production (…Xk9m)"     ← named credential
             "Development (…2b4f)"    ← another named credential
             "Custom key"
             "None"
OpenAI:    [Main (…k8p2)] ▼
Discord:   [ClawBot (…abc1)] ▼        ← now shows account credentials (not just "Custom")
           Options:
             "ClawBot (…abc1)"
             "Custom"
             "None"
```

**Key change:** When the user selects a named account credential, the frontend does NOT send a custom secret. Instead, when the machine is created with `auto_start: true`, the backend will use the all-account-credentials fallback (since no machine_credentials links exist yet for a brand new machine).

For explicit linking at creation time, the frontend could call `linkMachineCredential` after machine creation but before starting — but this adds complexity. For v1, we rely on the fallback: new machines get all account credentials. The user can then refine by linking/unlinking in the Integrations tab.

### Flow 5: Machine Startup — Credential Injection

**Backend behavior (startMachineInternal):**

```
1. Check machine_credentials for this machine
2. If any linked credentials exist:
   → Use ONLY linked credentials
   → Log: "using_linked_credentials, count=N"
3. If NO linked credentials:
   → Fall back to ALL account credentials (current behavior)
   → Log: "fallback_all_account_credentials, count=N"
4. Decrypt and build llmKeys map
5. Pass to agentClient.CreateVM()
```

This fallback ensures backward compatibility: every existing machine continues to work exactly as before. Users who want selective injection must explicitly link credentials in the Integrations tab.

---

## Data Model

### Current Schema

```sql
CREATE TABLE account_credentials (
    id              SERIAL PRIMARY KEY,
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,
    credential_type TEXT NOT NULL,
    encrypted_value TEXT NOT NULL,
    label           TEXT,                  -- nullable, optional
    last_validated  TIMESTAMPTZ,
    last_four       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(account_id, provider)           -- one per provider
);
```

### New Schema (Migration 009)

```sql
-- Backfill existing NULL labels
UPDATE account_credentials SET label = '' WHERE label IS NULL;

-- Change unique constraint to allow multiple credentials per provider
ALTER TABLE account_credentials DROP CONSTRAINT account_credentials_account_id_provider_key;
ALTER TABLE account_credentials ALTER COLUMN label SET NOT NULL;
ALTER TABLE account_credentials ALTER COLUMN label SET DEFAULT '';
ALTER TABLE account_credentials ADD CONSTRAINT account_credentials_account_id_provider_label_key
    UNIQUE(account_id, provider, label);

-- Junction table: which credentials are linked to which machines
CREATE TABLE machine_credentials (
    machine_id      UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    credential_id   INT  NOT NULL REFERENCES account_credentials(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (machine_id, credential_id)
);
```

**Key design decisions:**

1. **`label` becomes NOT NULL.** Existing rows with NULL labels get backfilled to empty string. The new UI requires a label for all new credentials.

2. **`UNIQUE(account_id, provider, label)`.** This allows two Anthropic credentials ("Production" and "Development") but prevents two with the same name for the same provider.

3. **`machine_credentials` is a simple junction table.** No extra metadata — just "this machine uses this credential." Cascading deletes handle both directions: deleting a machine removes its links, deleting a credential removes its links.

4. **No `credential_type` change.** The existing `credential_type` field (defaults to "api_key") already supports different types. Discord bot tokens use `credential_type = "bot_token"`. OAuth tokens would use `credential_type = "oauth_token"` — future extension, no schema change needed.

---

## API Changes

### Account Credentials

| Endpoint | Change |
|----------|--------|
| `GET /api/accounts/{accountId}/credentials` | **No change.** Returns all credentials. Now may return multiple per provider. |
| `PUT /api/accounts/{accountId}/credentials/{provider}` | **Modified.** `label` is now required in request body. Upsert conflict key changes from `(account_id, provider)` to `(account_id, provider, label)`. |
| `DELETE /api/accounts/{accountId}/credentials/{credentialId}` | **Changed.** Route parameter changes from `{provider}` to `{credentialId}` (integer). Deletes by primary key with account_id guard. |

### Machine Credentials (New)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/accounts/{accountId}/machines/{id}/credentials` | GET | List credentials linked to this machine |
| `/api/accounts/{accountId}/machines/{id}/credentials/{credentialId}` | POST | Link a credential to this machine |
| `/api/accounts/{accountId}/machines/{id}/credentials/{credentialId}` | DELETE | Unlink a credential from this machine |

All machine credential endpoints verify:
- Machine belongs to the account (account_id guard)
- Credential belongs to the account (account_id guard on link)

### Startup Logic Change

`startMachineInternal` changes from:
```
ListAccountCredentialsWithValues(accountID) → inject ALL
```
to:
```
ListMachineCredentialsWithValues(machineID)
  → if non-empty: inject only linked
  → if empty: ListAccountCredentialsWithValues(accountID) → inject ALL (fallback)
```

---

## Frontend Components

### Component Architecture

```
Settings page
└── IntegrationsHub (replaces APIKeys)
    ├── Credential cards (grid)
    └── AddCredentialModal (Radix Dialog)
        ├── Step 1: ProviderPicker
        └── Step 2: CredentialForm

MachineView page
└── "Integrations" tab (replaces "Secrets" tab)
    └── MachineIntegrations
        ├── Linked credentials list
        ├── ConnectDropdown (available credentials)
        └── SecretVault (collapsible, existing component)

CreateMachineModal
└── CredentialSelector (updated)
    └── Per-provider dropdown with named credentials
```

### IntegrationsHub (replaces APIKeys.tsx)

**State:**
- `credentials: AccountCredential[]` — all account credentials
- `showAddModal: boolean`
- `addStep: 1 | 2` — which step of the add flow
- `selectedProvider: CredentialProvider | null`
- `editingCredential: number | null` — credential ID being replaced

**Props:** `{ accountId: number }`

**Data flow:**
1. On mount: `listCredentials(accountId)` → populate grid
2. Add: open modal → pick provider → enter name + key → `putCredential(accountId, provider, { value, label })` → refetch
3. Replace: same as add but pre-fills the provider and label
4. Delete: `deleteCredentialByID(accountId, credentialId)` → refetch

### MachineIntegrations (new component)

**State:**
- `linkedCredentials: AccountCredential[]` — credentials linked to this machine
- `allCredentials: AccountCredential[]` — all account credentials (for the connect dropdown)
- `showConnect: boolean`

**Props:** `{ accountId: number; machineId: string }`

**Data flow:**
1. On mount: parallel fetch `listMachineCredentials` + `listCredentials`
2. Connect: `linkMachineCredential(accountId, machineId, credentialId)` → refetch linked
3. Disconnect: `unlinkMachineCredential(accountId, machineId, credentialId)` → refetch linked
4. Available credentials = `allCredentials.filter(c => !linkedCredentials.some(l => l.id === c.id))`

### CredentialSelector (updated)

**Changes:**
- When listing options for a provider, show all account credentials for that provider (not just one)
- Label each option with the credential name: `"Production (…Xk9m)"`
- Discord now shows account credentials in the dropdown (not just "Custom"/"None")

---

## File Changes Summary

| File | Action | Description |
|------|--------|-------------|
| `backend/migrations/009_named_credentials.sql` | New | Schema migration |
| `backend/internal/store/store.go` | Modify | Update types + interface |
| `backend/internal/store/postgres.go` | Modify | Update queries + add new methods |
| `backend/internal/api/credentials.go` | Modify | Add Discord, update delete route, require label |
| `backend/internal/api/machine_credentials.go` | New | Machine credential link/unlink handlers |
| `backend/internal/api/server.go` | Modify | Routes + startMachineInternal fallback |
| `frontend/src/lib/types.ts` | Modify | Add `id` to AccountCredential, add Discord provider |
| `frontend/src/lib/api.ts` | Modify | Add machine credential API functions, update delete |
| `frontend/src/components/IntegrationsHub.tsx` | New | Replaces APIKeys.tsx |
| `frontend/src/components/MachineIntegrations.tsx` | New | Machine-level credential management |
| `frontend/src/components/CredentialSelector.tsx` | Modify | Named credential dropdowns |
| `frontend/src/pages/Settings.tsx` | Modify | Import IntegrationsHub |
| `frontend/src/pages/MachineView.tsx` | Modify | "Integrations" tab replaces "Secrets" |
| `frontend/src/components/APIKeys.tsx` | Delete | Replaced by IntegrationsHub |

---

## Edge Cases & Considerations

### What happens when a linked credential is deleted?

The FK cascade on `machine_credentials` automatically removes the link. The machine is not affected until its next restart, when the deleted credential will simply be absent from the injected keys. No runtime error — just a missing env var.

**Should we warn?** Yes — when deleting a credential in IntegrationsHub, show how many machines are linked to it: "This credential is linked to 3 machines. They will lose access to this key on next restart."

To support this, we could add a count query, but for v1, a simpler approach: just warn generically "linked machines will lose access on restart."

### What if a machine has linked credentials but not for all providers?

Only linked credentials are injected. If a machine has an Anthropic credential linked but not OpenAI, only `ANTHROPIC_API_KEY` is set. This is by design — selective injection.

### What about the `Custom key` path in CredentialSelector?

Custom keys entered at machine creation are stored as per-machine secrets (in the `secrets` table), not as account credentials. This path remains unchanged. Custom keys are for one-off use cases where the user doesn't want to add the key to their account.

### Migration safety

The migration backfills NULL labels to empty string before adding the NOT NULL constraint. This is safe for existing data. The new unique constraint `(account_id, provider, label)` is also safe because the old constraint `(account_id, provider)` guaranteed at most one row per account+provider — so there can't be duplicate `(account_id, provider, '')` rows.

### Race condition: deleting credential while machine starts

If a credential is deleted between `ListMachineCredentialsWithValues` and `CreateVM`, the decrypted key simply won't be in the map. The machine starts without it. This is acceptable — same behavior as if the credential were deleted 1 second after the machine started.

---

## Future Extensions (Out of Scope)

1. **OAuth-based providers** (GitHub, Gmail, Telegram): Same `account_credentials` table with `credential_type = "oauth_token"`. Would need OAuth redirect endpoints and token refresh logic on the backend.

2. **Credential sharing across accounts**: Not needed yet. Would require a separate sharing model.

3. **Live credential rotation**: Pushing updated credentials to running VMs when a credential is replaced. Would require a WebSocket channel from control plane to agent.

4. **Usage tracking per credential**: Tracking which credential was used for which API call. Would require the LLM proxy to report credential IDs, not just provider names.
