# Exposing the OpenClaw Gateway Dashboard

## Problem Statement

Each Firecracker MicroVM runs an OpenClaw Gateway (`clawdbot`) on port 3000. The Gateway has a built-in web dashboard (Control UI) for managing agents, channels, sessions, and configuration — but this dashboard is **not currently enabled** in our VMs.

Users need a way to configure their Gateway (manage agents, inspect sessions, view usage analytics, edit config) without SSH-ing into the VM or using the terminal. The Gateway's built-in dashboard provides this functionality out of the box.

## Current State

### Services Running in VM

| Port | Service | Status | Proxy Route |
|------|---------|--------|-------------|
| 3000 | clawdbot gateway (API only) | Running | `/proxy/{machineID}/gateway/*` |
| 3001 | Playwright browser (CDP) | Running | `/proxy/{machineID}/browser/*` |
| 7681 | PTY server (terminal) | Running | `/proxy/{machineID}/terminal/*` |
| 18789 | Gateway Control UI | **Not started** | **None** |

### Proxy Chain (Browser → VM)

```
Browser (HTTPS + JWT cookie)
  → Cloudflare Worker (JWT + account/machine routing)
    → Cloudflare Tunnel → Host Agent :9091 (proxy token validation)
      → Firecracker VM :3000 (gateway), :3001 (browser), :7681 (terminal)
```

### Gateway Config (current)

```json
{
  "gateway": {
    "port": 3000,
    "host": "0.0.0.0",
    "mode": "local"
  }
}
```

The control UI is part of the `clawdbot` npm package but not enabled in the config.

## Proposed Solution

### Approach: Enable Control UI on Port 3000 (Same Server)

The OpenClaw Gateway can serve both its API and the Control UI from the same HTTP server. Since port 3000 is already proxied through the full proxy chain, **no new proxy routes are needed**.

The Control UI is a Lit-based SPA served as static files. It communicates with the Gateway via WebSocket RPC on the same port. Both HTTP (static files) and WebSocket (RPC) traffic go through the existing `/proxy/{machineID}/gateway/*` route.

### Why Not Port 18789?

Running the dashboard on a separate port (18789) would require:
- New proxy route in agent (`/proxy/{machineID}/dashboard/*`)
- New route in control plane (`/api/accounts/{accountId}/machines/{id}/dashboard/*`)
- Health check updates
- More init script changes

Using port 3000 avoids all of this since the proxy infrastructure already handles it.

## Architecture

### Request Flow (Dashboard Access)

```
1. User clicks "Gateway Dashboard" in workspace
2. Frontend opens new tab (or iframe) with:
   https://{account}.openclawmachines.com/{machine}/gateway/

3. Cloudflare Worker:
   - Validates JWT cookie ✓
   - Extracts account/machine slugs
   - Looks up route in KV (host_hostname + proxy_token)
   - Forwards to agent with X-Proxy-Token header

4. Host Agent (:9091):
   - Route: /proxy/{machineID}/gateway/*
   - Validates proxy token ✓
   - Proxies HTTP to VM_IP:3000/  (serves dashboard HTML/JS/CSS)

5. Dashboard loads, initiates WebSocket to same URL:
   wss://{account}.openclawmachines.com/{machine}/gateway/

6. Same chain for WebSocket:
   Worker → Tunnel → Agent → VM:3000 (WebSocket upgrade)
```

### WebSocket URL Discovery

The Control UI determines its WebSocket URL dynamically:

```typescript
const proto = location.protocol === "https:" ? "wss" : "ws";
const defaultUrl = `${proto}://${location.host}`;
```

**Problem**: Default URL is `wss://team.openclawmachines.com` (no machine slug) — this won't route correctly through the Worker.

**Solution**: Override via `gatewayUrl` query parameter:

```
https://team.openclawmachines.com/my-machine/gateway/?gatewayUrl=wss://team.openclawmachines.com/my-machine/gateway/
```

The dashboard checks multiple sources for this override (in order):
1. `gatewayUrl` query parameter
2. `gatewayUrl` hash parameter (`#?gatewayUrl=...`)
3. `gatewayUrl` localStorage value

### Authentication

Security is provided by three layers of authentication **before** traffic reaches the Control UI:

