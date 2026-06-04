# Simplified UI Redesign — Design Spec

## Goal

Replace the current multi-page dashboard with a clean, modern UI that matches the prototype (`frontend/public/prototype-simplified.html`). Fresh page-level components with the prototype's visual language, reusing existing infrastructure (API hooks, auth, Radix primitives).

## Key Decisions

- **Approach:** Fresh pages, shared primitives (Approach B). New visual layer + new page components; keep API client, auth, hooks, utilities.
- **No white-labeling** — deferred to a future phase.
- **Light/dark theme** with toggle — dark default, light mode for readability.
- **Responsive** — mobile-first chat, bottom tab bar on mobile, top navbar on desktop.
- **Prototype as north star** — design tokens, card styles, spacing, typography from the prototype. Note: the prototype includes partner/white-label views (Branding tab, Customers tab, Partner Login) which are out of scope for this phase. Theme toggle and responsive bottom tab bar are additions beyond the prototype.

---

## Architecture

### What We Keep

| Layer | Details |
|-------|---------|
| API client | `frontend/src/api/` — all hooks and fetch functions |
| Auth | Login, signup, session management, account switching |
| Router | React Router — restructured with new routes |
| Radix UI | Dialog, Toast, Select, Switch — restyled to match prototype |
| Utilities | `test-utils.tsx`, helpers, types |

### What We Replace

| Layer | Details |
|-------|---------|
| Layout | New `AppShell` with responsive nav (top bar desktop, bottom tabs mobile) |
| Pages | Dashboard, MachineDetail, Chat, Settings — all new |
| Theme system | New CSS custom property system with light/dark themes |
| Machine cards | New `MachineCard` matching prototype styling |
| Machine tabs | Reduced from 8 to 6: Overview, Model, Channels, Integrations, Browser, Backups |

### What We Remove

| Component | Reason |
|-----------|--------|
| Agents tab | Simplification |
| Plugins tab | Simplification |
| Skills tab | Simplification |
| Credentials tab | Folded into Model tab (provider cards) |
| Old layout/sidebar | Replaced by new AppShell |
| Multi-page machine creation | Replaced by modal (name + size) |

---

## Design System

### Design Tokens (from prototype)

Dark theme (default):
```css
--deep:         #07080d;    /* page background */
--surface:      #0c0e14;    /* elevated surfaces like nav, chat input area */
--card:         #12141e;    /* card backgrounds */
--card-hover:   #181b28;    /* card hover state */
--elevated:     #1a1d2c;    /* highest elevation */
--input:        #0f1119;    /* input field backgrounds */
--border:       #1c2035;    /* default borders */
--border-subtle:#151828;    /* subtle dividers */
--border-hover: #282d48;    /* interactive border hover */

--brand-400:    #fb923c;    /* light brand accent */
--brand-500:    #f97316;    /* primary brand */
--brand-600:    #ea580c;    /* brand buttons, active states */
--brand-700:    #c2410c;    /* brand hover */
--brand-glow:   rgba(249,115,22,0.06);  /* subtle brand background */

--green-400:    #4ade80;    /* success/running */
--green-500:    #22c55e;
--green-600:    #16a34a;
--green-glow:   rgba(74,222,128,0.15);  /* running status dot glow */
--yellow-400:   #facc15;    /* warnings */
--red-400:      #f87171;    /* errors/danger */
--red-600:      #dc2626;
--blue-400:     #60a5fa;    /* info/model accent */
--teal-400:     #2dd4bf;    /* channel accent */
--purple-400:   #a78bfa;    /* integration accent */

--text-primary:   #f0f1f4;
--text-secondary: #94979f;
--text-tertiary:  #5e626d;
--text-muted:     #3d4150;
```

Light theme:
```css
--deep:         #f8f9fb;
--surface:      #ffffff;
--card:         #ffffff;
--card-hover:   #f3f4f6;
--elevated:     #f9fafb;
--input:        #f3f4f6;
--border:       #e5e7eb;
--border-subtle:#f0f1f4;
--border-hover: #d1d5db;

--text-primary:   #111827;
--text-secondary: #4b5563;
--text-tertiary:  #6b7280;
--text-muted:     #9ca3af;

/* Brand and status colors stay the same */
```

### Typography

- **Body:** DM Sans (400, 500, 600, 700)
- **Mono:** JetBrains Mono (code, costs, terminal)
- **Scale:** 9px (hints) → 11px (labels, meta) → 12px (body small) → 13px (body) → 14px (card title) → 17px (modal title) → 20px (page title) → 22px (detail title)

