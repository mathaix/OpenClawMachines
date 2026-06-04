# SSH Tunnel Not Working — Investigation Report

## Summary

`ocm machines ssh <slug>` fails with `websocket: bad handshake`. The native cloudflared library integration is working correctly — the same error occurs with the official `cloudflared access ssh` binary. The root cause is that **cloudflared is not running inside the VM**, so the Cloudflare Tunnel is not connected.

## Symptoms

```
$ ocm machines ssh hvn58kw
Connecting to user@ssh-hvn58kw.openclawmachines.com ...
websocket: bad handshake
Connection closed by UNKNOWN port 65535
```

- Both `ssh-hvn58kw.openclawmachines.com` and `m-hvn58kw.openclawmachines.com` return **HTTP 530** (Cloudflare error code **1033** — Argo Tunnel error / tunnel not connected).
- The machine shows as `running` in the API.
- The official `cloudflared access ssh --hostname ssh-hvn58kw.openclawmachines.com` produces the identical error, confirming this is not a code issue.

## Root Cause

**cloudflared never started inside the VM.** Evidence:

1. `/var/log/cloudflared.log` does not exist — cloudflared never attempted to start
2. `ps -ef | grep cloudflared` shows no running process
3. `/var/log/openclaw-init.log` is empty — the init script's logging (`exec > >(tee ...)`) did not capture any output
4. `grep -i 'cloudflared\|tunnel\|SKIP' /var/log/openclaw-init.log` returns nothing
5. `env | grep -i tunnel` returns nothing — no tunnel token in environment

## Why cloudflared Didn't Start

The init script (`/sbin/overlay-init`, sourced from `scripts/init-openclaw.sh`) starts cloudflared in section 11:

```bash
# Line 460-467 of init-openclaw.sh
if [ -n "$TUNNEL_TOKEN" ] && command -v cloudflared >/dev/null 2>&1; then
    cloudflared tunnel run --token "$TUNNEL_TOKEN" >> /var/log/cloudflared.log 2>&1 &
    # ...
else
    echo "  [SKIP] cloudflared (no tunnel token or binary not found)"
fi
```

Two conditions must be met: `TUNNEL_TOKEN` must be non-empty AND `cloudflared` must be in PATH. We confirmed `cloudflared` exists at `/usr/local/bin/cloudflared` (installed in `rootfs/Dockerfile.openclaw`).

**Most likely cause: the VM is running an older rootfs** that predates the cloudflared/tunnel additions to the init script. Evidence:

- The init log is completely empty, meaning even the log redirect (`exec > >(tee -a /var/log/openclaw-init.log) 2>&1`) on line 12 never ran
- This suggests the `/sbin/overlay-init` in the running VM is an older version without the logging setup or the cloudflared section
- The Dockerfile installs cloudflared, and the init script references it, but both were likely added in the same rootfs build — if the VM is running a pre-tunnel rootfs, neither the binary nor the init section would exist

## Backend Architecture (Verified Working)

The server-side tunnel setup is correctly implemented:

1. **Tunnel creation** (`backend/internal/api/server.go:826`): `startMachineInternal()` creates a per-VM Cloudflare Tunnel via the CF API
2. **Ingress rules** (`backend/internal/tunnel/tunnel.go:307-324`): Configures both:
   - `m-{slug}.openclawmachines.com` → `http://localhost:8080` (authproxy)
   - `ssh-{slug}.openclawmachines.com` → `ssh://localhost:22` (sshd)
3. **DNS records** (`server.go:838-844`): Creates CNAME records for both hostnames → `{tunnelID}.cfargotunnel.com`
4. **Token passing** (`agentclient/client.go:143-144`): `TunnelToken` is passed from backend → agent API → metadata → VM
5. **SSH cert validation** (`init-openclaw.sh:475-523`): CF Access SSH CA is configured, sshd trusts it, principal check validates owner emails

Note: `TunnelToken` in `store.Machine` is tagged `json:"-"` (transient, not persisted to DB). It's only held in memory during `startMachineInternal()` and passed to the agent. This is by design — the token is a secret that shouldn't be stored.

## Fix

### 1. Rebuild rootfs (fixes cloudflared not starting)

Rebuild and upload the rootfs to GCS so new VMs boot with the latest init script:

```bash
make build-upload-rootfs   # build rootfs + upload to GCS
# or: make build-components  # rebuild all (rootfs + agent + CLI)
```

Then restart the machine so it boots with the new rootfs:

```bash
ocm machines stop hvn58kw
ocm machines start hvn58kw
```

### 2. Fix agent proxy routing (separate issue)

Even with cloudflared running, the host-side agent proxy cannot reach the gateway. Commit `7b5ad5c` changed the OpenClaw gateway bind from `lan` to `127.0.0.1` (loopback only), but `proxy.go` still connects directly to `VMIP:18789` across the TAP interface.

- **Per-VM tunnel path works**: `cloudflared → auth proxy :8080 → 127.0.0.1:18789` (all inside VM)
- **Agent proxy path broken**: `Agent :9091 → VMIP:18789` → refused (gateway not on TAP)
- **Health probes broken**: `checkPort(VMIP, 18789)` always returns "unreachable"

This means the agent reports the gateway as unreachable and the host-level proxy routes fail, even though the gateway is actually running fine inside the VM.

**Fix**: Route `proxy.go` through the auth proxy (`VMIP:8080`) instead of directly to the gateway. The agent mints a machine JWT (it already has the signing key per VM) and sends it to the auth proxy. See `docs/CurrentFeature.md` "Route Agent Proxy Through Auth Proxy" for the full plan.

## Verification Checklist

After rebuilding rootfs and restarting:

1. `grep cloudflared /sbin/overlay-init` inside VM — should show the cloudflared section
2. `/var/log/cloudflared.log` should exist and show tunnel connection
3. `curl -s https://m-{slug}.openclawmachines.com/health` should return `{"status":"ok"}` (not HTTP 530)
4. `ocm machines ssh {slug}` should connect via SSH

After agent proxy fix is deployed:

5. Agent health probes report gateway as reachable
6. Host-level proxy routes (`/proxy/{id}/gateway/*`) work through auth proxy
