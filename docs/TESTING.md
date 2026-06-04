# Testing

This document describes every test suite in the project, what each test verifies, how to run it, and where it can run.

## What to Run When

Pick the row that matches what you changed. Run **everything listed** — earlier columns are fast sanity checks, later columns catch integration issues.

| What you changed | Fast feedback | Before pushing | Before deploying |
|-----------------|--------------|----------------|-----------------|
| **Frontend** (`frontend/src/`) | `make test-frontend` | `make typecheck` | `make test-e2e` |
| **Backend API** (`backend/internal/api`, `store`, etc.) | `make test-go` | `make check` | `make test-e2e` |
| **Agent** (`backend/cmd/agent`, `internal/agentapi`, `selfupdate`) | `make test-go` | `make check` | `make smoke-test` (KVM) |
| **Auth proxy** (`backend/cmd/authproxy`) | `make test-go` | `make check` | `make smoke-test` (KVM) |
| **Worker** (`worker/`) | `make test-worker` | — | `make test-e2e` |
| **Init scripts** (`scripts/init-*.sh`, `scripts/ocm-*`) | — | `make shellcheck` | `make smoke-test` (KVM) |
| **Rootfs** (`rootfs/`, Dockerfile) | — | — | `make build-rootfs && make smoke-test` (KVM) |
| **OpenClaw fork** (TypeScript gateway) | — | — | `make build-openclaw-fork && make smoke-test` (KVM) |
| **Config assembly** (`internal/configassembly`) | `make test-go` | `make check` | `make smoke-test` (KVM) |
| **Anything before `make deploy-all`** | `make test` | `make check` | `make test-e2e` (after deploy) |
| **Anything before `make build-upload-rootfs`** | — | — | automatic (`smoke-test` is a dependency) |

### Common Workflows

**Daily development (laptop):**
```bash
make test          # 30s — Go + frontend + worker unit tests
make typecheck     # 5s — catch TypeScript errors
```

**Before pushing a PR:**
```bash
make check         # lint + vet + vuln-check + shellcheck
make test          # all unit tests
```

**After deploying to production:**
```bash
make test-e2e      # 10s — curl smoke tests against prod
```

**Building a new rootfs (KVM host):**
```bash
make build-rootfs                  # build the image
make smoke-test                    # boot 1 VM, verify gateway (~10min)
make build-upload-rootfs           # smoke-test runs automatically as gate
```

**Testing openclaw TypeScript changes (KVM host):**
```bash
make build-openclaw-fork           # pack tarball to rootfs/openclaw-fork.tgz
make smoke-test                    # rootfs auto-patches with tarball, boots VM
```

**Full integration suite (KVM host, ~35min):**
```bash
make test-integration              # all Firecracker tests
```

**Debugging a single integration test (KVM host):**
```bash
make test-integration-run TEST=TestGatewaySuite     # run one test suite
make test-integration-run TEST=TestInit_MetadataFetch  # run one specific test
```

### Where Can I Run What?

| Environment | Available tests |
|------------|----------------|
| **Any laptop** (macOS/Linux, no KVM) | `make test`, `make typecheck`, `make check`, `make test-e2e`, `make test-playwright` |
| **KVM host with root** (GCE VM) | All of the above + `make smoke-test`, `make test-integration`, `make test-integration-e2e` |

---

## Quick Reference

| Command | What it runs | Where it runs | Time |
|---------|-------------|---------------|------|
| `make test` | Go + frontend + worker unit tests | Anywhere | ~30s |
| `make test-go` | Go unit tests | Anywhere | ~10s |
| `make test-frontend` | Frontend Vitest | Anywhere (Node 18+) | ~5s |
| `make test-worker` | Worker Vitest | Anywhere (Node 18+) | ~5s |
| `make typecheck` | TypeScript type checking | Anywhere (Node 18+) | ~5s |
| `make check` | lint + vet + vuln-check + shellcheck | Anywhere | ~1min |
| `make test-e2e` | Production smoke tests (curl) | Anywhere (needs internet) | ~10s |
| `make test-playwright` | Playwright browser E2E | Anywhere (needs running app) | ~2min |
| `make smoke-test` | Boot 1 VM + verify gateway | KVM host + root | ~10min |
| `make test-integration` | Firecracker MicroVM tests | KVM host + root | ~35min |
| `make test-integration-e2e` | Tunnel E2E through Cloudflare | KVM host + root + CF creds | ~20min |
| `make test-integration-run TEST=X` | Single integration test | KVM host + root | varies |

