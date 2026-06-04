# Database Schema

Migration source: `backend/migrations/001_initial.sql`

8 tables, 15 indexes. Neon PostgreSQL 17.

Platform-only tables. Agent state (config, channels, memory, sessions) lives inside the MicroVM filesystem, managed by OpenClaw itself.

## Relationships

```
users 1──N account_members N──1 accounts
accounts 1──N machines
accounts 1──N llm_usage
accounts 1──N account_events
machines N──1 hosts
machines 1──N llm_usage
machines 1──N machine_events
```

---

## `users`

Individual users. SSO-only auth (Google/GitHub).

| Column | Type | Notes |
|--------|------|-------|
| `id` | `SERIAL` PK | |
| `email` | `TEXT` UNIQUE NOT NULL | |
| `name` | `TEXT` NOT NULL | |
| `avatar_url` | `TEXT` | nullable |
| `auth_provider` | `TEXT` NOT NULL | `'google'` or `'github'` |
| `auth_provider_id` | `TEXT` NOT NULL | provider's user ID |
| `created_at` | `TIMESTAMPTZ` | |

On signup, a default personal account is auto-created for the user.

---

## `accounts`

Billing entity — owns machines. Each user gets a default personal account on signup. Users can create additional team accounts.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `SERIAL` PK | |
| `name` | `TEXT` NOT NULL | display name |
| `slug` | `TEXT` UNIQUE NOT NULL | URL namespace |
| `plan` | `TEXT` DEFAULT `'free'` | `'free'`, `'pro'`, `'team'` |
| `billing_email` | `TEXT` | nullable |
| `stripe_customer_id` | `TEXT` | nullable |
| `created_by` | `INT` FK → `users(id)` | user who created the account |
| `created_at` | `TIMESTAMPTZ` | |

**Indexes:** `slug`

---

## `account_members`

Many-to-many join between users and accounts with role-based access.

| Column | Type | Notes |
|--------|------|-------|
| `account_id` | `INT` FK → `accounts(id)` ON DELETE CASCADE | |
| `user_id` | `INT` FK → `users(id)` ON DELETE CASCADE | |
| `role` | `TEXT` DEFAULT `'member'` | `'owner'`, `'admin'`, `'member'` |
| `created_at` | `TIMESTAMPTZ` | |

**Primary Key:** `(account_id, user_id)`
**Indexes:** `user_id`

---

## `hosts`

Worker Agent VMs — shared across accounts. Capacity-tracked for scheduling.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `SERIAL` PK | |
| `vm_name` | `TEXT` NOT NULL | GCP instance name |
| `vm_id` | `TEXT` | GCP instance ID |
| `zone` | `TEXT` NOT NULL | |
| `region` | `TEXT` NOT NULL | derived from zone |
| `machine_type` | `TEXT` NOT NULL | e.g. `n2-standard-8` |
| `external_ip` | `TEXT` | |
| `internal_ip` | `TEXT` | |
| `tunnel_url` | `TEXT` | Cloudflare tunnel base URL |
| `status` | `TEXT` DEFAULT `'provisioning'` | `provisioning`, `ready`, `draining`, `stopped` |
| `capacity_vcpus` | `INT` NOT NULL | total vCPUs available |
| `capacity_memory_mb` | `INT` NOT NULL | total memory available |
| `used_vcpus` | `INT` DEFAULT `0` | currently allocated |
| `used_memory_mb` | `INT` DEFAULT `0` | currently allocated |
| `machine_count` | `INT` DEFAULT `0` | running Machines on this Host |
| `created_at` | `TIMESTAMPTZ` | |

**Indexes:** `status`, `region`

---

## `machines`

The core entity — one OpenClaw agent instance per Machine.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `UUID` PK | `gen_random_uuid()` |
| `account_id` | `INT` FK → `accounts(id)` | owning account |
| `name` | `TEXT` NOT NULL | display name |
| `slug` | `TEXT` UNIQUE NOT NULL | URL: `{slug}.openclawmachines.com` |
| `status` | `TEXT` DEFAULT `'stopped'` | `stopped`, `provisioning`, `running`, `error` |
| `status_message` | `TEXT` | human-readable status detail |
| `vcpus` | `INT` DEFAULT `2` | |
| `memory_mb` | `INT` DEFAULT `2048` | |
| `host_id` | `INT` FK → `hosts(id)` | set by scheduler when running |
| `vm_ip` | `TEXT` | IP within host bridge (`192.168.100.x`) |
| `tunnel_hostname` | `TEXT` | `{slug}.openclawmachines.com` |
| `custom_domain` | `TEXT` | optional user CNAME |
| `gateway_token` | `TEXT` | OpenClaw gateway auth token |
| `provision_step` | `TEXT` | current provisioning step name |
| `provisioning_started_at` | `TIMESTAMPTZ` | |
| `provisioning_completed_at` | `TIMESTAMPTZ` | |
| `created_at` | `TIMESTAMPTZ` | |
| `started_at` | `TIMESTAMPTZ` | |
| `stopped_at` | `TIMESTAMPTZ` | |

**Indexes:** `account_id`, `host_id`, `status`, `slug`

---

## `llm_usage`

AI API usage tracking per Machine for billing, rolled up to account.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL` PK | |
| `account_id` | `INT` FK → `accounts(id)` | for billing rollup |
| `machine_id` | `UUID` FK → `machines(id)` | |
| `provider` | `TEXT` NOT NULL | `anthropic`, `openai`, `google` |
| `model` | `TEXT` NOT NULL | |
| `input_tokens` | `INT` DEFAULT `0` | |
| `output_tokens` | `INT` DEFAULT `0` | |
| `cost_microcents` | `BIGINT` DEFAULT `0` | |
| `request_id` | `TEXT` | |
| `created_at` | `TIMESTAMPTZ` | |

**Indexes:** `account_id`, `machine_id`, `created_at`

---

## `account_events`

Audit log for account lifecycle events (membership changes, plan changes, billing).

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL` PK | |
| `event_id` | `UUID` DEFAULT `gen_random_uuid()` | |
| `event_type` | `TEXT` NOT NULL | `account.created`, `member.invited`, `member.removed`, `plan.changed`, `billing.updated` |
| `account_id` | `INT` FK → `accounts(id)` NOT NULL | |
| `actor_user_id` | `INT` FK → `users(id)` | who performed the action |
| `target_user_id` | `INT` FK → `users(id)` | affected user (e.g. invited member) |
| `metadata` | `JSONB` | |
| `created_at` | `TIMESTAMPTZ` | |

**Indexes:** `account_id`, `event_type`

---

## `machine_events`

Audit log for machine lifecycle events (start, stop, provisioning, errors).

| Column | Type | Notes |
|--------|------|-------|
| `id` | `BIGSERIAL` PK | |
| `event_id` | `UUID` DEFAULT `gen_random_uuid()` | |
| `event_type` | `TEXT` NOT NULL | `machine.created`, `machine.started`, `machine.stopped`, `machine.error`, `machine.provisioning` |
| `event_source` | `TEXT` NOT NULL | `control_plane`, `agent`, `microvm` |
| `machine_id` | `UUID` FK → `machines(id)` NOT NULL | |
| `host_id` | `INT` | |
| `user_id` | `INT` | who triggered the action |
| `duration_ms` | `INT` | |
| `error_code` | `TEXT` | |
| `error_message` | `TEXT` | |
| `metadata` | `JSONB` | |
| `created_at` | `TIMESTAMPTZ` | |

**Indexes:** `machine_id`, `event_type`