| Layer | Auth Mechanism | What it Validates |
|-------|---------------|-------------------|
| Cloudflare Worker | JWT cookie (`ocm_token`) | User identity + account membership |
| Host Agent | Proxy token (`X-Proxy-Token`) | Machine-level access |
| Gateway Control UI | **Disabled** (see below) | N/A — handled by outer layers |

The Gateway's own auth (`device identity`, `token auth`) should be **disabled** for our use case. Since all requests are already authenticated by JWT + proxy token, requiring a third layer of auth inside the VM adds friction without improving security.

Config:
```json
{
  "gateway": {
    "controlUi": {
      "enabled": true,
      "dangerouslyDisableDeviceAuth": true,
      "allowInsecureAuth": true
    }
  }
}
```

The `mode: "local"` setting also implies single-user mode, which may further simplify auth.

### Security Headers

The Control UI sets strict headers:
```
X-Frame-Options: DENY
Content-Security-Policy: frame-ancestors 'none'
```

**Impact**:
- **New tab**: Works fine — no iframe restrictions apply
- **Iframe embedding**: Blocked by these headers

**Recommendation**: Open the dashboard in a **new tab** for MVP. If we later want iframe embedding, the agent proxy can strip these headers before forwarding to the client.

## Changes Required

### 1. Init Script — Enable Control UI

**File**: `scripts/init-openclaw.sh` (lines 187-206)

Update the gateway config to enable the Control UI:

```json
{
  "agents": {
    "defaults": {
      "model": { "primary": "anthropic/claude-sonnet-4" },
      "workspace": "/home/openclaw/.openclaw/workspace"
    }
  },
  "gateway": {
    "port": 3000,
    "host": "0.0.0.0",
    "mode": "local",
    "controlUi": {
      "enabled": true,
      "dangerouslyDisableDeviceAuth": true,
      "allowInsecureAuth": true
    }
  },
  "logging": {
    "level": "info"
  }
}
```

**Requires**: New snapshot (`/snapshot` with Full mode) since init script is baked into rootfs.

### 2. Agent Proxy — Strip Iframe Headers (Optional, for future iframe embedding)

**File**: `backend/internal/agentapi/proxy.go` — `proxyHTTP` function

If we later want to embed the dashboard in an iframe, modify `proxyHTTP` to strip `X-Frame-Options` and modify `Content-Security-Policy` when proxying gateway responses. For the new-tab approach, no changes needed.

### 3. Frontend — Dashboard Button in Workspace

**File**: `frontend/src/pages/MachineWorkspace.tsx`

Add a "Gateway" button to the workspace header that opens the dashboard in a new tab.

```typescript
const dashboardUrl = useMemo(() => {
  if (!machine || !account) return null;
  const baseUrl = dataPlaneUrl(account.slug, machine.slug, "gateway/", account.id, machine.id);
  const wsUrl = dataPlaneWsUrl(account.slug, machine.slug, "gateway/", account.id, machine.id);
  return `${baseUrl}?gatewayUrl=${encodeURIComponent(wsUrl)}`;
}, [machine, account]);
```

Header button:
```tsx
{isRunning && dashboardUrl && (
  <a
    href={dashboardUrl}
    target="_blank"
    rel="noopener noreferrer"
    className="bg-indigo-600 text-white px-3 py-1 rounded text-sm font-medium hover:bg-indigo-700"
  >
    Gateway
  </a>
)}
```

### 4. Frontend — Dashboard Tab in MachineView (Optional)

**File**: `frontend/src/pages/MachineView.tsx`

Add a "Dashboard" link/button to the machine detail tabs so users can access it even when not in the workspace view.

### 5. Control Plane — No Changes Needed

The control plane already proxies terminal and browser WebSocket traffic. For the dashboard:
- **Dev mode**: Accessed via `http://localhost:5173` → Vite proxy → backend → agent → VM
- **Production**: Accessed via subdomain routing (Worker → Tunnel → Agent → VM)

No new control plane routes needed because the Worker + Agent already handle `/gateway/*` traffic.

### 6. Cloudflare Worker — No Changes Needed

The Worker routes all subdomain traffic generically:
```
https://{account}.openclawmachines.com/{machine}/{subPath}
  → /proxy/{machineID}/{subPath}
```

The dashboard path (`/gateway/`) is already handled by the existing routing logic.

## Dev Mode Access

In dev mode (localhost), the data plane URL falls back to the control plane proxy:

