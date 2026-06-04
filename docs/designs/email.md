# Email Integration Design

## Current State

### How OpenClaw Machines Handles Email Today

Email is treated as a **skill** (CLI tool), not a platform-managed channel. Two email-related binaries are pre-baked into every Firecracker MicroVM rootfs:

| Tool | Version | Purpose | Protocol |
|------|---------|---------|----------|
| **himalaya** | v1.1.0 | Terminal email client | IMAP/SMTP/Maildir/Notmuch |
| **gws** | v0.11.1 | Google Workspace CLI | Gmail API, Drive, Calendar |

**Key difference from Telegram/Discord:** Email has no platform-managed credentials. Users bring their own email account and configure the CLI tools themselves (OAuth, app passwords, etc.). The platform does not inject email credentials via config assembly.

### Himalaya Details

Himalaya is a Rust CLI email client configured via `~/.config/himalaya/config.toml`. It connects to **existing** email accounts — it does not provide a mailbox itself.

Supported backends:
- **IMAP** — reading/fetching from remote servers
- **SMTP** — sending via remote servers
- **Maildir** — local mailbox format
- **Notmuch** — local email indexing
- **Sendmail** — Unix mail transport

Supports OAuth 2.0, app passwords, system keyring integration, PGP encryption, and JSON output for programmatic use. Has setup guides for Gmail, Outlook, ProtonMail, and iCloud.

**Limitation:** Himalaya requires a mailbox to connect to. Without a platform-provided mailbox, each user must configure their own email account manually inside the VM.

### Config Assembly & Channel Architecture

The platform's channel token injection (`configassembly/assembler.go`) currently supports:

```go
var ChannelTokenFieldName = map[string]string{
    "telegram": "botToken",
    "discord":  "token",
}
```

Email is **not** a channel type. No email secrets are managed by the platform.

---

## Two Separate Email Needs

### 1. Platform Transactional Email (Invitations, Alerts)

Emails sent by the OpenClaw Machines platform itself — invitation emails, billing alerts, error notifications.

- **Provider:** Resend (resend.com)
- **Domain:** `openclawmachines.com`
- **From addresses:** `noreply@openclawmachines.com`, `invites@openclawmachines.com`
- **Status:** Setup in progress (Phase 2 of accounts feature)

#### Setup Steps
1. Resend account (done — team: claramap)
2. Domain verification — add DKIM + SPF DNS records in Cloudflare
3. `RESEND_API_KEY` in GCP Secret Manager + Cloud Run env
4. Backend: `internal/email/resend.go` — thin HTTP POST client
5. Backend: `internal/email/templates.go` — invitation HTML template
6. Call `sendInvitationEmail()` from `handleCreateInvitation`

### 2. Per-Machine Email (New Feature — Not Yet Implemented)

Give each OpenClaw Machine its own email address: `machine-{id}@openclawmachines.com`. Agents can send and receive email as a first-class capability without users configuring their own accounts.

This would elevate email from a user-managed skill to a **platform-managed channel**, like Telegram and Discord.

---

## Per-Machine Email: Architecture Options

### Option A (Recommended): Resend for Both Sending and Receiving

Resend supports **inbound email** via webhooks on all tiers, including free. This means one provider handles both directions — no need for a separate Cloudflare Email Worker.

**Pricing:**
- Free: 3,000 emails/month (inbound counts toward quota)
- Pro: $20/month — 50,000 emails/month
- Scale: $90/month — 100,000 emails/month
- Overage: $0.90 per 1,000 emails

**Outbound (sending):**
- Use Resend API to send from `machine-{id}@openclawmachines.com`
- No per-address setup needed — any address on a verified domain works
- Backend proxies send requests from the VM agent

**Inbound (receiving):**
- Configure MX records to point to Resend
- Resend receives the email and fires an `email.received` webhook
- Webhook payload includes sender, recipient, subject, body, attachments
- Backend webhook handler parses recipient to extract machine ID
- Stores in Postgres (`machine_emails` table)
- Agent inside VM accesses inbox via gateway API

**Important: Resend inbound is webhook-only.** There is no API to list or poll for received emails. The Resend "retrieve email" API only returns metadata for *sent* emails. This means:
- You **must** run a webhook endpoint to receive inbound emails
- Storage (Postgres) is required — there's no Resend-hosted inbox to poll
- The Resend CLI cannot be used to check for received emails
- Emails would be lost if not captured by the webhook

