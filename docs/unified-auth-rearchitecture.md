# Unified Auth + Direct VM Access — Rearchitecture Plan

## Context

Two problems to solve together:

1. **Auth complexity**: Custom login/signup, password hashing, OAuth callbacks, JWT management — all hand-rolled. Can be replaced by Cloudflare Access (free for 50 users).

2. **No direct VM access**: MicroVMs are on a private bridge (192.168.x.x). Users interact only via web terminal proxied through 4 hops (Browser → Worker → Backend → Agent → VM). No SSH, no IDE integration, no dashboard exposure.

**Solution**: Unified Cloudflare Access authentication + per-VM cloudflared tunnels. One login, one cookie, one domain. SSH, web, and dashboards all authenticated at the edge.

---

## User Flows

### Flow 1: First-time visitor → signup

```
1. User visits yourdomain.com
   │
2. CF Access intercepts (no CF_Authorization cookie)
   │  → Redirects to CF Access login page
   │  → Shows: [Email OTP] [Google] [GitHub]
   │
3. User chooses auth method:
   │  Email OTP: enters email → receives 6-digit code → enters code
   │  Google: clicks → Google OAuth consent → redirect back
   │  GitHub: clicks → GitHub OAuth consent → redirect back
   │
4. CF Access sets CF_Authorization cookie (scoped to .yourdomain.com)
   │  → Forwards request to origin with headers:
   │     Cf-Access-Jwt-Assertion: <JWT with email, sub, iss, exp>
   │     Cf-Access-Authenticated-User-Email: user@example.com
   │
5. Request hits backend API
   │  → New middleware reads Cf-Access-Jwt-Assertion
   │  → Validates JWT signature against CF JWKS endpoint
   │  → Extracts email
   │  → DB lookup: SELECT * FROM users WHERE email = ?
   │  → NOT FOUND → auto-create user:
   │     INSERT INTO users (email, name, auth_provider) VALUES (...)
   │     INSERT INTO accounts (name, slug) VALUES ('Personal', 'user-{id}')
   │     INSERT INTO account_members (account_id, user_id, role) VALUES (..., 'owner')
   │
6. Frontend loads dashboard
   │  → Detects new user (no display_name or first_login flag)
   │  → Shows one-time profile completion:
   │
   │  ┌─ Welcome to OpenClaw! ─────────────────────────┐
   │  │  Email: user@example.com (verified)             │
   │  │                                                 │
   │  │  Display name: [________________]               │
   │  │  Workspace slug: [________________] (auto-fill) │
   │  │                                                 │
   │  │              [Get Started]                      │
   │  └─────────────────────────────────────────────────┘
   │
7. User completes profile → redirected to dashboard
   │  → Dashboard shows: "Create your first machine"
```

### Flow 2: Returning user → dashboard

```
1. User visits yourdomain.com
   │
2. CF Access checks CF_Authorization cookie
   │  → Cookie valid (within session duration, e.g. 7 days)
   │  → Forwards request with CF headers
   │
3. Backend reads CF JWT → looks up user → found
   │
4. Frontend loads dashboard immediately (no login page)
```

### Flow 3: Create & use a machine (web)

```
1. User clicks "New Machine" in dashboard
   │  → Backend creates machine record
   │  → Backend starts VM on host
   │  → Backend creates per-VM tunnel (CF API)
   │  → Backend creates DNS: m-{slug}.yourdomain.com → tunnel
   │  → VM boots, cloudflared connects, sshd starts
   │
2. Dashboard shows machine card:
   │
   │  ┌─ my-agent ──────────────────────────────────────┐
   │  │  Status: ● Running                              │
   │  │  URL: m-my-agent.yourdomain.com           │
   │  │                                                 │
   │  │  [Open Gateway] [Terminal] [SSH Info]            │
   │  └─────────────────────────────────────────────────┘
   │
3. User clicks "Open Gateway"
   │  → Browser navigates to https://m-my-agent.yourdomain.com/
   │  → CF Access cookie already valid (same .yourdomain.com domain)
   │  → NO second auth prompt
   │  → cloudflared tunnel → VM:18789 (gateway UI loads directly)
   │
4. User clicks "Terminal"
   │  → Browser opens https://m-my-agent.yourdomain.com/terminal/
   │  → Same cookie → WebSocket connects via tunnel → VM:7681
   │  → NO proxy chain (1 hop instead of 4)
```

### Flow 4: SSH into machine (power user)

