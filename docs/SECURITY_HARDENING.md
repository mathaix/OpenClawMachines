# Security Hardening Checklist

Findings from architecture and code review. Organized by when to fix.

## Hermes / Operator Access Findings (2026-05-14)

These findings came from debugging Hermes dashboard WebSocket connectivity and host-agent self-update in production. They do **not** prove that an unauthenticated internet user can reach a Hermes VM, but they do show that the current operator and host-control model has a large blast radius.

### Observed Access Path

- Operator credentials with access to the deployed JWT signing secret can mint an app JWT for a user/account. The backend token issuer is `backend/internal/auth/auth.go:92-105`, and the worker verifies the shared secret in `worker/worker.js:562-566`.
- The normal data-plane route then works as designed: `https://{account}.openclawmachines.com/{machine}/dashboard/...` -> Cloudflare Worker -> route resolution -> host agent proxy -> VM auth proxy -> Hermes dashboard on localhost.
- The Hermes dashboard HTML exposes a dashboard session token to the browser. With valid user access to the dashboard page, that token can open `/dashboard/api/ws`, `/dashboard/api/events`, and other dashboard APIs.
- A host-specific `agent_token` from the production database can call mutating host-agent endpoints, including `POST /trigger-update`. The endpoint is registered in `backend/internal/agentapi/server.go:133-156`, and the backend client calls it in `backend/internal/agentclient/client.go:665-683`.
- No SSH access to the Hermes VM or physical host was required for the dashboard tests or host-agent self-update trigger.

### Fix Now (Hermes / Host Control)

- [ ] **Move host control API off the public internet** — `backend/cmd/agent/main.go:300-319`, `backend/internal/agentapi/server.go:124-156`
  The host control API listens on `:9090`. Mutating routes require a bearer token, but the surface is still directly reachable when host firewalling permits it. Put this API behind private networking, a VPN, Cloudflare Access, or strict firewall allowlists. Treat public `:9090` exposure as a production security smell.

- [ ] **Make control-plane CIDR restrictions fail closed** — `backend/internal/agentapi/cidr.go:11-32`
  `CONTROL_ALLOWED_CIDRS` currently allows all traffic when empty or when every configured CIDR is invalid. That is useful during migration but unsafe as a steady-state default. Production should refuse to start, or at minimum refuse control API traffic, when the allowlist is empty or unparsable.

- [ ] **Rotate any host agent tokens used during operator debugging** — `backend/internal/agentapi/server.go:141-156`
  A host-specific `agent_token` is enough to trigger host updates and VM lifecycle actions. Any token used from a developer machine or copied into a shell session should be considered exposed and rotated.

- [ ] **Separate database read access from host-control authority**
  The host `agent_token` is stored in the production database. That means broad DB read access can become host-control access. Store host-control tokens in a stronger secret boundary, encrypt them with a narrowly-scoped KMS key, or require a backend-mediated, audited control-plane operation instead of exposing raw tokens to operators.

- [ ] **Replace local JWT minting with audited operator impersonation** — `backend/internal/auth/auth.go:92-105`
  Possession of the app JWT signing secret allows user/account impersonation. Operator debugging should use a short-lived, audited impersonation flow with explicit target user/account, reason, expiry, and log trail. Avoid giving local developer environments direct access to the signing secret.

- [ ] **Add audit events for dashboard access and host-control actions**
  Log who initiated dashboard debug access, machine route resolution, host `trigger-update`, rootfs upgrade, agent upload, manifest promotion, and token rotation. The log should include actor, target host/machine, source, and request ID, without storing bearer tokens or dashboard session tokens.

- [ ] **Sign and verify agent/rootfs release manifests**
  Host-agent self-update trusts the published agent manifest, and Hermes/rootfs upgrades trust published rootfs release metadata. GCS write access to these objects is effectively fleet-level code execution. Add release signing, verify signatures in the host agent before update, and restrict manifest promotion separately from artifact upload.

- [ ] **Canary host-agent self-update before fleet rollout**
  The observed self-update path temporarily made the host unavailable and marked it draining before recovery. Self-update should roll through a canary host first, wait for heartbeat/version/VM proxy health, and only then promote the manifest for broader rollout.

- [ ] **Harden Hermes writable runtime persistence**
  Hermes needs a writable rootfs/runtime because Hermes can self-update. That is functionally required, but it means a compromised guest has a stronger persistence path than an immutable rootfs. Constrain the writable area to the Hermes checkout/runtime directory where possible, keep platform services outside that writable path, and make rootfs/runtime version drift visible in host heartbeat.

- [ ] **Treat dashboard session tokens as high-value browser secrets**
  The dashboard token is intentionally available to the browser after authenticated page load. Any XSS in the dashboard origin/path can steal it and access chat, events, and terminal-style APIs. Add a dashboard CSP, avoid inline script where feasible, audit token lifetime/scope, and ensure tokens are never logged in worker, authproxy, or host-agent logs.

## Fix Now (Bugs)

- [ ] **Missing proxy token on file operations** — `backend/internal/api/machine_files.go:48-49, 88-89, 127-128`
  File list/download/zip proxy requests don't include `X-Proxy-Token` header. All other proxy routes include it. These requests either fail silently or bypass agent-side auth.