**Pros:**
- Single provider for send + receive — simplest architecture
- No Cloudflare Email Worker needed
- Webhook delivery — no polling required
- Included on free tier
- Handles parsing, attachments, threading
- Supports reply-to threading (replies stay in same thread)
- Agent-friendly — JSON API is easier for agents than IMAP

**Cons:**
- Not standard IMAP — himalaya can't connect natively
- Received emails count toward monthly quota
- Webhook reliability depends on Resend's infrastructure
- 3,000/month limit on free tier may be tight for many machines

### Option B: Resend (Outbound) + Cloudflare Email Routing (Inbound)

Use Resend for sending, Cloudflare Email Routing for receiving. Requires a Cloudflare Email Worker to parse and forward to backend.

**Pros:**
- Cloudflare Email Routing is free with no monthly quota
- Inbound doesn't count against Resend quota
- More control over inbound processing

**Cons:**
- Two systems to configure and maintain
- Must build and deploy a Cloudflare Email Worker
- More moving parts

### Option C: Hosted IMAP Server (Dovecot/Stalwart)

Run a lightweight IMAP/SMTP server. Programmatically create mailboxes per machine. Himalaya connects natively.

**Pros:**
- Standard protocols — himalaya works out of the box
- Full email features (folders, flags, search)

**Cons:**
- Significant infrastructure to operate
- Overkill for agent use cases

### Option D: Third-Party Hosted Email (Google Workspace, Fastmail)

**Cons:** Expensive ($6-12/mailbox/month), impractical for ephemeral machines.

---

## Recommendation

**Option A (Resend for both send + receive)** is the best fit:

1. Single provider — simplest to set up and maintain
2. No new infrastructure (no Email Workers, no mail servers)
3. Cost-effective (free tier sufficient for early usage)
4. Webhook-based inbound — real-time, no polling
5. Built-in attachment parsing, threading, and reply support
6. Agent-friendly JSON API
7. Works for ephemeral machines (mailbox is in Postgres, not tied to VM lifecycle)

If inbound volume grows beyond Resend's quota, can migrate receiving to **Option B** (Cloudflare Email Routing) without changing the outbound path.

### Proposed Architecture

```
Outbound:
  Agent in VM → Gateway API → Backend → Resend API → recipient

Inbound:
  Sender → MX record (Resend) → Resend receives email
    → Webhook POST to Backend (email.received event)
    → Parse recipient to extract machine ID
    → Store in Postgres (machine_emails table)
    → Agent polls via Gateway API (or push notification)
```

### Database Schema (Draft)

```sql
CREATE TABLE machine_emails (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id    UUID NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    direction     TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    from_address  TEXT NOT NULL,
    to_address    TEXT NOT NULL,
    subject       TEXT,
    body_text     TEXT,
    body_html     TEXT,
    headers       JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_machine_emails_machine_id ON machine_emails(machine_id);
CREATE INDEX idx_machine_emails_created_at ON machine_emails(created_at);
```

### Config Assembly Changes

Add email as a channel type with platform-managed credentials:

```go
var ChannelTokenFieldName = map[string]string{
    "telegram": "botToken",
    "discord":  "token",
    "email":    "address",  // machine's email address
}
```

The machine's email address would be injected into the gateway config, so the agent knows its identity without user configuration.

---

## Implementation Phases

### Phase 1: Platform Transactional Email (Current Priority)
- Resend domain verification
- `RESEND_API_KEY` in Secret Manager
- Backend invitation email sender
- HTML email template

### Phase 2: Per-Machine Outbound Email
- Backend API endpoint: `POST /machines/{id}/emails/send`
- Resend integration for per-machine sending
- Agent SDK: `ocm email send` command inside VM
- Email address assignment on machine creation

### Phase 3: Per-Machine Inbound Email
- Configure MX records for Resend inbound
- Set up Resend webhook endpoint on backend (`POST /webhooks/resend`)
- Backend handler: parse `email.received` event, extract machine ID from recipient, store in Postgres
- Agent SDK: `ocm email inbox` / `ocm email read` commands
- Webhook or polling for real-time notification inside VM

### Phase 4: Polish
- Attachment support (store in GCS, reference in Postgres)
- Email threading / conversation grouping
- Rate limiting and spam protection
- Email forwarding rules (user-configurable)
- Inbox cleanup for deleted/expired machines