```
1. User clicks "SSH Info" in dashboard
   │
   │  ┌─ SSH Access ───────────────────────────────────────────┐
   │  │                                                         │
   │  │  Option A: Quick connect (one command)                  │
   │  │  ┌────────────────────────────────────────────────────┐ │
   │  │  │ cloudflared access ssh \                           │ │
   │  │  │   --hostname m-my-agent.yourdomain.com       │ │
   │  │  └──────────────────────────────────────────[Copy]────┘ │
   │  │  Opens browser for CF Access auth, then SSH session.    │
   │  │  No password required — CF issues a short-lived cert.   │
   │  │                                                         │
   │  │  Option B: SSH config (persistent)                      │
   │  │  Add to ~/.ssh/config:                                  │
   │  │  ┌────────────────────────────────────────────────────┐ │
   │  │  │ Host my-agent                                      │ │
   │  │  │   HostName m-my-agent.yourdomain.com         │ │
   │  │  │   User openclaw                                    │ │
   │  │  │   ProxyCommand cloudflared access ssh \            │ │
   │  │  │     --hostname %h                                  │ │
   │  │  └──────────────────────────────────────────[Copy]────┘ │
   │  │  Then: ssh my-agent                                     │
   │  │                                                         │
   │  │  ▸ VS Code Remote setup                                 │
   │  │  ▸ Install cloudflared                                  │
   │  └─────────────────────────────────────────────────────────┘
   │
2. User runs: ssh my-agent
   │  → cloudflared opens browser for CF Access (if no active session)
   │  → CF Access authenticates (email OTP / Google / GitHub)
   │  → CF issues short-lived SSH certificate (8 hours)
   │  → cloudflared presents cert to sshd → access granted
   │  → Shell session on VM as 'openclaw' user (no password prompt)
   │
3. Or: code --remote ssh-remote+my-agent /workspace
   │  → VS Code opens with full IDE on the VM
```

### Flow 5: Expose a dashboard

```
1. Inside VM, user starts a server:
   │  $ streamlit run app.py --server.port 8501
   │
2. Server is accessible via tunnel path routing:
   │  https://m-my-agent.yourdomain.com/port/8501/
   │  → CF Access cookie valid → cloudflared → VM:8501
   │  → Dashboard loads in browser (1 hop)
   │
3. Frontend shows active ports (auto-detected or manual):
   │
   │  ┌─ Exposed Ports ────────────────────────────────┐
   │  │  8501  Streamlit  [Open] [Copy URL]             │
   │  │  3000  Dev Server [Open] [Copy URL]             │
   │  └────────────────────────────────────────────────┘
```

### Flow 6: Session expiry & re-auth

```
1. CF Access session expires (configurable: 1 day / 7 days / 30 days)
   │
2. User's next request to any *.yourdomain.com
   │  → CF Access intercepts → login page
   │  → User re-authenticates (email OTP / Google / GitHub)
   │  → New session cookie set
   │  → Request continues — no data loss, no logout from app
   │
3. Note: CF Access remembers the IdP choice per browser
   │  → Returning Google users just click "Continue with Google"
   │  → No email re-entry needed
```

---

## What Changes vs Current System

### Auth: removed (~400 lines)

| Current | After | File |
|---------|-------|------|
| `handleRegister` (email/password signup) | Removed — CF Access auto-signup | `api/server.go` |
| `handleLogin` (email/password login) | Removed — CF Access handles | `api/server.go` |
| `handleLogout` (cookie clear + KV revoke) | Removed — CF Access session mgmt | `api/server.go` |
| `handleAuthMe` (return current user) | Kept — reads CF JWT instead | `api/server.go` |
| Google/GitHub OAuth callbacks | Removed — CF Access handles OAuth | `api/oauth.go` |
| `GenerateToken` / `ValidateToken` | Removed — no custom JWT | `auth/auth.go` |
| `auth.Middleware` (JWT validation) | Replaced by CF JWT middleware | `auth/auth.go` |
| Password hashing (bcrypt) | Removed — no passwords stored | `auth/auth.go` |
| Login/Register pages | Removed — CF Access login screen | `frontend/pages/Login.tsx` |
| KV revocation list | Removed — CF Access manages sessions | `worker/worker.js` |
| Worker JWT validation | Removed — CF Access validates at edge | `worker/jwt.js` |

### Auth: new

| Component | Purpose | File |
|-----------|---------|------|
| CF Access JWT middleware | Validate `Cf-Access-Jwt-Assertion`, extract email | `auth/cfaccess.go` (new) |
| Auto-signup logic | Create user + account on first CF Access visit | `api/server.go` |
| Profile completion page | One-time display name + slug entry | `frontend/pages/Welcome.tsx` (new) |
| CF JWKS caching | Cache CF public keys for JWT validation | `auth/cfaccess.go` (new) |

### Proxy chain: removed (~1500 lines)

| Current | After | File |
|---------|-------|------|
| Backend WS proxy (terminal) | Direct tunnel | `api/machine_terminal.go` |
| Backend WS proxy (gateway) | Direct tunnel | `api/machine_gateway.go` |
| Backend WS proxy (browser) | Direct tunnel | `api/machine_browser.go` |
| Agent HTTP/WS proxy (user traffic) | Direct tunnel | `agentapi/proxy.go` (partial) |
| Worker request proxying | CF Access + tunnel | `worker/worker.js` (simplified) |
| WS ping/pong keepalive (4 locations) | Not needed (1 hop) | various |

### What stays

- **Authorization logic**: "Can user X access machine Y?" (ownership check in backend)
- **User/Account/Machine records**: DB schema mostly unchanged
- **Agent control plane**: VM create/destroy, logs, health (still via backend → agent)
- **Proxy tokens**: Agent still validates proxy_token for control plane requests
- **`useReconnectingWebSocket.ts`**: Browser can still lose connectivity

---

## Domain & Cookie Architecture

```
*.yourdomain.com          ← CF Access application scope
  │
  ├── yourdomain.com       ← Frontend SPA + Backend API
  │     CF Access → CF_Authorization cookie (.yourdomain.com)
  │     Backend reads Cf-Access-Jwt-Assertion header
  │
  ├── {account}.ocm.com          ← (legacy Worker route, removed after migration)
  │
  └── m-{slug}.yourdomain.com   ← Per-VM tunnel
        Same CF_Authorization cookie valid here
        cloudflared tunnel → VM directly
        No Worker, no backend proxy, no agent proxy
```

