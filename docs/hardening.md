# Runtime Hardening Review

## Scope

This document captures the runtime hardening findings from a review of:

- rootfs staging and refresh
- agent startup, shutdown, and heartbeat behavior
- metadata service behavior
- agent proxying and auth proxying
- Cloudflare tunnel management
- Firecracker VM networking and browser companion networking

Primary files reviewed:

- `backend/cmd/agent/main.go`
- `backend/cmd/authproxy/main.go`
- `backend/internal/agentapi/*`
- `backend/internal/agentclient/client.go`
- `backend/internal/apiproxy/*`
- `backend/internal/cdpproxy/proxy.go`
- `backend/internal/metadata/*`
- `backend/internal/network/*`
- `backend/internal/orchestrator/firecracker_linux.go`
- `backend/internal/rootfs/gcs.go`
- `backend/internal/tunnel/*`
- `scripts/init-openclaw.sh`
- `scripts/init-browser.sh`

## Executive Summary

The main issues are not isolated bugs. They cluster around three architectural weaknesses:

1. trust is derived too heavily from source IP and edge routing behavior
2. internal platform services and user-exposed app ports share the same TCP namespace
3. rootfs, tunnel, and VM lifecycle paths are not sufficiently atomic or supervised

The highest-risk issue is the current `/port/{port}` design. It preserves the app-preview feature, but it also bypasses the tighter auth model used for `/gateway` and `/terminal`.

## Findings

### 1. High: `/port/{port}` bypasses the intended auth boundary

The VM auth proxy intentionally allows `/port/{port}` without validating a machine token.

That means any caller who can reach the VM tunnel can proxy to arbitrary localhost HTTP and WebSocket services inside the VM through the reverse-proxy path, including internal OCM services if they are listening on TCP.

Today that includes at least:

- PTY server on `127.0.0.1:7681`
- OpenClaw gateway on `127.0.0.1:18789`

This collapses the distinction between:

- user app preview traffic
- terminal access
- gateway/control access

Edge HTTPS does not remove this issue. TLS terminates before traffic is forwarded to the local service, so the remaining trust boundary is still the auth proxy's routing policy. The core problem is authorization and service separation, not whether the public entrypoint uses HTTPS.

The problem is not the existence of a port-preview feature. The problem is that user app ports and internal platform services share the same localhost TCP namespace.

### 2. High: host control traffic is plaintext and exposure fails open

The host control API listens on `:9090`, the backend agent client talks to it over `http://<ip>:9090`, and the CIDR allowlist middleware becomes allow-all when:

- `CONTROL_ALLOWED_CIDRS` is empty
- all configured CIDRs are invalid

That creates two separate problems:

- no transport security for bearer-token-protected lifecycle traffic
- broader-than-intended exposure if the surrounding firewall rules are missing or drift

Even if the infrastructure firewall is correct, plaintext public control traffic is still a weak baseline.

### 3. High: source IP is used as a trust primitive without anti-spoofing controls

Several core services trust the VM source IP:

- metadata server
- LLM API proxy
- CDP proxy

The bridge code does not install per-TAP IP/MAC anti-spoofing rules. A compromised VM may be able to spoof another VM's bridge IP and interfere with services keyed off `RemoteAddr`.

The weakest case is the CDP proxy because it resolves the target from source IP alone.

### 4. High: `refresh-rootfs` is unsafe and can reintroduce stale images

`POST /refresh-rootfs` overwrites the base rootfs in place.

Current problems:

- no temp file + atomic rename
- no lock or exclusion against concurrent VM create
- no coordination with reflink copies that are using the same base image
- in GCS mode, the refresh source is still the embedded local rootfs path, so it can silently replace a newer staged GCS rootfs with a stale embedded image

This endpoint should not exist in its current form.

### 5. High: GCS rootfs staging is not resilient to transient control-plane failures

The GCS fetcher always fetches the manifest first. If manifest lookup fails, agent startup fails even if:

- a previously downloaded rootfs already exists locally
- the local staged image was previously verified