### Spacing & Radius

- `--radius-sm: 6px` — inputs, small cards
- `--radius: 10px` — cards, panels
- `--radius-lg: 14px` — modals
- Card padding: `16px 18px`
- Page max-width: `920px` (centered)
- Header height: `52px`

### Shadows

```css
--shadow-card: 0 1px 3px rgba(0,0,0,0.3), 0 0 0 1px rgba(255,255,255,0.02) inset;
--shadow-elevated: 0 4px 16px rgba(0,0,0,0.4), 0 0 0 1px rgba(255,255,255,0.02) inset;
--shadow-modal: 0 24px 64px rgba(0,0,0,0.6), 0 0 0 1px rgba(255,255,255,0.04) inset;
```

Light theme shadows:
```css
--shadow-card: 0 1px 3px rgba(0,0,0,0.08), 0 0 0 1px rgba(0,0,0,0.04) inset;
--shadow-elevated: 0 4px 16px rgba(0,0,0,0.12), 0 0 0 1px rgba(0,0,0,0.04) inset;
--shadow-modal: 0 24px 64px rgba(0,0,0,0.2), 0 0 0 1px rgba(0,0,0,0.06) inset;
```

### Visual Effects (from prototype)

- **Noise texture:** Subtle SVG fractal noise overlay (`body::before`, opacity 0.015, pointer-events: none)
- **Ambient glow:** Top-left radial gradient in brand color (`body::after`, 600x600px, opacity ~3%)
- Both effects are dark-theme only (hidden or reduced in light theme)

---

## Navigation

### Desktop (>=768px)

**Sticky top navbar** (52px height, glassmorphism blur):
```
[Logo: OpenClaw Machines]  [Dashboard]  [Chat]  [Settings]     [🌙/☀️]  [user@email]
```

- Active nav link: brand color + subtle brand glow background
- User pill: rounded, border, email display — click opens UserMenu dropdown
- Theme toggle: sun/moon icon button
- At 768-1023px (tablet): same top nav, but chat has no side panel

### Mobile (<768px)

**Bottom tab bar** (fixed, 56px) — this is a new addition beyond the prototype:
```
[Dashboard]  [Chat]  [Settings]
```

- Icons + labels
- Active tab: brand color
- Top area: minimal header with logo + user avatar (tap for UserMenu)
- Top navbar hidden on mobile, replaced by bottom tabs

### Machine Detail Navigation

Desktop breadcrumb above detail view: `← Dashboard / machine-name`
Mobile: back arrow + machine name in header

---

## Views

### 1. Dashboard

**Page header:** "Machines" + count badge + "New Machine" button

**Machine cards** in a vertical list (not grid — matches prototype):
- Card shows: name, status badge (running/stopped/starting with animated dot), model name, channel icons, created date
- Quick actions row at bottom: Workspace, Chat, Stop (running) or Start (stopped)
- Hover: slight lift (`translateY(-1px)`), elevated shadow, brighter border

**Empty state:** Centered message + "Create your first machine" CTA

**Create modal** (Radix Dialog):
- Name input
- Size picker: 3-column grid (Small/Standard/Pro with specs)
- Create + Cancel buttons

### 2. Machine Detail

**Back link** + detail header (machine name + status badge + action buttons)

**Top actions bar:** Start/Stop, Chat, Delete buttons

**Tab bar** (horizontal underline style, scrollable on mobile):

| Tab | Content |
|-----|---------|
| **Overview** | Setup cards (2x2 grid: Model, Channels, Integrations, Browser — each with completion state). Usage card (messages, tokens, cost). Machine info fields (ID, size, created, uptime). |
| **Model** | Primary model selector (dropdown). Credit balance display. Provider cards (Anthropic, OpenAI, Google, OpenRouter) — shows connected status, key mask, connect/configure actions. Subscription vs BYOK sections. |
| **Channels** | Channel cards grid: Webchat, Telegram, WhatsApp, Discord, Slack. Each shows status (active/setup needed), configure button. Webchat always available. Others need token config. |
| **Integrations** | Connected services list. Empty state with app icons (Gmail, Slack, GitHub, Sheets, Notion) + "Connect via chat" prompt pills. |
| **Browser** | Browser provider options (Built-in, BrowserBase, custom). Toggle on/off. noVNC viewer when active. |
| **Backups** | Existing backup/restore UI, restyled to match new design tokens. |

**Machine states:** Running (green pulsing dot), Stopped (gray dot), Starting/Provisioning (yellow dot, spinner). Error states show red dot + error message banner at top of detail view.