**Key insight**: CF Access cookie is scoped to `.yourdomain.com` (note leading dot). One authentication covers the main site AND all VM subdomains. No double-auth.

---

## CF Access Configuration

### Application setup (Cloudflare Dashboard → Zero Trust → Access → Applications)

```
Application name: OpenClaw Machines
Type: Self-hosted
Domain: *.yourdomain.com (wildcard)
         yourdomain.com   (apex)
Session duration: 7 days
```

### Access Policy

```
Policy name: Allow authenticated users
Action: Allow
Include: Everyone (any email that completes OTP/OAuth)
```

For future: restrict to specific email domains, require MFA, etc.

### Identity Providers (Zero Trust → Settings → Authentication)

```
1. One-time PIN (built-in, always available)
2. Google OAuth (configure client ID + secret)
3. GitHub OAuth (configure client ID + secret)
```

Users see all three options on the CF Access login page.

---

## Security Model

### Authorization: Short-lived signed tokens (not static allow-lists)

Authorization must be **fresh, not baked-in**. Instead of passing a static
`allowed_emails` list at VM boot, the backend issues short-lived, per-machine
signed tokens on every request/session.

#### Token format (machine access token)

```json
{
  "header": { "alg": "HS256", "typ": "JWT" },
  "payload": {
    "sub": "<user_id>",
    "email": "<user@example.com>",
    "mid": "<machine_id>",
    "aid": "<account_id>",
    "scopes": ["terminal", "gateway", "ports"],
    "iat": 1710000000,
    "exp": 1710000300
  }
}
```

- **Algorithm**: HMAC-SHA256 (`HS256`)
- **TTL**: 5 minutes (`exp = iat + 300`)
- **Signing key**: 256-bit random key, unique per machine, stored in `machines.signing_key` (base64-encoded)
- **Key generation**: `crypto/rand` 32 bytes at machine creation
- **Transport**: `X-Machine-Token` custom request header (never in cookies, never in URL query params for GET requests)
- **Scopes**: `terminal` (WS to ttyd), `gateway` (HTTP/WS to port 18789), `ports` (HTTP to user-started servers)

#### Issuance flow

```
1. Frontend calls GET /api/machines/{id}/token
2. Backend middleware validates CF JWT (see CF JWT Validation below)
3. Backend checks: user is member of machine's account + machine.status == "running"
4. Backend loads machine.signing_key from DB
5. Backend signs JWT with HS256, returns { token, expires_at }
6. Frontend stores token in memory (not localStorage), sets refresh timer at exp - 60s
7. Frontend attaches token as X-Machine-Token header on all requests to m-{slug}.yourdomain.com
```

#### VM auth proxy validation (every request)

```
1. Extract X-Machine-Token header — missing → 403
2. Parse JWT header — alg != HS256 → 403
3. Verify signature with machine's signing key (loaded from metadata at boot) — invalid → 403
4. Check exp — expired → 403
5. Check mid == this machine's ID (from metadata) — mismatch → 403
6. Check requested path against scopes:
   /terminal/* requires "terminal" scope
   /gateway/* or / requires "gateway" scope
   /port/* requires "ports" scope
   — scope missing → 403
7. Validate CF JWT from Cf-Access-Jwt-Assertion header (see below) — invalid → 403
8. Log: { email, machine_id, conn_id, path, method, status, origin, timestamp }
9. Forward request to upstream service
```

**No fallback**: If any validation step fails, the request is rejected. There is
no "permissive mode" or graceful degradation.

#### CF JWT validation (backend middleware + VM auth proxy)

Both the backend and VM auth proxy validate the CF Access JWT on every request:

```
1. Extract Cf-Access-Jwt-Assertion header — missing → 403
2. Fetch JWKS from https://{team}.cloudflareaccess.com/cdn-cgi/access/certs
   — Cache keys for 1 hour. On cache miss, fetch fresh.
   — If fetch fails AND cache is expired → 403 (fail closed, do NOT serve stale keys)
3. Verify JWT signature against JWKS public keys — invalid → 403
4. Check iss == "https://{team}.cloudflareaccess.com" — mismatch → 403
5. Check aud contains the CF Access Application Audience (AUD) tag
   — The AUD tag is a 64-char hex string from CF Access dashboard
   — Backend: loaded from OCM_CF_ACCESS_AUD env var
   — VM auth proxy: loaded from metadata at boot
   — mismatch → 403
6. Check exp — expired → 403
7. Extract sub (stable identity) + email (display)
```

**JWKS cache failure mode**: If the JWKS endpoint is unreachable and the cached keys
have expired (past TTL), ALL requests are rejected. This is intentional — fail closed
rather than trusting stale keys that may have been rotated.

#### Signing key lifecycle

- **Creation**: 32 bytes from `crypto/rand`, base64-encoded, generated at machine creation, stored in `machines.signing_key`
- **Rotation on member removal**: `UPDATE machines SET signing_key = <new_random> WHERE account_id = <affected_account>` — all outstanding tokens for affected machines become instantly invalid
- **Rotation on machine stop**: Part of the kill order (step 2) — prevents token use during teardown window
- **Rotation on machine disable**: Same as stop
- **Never shared**: Each machine has its own key. Compromising one key doesn't affect other machines