The cache-hit path also trusts the sidecar manifest plus file existence without rehashing the staged file. That means local corruption can survive indefinitely.

The current behavior is too brittle for a boot-critical path.

### 6. Medium-High: VM-side `authproxy` and `cloudflared` are not supervised after boot

Inside the VM, PID 1 starts:

- PTY server
- auth proxy
- cloudflared
- gateway
- config watcher

But the supervisor loop only actively restarts:

- PTY server
- gateway

If `authproxy` or VM-side `cloudflared` dies later, the machine can remain nominally "running" while becoming partially or fully unreachable.

### 7. Medium: browser companion networking leaks stale allow-rules on failure paths

When the browser companion VM is created, the host first inserts allow-rules between main VM and browser VM.

Several failure paths remove the browser TAP or rootfs but do not remove the inter-VM allow-rules. Orphan cleanup also misses these rules.

That creates stale bridge exceptions over time.

### 8. Medium: heartbeat reporting is GCP-specific and blocks multi-provider support

The host agent only sends a heartbeat if it can fetch an external IP from GCP metadata.

That means OVH, Vultr, Hetzner, and self-owned hosts will not report:

- `last_heartbeat`
- updated IP
- staged rootfs versions
- staged browser rootfs versions

This is both a reliability problem and a blocker for a generic host-provider architecture.

## `/port/*` Redesign

The right fix is to preserve preview functionality while removing internal control services from the TCP port space that `/port/*` can reach.

### Recommended model

Use two classes of endpoints:

1. user app endpoints
2. internal platform endpoints

User app endpoints remain TCP listeners and are allowed behind `/port/{port}`.

Internal platform endpoints should move to Unix sockets:

- gateway: `/run/openclaw-gateway.sock`
- PTY server: `/run/pty.sock`
- any future internal-only platform service: Unix socket, not TCP

Under that model:

- `/gateway/*` proxies to the gateway Unix socket
- `/terminal/*` proxies to the PTY Unix socket
- `/port/{port}` remains available for user dashboards and local web apps
- `/port/{port}` can no longer accidentally expose internal OCM services

This preserves the preview feature. Public traffic can still enter over HTTPS at the edge, but the local routing layer inside the VM no longer treats internal platform services and user app services as the same class of endpoint.

### Defense in depth

Even after moving internal services to Unix sockets:

1. deny reserved ports in `/port/*`
2. add an explicit expose model for user apps
3. surface exposure state in the control plane

Reserved denylist should include at least:

- `22`
- `80`
- `8080`
- `7681`
- `9090`
- `9091`
- `9222`
- `18789`

Longer-term, `/port/{port}` should only work for explicitly exposed ports, for example:

- `ocm expose 3000`
- auto-registration when a managed app runner starts a server

That allows:

- user dashboards
- app previews
- local dev pages

without leaving "proxy any localhost TCP port" as a permanent trust boundary.

## Recommended Remediation

### P0

Fix first:

1. move gateway and PTY server off TCP and onto Unix sockets
2. block `/port/*` from reaching reserved internal ports
3. make the control API fail closed when CIDR config is absent or invalid
4. stop using plaintext public HTTP for the agent control plane

### P1

Fix next:

1. add per-TAP anti-spoofing enforcement
2. make `refresh-rootfs` atomic and serialization-safe
3. disable or redesign `refresh-rootfs` in GCS mode
4. make VM-side `authproxy` and `cloudflared` supervised

### P2

Then:

1. make rootfs staging cache-aware and resilient to manifest outages
2. clean stale `AllowVMPair` rules on all browser VM failure paths
3. make heartbeat endpoint logic provider-neutral

## Control Plane Hardening Direction

The host control API should move toward one of these models:

1. mTLS between backend and agent
2. reverse control channel from agent to control plane
3. private-network-only reachability plus authenticated transport

Minimum bar:

- no plaintext bearer token transport over the public network
- no allow-all fallback when allowlist config is absent

## Networking Hardening Direction

The bridge needs explicit anti-spoofing controls tied to each TAP:

- expected source IP per TAP
- expected MAC per TAP
- drop frames that do not match the assigned identity