**Credit warning banners:** Shown on Overview tab and above chat input when credit usage exceeds 80% (yellow) or 95% (red). Shows remaining balance and link to add credits.

**Setup cards** on Overview are clickable — navigate to the relevant tab.

### 3. Chat

**Entry points:**
- Top-level "Chat" nav → machine picker (list of machines) → full chat
- Machine card "Chat" button → direct to chat
- Machine detail "Chat" action → direct to chat

**Desktop layout** (split pane):
```
[Chat pane (flex:1)]  [Side panel (380px, collapsible)]
  - Header               - Tabs: Desktop | Terminal | Files
  - Messages              - noVNC / terminal / file browser
  - Input area
```

**Mobile layout** (full screen):
```
[← machine-name]
[Messages (scrollable)]
[Input bar (pinned bottom)]
```

- No side panel on mobile
- Back button returns to machine picker or previous view
- Input bar stays above keyboard

**Chat content:** This embeds the ControlUI webchat via iframe. The prototype shows a native chat UI with message bubbles, tool cards, and thinking indicators — this is aspirational. For this phase, we embed the existing ControlUI webchat as an iframe filling the chat pane. The iframe URL is the machine's webchat endpoint (e.g., `https://{machine-slug}.openclawmachines.com`). The side panel (desktop only) shows the machine's browser/terminal/files if available.

**Machine picker** (shown when accessing Chat from nav with multiple machines):
- Vertical list of compact machine rows: status dot + machine name + model name + last activity time
- Tap to enter chat for that machine
- If only one machine, skip picker and go straight to chat
- Empty state: "No machines yet" + link to Dashboard to create one

### 4. Settings

**Settings tabs** (horizontal, underline style): Profile, Members, Usage, Billing

Restyled from existing settings pages using new design tokens. Card-based layout with settings rows.

| Tab | Content |
|-----|---------|
| **Profile** | Name, email, avatar, theme preference toggle, password/auth |
| **Members** | Team member list, invite, role management |
| **Usage** | Token usage, cost breakdown per machine, billing period |
| **Billing** | Plan info, payment method, invoices |

**Theme toggle** also accessible here in Profile settings (in addition to the navbar icon).

### Destructive Actions

All destructive actions (Stop machine, Delete machine) require a confirmation dialog (Radix AlertDialog):
- Stop: "Stop {machine-name}? This will shut down the VM."
- Delete: "Delete {machine-name}? This cannot be undone." + type machine name to confirm.

### Loading States

- **Dashboard:** Skeleton cards (3 placeholder cards with shimmer animation)
- **Machine detail:** Skeleton header + tab content area
- **Chat connecting:** "Connecting to {machine-name}..." spinner in chat pane
- **Machine starting:** Status badge shows "Starting" with yellow spinner, action buttons disabled
- **API errors:** Toast notification with error message, retry action where applicable

### User Menu

Clicking the user pill in the navbar opens a dropdown (Radix DropdownMenu):
- Account name + email
- Theme toggle (redundant with navbar icon, convenient)
- Account switcher (if user has multiple accounts)
- Logout

---

## Theme System

### Implementation

- CSS custom properties on `:root` (dark) and `[data-theme="light"]` (light)
- Theme preference stored in `localStorage` key `ocm-theme`
- On load: check localStorage → apply `data-theme` attribute to `<html>`
- Toggle component: `ThemeToggle` — sun/moon icon, animates on switch
- Respects `prefers-color-scheme` as initial default if no localStorage value

### Tailwind Integration

Map design tokens to Tailwind config so utility classes work:
```js
colors: {
  deep: 'var(--deep)',
  surface: 'var(--surface)',
  card: 'var(--card)',
  // ... etc
}
```

This lets us use `bg-card`, `text-primary`, `border-border` etc. in components.

---

## Responsive Breakpoints

| Breakpoint | Layout |
|------------|--------|
| `>= 1024px` | Full desktop — top nav, split-pane chat, side panel |
| `768px - 1023px` | Tablet — top nav, chat without side panel |
| `< 768px` | Mobile — bottom tab bar, full-screen chat, stacked cards |

### Mobile-Specific Behaviors

- Machine detail tabs: horizontal scroll strip
- Setup cards: single column stack
- Channel/provider cards: single column
- Create modal: full-width with padding
- Chat: full viewport, no chrome except thin header + input

---

## File Structure

### New Files

