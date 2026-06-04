-- Migration 001: Initial schema for OpenClaw Machines
-- Platform-only tables. Agent state (config, channels, memory, sessions)
-- lives inside the MicroVM filesystem, managed by OpenClaw itself.

-- ============================================================
-- Users
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id              SERIAL PRIMARY KEY,
    email           TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    avatar_url      TEXT,
    auth_provider   TEXT NOT NULL,          -- 'google' | 'github'
    auth_provider_id TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- Accounts (billing entity — owns machines)
-- ============================================================
CREATE TABLE IF NOT EXISTS accounts (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,   -- URL namespace
    plan            TEXT NOT NULL DEFAULT 'free',  -- 'free' | 'pro' | 'team'
    billing_email   TEXT,
    stripe_customer_id TEXT,
    created_by      INT NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_accounts_slug ON accounts(slug);

-- ============================================================
-- Account Members (users <-> accounts, many-to-many)
-- ============================================================
CREATE TABLE IF NOT EXISTS account_members (
    account_id      INT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id         INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            TEXT NOT NULL DEFAULT 'member',  -- 'owner' | 'admin' | 'member'
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_account_members_user_id ON account_members(user_id);

-- ============================================================
-- Hosts (Worker Agent VMs — shared across accounts)
-- ============================================================
CREATE TABLE IF NOT EXISTS hosts (
    id              SERIAL PRIMARY KEY,
    vm_name         TEXT NOT NULL,
    vm_id           TEXT,
    zone            TEXT NOT NULL,
    region          TEXT NOT NULL,
    machine_type    TEXT NOT NULL,

    external_ip     TEXT,
    internal_ip     TEXT,
    tunnel_url      TEXT,

    status          TEXT NOT NULL DEFAULT 'provisioning',
        -- 'provisioning' | 'ready' | 'draining' | 'stopped'

    capacity_vcpus      INT NOT NULL,
    capacity_memory_mb  INT NOT NULL,
    used_vcpus          INT NOT NULL DEFAULT 0,
    used_memory_mb      INT NOT NULL DEFAULT 0,
    machine_count       INT NOT NULL DEFAULT 0,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_hosts_status ON hosts(status);
CREATE INDEX IF NOT EXISTS idx_hosts_region ON hosts(region);

-- ============================================================
-- Machines (one OpenClaw instance per Machine)
-- ============================================================
CREATE TABLE IF NOT EXISTS machines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      INT NOT NULL REFERENCES accounts(id),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,   -- {slug}.openclawmachines.com
    status          TEXT NOT NULL DEFAULT 'stopped',
        -- 'provisioning' | 'running' | 'stopped' | 'error'
    status_message  TEXT,

    -- Sizing
    vcpus           INT NOT NULL DEFAULT 2,
    memory_mb       INT NOT NULL DEFAULT 2048,

    -- Placement (set by scheduler)
    host_id         INT REFERENCES hosts(id),
    vm_ip           TEXT,

    -- Networking
    tunnel_hostname TEXT,
    custom_domain   TEXT,

    -- OpenClaw gateway
    gateway_token   TEXT,

    -- Provisioning tracking
    provision_step          TEXT,
    provisioning_started_at TIMESTAMPTZ,
    provisioning_completed_at TIMESTAMPTZ,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,
    stopped_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_machines_account_id ON machines(account_id);
CREATE INDEX IF NOT EXISTS idx_machines_host_id ON machines(host_id);
CREATE INDEX IF NOT EXISTS idx_machines_status ON machines(status);
CREATE INDEX IF NOT EXISTS idx_machines_slug ON machines(slug);

-- ============================================================
-- LLM Usage (billing — tracked by LiteLLM proxy, keyed to machine/account)
-- ============================================================
CREATE TABLE IF NOT EXISTS llm_usage (
    id              BIGSERIAL PRIMARY KEY,
    account_id      INT NOT NULL REFERENCES accounts(id),
    machine_id      UUID NOT NULL REFERENCES machines(id),
    provider        TEXT NOT NULL,
    model           TEXT NOT NULL,
    input_tokens    INT NOT NULL DEFAULT 0,
    output_tokens   INT NOT NULL DEFAULT 0,
    cost_microcents BIGINT NOT NULL DEFAULT 0,
    request_id      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_llm_usage_account_id ON llm_usage(account_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_machine_id ON llm_usage(machine_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_created_at ON llm_usage(created_at);

-- ============================================================
-- Account Events (account lifecycle audit log)
-- ============================================================
CREATE TABLE IF NOT EXISTS account_events (
    id              BIGSERIAL PRIMARY KEY,
    event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      TEXT NOT NULL,
        -- 'account.created' | 'member.invited' | 'member.removed' |
        -- 'plan.changed' | 'billing.updated'
    account_id      INT NOT NULL REFERENCES accounts(id),
    actor_user_id   INT REFERENCES users(id),
    target_user_id  INT REFERENCES users(id),
    metadata        JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_account_events_account_id ON account_events(account_id);
CREATE INDEX IF NOT EXISTS idx_account_events_type ON account_events(event_type);

-- ============================================================
-- Machine Events (machine lifecycle audit log)
-- ============================================================
CREATE TABLE IF NOT EXISTS machine_events (
    id              BIGSERIAL PRIMARY KEY,
    event_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    event_type      TEXT NOT NULL,
        -- 'machine.created' | 'machine.started' | 'machine.stopped' |
        -- 'machine.error' | 'machine.provisioning'
    event_source    TEXT NOT NULL,
        -- 'control_plane' | 'agent' | 'microvm'

    machine_id      UUID NOT NULL REFERENCES machines(id),
    host_id         INT,
    user_id         INT,

    duration_ms     INT,
    error_code      TEXT,
    error_message   TEXT,
    metadata        JSONB,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_machine_events_machine_id ON machine_events(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_events_type ON machine_events(event_type);
