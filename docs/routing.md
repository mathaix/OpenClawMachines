# Data Plane Routing

*This document covers the data-plane request path. For system overview see [architecture.md](architecture.md), for tunnel details see [tunnel-architecture.md](tunnel-architecture.md).*

This document explains how requests flow from user browsers through Cloudflare to MicroVMs.

## Status

| Feature | Status |
|---------|--------|
| Subdomain routing | Implemented |
| JWT authentication | Implemented |
| Proxy token enforcement | Implemented |
| KV caching | Implemented |
| KV reconciler | **Not implemented** (self-healing on miss only) |
| Edge rate limiting | **Not implemented** (backend only, no Worker limits) |
| Terminal WebSocket | Implemented |

## Sequence Diagram

```
┌─────────┐     ┌─────────────┐     ┌─────────────┐     ┌───────────┐     ┌─────────────┐     ┌─────────┐
│ Browser │     │  Cloudflare │     │   Backend   │     │    KV     │     │    Agent    │     │ MicroVM │
│         │     │   Worker    │     │ Control API │     │  (Routes) │     │  (Host VM)  │     │         │
└────┬────┘     └──────┬──────┘     └──────┬──────┘     └─────┬─────┘     └──────┬──────┘     └────┬────┘
     │                 │                   │                  │                  │                 │
     │ GET https://acme.yourdomain.com/my-bot/terminal/ws                                   │
     │────────────────>│                   │                  │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ 1. Extract slugs  │                  │                  │                 │
     │                 │    account: acme  │                  │                  │                 │
     │                 │    machine: my-bot│                  │                  │                 │
     │                 │    subPath: /terminal/ws             │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ 2. Verify JWT cookie                 │                  │                 │
     │                 │    (ocm_token)    │                  │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ 3. Check KV       │                  │                  │                 │
     │                 │──────────────────────────────────────>│                  │                 │
     │                 │                   │    route:acme:my-bot                │                 │
     │                 │<──────────────────────────────────────│                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ (KV miss? Fallback to resolve)       │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ POST /api/internal/resolve           │                  │                 │
     │                 │ CF-Access-Client-Id: xxx             │                  │                 │
     │                 │ CF-Access-Client-Secret: yyy         │                  │                 │
     │                 │──────────────────>│                  │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │   {host_hostname, │                  │                  │                 │
     │                 │    machine_id,    │                  │                  │                 │
     │                 │    proxy_token}   │                  │                  │                 │
     │                 │<──────────────────│                  │                  │                 │
     │                 │                   │                  │                  │                 │
     │                 │ 4. Forward to Agent via Tunnel       │                  │                 │
     │                 │ GET https://{host}.yourdomain.com/proxy/{machine_id}/terminal/ws   │
     │                 │ X-Proxy-Token: {gateway_token}       │                  │                 │
     │                 │─────────────────────────────────────────────────────────>│                 │
     │                 │                   │                  │                  │                 │
     │                 │                   │                  │  5. Validate     │                 │
     │                 │                   │                  │     proxy token  │                 │
     │                 │                   │                  │                  │                 │
     │                 │                   │                  │  6. Lookup VM IP │                 │
     │                 │                   │                  │     from machineID                 │
     │                 │                   │                  │                  │                 │
     │                 │                   │                  │  7. Proxy to VM  │                 │
     │                 │                   │                  │                  │ ws://{vmIP}:7681/ws
     │                 │                   │                  │                  │────────────────>│
     │                 │                   │                  │                  │                 │
     │<═══════════════════════════════════════════════════════════════════════════════════════════>│
     │                 │                   │  WebSocket established (bidirectional)               │
     │                 │                   │                  │                  │                 │
```

## Components

### 1. Cloudflare Worker (`worker/worker.js`)

Entry point for all data plane traffic. Handles subdomain routing.

**Routes:**
| Hostname Pattern | Behavior |
|------------------|----------|
| `yourdomain.com` | → Frontend |
| `www.yourdomain.com` | → Frontend |
| `api.yourdomain.com` | → Backend API (the control-plane host) |
| `{account}.yourdomain.com` | → Subdomain routing (below) |

