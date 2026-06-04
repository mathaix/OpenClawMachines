# Chat Page Design — `/chat`

## Summary

Add a `/chat` route to the frontend that lets users chat with their running OpenClaw machines via the gateway WebSocket protocol. The page has a machine selector at the top and a chat interface below, built by extracting and rewriting PinchChat components (copy + rewrite to Tailwind, not npm dependency). Auto-connects using existing auth — no manual gateway URL/token entry.

Mobile-responsive from the start for phone use without an app store.

## User Flow

1. User navigates to `/chat` (must be authenticated via Cloudflare Access)
2. Page fetches machines via `listMachines(account.id)` using `account` from `useAuth()`, filters to `status === "running"`
3. If one running machine: auto-selects it. If multiple: user picks from dropdown.
4. If selected machine has no `gateway_token`: show "Gateway not ready" state
5. Check gateway health via `getGatewayHealth()` before connecting — show "Gateway starting..." during the ~55s startup window
6. Chat auto-connects via WebSocket using `dataPlaneWsUrl()` + `machine.gateway_token`
7. Switching machines: clear messages, disconnect current WebSocket, connect to new machine. Switching back reloads history via `chat.history`.
8. If no running machines: show empty state with link to dashboard

## Architecture

### Route

- Path: `/chat`
- Protected route (same `ProtectedRoute` wrapper as dashboard)
- Added to `App.tsx` as a top-level route (not under `/dashboard` layout — full-screen experience)
- Navigation: add "Chat" link to dashboard sidebar/nav in `Layout.tsx` (alongside Dashboard, Settings)
- Back button navigates to `/dashboard`

### File Structure

```
frontend/src/
├── pages/
│   └── ChatPage.tsx              — Route component: machine selector + chat container
└── components/
    └── chat/
        ├── MachineSelector.tsx    — Dropdown of running machines
        ├── ChatContainer.tsx      — Manages gateway connection lifecycle + readiness
        ├── ChatMessageList.tsx    — Scrollable message list with auto-scroll
        ├── ChatMessage.tsx        — Single message bubble (markdown, tool calls, images, thinking blocks)
        ├── ChatInput.tsx          — Text input with send/abort, mobile keyboard handling
        └── useGateway.ts          — WebSocket hook (extracted from PinchChat, rewritten)
```

### Extracting from PinchChat

**Approach: copy and rewrite.** PinchChat is MIT-licensed (github.com/MarlBurroW/pinchchat). We copy the logic, rewrite UI to Tailwind, and adapt the hook API. No npm dependency on PinchChat.

1. **`useGateway` hook** — WebSocket connection state machine: connect, authenticate (token), handle events (`chat`, `tick`, `health`), send messages (`chat.send`), abort (`chat.abort`), session management (`sessions.list`, `chat.history`). Adapt to accept gateway URL + token as props rather than from PinchChat's own connection dialog.

2. **Message rendering** — Markdown rendering with syntax highlighting, tool call visualization (colored badges, expandable results), thinking/reasoning blocks with elapsed time, inline image rendering, streaming text display.

3. **Chat input** — Enter to send, Shift+Enter for newline, abort button during streaming, disabled state when disconnected.

### What NOT to Include

- PinchChat's connection dialog (auto-connect via machine selector)
- Sidebar / multi-session navigation (single chat per machine)
- Split-view session comparison
- Theme panel (use existing app theme system)
- Drag-and-drop session reordering
- PWA/offline features
- i18n system
- Notification sounds

### Gateway WebSocket Protocol

The gateway speaks **OpenClaw Gateway Protocol v3** (NOT JSON-RPC 2.0). The existing `gatewayRPC` in `api.ts` is a one-shot convenience wrapper; the real protocol uses typed frames:

- **Request**: `{type: "req", id: "<uuid>", method: "<method>", params: {...}}`
- **Response**: `{type: "res", id: "<uuid>", ok: true, payload: {...}}`
- **Event**: `{type: "event", event: "<name>", payload: {...}}`

PinchChat's `useGateway` already speaks this protocol correctly.

**Connect sequence:**
1. Open WebSocket to `wsUrl`
2. Gateway may send `connect.challenge` event with nonce
3. Send `connect` request: `{type: "req", id: "<uuid>", method: "connect", params: {minProtocol: 3, maxProtocol: 3, auth: {token: machine.gateway_token}, client: {id: "ocm-chat", version: "1.0", platform: "web", mode: "webchat"}, role: "operator", scopes: ["operator.admin"]}}`
4. Receive response with `payload.type === "hello-ok"`, includes snapshot (presence, health, sessions)
5. Ready to send/receive messages

