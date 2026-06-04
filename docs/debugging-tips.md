# Debugging & Development Tips

This document collects debugging techniques, common gotchas, and diagnostic commands for the OpenClawMachines project. It covers every layer of the stack: frontend, Cloudflare Worker, backend API, agent proxy, Firecracker MicroVMs, and the OpenClaw gateway running inside them.

---

## Table of Contents

1. [WebSocket Debugging](#1-websocket-debugging)
2. [Proxy Chain Debugging](#2-proxy-chain-debugging)
3. [Firecracker VM Debugging](#3-firecracker-vm-debugging)
4. [Integration Test Tips](#4-integration-test-tips)
5. [Frontend Debugging](#5-frontend-debugging)
6. [Patterns & Library Tips](#6-patterns--library-tips)
7. [Common Gotchas](#7-common-gotchas)
8. [Useful Commands](#8-useful-commands)

---

## 1. WebSocket Debugging

### The Full WebSocket Path

Every WebSocket connection in the system traverses multiple proxy layers. Understanding which layer is failing is the key to debugging.

**Terminal WebSocket path (production):**

```
Browser (xterm.js)
  --> Cloudflare Worker (WebSocketPair proxy, validates JWT from ocm_token cookie)
    --> Cloudflare Tunnel (cloudflared QUIC)
      --> Agent proxy.go (gorilla/websocket bidirectional proxy, validates X-Proxy-Token)
        --> PTY server (agent --pty-server, port 7681 inside MicroVM)
```

**Terminal WebSocket path (dev mode):**

```
Browser (xterm.js)
  --> Vite dev proxy (ws: true)
    --> Backend machine_gateway.go (gorilla/websocket proxy)
      --> Agent proxy.go
        --> PTY server (port 7681)
```

**Gateway WebSocket path (production):**

```
Browser (iframe with OpenClaw SPA)
  --> Cloudflare Worker (WebSocketPair proxy)
    --> Cloudflare Tunnel
      --> Agent proxy.go (handleGatewayProxy, strips ?token= before forwarding)
        --> OpenClaw gateway (port 3000, challenge-response auth)
```

### WebSocket Close Codes

| Code | Meaning in This System |
|------|----------------------|
| **1000** | Normal closure. Connection ended cleanly. The `useReconnectingWebSocket` hook does **not** attempt reconnect on 1000 or 1001. |
| **1001** | Going away. Browser tab closing or navigation. No reconnect attempted. |
| **1006** | Abnormal closure (no close frame received). **Cloudflare swallowed the real close code.** This is the most common code you will see when something goes wrong behind the tunnel. The real error is upstream -- check agent logs. |
| **1008** | Policy violation. In this system, typically means the **Origin header check failed** in `proxy.go`'s `checkWSOrigin`. The agent rejects WebSocket upgrades from origins that are not `*.openclawmachines.com` (HTTPS) or `localhost`. |
| **1011** | Internal server error. The agent sends this via `websocket.FormatCloseMessage(websocket.CloseInternalServerErr, ...)` when it cannot connect to the upstream target (VM port unreachable). |

### Checking the Origin Header

The agent's `checkWSOrigin` function in `backend/internal/agentapi/proxy.go` accepts:
- Empty origin (non-browser clients like curl/websocat)
- `localhost` or `127.0.0.1` (any port, any scheme) for development
- `*.openclawmachines.com` over HTTPS only

The proxy also **sets** an Origin header when dialing the target VM:

```go
// proxy.go line 328-329
if u, parseErr := url.Parse(targetURL); parseErr == nil {
    dialHeaders.Set("Origin", "http://"+u.Host)
}
```

This is necessary because the OpenClaw gateway inside the VM validates origins too. The agent is a trusted proxy (already authenticated via proxy token), so it rewrites the Origin to match the target.

**If you see "origin missing or invalid" in gateway logs**, the `Origin` header is not being set by the proxy. Check that the `proxyBrowserWebSocket` function is being called (not a different code path) and that the target URL is parseable.

### Challenge-Response Auth (Gateway WebSocket)

The OpenClaw gateway uses a challenge-response protocol for WebSocket authentication:

1. Client opens WebSocket connection (HTTP upgrade succeeds)
2. Gateway sends `connect.challenge` event:
   ```json
   {"type":"event","event":"connect.challenge","payload":{"nonce":"b37386c8-...","ts":1770698976578}}
   ```
3. Client must respond within ~100ms with a `connect` request containing `auth.token`, device info, and protocol version
4. If no valid response arrives, the gateway closes the connection (shows up as close code 1006 after going through Cloudflare)

**Required fields in the connect response:**

```json
{
  "type": "req",
  "method": "connect",
  "auth": { "token": "<gateway_token>" },
  "minProtocol": 1,
  "maxProtocol": 1,
  "client": { "id": "...", "version": "...", "platform": "web", "mode": "control" }
}
```

The OpenClaw SPA handles this automatically when it receives the `?token=` query parameter. If the gateway WebSocket shows "disconnected", check:

1. Is `machine.gateway_token` populated? (Check the API response from `GET /api/accounts/{slug}/machines/{id}`)
2. Is the `?token=` parameter being appended to the iframe URL? (Inspect the iframe `src` in `GatewayDashboard.tsx`)
3. Is the token being stripped from the proxy URL before forwarding? (The agent's `handleGatewayProxy` does `q.Del("token")` to avoid leaking it to the VM)

**Failed approaches for gateway WebSocket auth (for context on what does NOT work):**

| Approach | Why It Failed |
|----------|--------------|
| Bearer header in WebSocket upgrade | Gateway ignores `Authorization` header for WebSocket -- uses challenge-response protocol only |
| Proxy-side message injection (intercepting frames to inject `auth.token`) | The SPA does not send a `connect` request at all without a token, so there is nothing to intercept and augment |
| Remove `--token` from OpenClaw CLI | Gateway refuses to start when bound to LAN without a token (safety requirement) |
| Remove `OPENCLAW_GATEWAY_TOKEN` env var | Same result -- gateway failed to start |

### Ping/Pong Keepalive

Both the agent proxy (`proxy.go`) and the backend proxy (`machine_gateway.go`) implement WebSocket keepalive to prevent idle timeouts from intermediary proxies (Cloudflare has a 100-second idle timeout):

- Ping interval: **25 seconds** (`wsPingInterval`)
- Pong timeout: **10 seconds** (`wsPongTimeout`)
- Read deadline: `wsPingInterval + wsPongTimeout` = **35 seconds**

If a connection drops after exactly ~100 seconds of inactivity, Cloudflare's idle timeout is killing it. Verify that pings are being sent by checking agent logs for keepalive-related errors.

### Goroutine Leak in Ping Ticker

The WebSocket ping ticker goroutine in `proxyWebSocketBidirectional` must use `select` with a stop channel, not `for range ticker.C`. The `for range` pattern blocks forever on the channel read when the ticker is stopped but the goroutine has already entered the loop iteration, causing a goroutine leak.

**Incorrect (leaks goroutine):**

```go
go func() {
    for range ticker.C {
        client.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
    }
}()
```

**Correct (responds to shutdown):**

```go
stop := make(chan struct{})

go func() {
    for {
        select {
        case <-stop:
            return
        case <-ticker.C:
            if err := client.WriteControl(websocket.PingMessage, nil,
                time.Now().Add(5*time.Second)); err != nil {
                return
            }
        }
    }
}()

// After reader goroutine exits:
ticker.Stop()
close(stop)
```

This pattern appears in both `backend/internal/agentapi/proxy.go` and `backend/internal/api/machine_gateway.go`. Also note that `SetReadDeadline` returns an error -- explicitly discard it with `_ =` to satisfy `golangci-lint errcheck`:

```go
_ = client.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
```

### Cloudflare Worker Close Code Restrictions

Cloudflare Workers only allow WebSocket close codes **1000** (normal) and **3000-4999** (application-defined). Any other code (including standard codes 1001-1015) is silently dropped, and the browser sees 1006 instead. This makes debugging disconnects through the Worker layer difficult because the real close code is lost.

**Fix:** Add a `safeCloseCode` mapping function in the Worker's WebSocket proxy:

```javascript
function safeCloseCode(code) {
    if (code === 1000 || (code >= 3000 && code <= 4999)) return code;
    return 4000 + (code % 1000);  // e.g., 1008 -> 4008
}
```

Apply to both directions of the bidirectional proxy:

```javascript
origin.addEventListener("close", (event) => {
    try { server.close(safeCloseCode(event.code), event.reason || ""); } catch {}
});
server.addEventListener("close", (event) => {
    try { origin.close(safeCloseCode(event.code), event.reason || ""); } catch {}
});
```

The mapping `4000 + (code % 1000)` preserves the original code's semantics (e.g., 1008 policy violation becomes 4008) while staying in the allowed range.

---

## 2. Proxy Chain Debugging

### Isolating the Failing Layer

Start from the innermost layer and work outward. At each layer, use curl or websocat to test connectivity.

**Layer 1: Inside the VM (port 3000/3001/7681)**

```bash
# From a terminal WebSocket session inside the VM:
curl -s http://localhost:3000/   # Gateway health
curl -s http://localhost:3001/   # Browser (Playwright)
curl -s http://localhost:7681/   # PTY server (will fail for HTTP, but confirms port is listening)
```

**Layer 2: Agent proxy (port 9091 from host)**

```bash
# Replace $VMIP with the bridge IP (e.g., 192.168.100.2)
# Test directly from the host VM:
curl -s "http://$VMIP:3000/"                              # Direct to gateway, bypass proxy
curl -s -H "X-Proxy-Token: $TOKEN" "http://localhost:9091/proxy/$MID/health"  # Through agent proxy
```

**Layer 3: Through Cloudflare Tunnel**

```bash
# From anywhere with internet access:
curl -s -H "Authorization: Bearer $JWT" \
     -H "X-Proxy-Token: $PROXY_TOKEN" \
     "https://$ACCOUNT.openclawmachines.com/$MACHINE_SLUG/health"
```

**Layer 4: Through the Worker**

```bash
# The Worker handles JWT validation, route resolution, and adds X-Proxy-Token
curl -s -b "ocm_token=$JWT" \
     "https://$ACCOUNT.openclawmachines.com/$MACHINE_SLUG/gateway/"
```

### Common Proxy Symptoms and Root Causes

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| "TypeError: Invalid URL" in browser console | Double-slash in proxy path (`http://ip:3000//`) or missing SPA base path | Check `chi.URLParam(r, "*")` returns. Empty string + `Sprintf` format with leading slash = double-slash. See Bug 1 in `CurrentFeature.md`. |
| "Invalid URL" when opening gateway directly | OpenClaw SPA's `__OPENCLAW_CONTROL_UI_BASE_PATH__` is empty string | The `proxyGatewayRoot` function must rewrite the base path in the HTML response. Check that `MachineSlug` is populated on the `VMInstance`. |
| iframe shows blank white page | CSP `frame-ancestors 'none'` blocking the iframe | The proxy strips `X-Frame-Options` and rewrites `frame-ancestors 'none'` to allow `*.openclawmachines.com`. Check that the rewrite is happening in `proxyHTTPWithAuth` and `proxyGatewayRoot`. |
| 401 from proxy endpoints | Missing or wrong proxy token | Worker sends `X-Proxy-Token` header. Agent checks header first, then `?token=` query param. If both are absent, returns 401. |
| 403 from proxy endpoints | Proxy token mismatch | Token comparison uses `subtle.ConstantTimeCompare`. Check that the token in the Worker's route cache (KV) matches the one stored on the VM instance. |
| 502 Bad Gateway | Agent cannot reach the VM's port | VM may not be running, gateway may not have started, or network bridge is down. Check `checkPort` results in the health endpoint. |
| WebSocket connects but immediately closes | Challenge-response auth timeout (gateway), or SPA missing token | See the challenge-response section above. |

### CSP Header Handling

The proxy strips or rewrites security headers that would block iframe embedding:

```go
// All three proxy functions (proxyHTTP, proxyHTTPWithAuth, proxyGatewayRoot) do this:
case "x-frame-options":
    continue  // strip entirely
case "content-security-policy":
    cleaned := strings.Replace(value,
        "frame-ancestors 'none'",
        "frame-ancestors 'self' openclawmachines.com *.openclawmachines.com", 1)
```

If the gateway dashboard still fails to load in an iframe, inspect the response headers in browser DevTools. Look for:
- A second `Content-Security-Policy` header that was not rewritten
- `frame-ancestors 'self'` without the openclawmachines.com domain
- The `X-Frame-Options` header still present (means the proxy is not in the path)

---

## 3. Firecracker VM Debugging

### Viewing VM Console Output

VM console output is captured via `vmConsoleWriter` and logged at `slog.Debug` level. To see it:

1. Enable debug logging in the agent (set log level to debug)
2. Or in integration tests, set the log level in `TestMain`:
   ```go
   slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
   ```

### Init Script Logs

The init script (`scripts/init-openclaw.sh`, injected as `/sbin/overlay-init`) writes to two log files:

| Log File | Contents |
|----------|----------|
| `/var/log/openclaw-init.log` | Full init script output: mount, network, metadata fetch, config write, PTY server start, gateway supervisor |
| `/var/log/openclaw-gateway.log` | Gateway process stderr (the `su -s /bin/bash openclaw -c "... exec openclaw gateway ..."` block redirects stderr here) |

**Reading logs from outside the VM (via terminal WebSocket):**

```bash
# View init log
cat /var/log/openclaw-init.log

# View gateway log (stderr from the openclaw gateway process)
cat /var/log/openclaw-gateway.log

# Follow gateway log in real-time
tail -f /var/log/openclaw-gateway.log
```

### Common VM Issues

**Gateway will not start:**

1. Check if `--token` was passed: The gateway requires `--token` when using `--bind lan`. Without it, the gateway refuses to start entirely. Snapshot `ocm-snapshot-20260210-042835` confirmed this -- gateway never started without `--token`.
2. Check if `OPENCLAW_GATEWAY_TOKEN` is set: The environment variable also enables token auth independently of the CLI flag.
3. Check the gateway log: `cat /var/log/openclaw-gateway.log`. Common errors include missing Node.js modules, permission errors on the workspace directory, or port 3000 already in use.

**Gateway started but not responding:**

The gateway core startup takes approximately **55 seconds** due to Node.js module loading. This is the baseline even with quick-start mode (`ocm_quick_start=1`), which only skips sidecars (channels, browser, cron, Gmail, Bonjour). Do not assume the gateway is broken until at least 90 seconds have passed.

**Networking problems:**

The VM's network is configured from the kernel command line (`ip=` parameter). Check:

```bash
# Inside the VM
ip addr show eth0          # Should have 192.168.100.X/24
ip route                   # Should have default via 192.168.100.1
cat /etc/resolv.conf       # Should have 8.8.8.8, 8.8.4.4
ping -c 1 192.168.100.1    # Should reach the host bridge
curl -sf http://192.168.100.1/health   # Metadata service on the bridge gateway IP
```

**Metadata service unreachable:**

The metadata server binds to the bridge gateway IP (typically `192.168.100.1`) on port 80, **not** `127.0.0.1:8080`. The init script waits up to 6 seconds (30 retries x 0.2s) for it. If metadata fetch fails:

1. Check that the bridge is up: `ip addr show ocmtest0` (or the production bridge name)
2. Check that the metadata server is running on the host
3. Check the metadata nonce: The VM reads `metadata_nonce=` from `/proc/cmdline` and sends it as `X-Metadata-Nonce` header

**RAM requirements:**

VMs require **2048 MB** of RAM. The gateway (Node.js + OpenClaw) plus Playwright (Chromium) consume significant memory. VMs with less memory may experience OOM kills.

### Supervisor Behavior

The init script (PID 1 inside the VM) runs a supervisor loop that:

1. Checks for a reload signal (`/tmp/.ocm-reload`, touched by the config watcher when the metadata config version changes)
2. Checks if the gateway process is alive (`kill -0 $GATEWAY_PID`)
3. If the gateway crashed, logs the last 20 lines of `/var/log/openclaw-gateway.log`, waits 2 seconds, and restarts
4. Polls every **5 seconds**

To kill and restart the gateway for testing:

```bash
# The gateway is started via "exec openclaw gateway", which becomes
# "node .../entry.js gateway" at runtime. Use pkill to match:
pkill -f 'entry.js gateway'
# The supervisor will detect the exit within 5 seconds and restart
```

---

## 4. Integration Test Tips

### Running Tests

Integration tests require a Linux KVM host with root privileges, Firecracker binary in PATH, and kernel/rootfs images.

```bash
# Full suite (~35 minutes sequential)
make test-integration

# Single test by name
make test-integration-run TEST=TestGateway_WebSocket

# Tunnel E2E tests (requires CF_API_TOKEN, CF_ACCOUNT_ID, CF_ZONE_ID)
make test-integration-e2e
```

### sudo + PATH Preservation

Integration tests need root (for bridge/TAP/iptables) but also need Go toolchain in PATH. The Makefile handles this:

```bash
cd backend && sudo env \
  "PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/sbin:$PATH" \
  "GOCACHE=$(go env GOCACHE)" \
  "GOPATH=$(go env GOPATH)" \
  "HOME=$HOME" \
  go test -v -tags integration -timeout 45m ./internal/integration/...
```

If you run tests manually, you must pass these environment variables or Go will not be found.

### Quick-Start Mode

Pass `ocm_quick_start=1` on the kernel command line to skip heavyweight gateway sidecars:

- `OPENCLAW_SKIP_CHANNELS=1`
- `OPENCLAW_SKIP_BROWSER_CONTROL_SERVER=1`
- `OPENCLAW_SKIP_CANVAS_HOST=1`
- `OPENCLAW_SKIP_CRON=1`
- `OPENCLAW_SKIP_GMAIL_WATCHER=1`
- `OPENCLAW_DISABLE_BONJOUR=1`

This reduces boot-to-ready from ~60 seconds to ~15-30 seconds, but the gateway core startup (~55 seconds of Node.js module loading) is not affected.

### Key Timeouts

| What | Timeout | Why |
|------|---------|-----|
| `waitForVMReady` | `vmCreationTimeout` | Waits for VM to reach "running" state |
| `waitForGateway` | **90 seconds** | Gateway core takes ~55s to start; 90s provides headroom |
| `testTerminalCommand` | 10-15 seconds | Sending a command via WebSocket terminal and reading output |
| Full suite | **45 minutes** (`-timeout 45m`) | Sequential VM boots, each ~60s for gateway |

### su -s vs su -l

Inside Firecracker VMs, **`su -l` hangs** due to PAM's `pam_keyinit.so` module. Always use `su -s /bin/bash username -c "..."` instead. The init script follows this pattern:

```bash
su -s /bin/bash openclaw -c "
  export HOME=/home/openclaw
  ...
  exec openclaw gateway --port 3000 --bind lan ...
"
```

### Rootfs Auto-Patching

The test harness automatically patches the rootfs if source files have changed:

1. Computes SHA256 fingerprint of: `scripts/init-openclaw.sh`, agent binary, utility scripts
2. Compares with stored fingerprint in `/var/lib/ocm/images/.rootfs-patched`
3. If stale: mounts the rootfs ext4 image, copies updated files, unmounts (~5 seconds)

Files that get patched:
- `/sbin/overlay-init` from `scripts/init-openclaw.sh`
- `/usr/local/bin/agent` from built agent binary
- `/usr/local/bin/ocm-metadata` from `scripts/ocm-metadata`
- `/usr/local/bin/ocm-test-llm` from `scripts/ocm-test-llm`
- `/usr/local/bin/ocm-env` from `scripts/ocm-env`

### Killing Processes in Tests

The `clawdbot` wrapper script uses `exec` to replace itself with `node .../entry.js`. So `pkill -f 'clawdbot.*gateway'` will not match the running process. Use:

```bash
pkill -f 'entry.js gateway'
```

---

## 5. Frontend Debugging

### Test Utils Provider Wrapping

The test utility at `frontend/src/test/test-utils.tsx` wraps all components with the full provider tree:

```tsx
function AllProviders({ children }: WrapperProps) {
  return (
    <ThemeProvider>
      <BrowserRouter>
        <AuthProvider>
          <ToastProvider>
            {children}
          </ToastProvider>
        </AuthProvider>
      </BrowserRouter>
    </ThemeProvider>
  );
}
```

**When adding a new context provider**, you must update this file or all tests that use `render` from `test-utils` will break. The provider order matters -- ThemeProvider must be outermost (it adds the `dark` class to the document), BrowserRouter must wrap anything using `useNavigate` or `Link`, and so on.

### xterm.js Addon Loading Order

Addons must be loaded in the correct order. The current Terminal component (`frontend/src/components/Terminal.tsx`) loads:

1. `FitAddon` -- must be loaded before `terminal.open()` so the initial fit works
2. `ClipboardAddon` -- clipboard integration
3. `WebLinksAddon` -- clickable URLs
4. `SearchAddon` -- Ctrl+Shift+F search
5. `Unicode11Addon` -- emoji/CJK rendering

After loading Unicode11, activate it: `terminal.unicode.activeVersion = "11"`.

The `allowProposedApi: true` option is required for the ClipboardAddon.

### WebSocket Reconnection Behavior

The `useReconnectingWebSocket` hook (`frontend/src/lib/useReconnectingWebSocket.ts`) implements exponential backoff:

- **Backoff formula:** `min(1s * 2^attempt, 30s)` -- delays are 1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
- **Max retries:** 10 (configurable)
- **Reset on success:** retry count resets to 0 on successful connection
- **Clean close codes (1000, 1001):** no reconnect attempted, status set to "disconnected"
- **Manual reconnect:** calling `reconnect()` resets the retry count and connects immediately
- **Unmount cleanup:** clears timers and closes WebSocket

The Terminal and LiveCam components both use this hook. If you see "error" status (red dot), the hook has exhausted all 10 retries. Click "Reconnect" to reset and try again.

### Terminal Protocol

The PTY server uses a simple text-prefix protocol:

- `'0'` + data: terminal I/O (input from client, output from server)
- `'1'` + JSON: resize messages (`{"columns": N, "rows": M}`)

On connection open, the terminal sends a resize message so the PTY knows the initial dimensions. On window resize or panel resize (via `ResizeObserver`), it sends another resize message.

### Theme / Dark Mode

The theme system (`frontend/src/lib/theme.tsx`) uses the `class` strategy:

- `darkMode: "class"` in Tailwind config
- ThemeProvider adds/removes `dark` class on `document.documentElement`
- Supports three modes: `light`, `dark`, `system`
- System mode listens to `prefers-color-scheme` media query
- Persisted in `localStorage` under the key used by ThemeProvider

Dashboard components use `dark:` Tailwind variants. The workspace is always dark (hardcoded `bg-[#1a1a2e]`).

### Right-Click Context Menu in Terminal

The Terminal component overrides the browser's context menu:

- **Right-click with text selected:** copies selection to clipboard
- **Right-click with no selection:** pastes from clipboard into terminal (sends as `0` + text)

Keyboard shortcuts `Ctrl+Shift+C/V` and `Cmd+C/V` are passed through to the browser via `attachCustomKeyEventHandler` returning `false`.

---

## 6. Patterns & Library Tips

### xterm.js Scoped Package Migration

The `xterm` npm package is the legacy unscoped name. The xterm.js project has moved to scoped packages under the `@xterm` organization. When migrating, update all addon packages simultaneously -- mixing scoped and unscoped packages causes type conflicts.

**Package mapping:**

| Old Package | New Package | Version |
|-------------|-------------|---------|
| `xterm` | `@xterm/xterm` | `^5.5.0` |
| `xterm-addon-fit` | `@xterm/addon-fit` | `^0.11.0` |
| (new) | `@xterm/addon-clipboard` | `^0.1.0` |
| (new) | `@xterm/addon-web-links` | `^0.12.0` |
| (new) | `@xterm/addon-search` | `^0.16.0` |
| (new) | `@xterm/addon-unicode11` | `^0.9.0` |

**Import changes:**

```typescript
// Before
import { Terminal } from "xterm";
import { FitAddon } from "xterm-addon-fit";
import "xterm/css/xterm.css";

// After
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { ClipboardAddon } from "@xterm/addon-clipboard";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { SearchAddon } from "@xterm/addon-search";
import { Unicode11Addon } from "@xterm/addon-unicode11";
import "@xterm/xterm/css/xterm.css";
```

**Gotcha: `allowProposedApi`.** Some addons (notably `@xterm/addon-clipboard`) require `allowProposedApi: true` in the Terminal constructor options. Without it, the addon silently fails to load. This is not documented prominently:

```typescript
const terminal = new Terminal({
  allowProposedApi: true,
  // ... other options
});
```

### React Resizable Panels (`react-resizable-panels`)

**localStorage persistence via `autoSaveId`.** Panel sizes are automatically saved to and restored from localStorage when `autoSaveId` is set on a `PanelGroup`. Each `PanelGroup` needs a unique ID, including nested groups:

```tsx
<PanelGroup direction="horizontal" autoSaveId="workspace-main">
  <Panel defaultSize={40} minSize={20}>
    <PanelGroup direction="vertical" autoSaveId="workspace-left">
      ...
    </PanelGroup>
  </Panel>
  <PanelResizeHandle />
  <Panel defaultSize={60} minSize={20}>...</Panel>
</PanelGroup>
```

**Collapsible panels with `ImperativePanelHandle`.** To programmatically collapse/expand panels (e.g., via keyboard shortcuts), use `ImperativePanelHandle` refs:

```tsx
import type { ImperativePanelHandle } from "react-resizable-panels";

const logPanelRef = useRef<ImperativePanelHandle>(null);

const togglePanel = useCallback((ref: React.RefObject<ImperativePanelHandle | null>) => {
    const panel = ref.current;
    if (!panel) return;
    if (panel.isCollapsed()) {
        panel.expand();
    } else {
        panel.collapse();
    }
}, []);

// The `collapsible` prop MUST be set on the Panel for collapse()/expand() to work.
// Without it, calling collapse() is a silent no-op.
<Panel ref={logPanelRef} defaultSize={50} minSize={10} collapsible>
```

**Tip:** Setting `minSize` too high (e.g., 20%) can prevent collapsing in narrow viewports.

---

## 7. Common Gotchas

Quick-reference table of symptoms, likely causes, and fixes.

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| Agent changes not taking effect after deploy | Agent runs inside the snapshot, not in Cloud Run | Build a new snapshot with `/snapshot` skill |
| Init script changes not taking effect | Init script is baked into rootfs | Build a **full** snapshot (rootfs rebuild), not quick |
| "Invalid URL" in gateway iframe | Double-slash in proxy URL or missing SPA base path | Check `proxyGatewayRoot` base path rewrite and `chi.URLParam(r, "*")` empty-string handling |
| Gateway iframe blank (no error) | CSP `frame-ancestors 'none'` blocking iframe | Verify proxy strips `X-Frame-Options` and rewrites CSP |
| Gateway WebSocket "disconnected" | Challenge-response auth failed (no token) | Ensure `?token=` is appended to iframe src URL with `machine.gateway_token` |
| WebSocket 1006 close code | Cloudflare swallowed the real close code | Check agent logs for the actual error |
| WebSocket 1008 close code | Origin header check failed | Verify origin is `*.openclawmachines.com` HTTPS or localhost |
| Terminal connects but no output | PTY server not running inside VM | Check `/var/log/openclaw-init.log` for PTY server start. Verify port 7681 is listening inside VM. |
| `su -l` hangs in VM | PAM `pam_keyinit.so` issue in Firecracker | Use `su -s /bin/bash username -c "..."` instead |
| Tests fail with "go: not found" | sudo does not inherit PATH | Use `make test-integration` which passes PATH explicitly |
| Frontend tests fail after adding provider | test-utils.tsx not updated | Add new provider to `AllProviders` in `frontend/src/test/test-utils.tsx` |
| Gateway takes >60s to respond | Normal -- Node.js module loading takes ~55s | `waitForGateway` timeout is 90s; do not assume failure before that |
| Stale deployment in production | Old code cached in Cloud Run or Worker | Run `make deploy-all` to redeploy everything |
| Environment variables not available in VM shell | `/etc/profile.d/` scripts only run in login shells | Source manually: `source /etc/profile.d/openclaw-identity.sh` |
| LLM API keys not working in VM | Dynamic provider env vars fetched from metadata | Check metadata service is reachable: `curl -sf http://192.168.100.1/v1/llm` inside VM |
| `pkill clawdbot` does not kill gateway | `clawdbot` is a shell wrapper that `exec`s into node | Use `pkill -f 'entry.js gateway'` |
| Proxy token collision with gateway token | Both use `?token=` query param | Worker sends proxy token as `X-Proxy-Token` header (not query param). Agent checks header first. |
| VM cannot reach internet | NAT rules not set up | Check `bridge.SetupNAT()` succeeded; verify iptables rules on host |
| Metadata nonce auth failing | Nonce not persisted or expired | Check `/run/ocm-nonce` inside VM; verify nonce matches what was passed on kernel cmdline |

---

## 8. Useful Commands

### Health Checks

```bash
# Backend health (Cloud Run)
curl -s https://api.openclawmachines.com/health | jq .

# Worker version
curl -s https://openclawmachines.com/__version | jq .

# Agent health (from host VM, port 9090 is control API)
curl -s -H "Authorization: Bearer $AGENT_TOKEN" http://localhost:9090/health | jq .

# Per-machine health through proxy (port 9091)
curl -s -H "X-Proxy-Token: $PROXY_TOKEN" \
     "http://localhost:9091/proxy/$MACHINE_ID/health" | jq .

# Gateway health directly from inside VM
curl -s http://192.168.100.2:3000/ | head -20

# Gateway health from host (bypass agent)
curl -s http://192.168.100.2:3000/

# Metadata service health
curl -s http://192.168.100.1/health
```

### Log Tailing

```bash
# Backend logs (Cloud Run)
make logs-backend
# or: gcloud run services logs tail ocm-backend --region=us-central1

# Agent logs (on host VM)
sudo journalctl -u ocm-agent -f

# Gateway logs (inside VM, via terminal)
tail -f /var/log/openclaw-gateway.log

# Init script log (inside VM)
cat /var/log/openclaw-init.log
```

### WebSocket Testing

```bash
# Test PTY server directly (from inside VM, bypasses all proxy layers)
websocat ws://192.168.100.2:7681/ws

# Test through agent proxy (bypasses tunnel)
websocat "ws://localhost:9091/proxy/$MACHINE_ID/terminal/ws" \
  --header "X-Proxy-Token:$PROXY_TOKEN"

# Test gateway WebSocket through agent proxy
websocat "ws://localhost:9091/proxy/$MACHINE_ID/gateway/" \
  --header "X-Proxy-Token:$PROXY_TOKEN"
```

### VM Inspection

```bash
# List all VMs on this host (control API)
curl -s -H "Authorization: Bearer $AGENT_TOKEN" http://localhost:9090/vms | jq .

# Get specific VM info
curl -s -H "Authorization: Bearer $AGENT_TOKEN" http://localhost:9090/vms/$MACHINE_ID | jq .

# Host info (capacity, region, machine list)
curl -s -H "Authorization: Bearer $AGENT_TOKEN" http://localhost:9090/info | jq .
```

### SSE Streams

```bash
# Provisioning progress (through proxy)
curl -N -H "X-Proxy-Token: $PROXY_TOKEN" \
     "http://localhost:9091/proxy/$MACHINE_ID/progress?machine_id=$MACHINE_ID"

# Log stream (through proxy)
curl -N -H "X-Proxy-Token: $PROXY_TOKEN" \
     "http://localhost:9091/proxy/$MACHINE_ID/logs?machine_id=$MACHINE_ID"
```

### CORS Verification

```bash
# Test CORS preflight for allowed origin
curl -sf -o /dev/null -w "%{http_code}" -X OPTIONS \
  https://e2etest.openclawmachines.com/testmachine/health \
  -H "Origin: https://openclawmachines.com" \
  -H "Access-Control-Request-Method: GET"
# Expected: 204

# Test CORS rejection for HTTP origin
curl -sf -o /dev/null -w "%{http_code}" -X OPTIONS \
  https://e2etest.openclawmachines.com/testmachine/health \
  -H "Origin: http://openclawmachines.com" \
  -H "Access-Control-Request-Method: GET"
# Expected: 403
```

### Snapshot Management

```bash
# Check current snapshot
make set-snapshot
# Output: Current snapshot: ocm-snapshot-XXXXXXXX-XXXXXX

# Set new snapshot
make set-snapshot NAME=ocm-snapshot-20260210-064918

# List available snapshots
gcloud compute snapshots list --format='value(name)' | grep ocm | head -10
```

### Running Individual Test Suites

```bash
# Go unit tests only (~10s)
make test-go

# Frontend tests only (~5s)
make test-frontend

# Worker tests only (~5s)
make test-worker

# TypeScript type checking
make typecheck

# All quality checks (lint + vet + vuln + shellcheck)
make check

# Single integration test
make test-integration-run TEST=TestProxy_TerminalWebSocket

# Production smoke tests (curl-based, no auth needed)
make test-e2e
```