---

## 1. Go Unit Tests (18 tests)

**File:** `backend/internal/agentapi/server_test.go`
**Command:** `make test-go`
**Runs on:** Any machine with Go installed

Tests the agent API server in-process using `httptest` — no VMs, no network. Validates authentication, authorization, and API contracts.

### Control API Auth (5 tests)
| Test | What it verifies |
|------|-----------------|
| `TestControlAPI_HealthIsPublic` | `/health` is accessible without auth |
| `TestControlAPI_AuthRequiredForVMs` | `/vms` returns 401 without Bearer token |
| `TestControlAPI_WrongTokenRejected` | Wrong Bearer token returns 401 |
| `TestControlAPI_InvalidFormatRejected` | Malformed auth header returns 401 |
| `TestControlAPI_ValidTokenAllowed` | Correct Bearer token returns 200 |

### Control API VM Lifecycle (3 tests)
| Test | What it verifies |
|------|-----------------|
| `TestControlAPI_CreateAndGetVM` | POST `/vms` creates a VM, GET `/vms/{id}` retrieves it |
| `TestControlAPI_CreateVMWithoutAuth` | VM creation requires auth |
| `TestControlAPI_DestroyVM` | DELETE `/vms/{id}` removes a VM |

### Proxy API Auth (8 tests)
| Test | What it verifies |
|------|-----------------|
| `TestProxyAPI_HealthProxyWithoutToken` | Proxy health returns 401 without token |
| `TestProxyAPI_HealthProxyWithWrongToken` | Proxy health returns 403 with wrong token |
| `TestProxyAPI_HealthProxyWithValidQueryToken` | Proxy health returns 200 with `?token=` query param |
| `TestProxyAPI_HealthProxyWithValidHeader` | Proxy health returns 200 with `X-Proxy-Token` header |
| `TestProxyAPI_GatewayProxyWithoutToken` | Gateway proxy returns 401 without token |
| `TestProxyAPI_BrowserProxyWithoutToken` | Browser proxy returns 401 without token |
| `TestProxyAPI_CannotAccessOtherVMWithWrongToken` | Cross-VM access blocked with wrong token |
| `TestProxyAPI_EachVMTokenOnlyWorksForItsOwn` | Each VM's proxy token is scoped to that VM only |

### E2E (2 tests)
| Test | What it verifies |
|------|-----------------|
| `TestProxyAPI_NonExistentVMReturns404` | Non-existent VM ID returns 404 |
| `TestE2E_CreateVMThenProxyAccess` | Full flow: create VM via control API, then access via proxy with token |

---

## 2. Frontend Unit Tests (39 tests)

**Command:** `make test-frontend`
**Runs on:** Any machine with Node 18+

Uses Vitest with jsdom. Tests React components, API client, and auth hook in isolation with mocked `fetch`.

### API Client (`frontend/src/lib/api.test.ts` — 20 tests)
Tests every function in the API client against mocked fetch responses. Verifies request shape (method, body, headers), response parsing, and error handling.