**Why this is better than allowed_emails:**
- Instant revocation: rotate signing key → all tokens invalid within seconds
- No stale authorization: tokens expire in 5 minutes, must be refreshed
- Scoped access: tokens encode what the user can do (terminal only, full access, read-only)
- Backend stays the authorization authority — VM just validates signatures

### Threat 1 (High): Per-VM authorization bypass

**Problem**: CF Access proves identity but not ownership. Any authenticated user who
knows a machine slug can reach it — the backend proxy that enforced ownership is gone.

**Mitigation**: VM auth proxy validates signed tokens (see above). No valid token =
no access, regardless of CF Access cookie.

```
Request → CF Access (authn) → cloudflared → VM auth proxy (authz) → service
                                              │
                                              ├── Valid signed token? → check scopes → forward
                                              └── No/expired/invalid token? → 403
```

Additionally, both the backend middleware and VM auth proxy validate the CF JWT
`iss` and `aud` claims on every request (see CF JWT Validation above).
Fail closed: if JWKS keys are stale and refresh fails, reject all requests.

### Threat 2 (High): Cross-VM CSRF via shared cookie

**Problem**: `CF_Authorization` cookie is scoped to `.yourdomain.com`. A malicious
script on one VM can make authenticated requests to another VM using the visitor's cookie.

**Mitigation**: Signed tokens solve this. The malicious page has the visitor's CF cookie
but NOT their signed machine token (tokens are per-machine, passed via custom header or
query param, not cookies). Without a valid token, the VM auth proxy rejects the request.

Additional defense-in-depth:
1. **Origin validation**: VM auth proxy checks `Origin` header on all requests + WebSocket upgrades — must match VM's own hostname
2. **CORS**: VM auth proxy returns strict CORS headers (no cross-origin allowed)
3. **Custom header requirement**: Signed token passed in `X-Machine-Token` header — cross-origin requests cannot set custom headers without CORS preflight (which is rejected)

### Threat 3 (High): Unrestricted signup ("Everyone" policy)

**Problem**: With "Everyone" policy, any email can self-provision an account.

**Mitigation**: For early rollout:
- CF Access policy restricted to **domain allowlist** or **specific invited emails**
- Low session TTL (e.g., 24 hours) during early rollout, increase later
- Backend auto-creates users with `status: pending_approval` — admin must activate
  before machine creation is allowed
- Add invite system: existing users invite by email → pre-creates user record

### Threat 4 (High): Revocation gap

**Problem**: Stopping a machine in the DB doesn't cut the tunnel. Users keep shell
access until cloudflared is killed.

**Mitigation**: Explicit, idempotent kill order:

```
Backend stops machine:
  1. Mark machine as "stopping" in DB (prevents new token issuance)
  2. Rotate machine signing key (invalidates all outstanding tokens)
  3. Delete tunnel via CF API (kill switch — cloudflared disconnects immediately)
  4. Delete DNS CNAME record
  5. Send stop signal to agent → agent kills VM process
  6. Verify tunnel is gone (GET tunnel → 404)
  7. Mark machine as "stopped" in DB
```

**On member removal / account disable (real-time revocation):**
  1. Rotate signing key for all affected machines (instant token invalidation — new requests fail within seconds)
  2. Send "revoke" control message to agent → agent forwards to VM auth proxy via Unix socket
     → VM auth proxy immediately closes all active WebSocket connections for the revoked user
     (close code 4401: "authorization revoked")
  3. Delete + recreate tunnels for affected machines to drop any in-flight TCP connections
     that survive the WebSocket close

**Worst-case revocation latency**: <5 seconds (signing key rotation is immediate,
control message delivery is sub-second over existing agent control plane).
Tokens that were issued before rotation are at most 5 minutes from expiry,
but active connections are killed immediately via the control message.

**Tunnel reaper**: Background goroutine periodically lists all tunnels via CF API,
compares against running machines in DB, deletes orphans. Catches leaks from
crashes or race conditions.

**Snapshot/restore**: Init script fetches fresh tunnel token from metadata on every
boot. Old tokens are stale. Backend always creates a new tunnel for restored VMs.

### Threat 5 (Medium): Identity stability (email vs sub)

**Problem**: Using email as primary key is fragile. Users can change email at their IdP.

**Mitigation**: Store and index `sub` (Subject) claim from CF JWT as stable identifier.
Map CF `sub` to users. Handle email changes explicitly:

```
1. Extract sub + email from CF JWT
2. DB lookup: SELECT * FROM users WHERE cf_sub = ?
3. Found → update email if changed (log the change)
4. Not found → SELECT * FROM users WHERE email = ? AND cf_sub IS NULL
   → Found? Link: UPDATE users SET cf_sub = ? WHERE email = ?
   → Not found? Auto-create with both cf_sub and email
```

Migration: `ALTER TABLE users ADD COLUMN cf_sub TEXT UNIQUE`

### Threat 6 (Medium): SSH static password

**Problem**: Gateway token is a static shared secret.

**Mitigation**: Cloudflare Access **short-lived SSH certificates** (required, not optional).

