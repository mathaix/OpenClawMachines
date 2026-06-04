# Design: `ocm browse` — Local Browser Controlled by AI

**Date:** 2026-03-06
**Status:** Design approved, ready for implementation
**Depends on:** SSH cert auth fix (deployed), existing CF tunnel infrastructure

## Problem

The AI agent controls a headless Chrome in a companion MicroVM. The user can't see what it's doing in real time, and the browser has no access to the user's authenticated sessions (cookies, passwords, etc). Users want the AI to drive their local browser — visible, using their logins.

## Solution

A CDP (Chrome DevTools Protocol) proxy on the bridge network that the gateway always connects to. The proxy routes CDP traffic to either the companion VM's Chrome (default) or the user's local Chrome (via reverse SSH tunnel). The `ocm browse` CLI command orchestrates the switch.

## Architecture

```
Normal mode (default):
  Gateway → bridgeIP:9222 → CDP proxy → browserVMIP:9222 → companion Chrome

Browse mode (ocm browse active):
  Gateway → bridgeIP:9222 → CDP proxy → vmIP:9222 → reverse SSH tunnel → user's Chrome
                                         ^^^^^^^^^^^
                                         tunnel binds inside main VM
```

The gateway never changes its `cdpUrl`. It always connects to `bridgeIP:9222`.

## Components

### 1. CDP Proxy (`backend/internal/cdpproxy/proxy.go`)

A TCP proxy on `bridgeGateway:9222`. Forwards all traffic (HTTP + WebSocket) transparently.

**Routing:** The bridge is shared across all VMs on a host. The proxy identifies which VM is connecting by source IP (same pattern as `apiproxy` and metadata server). It looks up the VM's browser target via the metadata server, which already tracks `BrowserVMIP` per VM.

**Target resolution per connection:**
1. Accept TCP connection, extract source IP
2. Query metadata server: `GetBrowserTarget(srcIP)` → returns target address
3. Dial target, bidirectional `io.Copy`

**Default target:** `browserVMIP:9222` (companion VM). Set at VM registration time in metadata.

**Remap:** `SetBrowserTarget(vmIP, target)` changes the target for a specific VM. Affects new connections only — existing WebSocket sessions continue to their original target until closed.

**Size:** ~80-100 lines. No CDP protocol parsing — just TCP forwarding.

### 2. Metadata Server Extensions (`backend/internal/metadata/`)

Add per-VM browser target tracking:

```go
// In VMConfig or a parallel map:
BrowserTarget string // default: browserVMIP:9222, overridden by remap
```

New methods:
- `GetBrowserTarget(vmIP string) string` — returns current CDP target for a VM
- `SetBrowserTarget(vmIP string, target string)` — remap (called by agent API)
- `ResetBrowserTarget(vmIP string)` — revert to default companion VM

### 3. Config Assembly Change (`backend/internal/configassembly/assembler.go`)

```go
// Line 273 — change from:
browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", params.BrowserVMIP)

// To:
browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", params.BridgeIP)
```

`BridgeIP` is already available as `o.bridge.Gateway` in the orchestrator. Pass it through `AssemblyParams`.

This is the only change that affects the companion VM flow — the gateway now connects to the proxy instead of directly to the companion VM. The proxy forwards transparently, so behavior is identical.

### 4. Agent Wiring (`backend/cmd/agent/main.go`)

Start CDP proxy after API proxy (line ~165), before orchestrator:

```go
cdpProxy := cdpproxy.New(metaSrv, bridgeGateway, 9222)
go cdpProxy.Start(ctx)
```

The proxy needs the metadata server reference to resolve source IPs to browser targets.

### 5. Remap Endpoint (`backend/internal/agentapi/handlers.go`)

On the control API (port 9090):

```
POST /vms/{machineID}/cdp-target
Body: { "target": "127.0.0.1:9222" }
Response: 200 OK

DELETE /vms/{machineID}/cdp-target
Response: 200 OK (reverts to companion VM)
```

The handler resolves `machineID` → `vmIP`, then calls `metaSrv.SetBrowserTarget(vmIP, target)` or `ResetBrowserTarget(vmIP)`.

### 6. CLI Command (`cli/internal/commands/machines_browse.go`)

```
ocm machines browse [NAME]
```

**Flags:**
- `--chrome-path` — path to Chrome binary (auto-detected on macOS/Linux)
- `--port` — local CDP port (default 9222)
- `--no-launch` — skip Chrome launch (user manages Chrome themselves)

**Flow:**

1. Resolve machine, check status == running
2. Generate SSH cert (same as `machines ssh`)
3. Build ProxyCommand (same as `machines ssh`)
4. Launch Chrome with `--remote-debugging-port=9222`
5. Start SSH with reverse tunnel: `-R 9222:localhost:9222 -N` (background, via `os/exec`)
6. Wait for tunnel to establish (~1-2s)
7. Call agent remap: `POST /vms/{id}/cdp-target {"target":"127.0.0.1:9222"}`
   - This call goes through the existing backend API proxy chain (CLI → Cloud Run → agent)