```
http://localhost:5173/api/accounts/{accountId}/machines/{machineId}/gateway/
```

This requires a new route in the control plane for proxying HTTP/WebSocket to the gateway from the dev frontend. The existing `handleBrowserProxy` only handles WebSocket, but `handleGatewayProxy` in the agent handles both HTTP and WebSocket.

**Add to server.go** (alongside existing browser/terminal routes):
```go
r.HandleFunc("/gateway/*", srv.handleGatewayProxy)
```

Where `handleGatewayProxy` mirrors the pattern of `handleBrowserProxy` but proxies to the agent's `/proxy/{machineID}/gateway/*` route, supporting both HTTP and WebSocket.

## User Flow

### Accessing the Gateway Dashboard

1. User navigates to workspace (`/workspace/{id}`)
2. Clicks "Gateway" button in the workspace header bar
3. New tab opens: `https://team.openclawmachines.com/my-machine/gateway/?gatewayUrl=wss://...`
4. Gateway Control UI loads (Lit SPA)
5. WebSocket RPC connects through the proxy chain
6. User can manage agents, view sessions, edit config

### What Users Can Do in the Dashboard

| Feature | Description |
|---------|-------------|
| **Agents** | List, create, update, delete agents; manage files and skills |
| **Sessions** | View active sessions, history, and logs |
| **Config** | Edit gateway configuration (models, providers, etc.) |
| **Usage** | View token usage, costs, time-series analytics |
| **Channels** | Manage messaging integrations (WhatsApp, Signal, etc.) |
| **Approvals** | Approve/deny tool execution requests |
| **Health** | Monitor gateway health and status |

## Implementation Plan

### Phase 1: Enable Dashboard (Minimal — requires snapshot rebuild)

1. Update `scripts/init-openclaw.sh` — add `controlUi` config section
2. Build new rootfs with `/snapshot` (Full mode)
3. Test: Start a machine, access `https://{account}.{domain}/{machine}/gateway/`
4. Verify: Dashboard loads, WebSocket connects, agents visible

### Phase 2: Frontend Integration

1. Add "Gateway" button to `MachineWorkspace.tsx` header
2. Add "Dashboard" link to `MachineView.tsx` (for stopped machines: disabled; running: opens new tab)
3. (Optional) Add gateway proxy handler to control plane `server.go` for dev mode

### Phase 3: Polish (Future)

1. Strip iframe headers in agent proxy for embedded dashboard
2. Add "Gateway" panel to workspace (replace "Browser View" placeholder or add as new panel)
3. Proxy auth token for seamless control UI authentication
4. Pre-configure gateway settings from account-level integrations

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Control UI auth disabled | Proxy chain provides JWT + proxy token auth. VM is not publicly accessible. |
| WebSocket URL mismatch | `gatewayUrl` query param overrides default URL discovery |
| X-Frame-Options blocks iframe | Use new tab for MVP; strip headers later if iframe needed |
| Gateway config conflicts with existing API | Control UI and API coexist on same port — standard OpenClaw setup |
| New snapshot required | Only init script change; no Dockerfile/rootfs changes |

## Files Summary

| File | Action | Required For |
|------|--------|-------------|
| `scripts/init-openclaw.sh` | Modify (gateway config) | Phase 1 |
| `frontend/src/pages/MachineWorkspace.tsx` | Modify (add Gateway button) | Phase 2 |
| `frontend/src/pages/MachineView.tsx` | Modify (add Dashboard link) | Phase 2 |
| `backend/internal/api/server.go` | Modify (add gateway proxy route for dev) | Phase 2 |
| `backend/internal/api/machine_gateway.go` | New (gateway proxy handler) | Phase 2 |
| `backend/internal/agentapi/proxy.go` | Modify (strip iframe headers) | Phase 3 |

## Verification

- [ ] Gateway dashboard loads at `https://{account}.openclawmachines.com/{machine}/gateway/`
- [ ] WebSocket RPC connects through the proxy chain
- [ ] Dashboard shows agent list and sessions
- [ ] Dashboard can edit gateway configuration
- [ ] Works in dev mode (`localhost:5173`)
- [ ] Authentication chain prevents unauthorized access (test with wrong JWT / missing cookie)
- [ ] Multiple machines on same host can each access their own dashboard