**Subdomain Routing Flow:**
1. Extract `accountSlug` from hostname
2. Extract `machineSlug` and `subPath` from pathname
3. Verify JWT from `ocm_token` cookie
4. Lookup route in KV (`route:{account}:{machine}`)
5. On KV miss, fallback to `/api/internal/resolve`
6. Forward to agent with `X-Proxy-Token` header

### 2. Backend Control Plane (`backend/internal/api/`)

Stateless API served by your control-plane deployment (port 8080).

**Internal Route (Service-to-Service):**
```
POST /api/internal/resolve
Headers:
  CF-Access-Client-Id: {service_token_id}
  CF-Access-Client-Secret: {service_token_secret}
Body:
  {"account_slug": "acme", "machine_slug": "my-bot"}
Response:
  {
    "account_id": 123,
    "machine_id": "uuid",
    "host_hostname": "ocm-host-xxx.yourdomain.com",
    "proxy_token": "abc123...",  // This is gateway_token from DB
    "user_ids": [1, 2, 3]
  }
```

### 3. Worker Agent (`backend/internal/agentapi/`)

Runs on each Host VM. Two ports:

**Control API (port 9090) — Firewalled, Control Plane only:**
| Route | Auth | Purpose |
|-------|------|---------|
| `GET /health` | None | Health check |
| `GET /info` | Bearer token | Host info + VM list |
| `POST /vms` | Bearer token | Create MicroVM |
| `GET /vms/{id}` | Bearer token | Get VM status |
| `DELETE /vms/{id}` | Bearer token | Destroy VM |
| `POST /refresh-rootfs` | Bearer token | Refresh rootfs cache |
| `GET /logs` | Bearer token | Agent systemd logs |

**Proxy API (port 9091) — Cloudflare Tunnel only:**

The proxy server **always binds to loopback (127.0.0.1)**. It is only accessible via Cloudflare Tunnel.
In dev mode without a tunnel token, the proxy is still loopback-only (inaccessible externally).

| Route | Auth | Target | Purpose |
|-------|------|--------|---------|
| `GET /health` | None | — | Health check |
| `GET /progress?machine_id=X` | None | — | Provisioning SSE |
| `GET /logs?machine_id=X` | None | — | Machine logs SSE |
| `GET /proxy/{id}/health` | X-Proxy-Token | — | VM health check (ports 3000, 3001) |
| `GET /proxy/{id}/logs` | X-Proxy-Token | — | VM logs SSE |
| `* /proxy/{id}/gateway/*` | X-Proxy-Token | VM:3000 | OpenClaw gateway (HTTP/WS) |
| `* /proxy/{id}/browser/*` | X-Proxy-Token | VM:3001 | Playwright CDP (WebSocket) |
| `* /proxy/{id}/terminal/*` | X-Proxy-Token | VM:7681 | ttyd shell (WebSocket) |

### 4. MicroVM Services

Each Firecracker MicroVM runs these services on the bridge network (192.168.100.x):

| Port | Service | Protocol |
|------|---------|----------|
| 3000 | OpenClaw Gateway | HTTP/WebSocket |
| 3001 | Playwright Browser | WebSocket (CDP) |
| 7681 | ttyd Terminal | WebSocket |

## Authentication Tokens

### Token Types

| Token | Where Generated | Where Stored | Where Validated | Purpose |
|-------|-----------------|--------------|-----------------|---------|
| **JWT (ocm_token)** | Backend on login | Browser cookie (HttpOnly) | Worker | User authentication |
| **Gateway Token** | Backend on machine start | DB `machines.gateway_token` | Agent `validateProxyToken` | Per-machine proxy auth |
| **Agent Token** | Secret store | VM metadata + control-plane env | Agent `bearerAuth` | Control Plane → Agent auth |
| **Service Token** | Cloudflare dashboard | Worker env vars | Backend `ServiceTokenMiddleware` | Worker → Backend auth |

### Token Flow

```
1. User Login
   Browser → Backend → JWT issued → Cookie set (domain=.yourdomain.com)

2. Machine Start
   Backend generates gateway_token → Stored in DB → Sent to Agent via CreateVM

3. Data Plane Request
   Browser sends cookie → Worker validates JWT → Worker gets gateway_token from KV/resolve
   → Worker sends X-Proxy-Token header → Agent validates token → Proxies to VM
```

