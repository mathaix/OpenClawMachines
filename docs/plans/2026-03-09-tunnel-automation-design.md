# Cloudflare Tunnel Automation for 3rd-Party Hosts

## Date: 2026-03-09

## Problem

3rd-party hosts (OVH, Hetzner, self-owned) enrolled via the host enrollment system have no Cloudflare tunnel. The enrollment guide instructs admins to manually create tunnels. This is error-prone and inconsistent with the GCP architecture where tunnels are created automatically during provisioning.

Without a host-level tunnel, per-VM tunnels can still be created (runtime.go handles this), but the host has no cloudflared process to establish connectivity through Cloudflare's edge network.

## Design

### Architecture (consistent with GCP)

Same two-level tunnel architecture as GCP hosts:

```
Control Plane (Cloud Run)
    ↕ HTTPS via agent_endpoint (direct IP:9090) — control commands
3rd-Party Host
    ├── ocm-agent (systemd service)
    │   ├── Control API (port 9090) — CreateVM, StopVM, Health
    │   ├── Proxy server (port 9091) — user traffic routing
    │   ├── Firecracker orchestrator
    │   └── Metadata server (bridge gateway:80)
    ├── cloudflared (host-level tunnel)
    │   └── Routes {VMName}.openclawmachines.com → localhost:9091
    └── MicroVMs
        └── cloudflared (per-VM tunnel, created by runtime.go)
            └── Routes m-{slug}.openclawmachines.com → authproxy:8080
```

### Registration Flow (what changes)

`handleAgentRegister` in enrollment.go currently:
1. Validates enrollment token
2. Generates per-host agent token
3. Creates host record (with `VMName` like `registered-<unixnano>`)
4. Returns credentials (agent_token, GCS manifests, backend URL)

Add after step 3:
5. Generate tunnel hostname: `{host.VMName}.openclawmachines.com`
6. Create host-level Cloudflare tunnel via `tunnel.CreateTunnel(ctx, host.VMName)`
7. Configure tunnel ingress: hostname → `http://localhost:9091`
8. Create DNS CNAME record for the tunnel
9. Update host record with `tunnel_url` and `tunnel_id` via `UpdateHostDetails`
10. Return `tunnel_token` in the registration response

**Tunnel creation is mandatory.** If it fails, registration fails. This prevents hosts without tunnels from being promoted to `ready` and receiving VM placements that would then hard-fail in `runtime.go` when `host.TunnelURL` is nil.

### Partial Failure Cleanup

If tunnel creation succeeds but DNS or ingress configuration fails:
- Delete the partially created tunnel via `tunnel.DeleteTunnel(ctx, tunnelID)` before returning error
- Match the provisioner's cleanup pattern in `provisioner.go:153`
- `CreateTunnel` in `tunnel.go` does not have orphan-conflict retry (unlike `CreateVMTunnel`), so cleanup prevents orphaned tunnel resources on retry

### Install Script Changes

The install script template in `enrollment.go` parses the registration response JSON and writes `/etc/ocm-agent/agent.env`. Changes:

1. Install script must extract `tunnel_token` from the registration response:
   ```bash
   TUNNEL_TOKEN=$(echo "$RESPONSE" | jq -r '.tunnel_token // empty')
   ```

2. Write to agent.env:
   ```bash
   TUNNEL_TOKEN=${TUNNEL_TOKEN}
   ```

3. Install cloudflared binary (the script currently does NOT install it):
   ```bash
   curl -sL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
     -o /usr/local/bin/cloudflared
   chmod +x /usr/local/bin/cloudflared
   ```

4. Remove the manual "Step 5: Set Up Cloudflare Tunnel" section from `docs/host-enrollment.md` since tunnel creation is now automated.

### Agent Changes

The agent change is larger than just reading an env var. Currently:
- `config.LoadAgent()` in `config.go` reads `TunnelToken` from GCE metadata only (line 148)
- `main.go:245` fatals if `cfg.TunnelToken` is empty

Required changes:
1. **`config.go` (`LoadAgent`)**: Read `TUNNEL_TOKEN` from environment variable first, fall back to GCE metadata. Add to the existing agent config loading path.
2. **`main.go:245`**: Change the fatal to a warning. If `TunnelToken` is empty after both env and metadata checks, log a warning and skip cloudflared startup. The agent can still function for control plane commands via `agent_endpoint`. This handles edge cases where tunnel creation succeeded but the token was lost.
3. The rest of cloudflared management (subprocess lifecycle, restart on failure, graceful shutdown) is unchanged — `startCloudflared` already takes a token parameter.

### Host Naming

Current patterns:
- GCP hosts: `VMName` = `ocm-host-{millis}` (set in `provisioner.go:119`)
- Registered hosts: `VMName` = `registered-{unixnano}` (set in `enrollment.go:131`)

