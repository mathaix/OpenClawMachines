# A4: Provider Abstraction + Host Enrollment Design

Date: 2026-03-09
Branch: `3rdpartyprov-part2`
Status: Approved

## Goal

Enable non-GCP hosts (OVH Dedicated, Hetzner, customer-owned) to join the OCM fleet via a self-registration enrollment flow with per-host agent tokens.

## Schema Changes

### Migration 030: Provider fields on hosts

```sql
ALTER TABLE hosts
ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'gcp',
ADD COLUMN IF NOT EXISTS provider_class TEXT NOT NULL DEFAULT 'managed',
ADD COLUMN IF NOT EXISTS lifecycle_mode TEXT NOT NULL DEFAULT 'provisioned',
ADD COLUMN IF NOT EXISTS agent_endpoint TEXT,
ADD COLUMN IF NOT EXISTS agent_endpoint_type TEXT NOT NULL DEFAULT 'public_http',
ADD COLUMN IF NOT EXISTS provider_host_id TEXT,
ADD COLUMN IF NOT EXISTS provider_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}'::jsonb,
ADD COLUMN IF NOT EXISTS agent_token TEXT;

CREATE INDEX IF NOT EXISTS idx_hosts_provider ON hosts(provider);
CREATE INDEX IF NOT EXISTS idx_hosts_lifecycle_mode ON hosts(lifecycle_mode);
```

### Migration 031: Enrollment tokens

```sql
CREATE TABLE host_enrollment_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL DEFAULT 'ovhcloud',
    provider_class TEXT NOT NULL DEFAULT 'registered',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    used_by_host_id INT REFERENCES hosts(id),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## API Endpoints

### Admin: Create enrollment token

`POST /api/admin/hosts/enrollment-tokens`

Request:
```json
{"provider": "ovhcloud", "provider_class": "registered", "labels": {"fleet": "primary"}, "expires_in_hours": 24}
```

Response:
```json
{"token": "ocm_enroll_...", "expires_at": "2026-03-10T...", "install_command": "curl -sL https://api.openclawmachines.com/api/agent/install | bash -s -- ocm_enroll_..."}
```

### Admin: List enrollment tokens

`GET /api/admin/hosts/enrollment-tokens`

### Agent: Register

`POST /api/agent/register`

Request:
```json
{
  "enrollment_token": "ocm_enroll_...",
  "agent_endpoint": "http://203.0.113.10:9090",
  "agent_endpoint_type": "public_http",
  "capabilities": {"kvm": true, "arch": "x86_64", "cpu_threads": 12, "memory_mb": 65536},
  "labels": {"provider": "ovhcloud"}
}
```

Response:
```json
{
  "host_id": 42,
  "agent_token": "ocm_agent_...",
  "backend_url": "https://api.openclawmachines.com",
  "rootfs_gcs_manifest": "gs://openclawmachines/rootfs/manifest.json",
  "agent_gcs_manifest": "gs://openclawmachines/agent/manifest.json",
  "gcs_credentials": "<base64-service-account-json>",
  "tunnel_token": ""
}
```

Registration flow:
1. Validate enrollment token (not expired, not used)
2. Create host record with provider fields from token + request
3. Generate per-host agent token, store on host
4. Mark enrollment token as used
5. Return bootstrap config

### Agent: Install script

`GET /api/agent/install`

Returns a bash script that:
1. Downloads agent binary from GCS (or from backend proxy)
2. Calls `POST /api/agent/register` with the enrollment token
3. Writes `/etc/ocm-agent/agent.env` from response
4. Writes GCS credentials to `/etc/ocm-agent/gcs-key.json`
5. Installs systemd unit
6. Starts agent

## Per-Host Agent Tokens

- Each registered host gets a unique `agent_token` stored in `hosts.agent_token`
- Heartbeat handler: if host has `agent_token`, validate against it instead of fleet-wide token
- Shutdown-notify: same per-host token check
- GCP hosts (legacy): continue using fleet-wide `FC_AGENT_TOKEN` until re-enrolled
- Token format: `ocm_agent_` + 32 hex chars

## Heartbeat Changes

Add `agent_endpoint` field to heartbeat payload. Agent reports its own endpoint (from env) so the control plane knows how to reach it.

In `handleAgentHeartbeat`:
- If host has `agent_endpoint` in heartbeat, update `hosts.agent_endpoint`
- If host has per-host `agent_token`, validate against it

## Agent Client Changes

In `agentclient.Client.agentURL(host)`:
```go
func (c *Client) agentURL(host *store.Host) string {
    if host.AgentEndpoint != nil && *host.AgentEndpoint != "" {
        return *host.AgentEndpoint
    }
    // Legacy: construct from ExternalIP
    ip := ""
    if host.ExternalIP != nil {
        ip = *host.ExternalIP
    } else if host.InternalIP != nil {
        ip = *host.InternalIP
    }
    return fmt.Sprintf("http://%s:9090", ip)
}
```

## Agent Bootstrap Changes

In `cmd/agent/main.go`:
- Add `AGENT_ENDPOINT` env var to config
- Report `agent_endpoint` in heartbeat payload
- `prefetchGCPMetadata` already falls back silently — no change needed

## Reconciler Changes

For registered hosts without a provider API (OVH, Hetzner, customer-owned):
- No GCP instance check available
- Use extended heartbeat staleness (10 minutes) before marking terminated
- Add `HeartbeatOnlyChecker` that always returns "exists=true" (let heartbeat staleness handle it)
- Reconciler selects checker based on `host.provider`

## Debugging

After enrollment, SSH to host and:
- Install Claude Code manually for debugging
- `sudo journalctl -u ocm-agent -f` for logs
- Agent heartbeat visible in admin panel
- Reconciler status visible in backend logs

## Not Included (deferred)

- Cloudflare Tunnel auto-setup (manual for now)
- OVH API for server status reconciliation
- Admin UI enrollment wizard (use API directly)
- Automated OS install

## Files Changed

| File | Change |
|------|--------|
| `backend/migrations/030_provider_fields.sql` | New — provider columns |
| `backend/migrations/031_enrollment_tokens.sql` | New — tokens table |
| `backend/internal/store/store.go` | Enrollment types + interface |
| `backend/internal/store/postgres.go` | Enrollment queries |
| `backend/internal/api/server.go` | Route registration |
| `backend/internal/api/enrollment.go` | New — enrollment handlers |
| `backend/internal/agentclient/client.go` | Use agent_endpoint |
| `backend/cmd/agent/main.go` | Report agent_endpoint in heartbeat |
| `backend/internal/reconciler/host.go` | Provider-aware checker |
| `backend/internal/reconciler/heartbeat_checker.go` | New — heartbeat-only checker |

## Tests

- Create enrollment token (valid, with expiry)
- Register host with valid token (host created, token marked used)
- Reject expired token
- Reject reused token
- Per-host token validated in heartbeat
- Agent client uses agent_endpoint when set
- Reconciler uses heartbeat-only checker for registered hosts
- Install script endpoint returns valid bash