CF Access issues a short-lived certificate after browser-based authentication.
sshd inside the VM is configured to trust CF's CA public key and validate certs.
No static passwords are used for SSH.

**SSH certificate flow:**

```
1. User runs: ssh my-agent (ProxyCommand: cloudflared access ssh --hostname %h)
2. cloudflared detects no valid cert → opens browser for CF Access login
3. CF Access authenticates user (email OTP / Google / GitHub)
4. CF issues short-lived SSH certificate:
   — Signed by CF's SSH CA key
   — Principal: user's CF email
   — Validity: 8 hours (configurable in CF dashboard)
5. cloudflared presents cert to sshd
6. sshd validates cert against CF CA public key (TrustedUserCAKeys)
7. sshd maps cert principal (email) to local user via AuthorizedPrincipalsCommand
8. Shell session starts — no password prompt
```

**sshd configuration** (inside VM):

```
# /etc/ssh/sshd_config additions
PubkeyAuthentication yes
TrustedUserCAKeys /etc/ssh/cf_ca.pub
AuthorizedPrincipalsCommand /usr/local/bin/cf-ssh-check %i %s
AuthorizedPrincipalsCommandUser nobody
PasswordAuthentication no
```

- `/etc/ssh/cf_ca.pub`: CF SSH CA public key, fetched from CF API at VM boot, stored in rootfs
- `/usr/local/bin/cf-ssh-check`: Script that checks if the cert principal (email) is an authorized user for this machine. Calls backend or reads metadata.
- `PasswordAuthentication no`: Static passwords are disabled entirely

**CF SSH CA key distribution:**
- CF publishes the SSH CA public key at `https://{team}.cloudflareaccess.com/cdn-cgi/access/certs`
- Downloaded once at VM boot and written to `/etc/ssh/cf_ca.pub`
- If CF rotates the CA key, new VMs pick it up automatically; running VMs need a restart or key refresh

**Why no password fallback**: A static password fallback undermines the entire
short-lived cert model. If the password exists, it becomes the attack path.
`PasswordAuthentication no` in sshd_config ensures the only SSH auth path is
through CF-issued certificates.

### Threat 7 (Medium): Audit & observability gap

**Problem**: Removing proxies removes centralized request logs and correlation IDs.

**Mitigation**: Three-layer replacement:

| Layer | What it logs | How |
|-------|-------------|-----|
| CF Access audit log | Auth events: who, when, which app, IdP used | CF dashboard + Logpush to S3/R2 |
| cloudflared tunnel log | Connection events, bytes transferred, errors | CF dashboard + Logpush |
| VM auth proxy | Per-request structured access logs (see format below) | stdout → agent LogManager → SSE |

**VM auth proxy log format** (JSON, one line per event):

```json
{
  "ts": "2026-02-12T22:15:30.123Z",
  "event": "request",
  "email": "user@example.com",
  "machine_id": "m-abc123",
  "conn_id": "c-7f3a9b",
  "method": "GET",
  "path": "/terminal/ws",
  "origin": "https://m-my-agent.yourdomain.com",
  "status": 101,
  "duration_ms": 2,
  "token_exp": "2026-02-12T22:20:00Z"
}
```

**WebSocket close events** (logged when WS connection terminates):

```json
{
  "ts": "2026-02-12T22:45:12.456Z",
  "event": "ws_close",
  "email": "user@example.com",
  "machine_id": "m-abc123",
  "conn_id": "c-7f3a9b",
  "path": "/terminal/ws",
  "close_code": 1000,
  "close_reason": "normal",
  "duration_s": 1782
}
```

**Auth rejection events** (logged on every 403):

```json
{
  "ts": "2026-02-12T22:15:30.789Z",
  "event": "auth_reject",
  "reason": "token_expired",
  "machine_id": "m-abc123",
  "origin": "https://m-other-vm.yourdomain.com",
  "cf_email": "attacker@example.com",
  "path": "/gateway/ws"
}
```

All logs go to stdout, collected by the agent's LogManager, and streamed via
SSE to the control plane. The control plane persists them for audit.

### Threat 8 (Low): WebSocket keepalive

**Problem**: CF edge still enforces idle timeouts on WebSocket connections through tunnels.
Cloudflare's documented idle timeout for WebSocket is 100 seconds. cloudflared tunnels
may have different behavior.

**Mitigation**: Keep ping/pong in ALL layers until proven unnecessary:

- **VM-side**: ttyd `--ping-interval 30`, gateway WS keepalive every 30s
- **Backend-side**: Keep existing ping/pong code during Phase B rollout
- **Frontend**: `useReconnectingWebSocket.ts` stays