Change registered host naming to match GCP pattern:
- `VMName` = `ocm-host-{millis}` (consistent with provisioner)
- Tunnel name: `ocm-{VMName}` (e.g., `ocm-ocm-host-1741520400000`)
- DNS hostname / `tunnel_url`: `{VMName}.openclawmachines.com`

### Store Changes

**No new columns needed.** The `hosts` table already has:
- `tunnel_url TEXT` — from migration 001 (stores `{VMName}.openclawmachines.com`)
- `vm_id TEXT` — can store tunnel ID (or use `provider_metadata` JSONB)

However, `CreateRegisteredHost` in `postgres.go:1942` does NOT currently write `tunnel_url`. Changes needed:
1. Add `tunnel_url` to the `CreateRegisteredHost` INSERT query
2. OR: call `UpdateHostDetails` after registration to set `tunnel_url` (this method already updates `tunnel_url` at `postgres.go:624`)

For `tunnel_id` storage, use `provider_metadata` JSONB (already exists, avoids new column + migration + updating all host scan queries):
```json
{"tunnel_id": "e1869607-xxxx-xxxx-xxxx-xxxxxxxxxxxx"}
```

### Cleanup

**Registered hosts are never auto-terminated** — the HeartbeatOnlyChecker keeps them at `unreachable` status. `cleanupMachinesOnHost` only runs on the terminated path. So cleanup needs an explicit trigger:

1. **New admin endpoint: `DELETE /api/admin/hosts/{hostId}`** — deregisters a host:
   - Reads `tunnel_id` from `provider_metadata`
   - Calls `tunnel.DeleteTunnelAndDNS(ctx, tunnelID, host.TunnelURL)`
   - Marks machines as error, releases placements
   - Marks host as `terminated`

2. **Existing tunnel reaper** — already cleans orphaned per-VM tunnels (`ocm-vm-*` prefix). Does NOT clean host tunnels. Could be extended to check host tunnel liveness, but the admin endpoint is the primary cleanup path for registered hosts.

### Placement Gating

`FindHostWithCapacity` in `postgres.go:479` already filters on `status = 'ready'` and `last_heartbeat > now() - 180s`, but does NOT check `tunnel_url IS NOT NULL`. For GCP hosts this was never an issue because the provisioner sets `tunnel_url` before marking the host `ready`.

For registered hosts, since tunnel creation is now mandatory during registration, `tunnel_url` will always be set before the first heartbeat promotes the host to `ready`. No additional placement gating is needed.

If tunnel creation is ever made optional in the future, add `AND tunnel_url IS NOT NULL` to the `FindHostWithCapacity` query.

### What Does NOT Change

- Per-VM tunnel creation (runtime.go) — already host-agnostic
- Tunnel reaper — already cleans orphaned VM tunnels
- Worker routing — already routes based on DNS/KV
- GCP host provisioning — unchanged, continues using GCE metadata for tunnel token
- Control plane → agent communication — stays on agent_endpoint (direct IP:9090)
- `syncRouteToKV` — uses `host.TunnelURL` which will now be populated for registered hosts

### Testing

- Unit test: `handleAgentRegister` creates tunnel and returns token when tunnel manager is available
- Unit test: `handleAgentRegister` fails when tunnel creation fails (mandatory)
- Unit test: `handleAgentRegister` cleans up partial tunnel on DNS/ingress failure
- Unit test: agent reads `TUNNEL_TOKEN` from env var (config.go change)
- Unit test: agent starts without fatal when `TUNNEL_TOKEN` is empty (warning path)
- Unit test: admin deregister endpoint deletes tunnel
- Integration: enrolled host starts cloudflared with returned token (requires real CF credentials, manual)

## Codex Review (2026-03-09)

Codex reviewed this design against the codebase and found 8 issues. All have been addressed:

| # | Finding | Resolution |
|---|---------|------------|
| P1 | Graceful degradation allows tunnel-less hosts to get VMs that hard-fail | Made tunnel creation mandatory during registration |
| P1 | Agent change larger than described — config.go + fatal check need changing | Expanded agent changes section with full scope |
| P1 | Store section incorrect — `host_hostname` is not a column, `CreateRegisteredHost` doesn't write `tunnel_url` | Corrected: use existing `tunnel_url` column, update `CreateRegisteredHost` or call `UpdateHostDetails` |
| P2 | Inconsistent on `tunnel_id` storage (provider_metadata vs new column) | Resolved: use `provider_metadata` JSONB, no new column |
| P2 | Install script misses `tunnel_token` extraction and cloudflared installation | Added both to install script changes section |
| P2 | Partial failure cleanup underspecified | Added explicit cleanup section matching provisioner pattern |
| P2 | Cleanup not wired for registered hosts (never auto-terminated) | Added admin deregister endpoint as explicit trigger |
| P3 | Naming mismatch (`registered-<nano>` vs `ocm-host-<millis>`) | Changed to use `ocm-host-{millis}` pattern |