| Test | What it verifies |
|------|-----------------|
| `should use credentials include for cookie auth` | All requests include cookies for cross-origin auth |
| `should handle HTTP error with JSON error body` | Error responses parse JSON `{error}` field |
| `should handle HTTP error with non-JSON body` | Non-JSON errors fall back to status text |
| `should handle 204 No Content` | Empty responses return `undefined` |
| `should handle network errors` | Network failures throw meaningful errors |
| `should get current user` | `getMe()` calls GET `/api/users/me` |
| `should register a new user` | `authRegister()` POSTs to `/api/auth/register` |
| `should login with email and password` | `authLogin()` POSTs to `/api/auth/login` |
| `should logout` | `authLogout()` POSTs to `/api/auth/logout` |
| `should get authenticated user via authMe` | `authMe()` calls GET `/api/auth/me` |
| `should list accounts` | `listAccounts()` calls GET `/api/accounts` |
| `should create an account` | `createAccount()` POSTs to `/api/accounts` |
| `should get a single account` | `getAccount()` calls GET `/api/accounts/{slug}` |
| `should list machines for an account` | `listMachines()` calls GET `/api/accounts/{slug}/machines` |
| `should get a single machine` | `getMachine()` calls correct endpoint |
| `should create a machine` | `createMachine()` POSTs with name, vcpus, memory |
| `should update a machine` | `updateMachine()` PUTs with partial fields |
| `should delete a machine` | `deleteMachine()` calls DELETE |
| `should start a machine` | `startMachine()` POSTs to `/start` |
| `should stop a machine` | `stopMachine()` POSTs to `/stop` |

### Auth Hook (`frontend/src/lib/auth.test.tsx` — 6 tests)
Tests the `useAuth()` React hook — session restoration, login, register, logout.

| Test | What it verifies |
|------|-----------------|
| `should initialize without user when no cookie/session` | Hook starts unauthenticated when no session exists |
| `should load user on mount when cookie session is valid` | Hook auto-loads user from valid session cookie |
| `should remain unauthenticated when authMe fails on mount` | Hook stays unauthenticated if session check fails |
| `should login with email and password` | `login()` calls API and updates user state |
| `should register with email, password, and name` | `register()` calls API and updates user state |
| `should logout by clearing user` | `logout()` clears user and calls API |

### MachineCard Component (`frontend/src/components/MachineCard.test.tsx` — 13 tests)
Tests the MachineCard UI component — rendering, links, buttons, status styling.

| Test | What it verifies |
|------|-----------------|
| `should render machine name and slug` | Name and slug text appear |
| `should render vCPU and memory info` | Hardware specs display correctly |
| `should render status badge` | Status badge is visible |
| `should link to the machine detail page when stopped` | Settings link routes to `/dashboard/accounts/{slug}/machines/{machineSlug}` |
| `should show gateway and workspace links when running` | Gateway and Workspace links appear for running machines |
| `should render running status with green styling` | Running status uses green badge |
| `should render error status with red styling` | Error status uses red badge |
| `should render provisioning status with yellow styling` | Provisioning status uses yellow badge |
| `should show Start button when stopped` | Start button appears for stopped machines |
| `should show Stop button when running` | Stop button appears for running machines |
| `should not show start/stop buttons without accountId` | Buttons hidden when no account context |
| `should show provision_step during provisioning` | Shows provisioning step text during provisioning |
| `should call startMachine on Start button click` | Start button triggers API call |

---

## 3. Cloudflare Worker Tests (48 tests)

**Command:** `make test-worker`
**Runs on:** Any machine with Node 18+