### Gateway Token Generation

```go
// backend/internal/api/server.go - handleStartMachine()
if machine.GatewayToken == nil {
    tokenBytes := make([]byte, 16)
    rand.Read(tokenBytes)
    token := hex.EncodeToString(tokenBytes)  // 32 hex chars
    machine.GatewayToken = &token
    store.UpdateMachine(ctx, machine)
}
```

### Gateway Token Validation

```go
// backend/internal/agentapi/proxy.go - validateProxyToken()
func validateProxyToken(w http.ResponseWriter, r *http.Request, expected string) bool {
    if expected == "" {
        // Reject — VM must have a token
        http.Error(w, "proxy token not configured", http.StatusForbidden)
        return false
    }
    token := r.URL.Query().Get("token")  // WebSocket
    if token == "" {
        token = r.Header.Get("X-Proxy-Token")  // HTTP
    }
    if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
        http.Error(w, "invalid proxy token", http.StatusForbidden)
        return false
    }
    return true
}
```

## Route Resolution

### Primary: Cloudflare KV

Fast edge lookup. KV keys:
- `account:{slug}` → `{account_id, user_ids[]}`
- `route:{account}:{machine}` → `{machine_id, host_hostname, proxy_token}`

TTL: routes=1hr, accounts=24hr

### Fallback: /api/internal/resolve

When KV misses, Worker calls backend:

```sql
-- backend/internal/store/postgres.go - ResolveRoute()
SELECT a.id, m.id, COALESCE(m.gateway_token, ''), COALESCE(h.tunnel_url, '')
FROM accounts a
JOIN machines m ON m.account_id = a.id
JOIN hosts h ON h.id = m.host_id
WHERE a.slug = $1
  AND m.slug = $2
  AND m.status = 'running'
  AND h.tunnel_url IS NOT NULL
```

### KV Self-Healing

Both Worker and Backend populate KV on successful resolve to reduce future latency.

## URL Patterns

### Production (Subdomain Routing via Worker)

```
# Terminal (ttyd WebSocket - port 7681)
wss://{account}.yourdomain.com/{machine}/terminal/ws

# Browser (Playwright CDP WebSocket - port 3001)
wss://{account}.yourdomain.com/{machine}/browser/ws

# Gateway (OpenClaw API - port 3000)
https://{account}.yourdomain.com/{machine}/gateway/api/health
wss://{account}.yourdomain.com/{machine}/gateway/ws

# Logs (SSE stream)
https://{account}.yourdomain.com/{machine}/logs
```

### Development (Control Plane Proxy via Backend)

The backend proxies WebSocket/HTTP to the agent for localhost development:

```
# Terminal
ws://localhost:8080/api/accounts/{accountId}/machines/{machineId}/terminal/ws

# Browser
ws://localhost:8080/api/accounts/{accountId}/machines/{machineId}/browser/ws

# Logs (SSE)
http://localhost:8080/api/accounts/{accountId}/machines/{machineId}/logs

# Progress (SSE)
http://localhost:8080/api/accounts/{accountId}/machines/{machineId}/progress
```

Backend proxy handlers: `machine_terminal.go`, `machine_browser.go`, `machine_logs.go`, `machine_progress.go`

## Security Boundaries

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Public Internet                                    │
│  ┌─────────────┐                                                            │
│  │   Browser   │ ←── JWT cookie (HttpOnly, Secure, SameSite=Lax)           │
│  └──────┬──────┘                                                            │
└─────────┼───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Cloudflare Edge                                      │
│  ┌─────────────┐     ┌─────────────┐                                        │
│  │   Worker    │────>│     KV      │  JWT validated here                    │
│  └──────┬──────┘     └─────────────┘  Service Token for backend calls       │
└─────────┼───────────────────────────────────────────────────────────────────┘
          │ X-Proxy-Token header
          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       Cloudflare Tunnel                                      │
