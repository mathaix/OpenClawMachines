# Terminal WebSocket Connectivity

*WebSocket terminals are a subset of the data plane. See [routing.md](routing.md) for the full request path and [architecture.md](architecture.md) for system overview.*

## Architecture (Data Plane WebSocket Path)

```
Browser (xterm.js)
  → CF Worker (WebSocketPair proxy)
    → CF Tunnel (cloudflared QUIC)
      → Agent proxy.go (gorilla/websocket bidirectional proxy)
        → PTY server (agent --pty-server, port 7681 inside MicroVM)
```

In dev mode (localhost), the frontend connects via the control-plane proxy instead:

```
Browser (xterm.js)
  → Vite dev proxy (ws: true)
    → Backend machine_terminal.go (gorilla/websocket proxy)
      → Agent proxy.go
        → PTY server (port 7681)
```

## Current Implementation

The PTY server is a custom Go WebSocket server (`backend/cmd/agent/ptyserver.go`) that
replaces the previous ttyd setup. It uses a simple text-prefix protocol:

- `'0'` + data — terminal I/O (input from client, output from server)
- `'1'` + JSON — resize messages (`{"columns": N, "rows": M}`)

No special WebSocket subprotocol is required. The agent's `handleTerminalProxy` uses the
generic `proxyBrowserWebSocket` function, which forwards any client-requested subprotocols
transparently but does not inject its own.

## Historical Note — ttyd Subprotocol Issue

Previously the terminal used ttyd (libwebsockets), which required the
`Sec-WebSocket-Protocol: tty` header. Without it, ttyd accepted the connection (101)
but silently sent no data. A dedicated `proxyTerminalWebSocket` function was created
to hardcode the `tty` subprotocol. This was removed when ttyd was replaced by the
custom PTY server in snapshot `ocm-snapshot-20260207-153436`.

---

## Testing WebSocket Locally

```bash
# Test PTY server directly (bypass agent, from inside VM):
websocat ws://192.168.100.2:7681/ws

# Test through agent proxy (bypass tunnel):
websocat "ws://localhost:9091/proxy/$MID/terminal/ws?token=$TOKEN"
```