**Test gate**: Before removing any keepalive layer, verify in production:
1. Open a terminal WS through the tunnel
2. Leave it idle for **>5 minutes** (not 2-3 minutes — the test must exceed CF's 100s timeout with margin)
3. Send a command — it must work without reconnection
4. Repeat 3 times on different machines

Only after this test passes consistently, remove backend-side keepalives **one layer
at a time** (e.g., remove backend WS proxy keepalive first, observe for 1 week,
then remove the next). Never remove all keepalive layers simultaneously.

### Threat 9 (Low): Slug reuse / DNS cache

**Problem**: Slug collisions or reuse could route to wrong VM.

**Mitigation**:
- Machine slugs include random suffix (e.g., `my-agent-a3f9`)
- On delete: DNS + tunnel deleted immediately
- New machine → fresh tunnel ID → old DNS → 404
- DNS TTL 60s (already `ttl: 1` auto in `CreateDNSRoute`)
- Enforce 5-minute cooldown before slug reuse

---

### Required test cases before removing proxies

These must all pass before Phase B4 (proxy removal):

1. **Cross-account access**: User A accesses `m-user-b-xxx.ocm.com` → **403 Forbidden**
2. **Cross-VM CSRF**: Malicious JS on VM A makes fetch to VM B → **blocked** (no valid token)
3. **Membership removal**: Remove user from account → new requests fail **immediately** (signing key rotated), active WS connections closed **within 5 seconds** (revoke control message)
4. **Machine stop**: Stop machine → tunnel deleted → URL returns error **immediately**
5. **Idle WebSocket**: Terminal idle for **>5 minutes** through tunnel (exceeds CF 100s timeout) → **stays connected**, repeated 3x on different machines
6. **Wrong audience JWT**: CF JWT with different `aud` claim → **rejected by VM auth proxy**
7. **Expired token**: Signed machine token past `exp` → **403, frontend auto-refreshes**
8. **Token from wrong machine**: Token for machine A presented to machine B → **403**
9. **SSH password disabled**: `ssh -o PreferredAuthentications=password user@m-{slug}.yourdomain.com` → **rejected** (only cert auth works)
10. **Revoke control message**: Remove user from account → active WS connections closed with code **4401** within **5 seconds**

---

## Failure & Recovery Paths

### CF Access Outage

Fail-closed by design. The JWKS cache has a configurable TTL (default 1 hour); when the cache expires and the CF Access JWKS endpoint is unreachable, all requests are rejected. There is no "break glass" backdoor, and this is intentional -- introducing a bypass would create an attack path that undermines the entire security model.

### CLI Lockout

The CLI token is stored locally and remains valid until the CF Access session expires. Re-login requires a browser-based authentication flow through CF Access. If browser auth fails (e.g., IdP is down or account is locked), the user must resolve the issue directly with their identity provider (Google account recovery, GitHub support, etc.).

### Rate Limiting

CF Access handles rate limiting at the edge, including brute-force protection on the login flow. The backend does not implement additional login rate limits since authentication is fully delegated to CF Access.

### Non-CF Deployments

The system is tightly coupled to Cloudflare Access for authentication and tunnel-based connectivity. A non-CF deployment would require building a JWT-issuer adapter that produces tokens with compatible `iss`, `aud`, and JWKS semantics. This is a known architectural constraint, not a bug -- the simplicity gains from CF coupling are a deliberate trade-off.

### Account Recovery

Account recovery is delegated entirely to the CF Access identity provider (Google, GitHub, or email OTP). OCM has no password reset flow, no recovery codes, and no admin-initiated password resets, because OCM stores no passwords.

---

## Implementation Phases

### Phase A: Unified Auth Migration

Independent of tunnel work. Can ship first.

**Step A1: CF Access setup** (Cloudflare Dashboard, no code)
- Create Zero Trust team
- Add Access application for `*.yourdomain.com` + `yourdomain.com`
- Configure email OTP + Google + GitHub IdPs
- Set session duration (7 days)

**Step A2: Backend — CF JWT middleware** (`backend/internal/auth/cfaccess.go`, new)
- Fetch CF JWKS from `https://{team}.cloudflareaccess.com/cdn-cgi/access/certs`
- Cache keys with TTL (e.g., 1 hour)
- Validate `Cf-Access-Jwt-Assertion` header on every request
- Extract `sub` + `email` from JWT
- User lookup by `cf_sub` first, then by `email` (for migration), then auto-create
- Auto-created users get `status: pending_approval` (or restrict CF Access policy to invited emails)
- Set user in request context

**Step A3: Backend — Remove old auth** (`backend/internal/auth/auth.go`, `api/server.go`)
- Remove `/api/auth/register`, `/api/auth/login`, `/api/auth/logout`
- Remove `GenerateToken`, `ValidateToken`, `CheckPassword`
- Remove Google/GitHub OAuth callback handlers
- Keep `/api/auth/me` → now reads from CF JWT context
- Remove `password_hash` from User model
- Migration: `DROP COLUMN password_hash`, `ADD COLUMN cf_sub TEXT UNIQUE`, `ADD COLUMN status TEXT DEFAULT 'active'`

**Step A4: Worker — Simplify** (`worker/worker.js`, `worker/jwt.js`)
- Remove custom JWT validation (`jwt.js` → delete)
- CF Access already validated at edge before request reaches Worker
- Worker reads `Cf-Access-Authenticated-User-Email` header for routing
- Keep: KV route lookup, proxy_token forwarding to agent

**Step A5: Frontend — Remove login pages** (`frontend/`)
- Delete `Login.tsx`, `Register.tsx` (if exists)
- Update `AuthProvider` context:
  - Remove `authLogin()`, `authRegister()`
  - `authMe()` now always succeeds (CF Access already authenticated)
  - If 401 from backend → redirect to CF Access login (rare, session expired mid-request)
- Add `Welcome.tsx` for first-time profile completion
- Update `App.tsx` routing — no `/login` route needed

### Phase B: SSH + Per-VM Tunnels

Requires Phase A (CF Access) to be in place.

**Step B1: Rootfs — sshd + cloudflared + auth proxy**
- `rootfs/Dockerfile.openclaw`: Add `openssh-server`, download `cloudflared` binary, add auth proxy binary
- Auth proxy enforces (see Security Model — VM auth proxy validation):
  - Validates signed machine token in `X-Machine-Token` header (HS256 signature + expiry + machine_id + scopes)
  - Validates CF JWT `iss` and `aud` claims against expected CF Access app (fail closed on stale JWKS)
  - Origin header must match VM's own hostname (anti-CSRF, blocks cross-VM WS hijacking)
  - Logs every request as structured JSON: email, machine_id, conn_id, method, path, status, origin, duration_ms, token_exp
  - Logs every WS close: conn_id, close_code, close_reason, duration_s
  - Logs every auth rejection: reason, origin, cf_email, path
  - Listens on Unix socket for "revoke" control messages from agent → closes WS connections for revoked users
- `scripts/init-openclaw.sh`:
  - Fetch CF SSH CA public key → write to `/etc/ssh/cf_ca.pub`
  - Configure sshd with `TrustedUserCAKeys`, `AuthorizedPrincipalsCommand`, `PasswordAuthentication no`
  - Configure auth proxy with machine_id + signing key + CF Access AUD tag + VM hostname from metadata
  - Start cloudflared with tunnel token from metadata (fresh token every boot, never cached)
  - Supervise sshd + cloudflared + auth proxy in PID 1 loop

**Step B2: Backend — Token issuance + tunnel lifecycle + revocation**
- `auth/tokens.go` (new): `IssueMachineToken(machineID, userID, email, scopes, signingKey) → (tokenString, expiresAt, error)`
  - Creates JWT: `{ sub: userID, email, mid: machineID, aid: accountID, scopes, iat, exp: iat+300 }`
  - Signs with HMAC-SHA256 using the machine's signing key
  - Returns token string + expiry timestamp
- `api/server.go` — New endpoint: `GET /api/machines/{id}/token`
  - Validates CF JWT (middleware)
  - Checks: user is member of machine's account, machine.status == "running"
  - Checks: machine.status != "stopping" (no tokens issued during teardown)
  - Calls `IssueMachineToken()`, returns `{ token: "...", expires_at: "2026-..." }`
- `api/server.go` — VM start:
  - Generate 32-byte signing key (`crypto/rand`), store base64-encoded in `machines.signing_key`
  - Create tunnel + DNS via CF API
  - Fetch CF SSH CA public key from CF Access certs endpoint
  - Pass via metadata: tunnel_token, signing_key, machine_id, vm_hostname, cf_access_aud, cf_ca_pubkey
- `api/server.go` — VM stop (idempotent kill order):
  1. `UPDATE machines SET status = 'stopping' WHERE id = ?` (prevents new token issuance)
  2. `UPDATE machines SET signing_key = <new_random> WHERE id = ?` (invalidates outstanding tokens)
  3. Send "revoke-all" control message to agent → agent forwards to VM auth proxy → proxy closes all active WS connections (close code 4401)
  4. Delete tunnel via CF API (kill switch — cloudflared disconnects)
  5. Delete DNS CNAME record
  6. Send stop signal to agent → agent kills VM process
  7. Verify tunnel gone (`GET /tunnels/{id}` → 404)
  8. `UPDATE machines SET status = 'stopped', signing_key = NULL WHERE id = ?`
- `api/server.go` — On member removal:
  1. Rotate signing keys for all affected machines (`UPDATE machines SET signing_key = <new_random> WHERE account_id = ?`)
  2. Send "revoke-user" control message to agent for each machine → VM auth proxy closes WS connections for the specific user
  3. Delete + recreate tunnels for affected machines to drop in-flight TCP connections
- `api/server.go` — Tunnel reaper (background goroutine, runs every 5 minutes):
  - List all tunnels via CF API
  - Compare against `machines WHERE status IN ('running', 'starting')` in DB
  - Delete orphaned tunnels (tunnel exists but no matching running machine)
  - Log all reaper actions
- `store.go`: Add `TunnelID`, `TunnelHostname`, `SigningKey` to Machine struct
- Migration: `ALTER TABLE machines ADD COLUMN tunnel_id TEXT, tunnel_hostname TEXT, signing_key TEXT`
- `metadata.go`: Pass tunnel_token + signing_key + machine_id + vm_hostname + cf_access_aud + cf_ca_pubkey to VM config

**Step B3: Frontend — Direct tunnel URLs**
- Update `MachineWorkspace.tsx`: links point to `https://m-{slug}.yourdomain.com/`
- Add SSH Access panel with connection instructions
- Add exposed ports panel
- Remove WebSocket proxy URL construction (connect directly to tunnel)

**Step B4: Cleanup — Remove proxy chain** (only after all 10 test cases pass)
- **Gate**: All test cases in Security Model must pass (cross-account 403, CSRF blocked,
  revocation <5s, idle WS >5 min, wrong aud rejected, expired token 403, SSH password rejected, etc.)
- Delete `api/machine_terminal.go`, `api/machine_gateway.go`, `api/machine_browser.go`
- Remove user-traffic proxy handlers from `agentapi/proxy.go` (keep control plane)
- Remove backend-side WS ping/pong code one layer at a time — monitor for regressions
- Simplify Worker (no more request proxying for user traffic)

---

## File Change Summary

### Phase A (Auth Migration)

| File | Change | Deploy |
|------|--------|--------|
| `backend/internal/auth/cfaccess.go` | **New**: CF JWT validation + JWKS cache | Backend |
| `backend/internal/auth/auth.go` | Remove JWT gen/validate, password check | Backend |
| `backend/internal/api/server.go` | Remove login/register/logout, add auto-signup | Backend |
| `backend/internal/api/oauth.go` | Delete file | Backend |
| `backend/internal/store/store.go` | Remove PasswordHash from User | Backend |
| `backend/internal/store/postgres.go` | Update user queries (no password) | Backend |
| `worker/worker.js` | Remove JWT validation, read CF headers | Worker |
| `worker/jwt.js` | Delete file | Worker |
| `frontend/src/pages/Login.tsx` | Delete file | Frontend |
| `frontend/src/pages/Welcome.tsx` | **New**: Profile completion | Frontend |
| `frontend/src/lib/auth.tsx` | Remove login/register, simplify to CF JWT | Frontend |
| `frontend/src/App.tsx` | Remove /login route, add /welcome | Frontend |
| DB migration | Drop password_hash, add cf_sub + status | Backend |

### Phase B (SSH + Tunnels)

| File | Change | Deploy |
|------|--------|--------|
| `rootfs/Dockerfile.openclaw` | Add openssh-server, cloudflared, auth proxy, cf-ssh-check | Full snapshot |
| `rootfs/cf-ssh-check` | **New**: AuthorizedPrincipalsCommand for SSH cert validation | Full snapshot |
| `scripts/init-openclaw.sh` | Configure sshd (CF certs, no passwords), start cloudflared + auth proxy | Full snapshot |
| `backend/internal/auth/tokens.go` | **New**: Per-machine signed token issuance | Backend |
| `backend/internal/tunnel/tunnel.go` | Per-VM tunnel create/delete | Backend |
| `backend/internal/api/server.go` | Token endpoint, tunnel lifecycle, revocation, reaper | Backend |
| `backend/internal/store/store.go` | TunnelID/TunnelHostname/SigningKey on Machine | Backend |
| `backend/internal/store/postgres.go` | Machine tunnel + signing key columns | Backend |
| `backend/internal/metadata/metadata.go` | Pass tunnel token + signing key to VM | Backend |
| `frontend/src/pages/MachineWorkspace.tsx` | SSH panel, direct tunnel URLs | Frontend |
| `backend/internal/api/machine_terminal.go` | Delete (proxy no longer needed) | Backend |
| `backend/internal/api/machine_gateway.go` | Delete (proxy no longer needed) | Backend |
| `backend/internal/api/machine_browser.go` | Delete (proxy no longer needed) | Backend |
| `backend/internal/agentapi/proxy.go` | Remove user-traffic handlers | Snapshot |

---

## Keepalive Impact

| Phase | Impact |
|-------|--------|
| Phase A (auth) | No change to keepalive code |
| Phase B (tunnels) | **Keep all keepalives during rollout.** Remove backend-side ping/pong only after passing the idle WS test gate (>5 min idle through tunnel, 3 repetitions). Remove one layer at a time with 1-week observation between each removal. Keep SSE keepalives (control plane) + frontend reconnect permanently. |

---

## Verification

### Phase A
1. Visit `yourdomain.com` → CF Access login page appears
2. Authenticate via email OTP → dashboard loads
3. Check DB: user auto-created with correct email
4. New user sees Welcome page → completes profile → dashboard
5. Returning user: dashboard loads immediately (cookie valid)
6. Session expiry → re-auth → seamless continuation

### Phase B (functional)
1. Create machine → tunnel created → DNS record appears
2. Visit `m-{slug}.yourdomain.com` with valid machine token → gateway UI loads
3. SSH: `cloudflared access ssh --hostname m-{slug}.yourdomain.com` → shell
4. Start server on port 8501 → accessible via tunnel with valid token
5. Control plane (logs, progress) still works via backend → agent
6. Audit: VM auth proxy logs appear in log console with email, conn_id, path

### Phase B (security — all 10 must pass before proxy removal)
1. **Cross-account**: User A accesses User B's VM → **403** (no valid token)
2. **Cross-VM CSRF**: Malicious JS on VM A fetches VM B → **blocked** (no token in header)
3. **Membership removal**: Remove user → access invalidated **within 5 seconds** (signing key rotation + revoke control message)
4. **Machine stop**: Stop machine → tunnel deleted **immediately** → URL errors
5. **Idle WebSocket**: Terminal idle **>5 minutes** through tunnel → stays connected (3 repetitions)
6. **Wrong audience JWT**: CF JWT with different `aud` → **rejected**
7. **Expired token**: Machine token past `exp` → **403**, frontend auto-refreshes
8. **Wrong machine token**: Token for machine A presented to machine B → **403**
9. **SSH password disabled**: Password-only SSH auth attempt → **rejected**
10. **Revoke control message**: Remove user from account → active WS connections closed with code **4401** within **5 seconds**

---

## See Also

- [architecture.md](architecture.md) — System architecture overview
- [routing.md](routing.md) — Data-plane auth enforcement
- [tunnel-architecture.md](tunnel-architecture.md) — Tunnel authentication