- [ ] **No bounds checking on machine resource requests** — `backend/internal/api/server.go:356-380, 442-446`
  `VCPUs` and `MemoryMB` accepted with no upper limit. A user could request 100000 vCPUs, exhausting host capacity or triggering unbounded GCP provisioning. Add limits: VCPUs [1-8], MemoryMB [512-8192].

- [ ] **File path proxy has no URL encoding** — `backend/internal/api/machine_files.go:43-49`
  `filePath` from query params is directly concatenated into the agent URL with `fmt.Sprintf`. No `url.QueryEscape()`, no path traversal validation. Attacker can send `path=../../etc/passwd` or break the URL with `path=foo&injected=true`.

## Fix Before Public Launch

- [ ] **No user authorization on agent proxy routes** — `backend/internal/agentapi/proxy.go:33-54`
  Proxy token is the only check on `/proxy/{machineID}/*`. No verification that the authenticated user owns the machine. If a proxy token leaks (logs, KV breach, referrer), any user can access another's terminal, browser, gateway, and logs.

- [ ] **API keys in GCE instance metadata** — `backend/internal/provisioner/provisioner.go:115-127`
  Anthropic, OpenAI, and Google API keys stored as GCE metadata items. Any host-level compromise exposes all provider keys. Migrate to GCP Secret Manager.

- [ ] **No slug format validation** — `backend/internal/api/server.go:277, 358`
  Account and machine slugs accept arbitrary strings. Slugs are used in KV keys, URLs, and tunnel routes. Validate with regex: `^[a-z0-9][a-z0-9-]*[a-z0-9]$`.

- [ ] **`/internal/resolve` returns proxy token without user context** — `backend/internal/api/server.go:1150-1186`
  Protected by CF service token, but backend doesn't verify the worker's JWT claims match the machine's owner. Move authz check to the backend for defense in depth.

- [ ] **No encryption key validation at startup** — `backend/pkg/crypto/crypto.go:13-16`
  `SECRET_ENCRYPTION_KEY` must be exactly 32 bytes but is only checked at encrypt/decrypt time, not at boot. Misconfigured deployments silently fail when secrets are used. Add startup validation.

## Fix When It Matters

- [ ] **Proxy token cached in Cloudflare KV** — `worker/worker.js:266-272`
  `proxy_token` stored in KV with 1-hour TTL. If KV namespace is compromised, all active proxy tokens are exposed. Consider fetching token from backend per-request instead.

- [ ] **Proxy token accepted in URL query params** — `backend/internal/agentapi/proxy.go:67-71`
  Necessary for WebSocket, but tokens in URLs leak via server logs, browser history, and referrer headers.

- [ ] **Firecracker launched without jailer** — `backend/internal/orchestrator/firecracker_linux.go:178-184`
  `VMCommandBuilder` doesn't use Firecracker's jailer for seccomp filtering. Jailer adds process-level isolation on top of KVM.

- [ ] **OAuth state comparison is not constant-time** — `backend/internal/api/oauth.go:61-67`
  Uses `==` instead of `subtle.ConstantTimeCompare`. Low risk since state is random and single-use.

- [ ] **No rate limiting on auth endpoints** — `backend/internal/api/server.go:113-125`
  OAuth and login endpoints have no rate limiting. Cloud Run provides some protection, but explicit limits would be better.

- [ ] **JWT fixed 24h expiry, no refresh tokens** — `backend/internal/auth/auth.go:43-56`
  No revocation mechanism. Compromised tokens valid for 24 hours.

- [ ] **WebSocket upgrader accepts all origins** — `backend/internal/agentapi/proxy.go:20-24`
  `CheckOrigin` returns true for all origins. Relies entirely on proxy token auth.

- [ ] **Control API depends on bearer token + network only** — `backend/internal/agentapi/server.go:32-54`
  Port 9090 binds `0.0.0.0` with only a bearer token. No mTLS, no IP allowlisting. If internal network is compromised, attacker can create/destroy any VM.

- [ ] **Machine ID in SSE query params** — `backend/internal/api/machine_progress.go:50`
  `machine_id` in URL query string can leak through logs and caches.

- [ ] **Path traversal risk in orchestrator** — `backend/internal/orchestrator/firecracker_linux.go:108-119`
  `MachineID` used in `filepath.Join` for rootfs/socket paths. Currently mitigated by UUID format from database, but should validate at API boundary.

## Verified Secure

- **SQL injection** — All queries in `store/postgres.go` use parameterized `$1, $2`
- **VM-to-VM isolation** — iptables blocks inter-VM traffic on bridge (`bridge_linux.go:98-101`)
- **GCP metadata blocked from VMs** — `169.254.169.254` dropped (`bridge_linux.go:91-95`)
- **Agent ports blocked from VMs** — ports 9090/9091 dropped from bridge (`bridge_linux.go:105-110`)
- **Proxy token timing-safe** — uses `subtle.ConstantTimeCompare` (`agentapi/proxy.go:56-84`)
- **Service token timing-safe** — uses `subtle.ConstantTimeCompare` (`api/server.go:1139-1142`)
- **Scheduler race conditions** — `FOR UPDATE` with transaction, no TOCTOU (`store/postgres.go:590-684`)
- **Secrets encrypted at rest** — AES-256-GCM (`pkg/crypto/crypto.go`)
- **CORS origin validation** — requires `https://` + `.openclawmachines.com` (`worker/worker.js:18-23`)