Uses Vitest with miniflare (Cloudflare's local runtime). Tests the edge worker that routes subdomain traffic to the correct backend/VM.

### Auth (`worker/test/auth.test.js` — 7 tests)
Tests JWT validation at the edge — cookie vs header, expiration, signature verification.

| Test | What it verifies |
|------|-----------------|
| `accepts valid JWT in ocm_token cookie` | Valid JWT in cookie passes authentication |
| `accepts valid JWT in Authorization Bearer header` | Valid JWT in Bearer header passes authentication |
| `rejects expired JWT` | Expired JWT returns 401 |
| `rejects JWT signed with wrong secret` | Wrong signing secret returns 401 |
| `rejects malformed token` | Garbage token returns 401 |
| `rejects request with no auth at all` | Missing auth returns 401 |
| `prefers cookie over Bearer header` | Cookie auth takes priority when both present |

### Smoke (`worker/test/smoke.test.js` — 4 tests)
Tests basic worker endpoints and routing.

| Test | What it verifies |
|------|-----------------|
| `returns version from env` | `/__version` returns deployed version |
| `returns JSON content-type` | `/__version` returns `application/json` |
| `returns 404 for unrecognized hostname` | Unknown host returns 404 |
| `returns 401 for subdomain request without JWT` | Subdomain requests require auth |

### URL Extractors (`worker/test/extractors.test.js` — 23 tests)
Tests URL parsing logic — subdomain extraction, machine slug parsing, origin validation.

| Group | Tests | What they verify |
|-------|-------|-----------------|
| `extractAccountSlug` | 9 | Parses account slug from subdomain (e.g., `user-1.openclawmachines.com` → `user-1`). Excludes `www`, `api`, `ocm-host-*` tunnel subdomains. |
| `extractMachinePath` | 6 | Parses machine slug and subpath from URL path (e.g., `/myvm/gateway/` → `{slug: "myvm", subpath: "gateway/"}`). |
| `isAllowedOrigin` | 8 | CORS origin validation — allows `*.openclawmachines.com` over HTTPS, rejects HTTP, external domains, and suffix attacks. |

### WebSocket (`worker/test/websocket.test.js` — 3 tests)
Tests WebSocket upgrade detection and proxying.

| Test | What it verifies |
|------|-----------------|
| `non-WebSocket request forwards to agent normally` | Regular HTTP requests are forwarded without WebSocket handling |
| `WebSocket upgrade triggers proxy path` | `Upgrade: websocket` header triggers WebSocket proxy |
| `WebSocket request without auth returns 401` | WebSocket connections require authentication |

### CORS (`worker/test/cors.test.js` — 7 tests)
Tests CORS preflight handling at the edge.

| Test | What it verifies |
|------|-----------------|
| `returns 204 with CORS headers for allowed https origin` | Valid HTTPS origin gets CORS headers |
| `returns 204 for subdomain origin` | Subdomain origins are allowed |
| `returns 403 for http (insecure) origin` | HTTP origins are rejected |
| `returns 403 for evil external origin` | External domains are rejected |
| `returns 403 when no origin is sent` | Missing origin is rejected |
| `includes CORS headers on 401 response for allowed origin` | Error responses include CORS headers for valid origins |
| `omits CORS headers for disallowed origin` | Invalid origins get no CORS headers |

### Routing (`worker/test/routing.test.js` — 5 tests)
Tests request routing from subdomain to backend VM.

| Test | What it verifies |
|------|-----------------|
| `returns 404 when resolve returns no route` | Missing route returns 404 |
| `returns 403 when resolved route has wrong user_ids` | User can't access another user's machine |
| `returns 502 when route is missing host_hostname` | Missing host info returns 502 |
| `returns 502 when route is missing machine_id` | Missing machine ID returns 502 |
| `returns 404 when no machine slug in path` | Missing machine slug in URL returns 404 |

---

## 4. Production Smoke Tests (10 checks)

**Command:** `make test-e2e`
**Runs on:** Anywhere with internet access

Curl-based checks against the production deployment. No browser needed. Tests that all public endpoints are live and security policies are enforced.

| Check | What it verifies |
|-------|-----------------|
| Worker health | `/__version` returns version string |
| Backend health | `/health` returns version string |
| CORS preflight (https) | OPTIONS with `https://` origin returns 204 |
| CORS preflight (http) | OPTIONS with `http://` origin returns 403 |
| CORS preflight (external) | OPTIONS with `https://evil.com` returns 403 |
| CORS on error responses | 401 response includes CORS headers |
| Invalid JWT rejected | Bearer `invalid-token` returns 401 |
| Auth providers endpoint | `/api/auth/providers` returns 200 |
| Frontend serves | Root domain returns 200 |
| www redirect | `www.` subdomain returns 200 |

---

## 5. Playwright Browser E2E Tests (20 tests)

**Command:** `make test-playwright`
**Runs on:** Any machine with a browser (Chromium via Playwright)
**Requires:** Running app (frontend + backend) or use `make test-playwright-ui` for interactive mode

Full browser automation tests. These launch a real browser, navigate pages, fill forms, and assert on rendered UI. Auth state is shared across specs via Playwright's `storageState`.

### Auth Setup (`frontend/e2e/auth.setup.ts`)
Not a test — runs before all specs to log in and save session cookies to `storageState.json`.

### Login (`frontend/e2e/login.spec.ts` — 6 tests)
Tests the login page UI and authentication flow.

| Test | What it verifies |
|------|-----------------|
| `renders login form with email and password fields` | Email and password inputs render |
| `shows OAuth buttons` | Google and GitHub OAuth buttons appear |
| `toggles between sign in and sign up` | Mode toggle adds name field and changes button text |
| `shows error on invalid credentials` | Bad credentials show error message |
| `successful login redirects to dashboard` | Valid login navigates to `/dashboard` |
| `redirects to dashboard if already authenticated` | Already logged-in users skip login page |

### Dashboard (`frontend/e2e/dashboard.spec.ts` — 3 tests)
Tests the main dashboard page.

| Test | What it verifies |
|------|-----------------|
| `loads and shows page content` | Dashboard loads without redirecting to login |
| `shows machine list or empty state` | Machine list or "no machines" message appears |
| `navigation links work` | Links between dashboard and settings pages work |

### Settings (`frontend/e2e/settings.spec.ts` — 3 tests)
Tests the settings page tabs.

| Test | What it verifies |
|------|-----------------|
| `loads settings page with tabs` | Profile, API Keys, Usage tabs render |
| `can switch to API Keys tab` | API Keys tab shows provider names |
| `can switch to Usage & Billing tab` | Usage tab shows spend information |

### Machine Lifecycle (`frontend/e2e/machine-lifecycle.spec.ts` — 7 tests)
Full machine lifecycle through the browser — create, start, verify workspace, stop, delete.

| Test | What it verifies |
|------|-----------------|
| `create a new machine` | Create form submits and machine appears in list |
| `start machine and provisioning completes` | Start button triggers provisioning, status goes to running |
| `workspace loads with terminal and log panels` | Workspace page renders xterm terminal instances |
| `machine detail tabs work while running` | Tabs (Overview, Terminal, Logs) switch content |
| `openclaw agent is running inside the MicroVM` | Log output shows agent, shell works, clawdbot gateway responds |
| `stop the machine` | Stop button returns machine to stopped state |
| `delete the machine` | Delete confirmation dialog removes machine |

### LLM Roundtrip (`frontend/e2e/llm-roundtrip.spec.ts` — 3 tests)
Tests the LLM proxy inside a running MicroVM.

| Test | What it verifies |
|------|-----------------|
| `create and start a machine` | Machine creation and startup via UI |
| `LLM request through apiproxy returns a response` | Curl to Anthropic API through the in-VM proxy gets a response |
| `cleanup: stop and delete machine` | Machine teardown after test |

---

## 6. Firecracker Integration Tests (29 tests)

**Command:** `make test-integration`
**Runs on:** Linux KVM host with root privileges only

These tests boot real Firecracker MicroVMs, set up network bridges, and test the full agent/proxy/VM stack. Each VM boot takes ~60s (gateway startup), so the full suite is ~35min sequential.

### Prerequisites

- `/dev/kvm` available (nested virtualization enabled)
- `firecracker` binary in PATH
- Root privileges (bridge, TAP, iptables)
- Kernel + rootfs images (auto-downloaded from GCS on first run)
- The rootfs is auto-patched if source files (init script, agent, utilities) have changed

### Prerequisite Checks (`linux_vm_test.go` — 4 tests)
Quick checks that skip the entire suite if the environment isn't ready.

| Test | What it verifies |
|------|-----------------|
| `TestPrerequisites_KVM` | `/dev/kvm` exists and is accessible |
| `TestPrerequisites_Firecracker` | `firecracker` binary is in PATH |
| `TestPrerequisites_Root` | Running as root (uid 0) |
| `TestPrerequisites_VMAssets` | Kernel and rootfs images exist (downloads if missing) |

### Network Infrastructure (`linux_vm_test.go` — 3 tests)
Tests the bridge/TAP/NAT setup that connects VMs to the host.

| Test | What it verifies |
|------|-----------------|
| `TestBridge_Setup` | Creates bridge `ocmtest0` on `192.168.200.0/24`, assigns IP |
| `TestBridge_TapLifecycle` | Creates and removes TAP devices on the bridge |
| `TestBridge_NAT` | Verifies iptables NAT and forwarding rules |

### Metadata Server (`linux_vm_test.go` — 1 test)
| Test | What it verifies |
|------|-----------------|
| `TestMetadata_RegisterUnregister` | Config registration and unregistration by VM IP |

### Orchestrator (`linux_vm_test.go` — 2 tests)
Tests Firecracker VM lifecycle management.

| Test | What it verifies |
|------|-----------------|
| `TestOrchestrator_CreateDestroy` | Creates a VM, verifies it's running, destroys it, verifies cleanup |
| `TestOrchestrator_List` | Creates multiple VMs, lists them, verifies count |

### Agent API (`linux_vm_test.go` — 4 tests)
Tests the agent's control and proxy APIs with a real running VM.

| Test | What it verifies |
|------|-----------------|
| `TestAgent_HealthEndpoint` | Agent `/health` returns 200 |
| `TestAgent_Authentication` | Agent rejects requests without valid Bearer token |
| `TestAgent_CreateVM` | POST `/vms` creates a VM through the agent |
| `TestAgent_DestroyVM` | DELETE `/vms/{id}` destroys a VM through the agent |

### Proxy (`linux_vm_test.go` — 3 tests)
Tests the proxy layer that sits between clients and VMs.

| Test | What it verifies |
|------|-----------------|
| `TestProxy_HealthCheck` | Proxy health endpoint returns gateway/browser/PTY status |
| `TestProxy_Authentication` | Proxy rejects missing/wrong tokens, accepts valid token |
| `TestProxy_TerminalWebSocket` | WebSocket terminal works through the proxy — sends command, reads output |

### Full Workflow (`linux_vm_test.go` — 2 tests)
| Test | What it verifies |
|------|-----------------|
| `TestE2E_FullWorkflow` | Create VM → wait for running → WebSocket terminal test → destroy |
| `TestAgent_ProgressSSE` | SSE stream emits all progress steps during VM creation |

### Gateway (`gateway_test.go` — 8 tests)
Tests the clawdbot gateway running inside the MicroVM. All subtests share a single VM.

| Test | What it verifies |
|------|-----------------|
| `TestGatewaySuite/HealthDirect` | HTTP GET directly to VM's auth proxy returns healthy |
| `TestGatewaySuite/HealthViaProxy` | Health check through the agent proxy layer works |
| `TestGatewaySuite/HealthEndpointStatus` | Health response JSON has correct `gateway`/`status` fields |
| `TestGatewaySuite/WebSocket` | WebSocket challenge-response auth with correct gateway token succeeds |
| `TestGatewaySuite/WebSocketBadToken` | WebSocket connect with bad gateway token is rejected (UNAUTHORIZED) |
| `TestGatewaySuite/AuthProtectedAPIViaProxy` | Bearer token forwarded through proxy to auth-protected gateway endpoints |
| `TestGatewaySuite/ProxyRejectsWithoutProxyToken` | Agent proxy returns 401 when no proxy token provided |
| `TestGatewaySuite/ProxyRejectsWrongProxyToken` | Agent proxy returns 403 when wrong proxy token provided |

### Logging & Progress (`logging_test.go` — 3 tests)
Tests SSE-based log and progress streaming from VMs.

| Test | What it verifies |
|------|-----------------|
| `TestLogging_SSE` | Log stream includes init sequence entries (metadata, PTY, etc.) |
| `TestLogging_SSEContentType` | SSE response has correct `text/event-stream` headers |
| `TestLogging_ProgressFullSequence` | Progress stream emits all steps: allocating → machine_ready |

### Init Script (`init_test.go` — 2 tests)
Tests the VM init script behavior.

| Test | What it verifies |
|------|-----------------|
| `TestInit_MetadataFetch` | Init script fetches config from metadata server, writes `openclaw.json`, sets env vars |
| `TestInit_GatewayRestart` | Supervisor restarts gateway after process kill (requires `pkill` in rootfs) |

### Cloudflare Tunnel E2E (`tunnel_test.go` — 4 tests)

**Command:** `make test-integration-e2e`
**Additional requirements:** `CF_API_TOKEN`, `CF_ACCOUNT_ID`, `CF_ZONE_ID` env vars, `cloudflared` binary

| Test | What it verifies |
|------|-----------------|
| `TestTunnel_Lifecycle` | Create → configure → DNS route → delete tunnel lifecycle |
| `TestTunnel_HealthE2E` | VM health check through a real Cloudflare tunnel |
| `TestTunnel_TerminalE2E` | WebSocket terminal through a real Cloudflare tunnel |
| `TestTunnel_GatewayE2E` | Gateway health through a real Cloudflare tunnel |

---

## 7. Rootfs Auto-Patching

The integration test harness automatically patches the rootfs if source files have changed since the last patch. This avoids full snapshot rebuilds during development.

**How it works:**
1. Computes SHA256 fingerprint of: scripts, agent binary, authproxy binary, utility scripts, and openclaw tarball (if present)
2. Compares with stored fingerprint in `/var/lib/ocm/images/.rootfs-patched`
3. If stale: mounts the rootfs ext4 image, copies updated files, unmounts (~5s for binaries, ~30s if openclaw tarball is injected)
4. If up-to-date: skips with a log message

**What gets patched:**
- `/sbin/overlay-init` ← `scripts/init-openclaw.sh`
- `/usr/local/bin/agent` ← built from source (`go build ./cmd/agent`)
- `/usr/local/bin/authproxy` ← built from source (`go build ./cmd/authproxy`)
- `/usr/local/bin/ocm-metadata` ← `scripts/ocm-metadata`
- `/usr/local/bin/ocm-test-llm` ← `scripts/ocm-test-llm`
- `/usr/local/bin/ocm-env` ← `scripts/ocm-env`
- `/usr/local/bin/cf-ssh-check` ← `scripts/cf-ssh-check`
- OpenClaw fork ← `rootfs/openclaw-fork.tgz` (optional, via chroot + `pnpm install -g`)

**Testing openclaw TypeScript changes:**
1. Build the tarball: `make build-openclaw-fork` (requires `OPENCLAW_DIR` pointing to your fork)
2. Run integration tests: `make smoke-test` or `make test-integration`
3. The patcher detects the tarball, bind-mounts `/proc` and `/dev`, and runs `pnpm install -g` inside a chroot to overwrite the existing openclaw install
4. If the tarball doesn't exist, openclaw patching is skipped (no error)

---

## Test Counts Summary

| Suite | Tests | Runtime |
|-------|-------|---------|
| Go unit tests | 18 | ~10s |
| Frontend unit tests | 39 | ~5s |
| Worker unit tests | 48 | ~5s |
| Production smoke tests | 10 | ~10s |
| Playwright browser E2E | 20 | ~2min |
| Integration (local) | 30 | ~35min |
| **Total** | **165** | |