│  ┌─────────────────────────────────────────────────────────────────────┐    │
│  │  {host}.yourdomain.com → cloudflared → localhost:9091        │    │
│  └─────────────────────────────────────────────────────────────────────┘    │
└─────────┼───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                          Host VM (GCP)                                       │
│                                                                              │
│  ┌──────────────────────┐     ┌──────────────────────┐                      │
│  │ Agent :9090 (Control)│     │ Agent :9091 (Proxy)  │                      │
│  │ Bearer token auth    │     │ X-Proxy-Token auth   │                      │
│  │ Firewalled (no ext)  │     │ Tunnel only          │                      │
│  └──────────────────────┘     └──────────┬───────────┘                      │
│                                          │                                   │
│  ┌───────────────────────────────────────┼────────────────────────────────┐ │
│  │                    Bridge Network (192.168.100.0/24)                   │ │
│  │                                       │                                │ │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐                    │ │
│  │  │  MicroVM 1  │  │  MicroVM 2  │  │  MicroVM N  │                    │ │
│  │  │ .100.2      │  │ .100.3      │  │ .100.N+1    │                    │ │
│  │  │ :3000 gw    │  │ :3000 gw    │  │ :3000 gw    │                    │ │
│  │  │ :3001 cdp   │  │ :3001 cdp   │  │ :3001 cdp   │                    │ │
│  │  │ :7681 ttyd  │  │ :7681 ttyd  │  │ :7681 ttyd  │                    │ │
│  │  └─────────────┘  └─────────────┘  └─────────────┘                    │ │
│  └────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│  Security:                                                                   │
│  - VMs cannot reach GCP metadata (169.254.169.254 blocked)                  │
│  - VMs cannot reach other VMs (iptables FORWARD rules)                      │
│  - VMs can only reach bridge gateway (192.168.100.1) and internet           │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Caveats and Limitations

### Service Token Bypass (Dev Mode)

The `/api/internal/resolve` endpoint requires Cloudflare Service Tokens in production.
In dev mode (when `CF_SERVICE_TOKEN_ID` is empty), the middleware is bypassed with a warning:

```go
// backend/internal/api/server.go - ServiceTokenMiddleware
if s.cfServiceTokenID == "" {
    if s.devMode {
        log.Printf("WARNING: ServiceTokenMiddleware bypassed in dev mode")
        next.ServeHTTP(w, r)
        return
    }
    // Production: reject
}
```

### KV Reconciler (Not Implemented)

The KV store uses **self-healing on miss** only:
1. Worker checks KV for route
2. On miss, Worker calls `/api/internal/resolve`
3. Both Worker and Backend write to KV (fire-and-forget)

There is **no background reconciler** to sync DB → KV. Routes are only populated on access.
Stale routes expire via TTL (1hr for routes, 24hr for accounts).

### Rate Limiting (Backend Only)

Rate limiting is **not implemented** at the Worker edge.
The backend had a rate limiting package but it was unused and has been removed.

Future consideration: Add Worker-level rate limiting for:
- `/api/internal/resolve` calls
- Subdomain routing requests

### DNS Route Failure

During host provisioning, DNS route creation can fail. The provisioner now **hard-fails** on DNS failure:

```go
// backend/internal/provisioner/provisioner.go
if err := p.tunnel.CreateDNSRoute(ctx, tunnelID, tunnelHostname); err != nil {
    p.tunnel.DeleteTunnel(ctx, tunnelID)
    return nil, fmt.Errorf("create DNS route: %w", err)  // Hard fail
}
```

This ensures hosts are only marked ready when fully routable.

## Troubleshooting

### Request returns 401 Unauthorized
- JWT cookie missing or expired
- Cookie domain mismatch (must be `.yourdomain.com`)

### Request returns 403 Forbidden
- User not a member of the account
- Gateway token empty (VM not started properly)
- Gateway token mismatch

### Request returns 404 Not Found
- Machine slug doesn't exist
- Machine not running (status != 'running')
- Host has no tunnel URL

### Request returns 502 Bad Gateway
- Host agent unreachable
- Cloudflare Tunnel down
- MicroVM service not responding

### WebSocket fails to connect
- Check if ttyd/gateway is running in VM
- Check agent logs for proxy errors
- Verify tunnel is connected (`cloudflared` process)

## See Also

- [tunnel-architecture.md](tunnel-architecture.md) — Per-VM tunnel lifecycle, auth proxy, and tunnel reaper
- [terminal_connectivity.md](terminal_connectivity.md) — WebSocket terminal protocol and PTY server details
