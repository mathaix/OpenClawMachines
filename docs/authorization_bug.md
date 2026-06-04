# Authorization Bug: CF Access JWT Not Reaching Backend

**Date:** 2026-03-10
**Severity:** P0 — new user login broken; existing sessions degrade within 24h

---

## TL;DR

Three interacting issues prevented CF Access JWTs from reaching the backend:

1. **CF Access no longer sets `CF_Authorization` cookie** — uses `CF_AppSession` (opaque) instead
2. **`/api/*` wasn't in CF Access protected paths** — JWT header never added to API XHR
3. **Cloudflare strips `Cf-Access-Jwt-Assertion` from outbound Worker `fetch()`** — even when present, the header is removed before reaching the backend

**Fix:** Worker reads the JWT from the inbound CF Access header and forwards it via `X-Cf-Access-Jwt` (custom header CF won't strip). Backend reads this header as a JWT source.

---

## Current Status

| Component | State | Details |
|-----------|-------|---------|
| CF Access paths | **Applied** | `/dashboard`, `/api/auth`, `/cli-auth` configured in CF dashboard |
| Worker `X-Cf-Access-Jwt` forwarding | **In working tree** | `worker/worker.js` — not committed or deployed |
| Backend `X-Cf-Access-Jwt` header check | **In working tree** | `backend/internal/auth/auth.go` — not committed or deployed |
| Worker debug endpoints | **Deployed** | `/__auth-debug`, `/api/auth/__debug` — remove after fix verified |
| Backend `X-Cf-Access-Jwt` tests | **Written** | `auth_test.go` — 3 new tests |
| Worker CF JWT inbound tests | **Written** | `cf-jwt-forwarding.test.js` — 3 tests |
| CLI auth (`CliAuth.tsx`) | **Broken** | Reads `CF_Authorization` cookie that no longer exists |
| ProtectedRoute deep-link gap | **Known issue** | `/welcome`, `/workspace/:id` not CF-protected at edge |

---

## Root Cause

### Issue 1: `CF_Authorization` cookie gone

CF Access now uses `CF_AppSession` (opaque, HttpOnly) instead of `CF_Authorization` (JWT, readable).

**Evidence:** Worker debug endpoint:
```json
{ "cookie_names": ["CF_AppSession", "ocm_token"], "has_cf_authorization_cookie": false }
```

**Code depending on `CF_Authorization` (all broken):**

| File | Line | What it does | Status |
|------|------|-------------|--------|
| `CliAuth.tsx` | 22 | Reads cookie to pass JWT to CLI | **Broken — blocks CLI login** |
| `api.ts` | 9 | `getCfAccessJwt()` reads cookie | Dead in current same-origin build — see note below |
| `api.ts` | 19-24 | Sends cookie as header | Dead in current same-origin build — see note below |
| `auth.tsx` | 44 | Diagnostic log | Always false — safe to remove |
| `auth.tsx` | 72 | Clears cookie on logout | No-op — safe to remove |

**Note on `api.ts` removal:** The `getCfAccessJwt()` shim is dead in the current same-origin deployment (frontend served via `/api` proxy, no cross-origin). However, `api.ts:4` supports `VITE_API_URL` overrides for cross-origin builds, and `docs/research/deployment-lifecycle.md` still documents this path. Scoping: safe to remove for current deploy, but if cross-origin builds are revived, a replacement header-forwarding mechanism would be needed.

### Issue 2: `/api/*` not in CF Access protected paths

CF Access adds `Cf-Access-Jwt-Assertion` ONLY to requests matching protected path rules. `/api/*` was absent, so frontend XHR to `/api/auth/me` never got the header.

**Fix applied:** Added `/api/auth` to CF Access protected paths.

### Issue 3: CF strips `Cf-Access-Jwt-Assertion` from Worker `fetch()`

Cloudflare strips this header from outbound Worker `fetch()` calls (security measure against header spoofing). Even with Issue 2 fixed, the JWT never reaches the backend.

**Evidence:**
- Worker: `cf_jwt_header_present: true, cf_jwt_header_len: 958`
- Backend: `has_jwt: false, jwt_len: 0`

---

## Fix (in working tree, not deployed)

### Worker: forward JWT via custom header

**File:** `worker/worker.js`

```javascript
const cfJwtHeader = request.headers.get("Cf-Access-Jwt-Assertion") || "";

if (cfJwtHeader && isFrontendApi) {
  modifiedHeaders.set("X-Cf-Access-Jwt", cfJwtHeader);
}

// Fallback: CF_Authorization cookie (if present from cookie mirroring)
if (!cfJwtHeader && isFrontendApi) {
  const cookieHeader = request.headers.get("Cookie") || "";
  const match = cookieHeader.match(/CF_Authorization=([^;]+)/);
  if (match) {
    modifiedHeaders.set("X-Cf-Access-Jwt", match[1]);
  }
}
```

### Backend: read custom header

**File:** `backend/internal/auth/auth.go`

```go
cfJWT := r.Header.Get("Cf-Access-Jwt-Assertion")
jwtSource := "header"
if cfJWT == "" {
    cfJWT = r.Header.Get("X-Cf-Access-Jwt")
    jwtSource = "x-header"
}
if cfJWT == "" {
    if cookie, err := r.Cookie("CF_Authorization"); err == nil {
        cfJWT = cookie.Value
        jwtSource = "cookie"
    }
}
```

### Request flow after fix

```
Browser → CF Access (adds Cf-Access-Jwt-Assertion) → Worker
  Worker reads header, copies to X-Cf-Access-Jwt
  Worker fetch() to Cloud Run (CF strips original, X-Cf-Access-Jwt survives)
  Backend DualModeMiddleware reads X-Cf-Access-Jwt → validates → 200
```

---

## Test Gap (Deployment Blocker)

The `X-Cf-Access-Jwt` header path is the **only path that will work in production**. Without tests, a middleware refactor can silently break auth for all users.

**Existing tests** (`backend/internal/auth/auth_test.go`):
- `TestDualMode_CfJWTValid` — `Cf-Access-Jwt-Assertion` header
- `TestDualMode_CfJWTInvalid_NoFallback` — invalid CF JWT
- `TestDualMode_NoCfJWT_ValidBearer` — legacy Bearer token
- `TestDualMode_NoCfJWT_ValidCookie` — legacy `ocm_token` cookie
- `TestDualMode_NoCfJWT_NoToken` — no auth

**Required before deploy:**
```go
func TestDualMode_XCfAccessJwt(t *testing.T)            // valid JWT via X-Cf-Access-Jwt → 200
func TestDualMode_XCfAccessJwt_Invalid(t *testing.T)     // invalid JWT → 401
func TestDualMode_XCfAccessJwt_Precedence(t *testing.T)  // Cf-Access-Jwt-Assertion wins over X-Cf-Access-Jwt
```

**Worker tests (also required before deploy):**
- CF JWT inbound handling (`/__auth-debug` path)
- (Future) End-to-end forwarding if `HOST_MAP` becomes overridable in tests

---

## CF Access Protected Paths

### Current configuration

| Domain | Path | Why required |
|--------|------|-------------|
| `openclawmachines.com` | `/dashboard` | Triggers CF Access login on page load. XHR can't handle the 302 redirect. |
| `openclawmachines.com` | `/api/auth` | Adds JWT header to `/api/auth/me` XHR calls. |
| `openclawmachines.com` | `/cli-auth` | CLI login bridge — browser passes JWT to CLI local server. |

### ProtectedRoute vs CF Access alignment

`App.tsx` wraps these routes in `ProtectedRoute` (which calls `/api/auth/me`):

| App route | CF Access protected? | Deep-link safe? |
|-----------|:-------------------:|:---------------:|
| `/dashboard/*` | Yes | Yes |
| `/cli-auth` | Yes | Yes |
| `/welcome` | **No** | **No** — XHR 302 → CORS failure → reload loop |
| `/workspace/:id` | **No** | **No** — same failure mode |
| `/workspace/:id/gateway` | **No** | **No** — same failure mode |

**Impact:** Users who bookmark or share `/workspace/:id` URLs will hit a reload loop if they don't have an active CF Access session.

**Recommended fix:** `ProtectedRoute` detects XHR 302/CORS failure and redirects to `/dashboard` (which IS CF-protected and triggers login). More robust than trying to keep CF Access path config in sync with every app route.

**Caveat:** The redirect must exclude `/cli-auth?port=...` — that path is already CF Access protected and serves as the CLI login bridge. Redirecting it to `/dashboard` would break `ocm login`.

### Paths that must NOT be CF Access protected

- `/api/waitlist`, `/api/telemetry` — public landing page (no session)
- `/api/agent/*` — agent-to-backend (CF service tokens)
- `/api/internal/*` — Worker-to-backend (CF service tokens)

---

## Why Other Endpoints Still Work (for now)

`DualModeMiddleware` tries three sources:

1. `Cf-Access-Jwt-Assertion` header → **empty** (stripped by CF)
2. `CF_Authorization` cookie → **absent** (no longer set)
3. `ocm_token` cookie → **present** (legacy HS256 JWT, 24h TTL)

The `ocm_token` fallback authenticates everything except `/api/auth/me`, which requires `claims.CfSub != ""` (`server.go:972`). Legacy tokens don't carry `CfSub`.

**Result:** Existing users with `ocm_token` can browse the dashboard but can't refresh auth. New users can't log in at all. All sessions break within 24h.

---

## CLI Auth Breakage (Separate Issue)

`CliAuth.tsx:22` reads `CF_Authorization` from `document.cookie`:
```typescript
const match = document.cookie.match(/(?:^|;\s*)CF_Authorization=([^;]+)/);
```

This cookie no longer exists. CLI login (`ocm login`) is broken independently.

**CLI contract constraint:** The CLI opens a browser to `/cli-auth?port=N`, then listens on `localhost:N/callback?token=...` for a transferable token string. It validates this token as `Authorization: Bearer ...` against `/api/auth/me` (`login.go:69-70,141-144`). Any fix must deliver a Bearer-compatible token to the localhost callback — an HttpOnly cookie does not satisfy this contract.

**Fix options:**
1. **Cookie mirroring (recommended)** — Worker mirrors `Cf-Access-Jwt-Assertion` into a readable `CF_Authorization` cookie on `/cli-auth` page loads (CF Access protected path). `CliAuth.tsx` reads it and passes to CLI callback. Cookie mirroring is NOT currently implemented. A previous commit added infrastructure (`extraSetCookie`) but the variable was never assigned — it was dead code (now removed). If this option is chosen, the mirroring logic must be written from scratch. Smallest change, preserves CLI contract.
2. **Fetch JWT from Worker** — Add `/api/auth/cli-token` endpoint at Worker that reads the CF Access header and returns the JWT as JSON. `CliAuth.tsx` fetches this and passes to CLI callback. Slightly more code but avoids cookie dependency entirely.
3. ~~**Identity endpoint**~~ — `CliAuth.tsx` calls `/cdn-cgi/access/get-identity`, then exchanges for `ocm_token` via `/api/auth/me`. **Not viable:** `handleAuthMe` returns user data, not a raw token. The CLI needs a transferable Bearer token for `login.go:141`, not an HttpOnly session cookie. This option would require changing the CLI auth contract.

**Dependency:** Whichever option is chosen, do NOT remove `CF_Authorization` references from `CliAuth.tsx` until the replacement is implemented and tested.

---

## `ocm_token` Role and Removal Path

### 1. Control-plane API fallback (cleanup debt)

`DualModeMiddleware` (`auth.go:56–71`) falls back to `ocm_token` when CF Access JWT is absent. Once `X-Cf-Access-Jwt` forwarding is deployed, this fallback masks failures instead of surfacing them.

**Can remove:** Delete legacy fallback from `DualModeMiddleware` after fix is deployed and verified.

### 2. Data-plane browser bridge (architectural dependency)

Worker validates `ocm_token` for subdomain routing (`worker.js:502`, `jwt.js:80`). Authenticates `{account}.openclawmachines.com/{machine}/*` before forwarding to the agent.

**Cannot remove yet.** Requires replacement:
- Per-VM tunnels (Phase B of `unified-auth-rearchitecture.md`) eliminate subdomain routing entirely
- Or: Worker validates CF Access JWT for subdomain requests (JWKS in Workers)
- Or: dedicated short-lived edge session token

### Machine tokens are separate

`auth/tokens.go:20` (`IssueMachineToken`) creates short-lived per-VM HS256 JWTs for the in-VM auth proxy. These are not `ocm_token` and should stay.

### Removal order

1. **Now:** Deploy `X-Cf-Access-Jwt` forwarding (unblocks login)
2. **Soon:** Remove `ocm_token` fallback from `DualModeMiddleware` (single auth path)
3. **Phase B:** Per-VM tunnels → remove Worker subdomain routing → delete `jwt.js` → stop issuing `ocm_token`

---

## Remaining Cleanup

### Remove from Worker (after fix verified)
- `/__auth-debug` and `/api/auth/__debug` debug endpoints
- Cookie mirroring `Set-Cookie: CF_Authorization` — **keep only if used for CLI auth fix (Option 1)**

### Remove from frontend (dead code)
- `api.ts:8-12` — `getCfAccessJwt()` (reads nonexistent cookie)
- `api.ts:19-24` — header injection using `getCfAccessJwt()` (dead)
- `auth.tsx:44` — `CF_Authorization` diagnostic log (always false)
- `auth.tsx:72` — `CF_Authorization` clearing in logout (no-op)

**Do NOT remove until CLI fix is in place:**
- `CliAuth.tsx:22-23` — `CF_Authorization` cookie read (CLI login bridge)

### ProtectedRoute deep-link fix
- Detect 302/CORS failure in `ProtectedRoute` and redirect to `/dashboard`

### Update docs
- `docs/auth-flow.md:42` — `CF_Authorization` → `CF_AppSession` behavior
- `docs/auth-flow.md:129` — CLI flow diagram
- `docs/routing.md:92` — add `X-Cf-Access-Jwt` path
- `docs/unified-auth-rearchitecture.md:240` — cookie architecture section

---

## Architectural Assessment

The root cause is structural: **auth spans CF Access → Worker → backend → legacy fallback, and each hop makes assumptions about how Cloudflare exposes identity.**

| Assumption | Where encoded | Still true? |
|-----------|---------------|:-----------:|
| CF Access sets `CF_Authorization` JWT cookie | `CliAuth.tsx:22`, `api.ts:9`, `auth-flow.md:42` | **No** |
| `Cf-Access-Jwt-Assertion` passes through Worker fetch | `auth-flow.md:43`, `auth.go:28` | **No** |
| CF Access protects all authenticated paths | `unified-auth-rearchitecture.md:690` | **Partially** — path-specific, not wildcard |
| App routes match CF Access paths | `App.tsx:93-125` vs CF dashboard | **Fragile** — manual sync, silent drift |
| `ocm_token` is temporary migration scaffolding | `auth.go:56` | **No** — still load-bearing for data plane |

The `X-Cf-Access-Jwt` fix is a transport-layer patch. The cleaner target architecture (`unified-auth-rearchitecture.md`) eliminates most brittleness via:
1. Wildcard CF Access coverage (no path-matching fragility)
2. Single auth mechanism (no dual-mode fallback masking)
3. Per-VM tunnels (no Worker in data-plane auth path)
4. Machine tokens for VM access (no `ocm_token` bridge)

But that's a multi-phase migration. The `X-Cf-Access-Jwt` fix is the correct immediate action.

---

## Verification

### Before deploy (blocking)
- Write `X-Cf-Access-Jwt` middleware tests (see [Test gap](#test-gap-deployment-blocker))
- Tests pass: `make test-go`

### After deploy (manual)
1. Clear all cookies for `openclawmachines.com`
2. Visit `openclawmachines.com/dashboard` — CF Access login page appears
3. Authenticate → redirect back to `/dashboard` → dashboard loads with user data
4. Backend logs: `jwt_source: "x-header"`, `has_jwt: true`
5. Deep-link: navigate to `/workspace/{id}` — verify behavior (reload loop expected until ProtectedRoute fix)
6. CLI: `ocm login` — expected to fail until CLI auth fix is deployed

---

## References

| File | Relevance |
|------|-----------|
| `backend/internal/auth/auth.go:24` | `DualModeMiddleware` — JWT source checking |
| `backend/internal/auth/auth_test.go:50` | Existing middleware tests (missing `X-Cf-Access-Jwt`) |
| `backend/internal/auth/tokens.go:20` | Machine tokens (separate, keep) |
| `backend/internal/api/server.go:972` | `handleAuthMe` — requires `CfSub` |
| `backend/internal/api/server.go:1022` | `ocm_token` issuance after CF Access auth |
| `worker/worker.js` | `X-Cf-Access-Jwt` forwarding + debug endpoints |
| `worker/jwt.js:80` | `verifyRequestJWT` — data-plane HS256 validation |
| `frontend/src/pages/CliAuth.tsx:22` | CLI bridge reads `CF_Authorization` cookie (broken) |
| `frontend/src/lib/api.ts:9` | `getCfAccessJwt()` — dead code |
| `frontend/src/App.tsx:93-125` | `ProtectedRoute` wrapping vs CF Access paths |
| `docs/auth-flow.md` | Intended flow (stale on cookie behavior) |
| `docs/unified-auth-rearchitecture.md` | Target architecture |
| `docs/routing.md:89` | Data-plane auth via `ocm_token` |