Without this, source-IP-based trust remains too weak for metadata and proxy routing.

## Rootfs Hardening Direction

Rootfs staging should be treated as a boot-critical artifact pipeline.

Recommended properties:

1. staged image written to temp path, then atomically renamed
2. lock to exclude concurrent create/refresh races
3. manifest fetch failure should not brick restart if a previously verified staged image exists
4. periodic or startup re-verification of staged image hash
5. no fallback path that can silently replace a staged GCS rootfs with an embedded local image

## Tunnel and VM-Side Process Hardening

Inside the VM, PID 1 should supervise:

- gateway
- PTY server
- auth proxy
- cloudflared

At minimum:

1. restart `authproxy` if it exits
2. restart VM-side `cloudflared` if it exits
3. surface degraded state explicitly through health endpoints
4. fail startup clearly if the tunnel path is required and unavailable

## Multi-Provider Hardening Implications

The current heartbeat path assumes GCP metadata and GCP external IP discovery.

To support OVH, Hetzner, Vultr, and self-owned servers, the agent needs:

- provider-neutral endpoint reporting
- heartbeat independent of GCP metadata
- explicit agent endpoint or reverse transport model

This is both an architecture concern and a hardening concern because stale liveness data weakens every later reconciliation path.

## Test Gaps

The following test areas are missing or insufficiently covered:

1. `/port/*` reaching internal OCM services
2. concurrent `refresh-rootfs` and VM create
3. GCS manifest outage with previously verified staged rootfs present
4. corruption of a staged rootfs with a matching sidecar manifest
5. `authproxy` and VM-side `cloudflared` crash after boot
6. stale `AllowVMPair` cleanup on browser VM failure
7. source-IP spoofing or TAP anti-spoofing enforcement
8. provider-neutral heartbeat behavior outside GCP

## Test-First Work Plan

### PR 1: `/port/*` hardening

Tests first:

- `/port/*` cannot reach reserved internal ports
- `/gateway/*` still works through the dedicated path
- `/terminal/*` still works through the dedicated path
- user app preview on an allowed port still works

Suggested files:

- `backend/cmd/authproxy/main_test.go`
- `backend/internal/integration/dataplane_test.go`

### PR 2: agent control transport hardening

Tests first:

- control API rejects requests when allowlist config is empty
- control API rejects requests when allowlist config is invalid
- backend agent client uses the hardened transport path

Suggested files:

- `backend/internal/agentapi/server_test.go`
- `backend/internal/agentclient/client_test.go`

### PR 3: rootfs refresh and staging hardening

Tests first:

- `refresh-rootfs` uses atomic swap semantics
- concurrent create does not read a partially refreshed base image
- GCS manifest failure still allows use of a previously verified staged rootfs

Suggested files:

- `backend/internal/rootfs/gcs_test.go`
- `backend/internal/agentapi/handlers_test.go`
- `backend/internal/orchestrator/helpers_test.go`

### PR 4: VM-side supervision hardening

Tests first:

- VM health degrades when `authproxy` dies
- VM health degrades when VM-side `cloudflared` dies
- supervisor restarts both processes

Suggested files:

- integration coverage around VM boot and service supervision
- shell-based tests for `scripts/init-openclaw.sh`

### PR 5: anti-spoofing and browser rule cleanup

Tests first:

- per-TAP anti-spoofing rules are installed
- browser VM failure paths remove inter-VM allow-rules
- orphan cleanup removes stale browser networking rules

Suggested files:

- `backend/internal/network/network_test.go`
- `backend/internal/orchestrator/helpers_test.go`

## Open Questions

These findings assume the repo code is the primary enforcement layer.

Questions to validate separately:

1. Are infrastructure firewalls guaranteeing that `9090` and `9091` are not publicly reachable?
2. Is Cloudflare Access intended to be the only authorization boundary for all `/port/*` traffic?
3. Are there any existing operational controls outside this repo that already restart VM-side `cloudflared` or `authproxy`?

Those answers may change severity, but they do not change the underlying architectural weaknesses.
