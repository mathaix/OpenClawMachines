# OpenClaw Machines — Security & Architecture Review
Date: 2026-02-04

## Scope
- Reviewed backend Go control-plane, migrations, agent client, scheduler/provisioner, and React frontend (auth, API wrappers, machine UX).
- No runtime tests were executed; findings are static-code-based.

## Summary (top risks)
1) Account authorization missing on most account-scoped endpoints, enabling cross-tenant access.  
2) Secrets handled in plaintext (storage, transit, host metadata).  
3) Weak token handling (localStorage + query-string tokens, permissive WS origin).  
4) Agent/gateway calls unauthenticated or optional, exposing VM control if host IP reachable.  
5) CORS overly permissive with credentials enabled; config allows empty secrets.

## High / Critical Findings
- **Account auth bypass**: Account routes use `accountId` path params without verifying membership/role (machines CRUD/start/stop, secrets/files/logs, member listing, admin host ops). Any logged-in user can operate on other accounts by ID guessing. (`backend/internal/api/*`, especially `server.go`, `machine_files.go`, `machine_logs.go`, `machine_secrets.go`)
- **Plaintext secrets**: Secrets stored as provided, no encryption/KMS; keys sent from frontend in clear and written to DB column `encrypted_value`. Host provisioning injects global LLM/API keys into GCP instance metadata (readable by anyone with metadata access). (`backend/internal/api/machine_secrets.go`, migrations, `frontend/src/components/SecretVault.tsx`, `backend/internal/provisioner/provisioner.go`)
- **Token leakage surface**: JWTs kept in `localStorage`, appended to download/WS URLs, and backend accepts tokens via query param; WS `CheckOrigin: true`. Increases risk of token exfil via referrers/logs or XSS. (`frontend/src/lib/api.ts`, `frontend/src/lib/auth.tsx`, `backend/internal/api/machine_browser.go`, `backend/internal/auth/auth.go`)
- **Agent interface weakly protected**: Control-plane proxies to agent ports 9090/9091 without auth headers; `agentToken` can be empty. If host IP exposed, attackers can start/stop VMs or read files/logs cross-tenant. (`backend/internal/agentclient/client.go`, proxy handlers)

## Medium Findings
- **CORS/config lax**: Default `CORS_ORIGINS="*"` with `AllowCredentials=true`; JWT secret and agent token not required at startup. (`backend/internal/api/server.go`, `backend/internal/config/config.go`)
- **Region hardcoded**: Scheduler uses fixed `"us-central1"` instead of configured zone/region, risking misplacement. (`backend/internal/scheduler/scheduler.go`)
- **Unfinished components**: LLM proxy, Cloudflare tunnel, and TLS/custom-domain flows are stubs; absence may block prod readiness. (`backend/internal/llmproxy/proxy.go`, `backend/internal/tunnel/tunnel.go`)
- **Testing gap**: No auth/ACL or secret-handling tests; frontend lacks e2e/contract coverage around tokens and downloads.

## Recommendations (prioritized)
1) Implement membership/role checks middleware for all account-scoped routes; lock admin host ops to owner/admin roles.  
2) Require non-empty `JWT_SECRET` and `FC_AGENT_TOKEN` at startup; fail fast otherwise.  
3) Move auth to HTTP-only, SameSite cookies or header-only bearer tokens; forbid query-string tokens; tighten WS upgrader with strict origin check.  
4) Encrypt secrets at rest (e.g., KMS envelope); never store plaintext; redact values on reads; avoid injecting global API keys into VM metadata—fetch per-request via secure channel.  
5) Authenticate control-plane → agent traffic (mTLS or signed tokens on every call); keep agent ports private/internal only.  
6) Restrict CORS to an allowlist of frontend origins; disable credentials when using `*`.  
7) Parameterize scheduler region from config; add unit tests for placement and auth guards.  
8) Finish tunnel/LLM proxy implementations with security (authn/z, TLS, rate limits) and add integration tests.

## Open Questions
- What is the intended RBAC model (owner/admin/member) for account and host operations?  
- Are agents guaranteed to be reachable only on a private network, or must the control plane enforce auth on every hop?
