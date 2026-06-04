# White-Label Partner Platform — Proposal

## Overview

Enable partners to offer OCM as their own branded product at `{partner}.openclawmachines.com`. Partners manage machines for their customers via CLI and config-as-code. End-users see only the partner's branding.

## User Stories

**Partner (Acme AI):**
- Signs up for an OCM account with partner plan
- Configures branding (logo, color, app name) in Settings
- Adds customers via CLI or dashboard
- Provisions and configures machines for customers
- Manages billing — OCM bills Acme, Acme bills their customers

**End-user (Acme's customer):**
- Visits `acme.openclawmachines.com`
- Signs up with email (magic link) — auto-joined to Acme's account
- Sees Acme branding everywhere, no OCM references
- Uses chat, manages their machine(s)
- Never sees account switcher or team settings

---

## Architecture

### Subdomain Routing

```
acme.openclawmachines.com
  → Cloudflare Worker extracts subdomain "acme"
  → Resolves account by slug
  → Serves frontend with branding overrides
  → All API calls scoped to that account
```

No custom domain DNS management needed. Partners who later want `ai.acme.com` add a CNAME to `acme.openclawmachines.com`.

### Auth Scoping

When login happens on a partner subdomain:

1. Login page fetches branding from `/api/branding?slug=acme`
2. Signup auto-attaches user to the partner account with a configured default role
3. No account creation — user joins the existing partner account
4. Account switcher hidden for partner-scoped users
5. Partner controls: self-signup allowed or invite-only, default role, auth methods

### Account Hierarchy

```
Partner Account (Acme, plan: "partner")
  ├── Owner: admin@acme.com (role: owner)
  ├── Member: sarah@customer.com (role: member)
  ├── Member: bob@othercorp.com (role: member)
  └── Machines:
       ├── sarah-agent (assigned to sarah@customer.com)
       ├── bob-agent (assigned to bob@othercorp.com)
       └── shared-support (accessible to all members)
```

No nested accounts — partners and their customers share one account. Machine assignment controls who sees what.

---

## Database Changes

### New: `account_branding` table

```sql
CREATE TABLE account_branding (
    account_id      INTEGER PRIMARY KEY REFERENCES accounts(id),
    app_name        TEXT,              -- "Acme AI"
    logo_url        TEXT,              -- partner's logo
    brand_color     TEXT,              -- hex, e.g. "#2563eb"
    favicon_url     TEXT,
    support_email   TEXT,
    support_url     TEXT,
    hide_ocm_badge  BOOLEAN DEFAULT false,
    custom_css      TEXT,              -- optional CSS overrides
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);
```

### New: `account_settings` table

```sql
CREATE TABLE account_settings (
    account_id          INTEGER PRIMARY KEY REFERENCES accounts(id),
    partner_mode        BOOLEAN DEFAULT false,
    self_signup_enabled BOOLEAN DEFAULT true,
    default_member_role TEXT DEFAULT 'member',  -- role for self-signup users
    auth_methods        TEXT[] DEFAULT '{magic_link}',
    default_machine_config JSONB,  -- template for new customer machines
    created_at          TIMESTAMPTZ DEFAULT now(),
    updated_at          TIMESTAMPTZ DEFAULT now()
);
```

### Modify: `accounts` table

```sql
ALTER TABLE accounts ADD COLUMN plan TEXT DEFAULT 'free';
-- Add 'partner' as a valid plan value
```

### New: `machine_assignments` table

```sql
CREATE TABLE machine_assignments (
    machine_id  INTEGER REFERENCES machines(id) ON DELETE CASCADE,
    user_id     INTEGER REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (machine_id, user_id)
);
```

Partners assign machines to specific customers. Unassigned machines are visible to all members.

---

## API Changes

### Branding endpoint (public)

```
GET /api/branding?slug={slug}
→ { app_name, logo_url, brand_color, favicon_url, hide_ocm_badge }
```

Cached in Cloudflare KV for fast resolution.

### Partner management endpoints (owner/admin)

```
GET    /api/accounts/{id}/partner/settings
PUT    /api/accounts/{id}/partner/settings
GET    /api/accounts/{id}/partner/branding
PUT    /api/accounts/{id}/partner/branding

GET    /api/accounts/{id}/partner/customers          -- list members with their machines
POST   /api/accounts/{id}/partner/customers          -- add customer (creates user + sends magic link)
DELETE /api/accounts/{id}/partner/customers/{userId}  -- remove customer

POST   /api/accounts/{id}/partner/customers/{userId}/machines  -- create machine for customer
POST   /api/accounts/{id}/partner/apply               -- apply config-as-code YAML
POST   /api/accounts/{id}/partner/diff                -- dry-run config-as-code
```

### Auth changes

```
POST /api/auth/signup
  body: { email, partner_slug? }
  → if partner_slug: auto-join partner account with default role
  → if no partner_slug: create personal account (existing flow)
```

---

## CLI Commands

### `ocm partner` command group

```bash
# Customer management
ocm partner customers list
ocm partner customers add --email sarah@customer.com
ocm partner customers add --emails-from customers.csv
ocm partner customers remove --email sarah@customer.com

# Machine management for customers
ocm partner machines list                                    # all customer machines
ocm partner machines list --customer sarah@customer.com      # specific customer
ocm partner machines create --customer sarah@customer.com \
  --name sales-agent --size standard
ocm partner machines configure --machine sales-agent \
  --model claude-sonnet-4-6 \
  --channel telegram --token "bot123..." \
  --browser enabled

# Branding
ocm partner branding set --app-name "Acme AI" --color "#2563eb" --logo-url "https://..."
ocm partner branding get

# Config-as-code
ocm partner apply -f platform.yaml          # provision everything
ocm partner diff -f platform.yaml           # show what would change
ocm partner export > current-state.yaml     # export current config

# Settings
ocm partner settings set --self-signup=true --default-role=member
```

### Config-as-code format

```yaml
# platform.yaml
branding:
  app_name: "Acme AI"
  brand_color: "#2563eb"
  logo_url: "https://acme.com/logo.svg"
  hide_ocm_badge: true

settings:
  self_signup: true
  default_role: member
  auth_methods: [magic_link]

defaults:
  machine:
    size: standard
    model: claude-sonnet-4-6
    browser: true
    channels:
      - type: webchat

customers:
  - email: sarah@customer.com
    machines:
      - name: sales-agent
        model: claude-opus-4-6
        channels:
          - type: telegram
            token: ${SARAH_TELEGRAM_TOKEN}
          - type: webchat

  - email: bob@othercorp.com
    machines:
      - name: support-bot
        # inherits from defaults.machine
```

Environment variables (`${VAR}`) are resolved from the shell environment at apply time — secrets never stored in the YAML.

---

## Frontend Changes

### Theme loader

On app startup:
1. Check if hostname has a non-`app` subdomain
2. Fetch `/api/branding?slug={subdomain}`
3. Override CSS variables: `--brand-600`, logo, app name
4. Hide OCM badge if `hide_ocm_badge` is true
5. Hide account switcher for partner-scoped users

### Settings → Branding tab (partner plan only)

- Logo upload
- Brand color picker
- App name input
- Preview of branded login page
- Toggle: hide OCM badge

### Settings → Customers tab (partner plan only)

- List customers with their machines
- Add customer (email)
- Assign/unassign machines

---

## Cloudflare Worker Changes

Current flow:
```
Request → extract hostname → resolve machine slug → proxy to agent
```

Partner flow addition:
```
Request to acme.openclawmachines.com (no machine slug)
  → KV lookup: partner:acme → { account_id, branding }
  → Serve frontend with branding injected
```

Add to KV sync: when branding is updated, push to `partner:{slug}` KV key.

---

## Rollout Plan

### Phase 1: Branding (1-2 days)
- `account_branding` table + migration
- Branding API endpoint
- Frontend theme loader
- Branding tab in Settings

### Phase 2: Partner auth scoping (2-3 days)
- `account_settings` table
- Subdomain-scoped signup/login
- Hide account switcher for partner users
- Self-signup toggle

### Phase 3: CLI partner commands (2-3 days)
- `ocm partner customers` CRUD
- `ocm partner machines` with `--customer` flag
- `ocm partner branding` commands

### Phase 4: Config-as-code (2-3 days)
- YAML parser + validator
- `ocm partner apply` / `diff` / `export`
- Environment variable resolution

### Phase 5: Machine assignment (1-2 days)
- `machine_assignments` table
- Filter machine list by assignment
- Partner dashboard showing customer → machine mapping

---

## Pricing Model

| Plan | Price | Includes |
|------|-------|----------|
| Partner | $199/mo | 10 customer machines, branding, CLI management |
| Partner Pro | $499/mo | 50 machines, SSO, priority support |
| Partner Enterprise | Custom | Unlimited machines, custom domain, SLA |

Partners pay OCM. Partners bill their own customers however they want.

---

## What This Doesn't Include (Future)

- Custom domain support (`ai.acme.com` via CNAME)
- Partner SSO (SAML/OIDC for partner's auth provider)
- Usage dashboards for partners (per-customer cost breakdown)
- Webhook notifications (customer signup, machine status changes)
- Partner API keys (non-interactive auth for CI/CD)
- Reseller billing (OCM bills partner's customers directly)
