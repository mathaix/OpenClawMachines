# CLI Integrations — Device Bridge & Tunneling

## Context

The CLI (`ocm`) already tunnels SSH through Cloudflare Access. The same transport can carry device streams (camera, mic, screen, clipboard) from the user's local machine to the agent VM. This gives remote AI agents access to local hardware without peer-to-peer networking or new infrastructure.

## What Exists Today

- SSH tunnel via CF Access (ProxyCommand + WebSocket) ✓
- Port forwarding works: `ocm machines ssh <name> -- -L 8080:localhost:18789` ✓
- Agent HTTP/WebSocket endpoints inside VM ✓
- Dashboard accessible via port forward ✓

## Device Bridge Concept

```
Local Mac (CLI)                         Remote VM (Agent)
──────────────                          ──────────────────

ocm connect <name> --devices camera,mic
  │
  ├─ Camera capture ──── WebSocket ────→ Agent receives frames
  ├─ Mic capture ──────── WebSocket ────→ Agent receives audio
  ├─ Screen capture ──── WebSocket ────→ Agent receives screenshots
  │
  └─ All data flows through existing CF tunnel (TLS encrypted)
      No new infrastructure. No peer-to-peer. No STUN/TURN.
```

## Video Approaches

| Approach | FPS | Latency | Infrastructure | Use Case |
|----------|-----|---------|---------------|----------|
| Frame-by-frame (JPEG over WebSocket) | 1-5 | 200ms-1s | None — uses existing tunnel | AI vision, monitoring, claw machine AI control |
| Video codec stream (H.264 over WebSocket) | 15-30 | 100-300ms | Encoder on client | Smoother viewing, human-assisted control |
| WebRTC | 30 | ~100ms | STUN/TURN servers, signaling | Real-time human control, live interaction |

**Recommendation:** Start with frame-by-frame. The AI doesn't need 30fps. Add codec streaming later if human real-time control is needed. WebRTC is a separate project.

## Proposed Commands

```bash
# One-shot captures
ocm capture screen <name>              # screenshot → agent
ocm capture photo <name>               # camera photo → agent
ocm capture clipboard <name>           # clipboard text → agent

# Streaming
ocm connect <name> --devices camera    # continuous camera frames → agent
ocm connect <name> --devices mic       # bidirectional audio
ocm connect <name> --devices camera,mic,screen  # multiple devices

# Convenience
ocm voice <name>                       # shortcut for mic + speaker
ocm browse <name>                      # local browser controlled by agent (CDP)

# Tunneling (already works today)
ocm machines ssh <name> -- -L 8080:localhost:18789   # dashboard
ocm machines ssh <name> -- -D 1080                   # SOCKS proxy
```

## Build Order (smallest useful slice first)

### Phase 1: One-shot captures (days)
- `ocm capture screen` — shells out to `screencapture` (macOS)
- `ocm capture photo` — shells out to `imagesnap` (macOS)
- `ocm capture clipboard` — `golang.design/x/clipboard` (cross-platform)
- Agent endpoint: `POST /device/capture` receives image/text
- No streaming, no CGo, no persistent connections

### Phase 2: Camera streaming (days)
- `ocm connect --devices camera` — capture frames at 1-5 FPS
- JPEG compress, send over WebSocket to agent
- Agent endpoint: `/device/camera` WebSocket handler
- Gateway tool: AI can request "take a photo" or "start watching"

### Phase 3: Voice (week)
- Bidirectional audio over WebSocket
- Opus encoding for compression
- macOS: AVFoundation for mic capture, speaker playback
- Agent endpoint: `/device/voice` WebSocket handler
- Gateway tool: AI can listen and speak

### Phase 4: Browser bridge (week+)
- `ocm browse` — opens local Chrome, controlled via CDP (chromedp)
- Agent sends navigation/click/type commands
- CLI relays via WebSocket
- Agent sees page via screenshots or DOM snapshots

## Protocol