8. Print status: "AI is now controlling your local Chrome. Press Ctrl-C to stop."
9. Block on signal (SIGINT/SIGTERM)
10. Cleanup: revert remap (DELETE), kill SSH subprocess, optionally kill Chrome

**Subprocess management:** Uses `os/exec.Command` (not `syscall.Exec`) because the CLI must supervise multiple processes simultaneously and handle cleanup.

**Chrome detection:**
- macOS: `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`
- Linux: `google-chrome` or `chromium-browser` via `exec.LookPath`

## Data Flow Detail

### Remap call path

The CLI doesn't talk to the agent directly. It goes through the backend API:

```
CLI → POST https://api.openclawmachines.com/api/machines/{id}/cdp-target
     → Backend resolves machine → host
     → Backend calls agent: POST http://host:9090/vms/{machineID}/cdp-target
     → Agent calls metaSrv.SetBrowserTarget(vmIP, "127.0.0.1:9222")
     → Next CDP connection from gateway routes to 127.0.0.1:9222
     → Which is the reverse SSH tunnel → user's Chrome
```

This means a new backend endpoint is needed: `POST /api/machines/{id}/cdp-target` that proxies to the agent. Same pattern as existing machine management endpoints.

### Reverse tunnel binding

The SSH reverse tunnel `-R 9222:localhost:9222` binds inside the **main VM** (not the host). From the main VM's perspective, `127.0.0.1:9222` is the tunnel endpoint. The CDP proxy running on the host routes `vmIP:9222` (the main VM's bridge IP on port 9222) → which enters the VM via the tunnel → reaches user's local Chrome.

Wait — that's wrong. The reverse tunnel binds on the SSH server side, which is the **VM**. But the CDP proxy runs on the **host** (bridge gateway). The proxy would need to connect to `vmIP:9222` (the main VM's IP) to reach the tunnel endpoint. But the main VM isn't listening on 9222 by default — the tunnel creates that listener inside the VM.

Actually this is correct:
- Reverse tunnel `-R 9222:localhost:9222` makes the VM listen on `0.0.0.0:9222` (or `127.0.0.1:9222` depending on sshd config)
- CDP proxy on the host dials `vmIP:9222` → reaches the tunnel → reaches user's Chrome
- Need `GatewayPorts yes` in sshd_config or bind to `0.0.0.0` explicitly: `-R 0.0.0.0:9222:localhost:9222`

## Side Effects

| Side Effect | Mitigation |
|-------------|------------|
| Active CDP sessions drop on remap | Remap only affects new connections. Gateway reconnects automatically on next browser action. |
| User's cookies exposed to AI | Explicit opt-in only. CLI prints warning. Cleanup reverts on Ctrl-C. |
| Companion VM idles during browse | Acceptable. Stopping it adds complexity for marginal savings. |
| Port 9222 collision in VM | Reverse tunnel binds on main VM (not companion VM). No collision. |
| sshd GatewayPorts | Need `-R 0.0.0.0:9222:localhost:9222` or sshd `GatewayPorts yes` for host-reachable binding. |

## Testing

- **Unit tests:** CDP proxy target routing (mock metadata server, verify forwarding)
- **Gateway E2E:** Config assembly now produces `bridgeIP:9222` instead of `browserVMIP:9222`
- **Integration:** Full flow requires Firecracker — test remap endpoint + proxy forwarding
- **CLI:** Can't easily test `syscall.Exec` pattern; test arg construction and Chrome detection

## Implementation Order

1. CDP proxy package (new, ~80-100 lines)
2. Metadata server: browser target tracking methods
3. Config assembly: `cdpUrl` → `bridgeIP:9222`
4. Agent wiring: start CDP proxy in main.go
5. Agent API: remap endpoint
6. Backend API: proxy endpoint to agent
7. CLI: `machines_browse.go` command
8. sshd config: ensure `GatewayPorts` works for reverse tunnel

## Files to Create/Modify

| File | Action |
|------|--------|
| `backend/internal/cdpproxy/proxy.go` | **Create** — TCP proxy with per-VM target routing |
| `backend/internal/metadata/metadata.go` | **Modify** — add browser target tracking |
| `backend/internal/metadata/server_linux.go` | **Modify** — expose browser target methods |
| `backend/internal/configassembly/assembler.go` | **Modify** — cdpUrl points to bridgeIP |
| `backend/internal/configassembly/assembler_test.go` | **Modify** — update expected cdpUrl |
| `backend/cmd/agent/main.go` | **Modify** — start CDP proxy |
| `backend/internal/agentapi/handlers.go` | **Modify** — add remap endpoint |
| `backend/internal/agentapi/server.go` | **Modify** — register remap route |
| `backend/internal/api/server.go` | **Modify** — add proxy endpoint for remap |
| `cli/internal/commands/machines_browse.go` | **Create** — CLI command |
| `scripts/init-openclaw.sh` | **Modify** — ensure GatewayPorts in sshd config |