**Session strategy:** On connect, call `sessions.list`. If sessions exist, resume the most recent one by calling `chat.history` with its `sessionKey`. If no sessions exist, send first message which auto-creates a session. No concept of a "default" session — it's always most-recent-or-new.

**WebSocket path:** The WebSocket goes through the existing proxy chain (browser → Cloudflare → Cloud Run → cloudflared → auth proxy → gateway on port 18789). This path already works for the GatewayDashboard iframe and `gatewayRPC`, so WebSocket upgrade is supported.

### Machine Selector

- Compact bar at top of page
- Dropdown populated from `listMachines()` filtered to `status === "running"` AND `gateway_token` is defined
- Shows machine name + status dot
- Polls every 10 seconds (matches MachineWorkspace pattern) to pick up status changes
- On selection change: disconnect current WebSocket, clear messages, connect to new machine
- If selected machine stops running: show disconnected state, refresh machine list
- Selected machine persisted to `sessionStorage` with key `ocm_chat_machine_${accountId}`

## Layout

### Desktop (>768px)

```
┌─────────────────────────────────────────────┐
│  ← Back    [Machine Selector ▼]    status   │  <- compact top bar
├─────────────────────────────────────────────┤
│                                             │
│              Message List                   │  <- scrollable, flex-grow
│              (auto-scroll to bottom)        │
│                                             │
│                                             │
├─────────────────────────────────────────────┤
│  [Type a message...              ] [Send]   │  <- sticky bottom
└─────────────────────────────────────────────┘
```

### Mobile (<768px)

```
┌──────────────────────────┐
│ ← [Machine ▼]    status  │  <- compact, single line
├──────────────────────────┤
│                          │
│     Message List         │  <- 100dvh minus bars
│                          │
│                          │
├──────────────────────────┤
│ [Message...]    [Send]   │  <- above virtual keyboard
└──────────────────────────┘
```

- Full viewport height: `100dvh` (dynamic viewport height handles mobile browser chrome)
- Input stays above virtual keyboard
- Touch-friendly tap targets (min 44px)
- Message bubbles: full-width on mobile, max-width on desktop

## Styling

- Tailwind CSS matching existing app design system
- Dark mode support via existing `ThemeProvider`
- No PinchChat CSS variables — rewrite to Tailwind utilities
- Message bubbles: user messages right-aligned (accent color), assistant messages left-aligned (neutral)
- Tool call badges: colored pills matching PinchChat's visual approach but in Tailwind
- Code blocks: syntax highlighted (use existing or add `prism-react-renderer`)

## State Management

- Machine list: local `useState` + polling (same as Dashboard)
- Selected machine: local `useState`, persisted to `sessionStorage` scoped by account ID
- Gateway connection: `useGateway` hook manages WebSocket lifecycle
- Messages: managed by `useGateway` hook (in-memory, reloaded via `chat.history` on reconnect/machine switch)
- No global state store needed

## Error Handling

- **Gateway not ready** (no `gateway_token` or health check fails): show "Gateway starting..." with spinner. Poll `getGatewayHealth()` every 5 seconds until healthy, then auto-connect.
- **WebSocket disconnect**: show reconnecting indicator, auto-reconnect with exponential backoff (from PinchChat's `useGateway`)
- **Machine stopped while chatting**: show "Machine stopped" banner, update machine list
- **No running machines**: empty state with "Start a machine" CTA linking to `/dashboard`
- **Tab backgrounded and returned**: `useGateway` reconnect handles this via WebSocket `onclose` → auto-reconnect

## Testing

- Unit test `useGateway` hook with mock WebSocket
- Unit test `MachineSelector` with mock machine list
- Component test message rendering (markdown, tool calls, thinking blocks)
- No E2E test needed initially (would require running gateway)

## Dependencies

New:
- `react-markdown` + `remark-gfm` — markdown rendering
- `prism-react-renderer` — syntax highlighting for code blocks

Existing (already in frontend):
- React Router (new route)
- Tailwind CSS
- `src/lib/api.ts` (machine list, data plane URLs, `getGatewayHealth`)
- `src/lib/auth.tsx` (account context via `useAuth()`)
- `src/components/Toast.tsx` (error notifications)