```
frontend/src/
├── styles/
│   └── theme.css              # Design tokens (dark + light), base styles, animations
├── components/
│   ├── AppShell.tsx            # Layout wrapper: navbar (desktop) + bottom tabs (mobile) + main area
│   ├── ThemeToggle.tsx         # Sun/moon toggle, reads/writes localStorage
│   ├── MachineCard.tsx         # Dashboard card (replace existing)
│   ├── StatusBadge.tsx         # Running/stopped/starting badge with animated dot
│   ├── SetupCard.tsx           # Overview tab setup cards
│   ├── CreateMachineModal.tsx  # Simplified create modal (replace existing)
│   ├── MachinePicker.tsx       # Chat entry — list machines to pick
│   └── UserMenu.tsx            # Dropdown: account info, theme, logout
├── pages/
│   ├── Dashboard.tsx           # Machine list + create
│   ├── MachineDetail.tsx       # Tabbed detail view (6 tabs)
│   ├── Chat.tsx                # Chat view — picker + embedded webchat
│   └── Settings.tsx            # Restyled settings
└── pages/machine-tabs/
    ├── OverviewTab.tsx         # Setup cards + usage + info
    ├── ModelTab.tsx            # Model picker + providers
    ├── ChannelsTab.tsx         # Channel cards
    ├── IntegrationsTab.tsx     # Connected services
    ├── BrowserTab.tsx          # Browser config + viewer
    └── BackupsTab.tsx          # Backup/restore (reuse existing logic)
```

### Modified Files

```
frontend/src/App.tsx            # New route structure
frontend/src/index.css          # Import theme.css, remove old styles
frontend/tailwind.config.js     # Add design token colors
```

### Removed Files (or deprecated)

All existing page components under the old route structure that are replaced. Keep the API layer, auth, hooks, and utility files.

---

## Route Structure

```
/                       → redirect to /dashboard
/login                  → existing login (restyled)
/signup                 → existing signup (restyled)
/dashboard              → Dashboard (machine list)
/machines/:id           → MachineDetail (tabbed, default to Overview)
/machines/:id/:tab      → MachineDetail (specific tab)
/chat                   → Chat (machine picker or direct if single)
/chat/:machineId        → Chat (specific machine)
/settings               → Settings (default tab)
/settings/:tab          → Settings (specific tab)
/workspace/:id          → existing workspace view (keep as-is for now)
```

---

## Component Details

### AppShell

```tsx
<AppShell>
  {/* Desktop: top navbar */}
  {/* Mobile: bottom tab bar */}
  <main>{children}</main>
</AppShell>
```

- Uses `useMediaQuery` or CSS to switch between top nav and bottom tabs
- Navbar: logo, nav links (Dashboard, Chat, Settings), theme toggle, user menu
- Bottom tabs: icons + labels for Dashboard, Chat, Settings
- Active state: brand color

### MachineCard

Props: `machine: Machine`

Displays:
- Header: name + StatusBadge
- Meta row: model name, channel icons, created date
- Actions row (border-top): Start/Stop button, Chat button

On click (card body): navigate to `/machines/:id`
Chat button: navigate to `/chat/:id`

### Chat Page

The chat view embeds the ControlUI webchat via iframe. The URL is the machine's webchat endpoint.

Desktop: split layout with collapsible side panel (Desktop/Terminal/Files tabs)
Mobile: full-screen iframe with thin header bar

### ThemeToggle

- Reads initial theme from localStorage or `prefers-color-scheme`
- Sets `data-theme` attribute on `document.documentElement`
- Persists to localStorage on toggle
- Renders sun icon (light mode) or moon icon (dark mode)
- Smooth icon transition animation

---

## Interaction Patterns

### Animations (from prototype)

- **fadeUp:** `opacity: 0, translateY(6px) → opacity: 1, translateY(0)` — used for view transitions, tab panels
- **modalIn:** `opacity: 0, scale(0.96), translateY(8px) → opacity: 1, scale(1), translateY(0)` — modal entrance
- **pulse:** `opacity: 1 → 0.4 → 1` — running status dot
- **Card hover:** `translateY(-1px)` + elevated shadow

### Transitions

- All interactive elements: `transition: all 0.15s ease`
- View changes: fadeUp animation (0.3s cubic-bezier)
- Side panel collapse: `width 0.25s ease`

---

## What This Spec Does NOT Cover

- White-label / partner branding (deferred)
- New API endpoints (we use existing ones)
- Backend changes (none needed)
- ControlUI modifications (embedded as-is via iframe)
- Billing/pricing UI changes