JSON commands over WebSocket (from ocm-cli-requirements.md):

```
Agent → CLI:
{ "action": "capture_photo" }
{ "action": "capture_screen" }
{ "action": "start_stream", "device": "camera", "fps": 2 }
{ "action": "stop_stream", "device": "camera" }

CLI → Agent:
{ "type": "photo", "format": "jpeg", "data": "<base64>" }
{ "type": "screen", "format": "png", "data": "<base64>" }
{ "type": "frame", "device": "camera", "seq": 42, "data": "<base64>" }
```

## Architecture Decisions to Make

1. **macOS-only or cross-platform?** Phase 1 can shell out to macOS tools. Cross-platform needs CGo or pure Go libraries.
2. **Agent endpoint design** — new WebSocket endpoints on the agent, or multiplex over existing proxy connection?
3. **How does the AI request captures?** OpenClaw tool definition that triggers the agent to request from the CLI bridge.
4. **Frame rate and resolution** — configurable? Fixed? Adaptive based on bandwidth?
5. **Security** — explicit device opt-in per session. No persistent permissions. User sees all activity.

## Tunneling Quick Reference

These work today with no code changes:

```bash
# Dashboard in local browser
ocm machines ssh <name> -- -L 18789:localhost:18789
# then open localhost:18789

# Any VM port to local
ocm machines ssh <name> -- -L <local-port>:localhost:<vm-port>

# SOCKS proxy (browse as the VM)
ocm machines ssh <name> -- -D 1080
# set browser proxy to socks5://localhost:1080

# Expose local service to VM
ocm machines ssh <name> -- -R <vm-port>:localhost:<local-port>
```

## Browser Bridge

### Current Architecture

The gateway uses CDP (Chrome DevTools Protocol) to control a headless Chrome in a companion MicroVM. The `cdpUrl` is hardcoded from the companion VM IP during config assembly.

```
MicroVM (main)                    MicroVM (browser companion)
──────────────                    ──────────────────────────
Gateway (Node.js)                 Chrome --headless=new
  │                                 ├─ listens on 127.0.0.1:9223
  │                                 └─ socat relays 0.0.0.0:9222 → 127.0.0.1:9223
  └── cdpUrl: http://{browser-vm-ip}:9222
```

**Key files:**
- `backend/internal/configassembly/assembler.go` (lines 267-279) — injects `browser.cdpUrl`
- `scripts/init-browser.sh` — Chrome + socat relay in companion VM
- `backend/internal/network/network.go` — computes `BrowserVMIP`

### CDP Proxy Design (Provider Proxy Pattern)

Use the same pattern as the LLM provider proxy: the gateway always connects to a **fixed local URL**. The agent proxies to the actual browser, and the target can be remapped at runtime without restarting the gateway.

```
Provider proxy (existing):
  Gateway → http://{bridgeIP}:4000/anthropic → Agent proxy → real API
            ^^^^ fixed, never changes          remappable target

CDP proxy (new, same pattern):
  Gateway → http://{bridgeIP}:9222 → Agent CDP proxy → actual browser
            ^^^^ fixed, never changes  remappable target
```

**Config assembly change:**
```go
// Before (hardcoded to companion VM):
browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", params.BrowserVMIP)

// After (always points to bridge proxy):
browser["cdpUrl"] = fmt.Sprintf("http://%s:9222", params.BridgeIP)
```

The gateway never knows where the actual browser is. It always connects to `bridgeIP:9222`.

**Agent CDP proxy (~50-80 lines, new `cdpproxy` package):**
- TCP/WebSocket proxy on bridge IP port 9222
- Default target: companion browser VM (`{browserVMIP}:9222`)
- Remap endpoint: `POST /cdp/target` on agent API
- Forwards all CDP traffic (JSON commands + WebSocket frames) transparently

**Remap API:**
```
POST /cdp/target
{ "url": "localhost:9222" }    ← switch to local browser
{ "url": "{browserVMIP}:9222" } ← switch back to companion VM
```

### `ocm browse` — Local Browser Controlled by AI

User runs Chrome locally. AI controls it via CDP through the existing SSH tunnel. User's cookies, logins, and downloads are available to the AI.

```
Local Mac                              Remote VM
──────────                             ──────────────────

Chrome (--remote-debugging-port=9222)  Agent CDP proxy (bridgeIP:9222)
  │                                      │
  └──── reverse SSH tunnel ──────────────┘
        -R 9222:localhost:9222           target remapped to localhost:9222
                                         │
                                         Gateway connects to bridgeIP:9222
                                         (same URL as always — doesn't know
                                          it's now reaching user's Chrome)
```

**What `ocm browse <name>` does:**
1. Launch Chrome with `--remote-debugging-port=9222` (or attach to existing)
2. Open reverse SSH tunnel: `-R 9222:localhost:9222`
3. Call agent API: `POST /cdp/target { "url": "localhost:9222" }`
4. On disconnect (Ctrl-C): remap back to companion VM, close tunnel

**Why this is powerful:**
- AI browses with user's authenticated session — no separate login
- User watches AI navigate in real time (Chrome is local)
- Downloads go to user's machine
- No headless Chrome VM needed when using local browser
- No gateway restart — just a proxy target remap

### Side Effects & Mitigations

| Side Effect | Impact | Mitigation |
|-------------|--------|------------|
| **Active CDP sessions drop on remap** | Gateway loses WebSocket to old browser | Remap only at session boundaries (user-initiated `ocm browse`). Gateway reconnects automatically. |
| **Security surface** | User's local Chrome (cookies, sessions) exposed to AI | Explicit opt-in only (`ocm browse`). CLI shows warning. Tunnel closes on Ctrl-C. No persistent permissions. |
| **Companion VM idles during local browse** | Wasted resources | Acceptable. Stopping it adds complexity for marginal savings. |
| **Single target at a time** | Can't use companion + local simultaneously | Fine for the use case. One browser at a time. |
| **Port 9222 collision** | Reverse tunnel vs companion VM | No collision — reverse tunnel binds to main VM `127.0.0.1:9222`, companion VM listens on its own IP `{browserVMIP}:9222`. |
| **Proxy latency** | Extra hop for CDP traffic | Negligible — CDP is JSON commands + occasional screenshots, not video. |

### Implementation Plan

| Component | File | Work |
|-----------|------|------|
| CDP proxy | `backend/internal/cdpproxy/proxy.go` (new) | TCP/WebSocket proxy, ~50-80 lines. Default target from config, remap via channel. |
| Remap endpoint | `backend/internal/agentapi/handlers.go` | `POST /cdp/target` — validates URL, sends to proxy via channel |
| Config assembly | `backend/internal/configassembly/assembler.go` | Change `cdpUrl` to always use `bridgeIP:9222` |
| Agent startup | `backend/cmd/agent/main.go` or orchestrator | Start CDP proxy alongside API proxy |
| CLI command | `cli/internal/commands/machines_browse.go` (new) | Launch Chrome, reverse tunnel, remap call, cleanup on exit |

### Manual Workaround (Works Today — Partially)

```bash
# 1. Start Chrome with remote debugging
/Applications/Google\ Chrome.app/Contents/MacOS/Google\ Chrome \
  --remote-debugging-port=9222

# 2. Reverse tunnel CDP port into VM
ocm machines ssh <name> -- -R 9222:localhost:9222
```

This gets the tunnel working but the gateway still connects to the companion VM IP. The CDP proxy is the missing piece — once added, the manual approach works with a curl to the remap endpoint, and `ocm browse` wraps it into one command.

## Open Questions

- What camera/video frame rate does the claw machine AI actually need?
- Should the device bridge be part of the CLI or a separate daemon?
- How to handle multiple simultaneous device bridges to different machines?
- Should port forwarding get a convenience command (e.g., `ocm tunnel <name> 8080`)?
