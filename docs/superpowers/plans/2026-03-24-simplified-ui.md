# Simplified UI Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current multi-page dashboard with a clean, responsive UI matching the prototype design — new visual layer, fresh pages, light/dark theming.

**Architecture:** Fresh page-level components with the prototype's design tokens and visual language. Keep existing infrastructure (API hooks, auth, Radix primitives, operations). Modify Tailwind config to use CSS custom properties for theming. New AppShell layout with responsive nav.

**Tech Stack:** React 18, TypeScript, Tailwind CSS 3, Radix UI (Dialog, DropdownMenu, AlertDialog, Toast), Vite, Vitest, DM Sans + JetBrains Mono fonts.

**Spec:** `docs/superpowers/specs/2026-03-24-simplified-ui-design.md`
**Prototype:** `frontend/public/prototype-simplified.html`

---

## File Map

### New Files

| File | Responsibility |
|------|---------------|
| `frontend/src/styles/theme.css` | CSS custom properties (dark/light), base styles, animations, visual effects |
| `frontend/src/components/AppShell.tsx` | Layout wrapper: top navbar (desktop) + bottom tabs (mobile) + main content area |
| `frontend/src/components/UserMenu.tsx` | Radix DropdownMenu: account info, theme toggle, account switcher, logout |
| `frontend/src/components/StatusBadge.tsx` | Status indicator: running/stopped/starting/error with animated dots |
| `frontend/src/components/SetupCard.tsx` | Clickable setup card for Overview tab (icon, title, description, completion state) |
| `frontend/src/components/SkeletonCard.tsx` | Shimmer loading placeholder for machine cards |
| `frontend/src/components/MachinePicker.tsx` | Chat entry — compact machine list for selecting which to chat with |
| `frontend/src/components/ThemeToggle.tsx` | Standalone theme toggle button (reusable in navbar + Settings) |
| `frontend/src/components/ConfirmDialog.tsx` | Radix AlertDialog wrapper for destructive action confirmations |
| `frontend/src/pages/machine-tabs/OverviewTab.tsx` | Setup cards grid + usage card + machine info |
| `frontend/src/pages/machine-tabs/ModelTab.tsx` | Model picker + provider cards |
| `frontend/src/pages/machine-tabs/ChannelsTab.tsx` | Channel configuration cards |
| `frontend/src/pages/machine-tabs/IntegrationsTab.tsx` | Connected services + empty state |
| `frontend/src/pages/machine-tabs/BrowserTab.tsx` | Browser toggle + provider options |
| `frontend/src/pages/machine-tabs/BackupsTab.tsx` | Thin wrapper restyling existing BackupsTab |

### Modified Files

| File | Changes |
|------|---------|
| `frontend/src/index.css` | Replace component-layer styles with `@import './styles/theme.css'`, keep blog styles |
| `frontend/tailwind.config.cjs` | Add CSS custom property references for theming colors |
| `frontend/src/lib/theme.tsx` | Update to set `data-theme` attribute + `dark` class (dual approach) |
| `frontend/src/App.tsx` | New route structure with AppShell layout |
| `frontend/src/pages/Dashboard.tsx` | Rewrite with new design tokens, vertical card list, skeleton loading |
| `frontend/src/components/MachineCard.tsx` | Rewrite to match prototype card styling |
| `frontend/src/components/CreateMachineModal.tsx` | Simplify to name + size picker |
| `frontend/src/pages/MachineView.tsx` | Rewrite as MachineDetail with 6 tabs |
| `frontend/src/pages/ChatPage.tsx` | Rewrite with iframe webchat + machine picker |
| `frontend/src/pages/Settings.tsx` | Restyle with new tabs (Profile, Members, Usage, Billing) |
| `frontend/src/components/ThemeToggle.tsx` | Create as standalone reusable component (used in AppShell navbar + Settings Profile) |
| `frontend/src/test/test-utils.tsx` | No changes expected (providers stay the same) |

---

## Task 1: Design Tokens & Theme CSS

**Files:**
- Create: `frontend/src/styles/theme.css`
- Modify: `frontend/src/index.css`
- Modify: `frontend/tailwind.config.cjs`
- Modify: `frontend/src/lib/theme.tsx`

This task establishes the visual foundation. Everything else builds on these tokens.

- [ ] **Step 1: Create `theme.css` with design tokens**

Create `frontend/src/styles/theme.css`:

```css
/* ═══════════════════════════════════════════════
   DESIGN TOKENS — Dark (default)
   ═══════════════════════════════════════════════ */
:root {
  --deep:         #07080d;
  --surface:      #0c0e14;
  --card:         #12141e;
  --card-hover:   #181b28;
  --elevated:     #1a1d2c;
  --input:        #0f1119;
  --border:       #1c2035;
  --border-subtle:#151828;
  --border-hover: #282d48;

  --brand-400:    #fb923c;
  --brand-500:    #f97316;
  --brand-600:    #ea580c;
  --brand-700:    #c2410c;
  --brand-glow:   rgba(249,115,22,0.06);

  --green-400:    #4ade80;
  --green-500:    #22c55e;
  --green-600:    #16a34a;
  --green-glow:   rgba(74,222,128,0.15);
  --yellow-400:   #facc15;
  --red-400:      #f87171;
  --red-600:      #dc2626;
  --blue-400:     #60a5fa;
  --teal-400:     #2dd4bf;
  --purple-400:   #a78bfa;

  --text-primary:   #f0f1f4;
  --text-secondary: #94979f;
  --text-tertiary:  #5e626d;
  --text-muted:     #3d4150;

  --font-body: 'DM Sans', system-ui, -apple-system, sans-serif;
  --font-mono: 'JetBrains Mono', 'SF Mono', 'Fira Code', monospace;

  --radius-sm: 6px;
  --radius: 10px;
  --radius-lg: 14px;

  --shadow-card: 0 1px 3px rgba(0,0,0,0.3), 0 0 0 1px rgba(255,255,255,0.02) inset;
  --shadow-elevated: 0 4px 16px rgba(0,0,0,0.4), 0 0 0 1px rgba(255,255,255,0.02) inset;
  --shadow-modal: 0 24px 64px rgba(0,0,0,0.6), 0 0 0 1px rgba(255,255,255,0.04) inset;
}

/* ═══════════════════════════════════════════════
   DESIGN TOKENS — Light
   ═══════════════════════════════════════════════ */
[data-theme="light"] {
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

  --shadow-card: 0 1px 3px rgba(0,0,0,0.08), 0 0 0 1px rgba(0,0,0,0.04) inset;
  --shadow-elevated: 0 4px 16px rgba(0,0,0,0.12), 0 0 0 1px rgba(0,0,0,0.04) inset;
  --shadow-modal: 0 24px 64px rgba(0,0,0,0.2), 0 0 0 1px rgba(0,0,0,0.06) inset;
}

/* ═══════════════════════════════════════════════
   BASE STYLES
   ═══════════════════════════════════════════════ */
body {
  font-family: var(--font-body);
  background: var(--deep);
  color: var(--text-primary);
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  min-height: 100vh;
  line-height: 1.5;
}

/* Noise texture (dark only) */
:root:not([data-theme="light"]) body::before {
  content: '';
  position: fixed;
  inset: 0;
  opacity: 0.015;
  background-image: url("data:image/svg+xml,%3Csvg viewBox='0 0 256 256' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='noise'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='4' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23noise)'/%3E%3C/svg%3E");
  pointer-events: none;
  z-index: 9999;
}

/* Ambient glow (dark only) */
:root:not([data-theme="light"]) body::after {
  content: '';
  position: fixed;
  top: -200px;
  left: -200px;
  width: 600px;
  height: 600px;
  background: radial-gradient(circle, rgba(249,115,22,0.03) 0%, transparent 70%);
  pointer-events: none;
  z-index: 0;
}

/* ═══════════════════════════════════════════════
   SCROLLBAR
   ═══════════════════════════════════════════════ */
::-webkit-scrollbar { width: 5px; height: 5px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb { background: var(--border); border-radius: 3px; }
::-webkit-scrollbar-thumb:hover { background: var(--border-hover); }

/* ═══════════════════════════════════════════════
   ANIMATIONS
   ═══════════════════════════════════════════════ */
@keyframes fadeUp {
  from { opacity: 0; transform: translateY(6px); }
  to { opacity: 1; transform: translateY(0); }
}

@keyframes modalIn {
  from { opacity: 0; transform: scale(0.96) translateY(8px); }
  to { opacity: 1; transform: scale(1) translateY(0); }
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

@keyframes shimmer {
  0% { background-position: -200% 0; }
  100% { background-position: 200% 0; }
}

.animate-fade-up { animation: fadeUp 0.3s cubic-bezier(0.16, 1, 0.3, 1); }
.animate-modal-in { animation: modalIn 0.25s cubic-bezier(0.16, 1, 0.3, 1); }
.animate-pulse-dot { animation: pulse 2.5s ease-in-out infinite; }
.animate-shimmer {
  background: linear-gradient(90deg, var(--card) 25%, var(--card-hover) 50%, var(--card) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.5s infinite;
}
```

- [ ] **Step 2: Update `index.css`**

Replace the content of `frontend/src/index.css`. Keep `@tailwind` directives and blog styles, but remove the `@layer components` block (status badges, buttons, card — these are now in `theme.css` or Tailwind utilities). Add `@import './styles/theme.css';` after the tailwind imports.

Note: `@import` must come BEFORE `@tailwind` directives for PostCSS. However, with Vite's CSS handling, an alternative is to import `theme.css` in `main.tsx` before `index.css`. The implementer should verify which approach works:

**Option A** (preferred — import in main.tsx):
In `frontend/src/main.tsx`, add `import './styles/theme.css';` before the `index.css` import.

**Option B** (if Vite handles @import correctly):
```css
@import './styles/theme.css';

@tailwind base;
@tailwind components;
@tailwind utilities;

/* ── Blog article custom elements ────────── */
/* (keep all existing .blog-content styles unchanged) */
```

Either way, the body styles in `index.css` (`@apply bg-gray-50 text-gray-900 dark:bg-surface-deep dark:text-gray-100 font-body antialiased;`) should be removed since `theme.css` now handles body styling via CSS custom properties.

- [ ] **Step 3: Update Tailwind config**

Modify `frontend/tailwind.config.cjs` to reference CSS custom properties so Tailwind utilities like `bg-card`, `text-primary` work with the theme system:

```js
/** @type {import('tailwindcss').Config} */
module.exports = {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // CSS custom property references — these change with theme
        deep: 'var(--deep)',
        surface: 'var(--surface)',
        card: { DEFAULT: 'var(--card)', hover: 'var(--card-hover)' },
        elevated: 'var(--elevated)',
        input: 'var(--input)',
        border: { DEFAULT: 'var(--border)', subtle: 'var(--border-subtle)', hover: 'var(--border-hover)' },
        // Brand colors (same in both themes)
        brand: {
          50: "#fff7ed", 100: "#ffedd5", 200: "#fed7aa", 300: "#fdba74",
          400: "var(--brand-400)", 500: "var(--brand-500)",
          600: "var(--brand-600)", 700: "var(--brand-700)",
          glow: 'var(--brand-glow)',
        },
        // Semantic text colors
        'text-primary': 'var(--text-primary)',
        'text-secondary': 'var(--text-secondary)',
        'text-tertiary': 'var(--text-tertiary)',
        'text-muted': 'var(--text-muted)',
        // Status colors
        green: { 400: 'var(--green-400)', 500: 'var(--green-500)', 600: 'var(--green-600)' },
        yellow: { 400: 'var(--yellow-400)' },
        red: { 400: 'var(--red-400)', 600: 'var(--red-600)' },
        blue: { 400: 'var(--blue-400)' },
        teal: { 400: 'var(--teal-400)' },
        purple: { 400: 'var(--purple-400)' },
      },
      fontFamily: {
        body: ['"DM Sans"', "system-ui", "sans-serif"],
        mono: ['"JetBrains Mono"', '"SF Mono"', "monospace"],
      },
      borderRadius: {
        sm: 'var(--radius-sm)',
        DEFAULT: 'var(--radius)',
        lg: 'var(--radius-lg)',
      },
      boxShadow: {
        card: 'var(--shadow-card)',
        elevated: 'var(--shadow-elevated)',
        modal: 'var(--shadow-modal)',
      },
      typography: {
        // keep existing typography config
        DEFAULT: { css: { maxWidth: "680px", fontSize: "1.125rem", lineHeight: "1.7" } },
        invert: {
          css: {
            "--tw-prose-body": "#d1d5db", "--tw-prose-headings": "#f9fafb",
            "--tw-prose-links": "#2dd4bf", "--tw-prose-bold": "#f3f4f6",
            "--tw-prose-quotes": "#9ca3af", "--tw-prose-quote-borders": "#ef4444",
            "--tw-prose-counters": "#9ca3af", "--tw-prose-bullets": "#6b7280",
            "--tw-prose-hr": "#374151", "--tw-prose-th-borders": "#374151",
            "--tw-prose-td-borders": "#1f2937", "--tw-prose-code": "#f9fafb",
            "--tw-prose-pre-bg": "#0a0f1e", "--tw-prose-pre-code": "#d1d5db",
          },
        },
      },
    },
  },
  plugins: [require("@tailwindcss/typography")],
}
```

- [ ] **Step 4: Update theme provider to set `data-theme` attribute**

Modify `frontend/src/lib/theme.tsx` — the existing `applyTheme` function sets the `dark` class. Update it to also set `data-theme` attribute so CSS custom properties switch:

In `applyTheme()`, change to:
```ts
function applyTheme(resolved: ResolvedTheme) {
  const root = document.documentElement;
  root.setAttribute("data-theme", resolved);
  if (resolved === "dark") {
    root.classList.add("dark");
  } else {
    root.classList.remove("dark");
  }
}
```

- [ ] **Step 5: Verify the theme system works**

Run: `cd frontend && npx vite build --mode development 2>&1 | head -20`
Expected: Build succeeds with no CSS errors.

Run: `make typecheck`
Expected: No type errors in theme.tsx.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/styles/theme.css frontend/src/index.css frontend/tailwind.config.cjs frontend/src/lib/theme.tsx
git commit -m "feat(ui): add design tokens and dual-theme CSS system

Add theme.css with dark/light CSS custom properties matching the prototype.
Update Tailwind config to reference custom properties. Update ThemeProvider
to set data-theme attribute for CSS variable switching."
```

---

## Task 2: StatusBadge, SkeletonCard & ThemeToggle Components

**Files:**
- Create: `frontend/src/components/StatusBadge.tsx`
- Create: `frontend/src/components/SkeletonCard.tsx`
- Create: `frontend/src/components/ThemeToggle.tsx`

Small, self-contained components with no dependencies on other new components.

- [ ] **Step 1: Create StatusBadge**

Create `frontend/src/components/StatusBadge.tsx`:

```tsx
import { clsx } from "clsx";

type MachineStatus = "running" | "stopped" | "provisioning" | "starting" | "error" | string;

interface StatusBadgeProps {
  status: MachineStatus;
  className?: string;
}

const statusConfig: Record<string, { bg: string; text: string; dotColor: string; glow?: boolean }> = {
  running: {
    bg: "bg-[rgba(74,222,128,0.08)]",
    text: "text-green-400",
    dotColor: "bg-green-400",
    glow: true,
  },
  stopped: {
    bg: "bg-[rgba(107,114,128,0.1)]",
    text: "text-text-tertiary",
    dotColor: "bg-text-muted",
  },
  provisioning: {
    bg: "bg-[rgba(250,204,21,0.08)]",
    text: "text-yellow-400",
    dotColor: "bg-yellow-400",
  },
  starting: {
    bg: "bg-[rgba(250,204,21,0.08)]",
    text: "text-yellow-400",
    dotColor: "bg-yellow-400",
  },
  error: {
    bg: "bg-[rgba(248,113,113,0.08)]",
    text: "text-red-400",
    dotColor: "bg-red-400",
  },
};

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const config = statusConfig[status] ?? statusConfig.stopped;
  return (
    <span
      className={clsx(
        "inline-flex items-center gap-[5px] text-[11px] font-medium px-2 py-[2px] rounded-full capitalize",
        config.bg,
        config.text,
        className
      )}
    >
      <span
        className={clsx(
          "w-[5px] h-[5px] rounded-full",
          config.dotColor,
          config.glow && "shadow-[0_0_6px_var(--green-glow)] animate-pulse-dot"
        )}
      />
      {status}
    </span>
  );
}
```

- [ ] **Step 2: Create SkeletonCard**

Create `frontend/src/components/SkeletonCard.tsx`:

```tsx
export function SkeletonCard() {
  return (
    <div className="bg-card border border-border rounded-[var(--radius)] p-4 shadow-card">
      <div className="flex items-center justify-between mb-3">
        <div className="h-4 w-32 rounded animate-shimmer" />
        <div className="h-5 w-16 rounded-full animate-shimmer" />
      </div>
      <div className="flex gap-3 mb-3">
        <div className="h-3 w-24 rounded animate-shimmer" />
        <div className="h-3 w-16 rounded animate-shimmer" />
      </div>
      <div className="border-t border-border-subtle pt-3 flex gap-2">
        <div className="h-7 w-20 rounded animate-shimmer" />
        <div className="h-7 w-16 rounded animate-shimmer" />
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Create ThemeToggle**

Create `frontend/src/components/ThemeToggle.tsx` — standalone button component:

```tsx
import { Sun, Moon } from "lucide-react";
import { useTheme } from "../lib/theme";

interface ThemeToggleProps {
  className?: string;
}

export function ThemeToggle({ className }: ThemeToggleProps) {
  const { resolvedTheme, setTheme } = useTheme();
  return (
    <button
      onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
      className={`p-1.5 text-text-tertiary hover:text-text-secondary rounded-[var(--radius-sm)] hover:bg-[rgba(255,255,255,0.03)] transition-all duration-150 ${className ?? ""}`}
      aria-label="Toggle theme"
    >
      {resolvedTheme === "dark" ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
    </button>
  );
}
```

This component is used in AppShell navbar (Task 5) and Settings Profile tab (Task 12).

- [ ] **Step 4: Verify fonts are loaded**

Check `frontend/index.html` for Google Fonts link tags for DM Sans and JetBrains Mono. If not present, add:
```html
<link rel="preconnect" href="https://fonts.googleapis.com" />
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin />
<link href="https://fonts.googleapis.com/css2?family=DM+Sans:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet" />
```

- [ ] **Step 5: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/StatusBadge.tsx frontend/src/components/SkeletonCard.tsx frontend/src/components/ThemeToggle.tsx
git commit -m "feat(ui): add StatusBadge, SkeletonCard, and ThemeToggle components

StatusBadge: machine status with animated dot (running/stopped/starting/error).
SkeletonCard: shimmer loading placeholder for dashboard.
ThemeToggle: reusable sun/moon toggle for navbar and settings."
```

---

## Task 3: ConfirmDialog Component

**Files:**
- Create: `frontend/src/components/ConfirmDialog.tsx`

Reusable confirmation dialog for destructive actions.

- [ ] **Step 1: Install @radix-ui/react-alert-dialog if not present**

Run: `cd frontend && grep -q "react-alert-dialog" package.json && echo "already installed" || npm install @radix-ui/react-alert-dialog`

- [ ] **Step 2: Create ConfirmDialog**

Create `frontend/src/components/ConfirmDialog.tsx`:

```tsx
import * as AlertDialog from "@radix-ui/react-alert-dialog";

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel?: string;
  confirmVariant?: "danger" | "primary";
  onConfirm: () => void;
  /** If set, user must type this string to confirm */
  confirmText?: string;
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel = "Confirm",
  confirmVariant = "danger",
  onConfirm,
  confirmText,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState("");
  const canConfirm = confirmText ? typed === confirmText : true;

  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="fixed inset-0 bg-black/65 backdrop-blur-sm z-[300]" />
        <AlertDialog.Content className="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 bg-card border border-border rounded-[var(--radius-lg)] p-6 w-full max-w-[400px] shadow-modal z-[301] animate-modal-in">
          <AlertDialog.Title className="text-[17px] font-semibold tracking-tight mb-2">
            {title}
          </AlertDialog.Title>
          <AlertDialog.Description className="text-[13px] text-text-secondary mb-4">
            {description}
          </AlertDialog.Description>
          {confirmText && (
            <div className="mb-4">
              <label className="block text-[11px] font-medium text-text-tertiary uppercase tracking-wider mb-1">
                Type "{confirmText}" to confirm
              </label>
              <input
                type="text"
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                className="w-full px-3 py-2 text-[13px] bg-input border border-border rounded-[var(--radius-sm)] text-text-primary outline-none focus:border-brand-500 focus:shadow-[0_0_0_2px_rgba(249,115,22,0.08)]"
                autoFocus
              />
            </div>
          )}
          <div className="flex gap-2 justify-end">
            <AlertDialog.Cancel className="px-3 py-[5px] text-[12px] font-medium text-text-secondary border border-border rounded-[var(--radius-sm)] hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary transition-all duration-150">
              Cancel
            </AlertDialog.Cancel>
            <AlertDialog.Action
              disabled={!canConfirm}
              onClick={onConfirm}
              className={`px-3 py-[5px] text-[12px] font-medium rounded-[var(--radius-sm)] transition-all duration-150 disabled:opacity-50 ${
                confirmVariant === "danger"
                  ? "text-red-400 border border-[rgba(248,113,113,0.15)] hover:bg-[rgba(248,113,113,0.06)]"
                  : "bg-brand-600 text-white hover:bg-brand-700"
              }`}
            >
              {confirmLabel}
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
```

Note: Need to add `import { useState } from "react";` at the top.

- [ ] **Step 3: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/ConfirmDialog.tsx frontend/package.json frontend/package-lock.json
git commit -m "feat(ui): add ConfirmDialog for destructive action confirmations

Radix AlertDialog wrapper with optional type-to-confirm for delete actions."
```

---

## Task 4: UserMenu Component

**Files:**
- Create: `frontend/src/components/UserMenu.tsx`

Depends on: existing `useAuth`, `useTheme`, Radix DropdownMenu (already installed).

- [ ] **Step 1: Create UserMenu**

Create `frontend/src/components/UserMenu.tsx`:

```tsx
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Sun, Moon, Monitor, LogOut, Users } from "lucide-react";
import { useAuth } from "../lib/auth";
import { useTheme } from "../lib/theme";

const themeOptions = [
  { value: "light" as const, label: "Light", Icon: Sun },
  { value: "dark" as const, label: "Dark", Icon: Moon },
  { value: "system" as const, label: "System", Icon: Monitor },
];

export function UserMenu() {
  const { user, account, accounts, switchAccount, logout } = useAuth();
  const { theme, setTheme } = useTheme();

  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button className="text-[11px] text-text-tertiary px-3 py-[5px] rounded-full bg-card border border-border tracking-[0.01em] hover:text-text-secondary hover:border-border-hover transition-all duration-150 outline-none">
          {user?.email}
        </button>
      </DropdownMenu.Trigger>

      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align="end"
          sideOffset={6}
          className="min-w-[200px] bg-card border border-border rounded-[var(--radius)] p-1 shadow-elevated z-[200] animate-fade-up"
        >
          {/* Account info */}
          <div className="px-3 py-2 border-b border-border-subtle mb-1">
            <div className="text-[12px] font-medium text-text-primary">{account?.name}</div>
            <div className="text-[11px] text-text-muted">{user?.email}</div>
          </div>

          {/* Account switcher */}
          {accounts && accounts.length > 1 && (
            <>
              <DropdownMenu.Label className="px-3 py-1 text-[10px] font-medium text-text-muted uppercase tracking-wider">
                Switch Account
              </DropdownMenu.Label>
              {accounts.filter(a => a.id !== account?.id).map(a => (
                <DropdownMenu.Item
                  key={a.id}
                  onClick={() => switchAccount(a.id)}
                  className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-text-secondary rounded-[var(--radius-sm)] cursor-pointer hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary outline-none"
                >
                  <Users className="w-3.5 h-3.5 opacity-50" />
                  {a.name}
                </DropdownMenu.Item>
              ))}
              <DropdownMenu.Separator className="h-px bg-border-subtle my-1" />
            </>
          )}

          {/* Theme */}
          <DropdownMenu.Label className="px-3 py-1 text-[10px] font-medium text-text-muted uppercase tracking-wider">
            Theme
          </DropdownMenu.Label>
          {themeOptions.map(({ value, label, Icon }) => (
            <DropdownMenu.Item
              key={value}
              onClick={() => setTheme(value)}
              className={`flex items-center gap-2 px-3 py-1.5 text-[12px] rounded-[var(--radius-sm)] cursor-pointer outline-none ${
                theme === value
                  ? "text-brand-500 bg-brand-glow"
                  : "text-text-secondary hover:bg-[rgba(255,255,255,0.03)] hover:text-text-primary"
              }`}
            >
              <Icon className="w-3.5 h-3.5" />
              {label}
            </DropdownMenu.Item>
          ))}

          <DropdownMenu.Separator className="h-px bg-border-subtle my-1" />

          {/* Logout */}
          <DropdownMenu.Item
            onClick={logout}
            className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-red-400 rounded-[var(--radius-sm)] cursor-pointer hover:bg-[rgba(248,113,113,0.06)] outline-none"
          >
            <LogOut className="w-3.5 h-3.5" />
            Sign out
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}
```

- [ ] **Step 2: Check lucide-react is installed**

Run: `cd frontend && grep -q "lucide-react" package.json && echo "installed" || npm install lucide-react`

- [ ] **Step 3: Verify build**

Run: `make typecheck`
Expected: No errors. (Note: `switchAccount` and `accounts` must exist on useAuth — check if they do, and if not, the component should conditionally render account switcher only when available.)

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/UserMenu.tsx
git commit -m "feat(ui): add UserMenu dropdown component

Radix DropdownMenu with account info, theme switcher, account switcher, and logout."
```

---

## Task 5: AppShell Layout

**Files:**
- Create: `frontend/src/components/AppShell.tsx`
- Modify: `frontend/src/App.tsx`

This is the core layout change. Replaces the existing `Layout.tsx` with a new responsive shell.

- [ ] **Step 1: Create AppShell**

Create `frontend/src/components/AppShell.tsx`:

```tsx
import { Link, Outlet, useLocation, Navigate } from "react-router-dom";
import { LayoutDashboard, MessageSquare, Settings, Sun, Moon } from "lucide-react";
import { useAuth } from "../lib/auth";
import { useTheme } from "../lib/theme";
import { UserMenu } from "./UserMenu";

const navItems = [
  { label: "Dashboard", path: "/dashboard", Icon: LayoutDashboard },
  { label: "Chat", path: "/chat", Icon: MessageSquare },
  { label: "Settings", path: "/settings", Icon: Settings },
];

export function AppShell() {
  const { user, account, loading, accountError, isAdmin } = useAuth();
  const { resolvedTheme, setTheme } = useTheme();
  const location = useLocation();

  if (!loading && user && !account && !accountError) {
    return <Navigate to="/welcome" replace />;
  }

  const isActive = (path: string) => location.pathname.startsWith(path);

  return (
    <div className="min-h-screen flex flex-col relative z-[1]">
      {/* Desktop top navbar */}
      <header className="hidden md:flex items-center justify-between px-6 h-[52px] bg-[rgba(12,14,20,0.85)] backdrop-blur-[16px] backdrop-saturate-[1.2] border-b border-border sticky top-0 z-[100]">
        <div className="flex items-center gap-5">
          <Link to="/dashboard" className="flex items-center gap-2 font-semibold text-[15px] tracking-tight">
            <span className="text-red-500">OpenClaw</span>
            <span className="text-teal-400">Machines</span>
          </Link>
          <nav className="flex gap-[1px]">
            {navItems.map(({ label, path }) => (
              <Link
                key={path}
                to={path}
                className={`px-3 py-1.5 text-[13px] font-medium rounded-[var(--radius-sm)] transition-all duration-150 ${
                  isActive(path)
                    ? "text-brand-500 bg-brand-glow"
                    : "text-text-tertiary hover:text-text-secondary hover:bg-[rgba(255,255,255,0.03)]"
                }`}
              >
                {label}
              </Link>
            ))}
            {isAdmin && (
              <Link
                to="/dashboard/admin"
                className={`px-3 py-1.5 text-[13px] font-medium rounded-[var(--radius-sm)] transition-all duration-150 ${
                  isActive("/dashboard/admin")
                    ? "text-brand-500 bg-brand-glow"
                    : "text-text-tertiary hover:text-text-secondary hover:bg-[rgba(255,255,255,0.03)]"
                }`}
              >
                Admin
              </Link>
            )}
          </nav>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
            className="p-1.5 text-text-tertiary hover:text-text-secondary rounded-[var(--radius-sm)] hover:bg-[rgba(255,255,255,0.03)] transition-all duration-150"
            aria-label="Toggle theme"
          >
            {resolvedTheme === "dark" ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
          </button>
          <UserMenu />
        </div>
      </header>

      {/* Mobile top bar (minimal) */}
      <header className="md:hidden flex items-center justify-between px-4 h-[48px] bg-surface border-b border-border">
        <Link to="/dashboard" className="flex items-center gap-1.5 font-semibold text-[14px]">
          <span className="text-red-500">OCM</span>
        </Link>
        <UserMenu />
      </header>

      {/* Main content */}
      <main className="flex-1 max-w-[920px] w-full mx-auto px-6 py-7 pb-24 md:pb-7">
        <Outlet />
      </main>

      {/* Mobile bottom tab bar */}
      <nav className="md:hidden fixed bottom-0 left-0 right-0 h-[56px] bg-[rgba(12,14,20,0.92)] backdrop-blur-[20px] border-t border-border z-[200] flex items-center justify-around">
        {navItems.map(({ label, path, Icon }) => (
          <Link
            key={path}
            to={path}
            className={`flex flex-col items-center gap-0.5 py-1 px-4 ${
              isActive(path) ? "text-brand-500" : "text-text-tertiary"
            }`}
          >
            <Icon className="w-5 h-5" />
            <span className="text-[10px] font-medium">{label}</span>
          </Link>
        ))}
      </nav>
    </div>
  );
}
```

- [ ] **Step 2: Update App.tsx with new routes**

Rewrite `frontend/src/App.tsx` to use AppShell as the layout for protected routes:

```tsx
import { lazy, Suspense } from "react";
import { Routes, Route, Navigate, useParams } from "react-router-dom";
import { useAuth } from "./lib/auth";
import { AppShell } from "./components/AppShell";
import { Admin } from "./pages/Admin";
import { Dashboard } from "./pages/Dashboard";
import { Landing } from "./pages/Landing";
import { Pricing } from "./pages/Pricing";
import { MachineView } from "./pages/MachineView";
import { MachineWorkspace } from "./pages/MachineWorkspace";
import { Settings } from "./pages/Settings";
import { OnboardingWizard } from "./pages/OnboardingWizard";
import { SignedOut } from "./pages/SignedOut";
import { CliAuth } from "./pages/CliAuth";
import { InvitationAccept } from "./pages/InvitationAccept";
import { Login } from "./pages/Login";

const Blog = lazy(() => import("./pages/Blog"));
const BlogPost = lazy(() => import("./pages/BlogPost"));
const Docs = lazy(() => import("./pages/Docs"));
const DocsPage = lazy(() => import("./pages/DocsPage"));
const ChatPage = lazy(() => import("./pages/ChatPage"));

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  // Keep the existing ProtectedRoute implementation exactly as-is
  const { user, loading } = useAuth();
  if (loading) return <div className="flex items-center justify-center h-screen bg-deep" />;
  if (!user) {
    const key = "ocm_auth_retry";
    const hasCfCookie = document.cookie.includes("CF_Authorization");
    const attempt = sessionStorage.getItem(key) ? 2 : 1;
    console.warn("[OCM Auth] ProtectedRoute: no user", {
      attempt, path: window.location.pathname, hasCfCookie,
      cookies: document.cookie ? "(present)" : "(empty)",
    });

    if (sessionStorage.getItem(key)) {
      sessionStorage.removeItem(key);
      return (
        <div className="flex items-center justify-center h-screen bg-deep">
          <div className="text-center">
            <p className="text-text-secondary mb-4">Unable to authenticate. Please sign in to continue.</p>
            <button
              onClick={() => {
                const domains = ["", ".openclawmachines.com"];
                for (const domain of domains) {
                  const domainPart = domain ? `; domain=${domain}` : "";
                  document.cookie = `CF_Authorization=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
                  document.cookie = `CF_AppSession=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
                }
                window.location.reload();
              }}
              className="bg-brand-600 text-white px-6 py-2 rounded-[var(--radius-sm)] text-sm font-medium hover:bg-brand-700"
            >
              Sign in
            </button>
          </div>
        </div>
      );
    }

    sessionStorage.setItem(key, "1");
    const domains = ["", ".openclawmachines.com"];
    for (const domain of domains) {
      const domainPart = domain ? `; domain=${domain}` : "";
      document.cookie = `CF_Authorization=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
      document.cookie = `CF_AppSession=; expires=Thu, 01 Jan 1970 00:00:00 UTC; path=/${domainPart}`;
    }
    window.location.reload();
    return <div className="flex items-center justify-center h-screen bg-deep" />;
  }
  sessionStorage.removeItem("ocm_auth_retry");
  return <>{children}</>;
}

function GatewayRedirect() {
  const { id } = useParams<{ id: string }>();
  return <Navigate to={`/workspace/${id}?view=gateway`} replace />;
}

export function App() {
  return (
    <Routes>
      {/* Public routes */}
      <Route path="/" element={<Landing />} />
      <Route path="/pricing" element={<Pricing />} />
      <Route path="/docs" element={<Suspense fallback={<div className="min-h-screen bg-deep" />}><Docs /></Suspense>} />
      <Route path="/docs/:slug" element={<Suspense fallback={<div className="min-h-screen bg-deep" />}><DocsPage /></Suspense>} />
      <Route path="/blog" element={<Suspense fallback={<div className="min-h-screen bg-deep" />}><Blog /></Suspense>} />
      <Route path="/blog/:slug" element={<Suspense fallback={<div className="min-h-screen bg-deep" />}><BlogPost /></Suspense>} />
      <Route path="/signed-out" element={<SignedOut />} />
      <Route path="/login" element={<ProtectedRoute><Login /></ProtectedRoute>} />
      <Route path="/cli-auth" element={<ProtectedRoute><CliAuth /></ProtectedRoute>} />
      <Route path="/welcome" element={<ProtectedRoute><OnboardingWizard /></ProtectedRoute>} />
      <Route path="/invitations/:token" element={<InvitationAccept />} />

      {/* Full-screen views */}
      <Route path="/workspace/:id" element={<ProtectedRoute><MachineWorkspace /></ProtectedRoute>} />
      <Route path="/workspace/:id/gateway" element={<GatewayRedirect />} />

      {/* AppShell layout routes */}
      <Route element={<ProtectedRoute><AppShell /></ProtectedRoute>}>
        <Route path="/dashboard" element={<Dashboard />} />
        <Route path="/dashboard/admin" element={<Admin />} />
        <Route path="/machines/:id" element={<MachineView />} />
        <Route path="/machines/:id/:tab" element={<MachineView />} />
        <Route path="/chat" element={<Suspense fallback={<div>Loading...</div>}><ChatPage /></Suspense>} />
        <Route path="/chat/:machineId" element={<Suspense fallback={<div>Loading...</div>}><ChatPage /></Suspense>} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/settings/:tab" element={<Settings />} />
      </Route>
    </Routes>
  );
}
```

- [ ] **Step 3: Verify build and typecheck**

Run: `make typecheck`
Expected: No type errors. There may be import warnings for MachineCreate which is no longer routed — that's fine, we'll clean up later.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/AppShell.tsx frontend/src/App.tsx
git commit -m "feat(ui): add AppShell layout with responsive navigation

New layout wrapper replacing Layout.tsx. Desktop: glassmorphism top navbar.
Mobile: bottom tab bar. Routes restructured for /machines/:id and /settings/:tab."
```

---

## Task 6: Rewrite MachineCard

**Files:**
- Modify: `frontend/src/components/MachineCard.tsx`

Rewrite to match prototype's card styling. Keep existing logic (start/stop, model display), update visual presentation.

- [ ] **Step 1: Rewrite MachineCard**

Rewrite `frontend/src/components/MachineCard.tsx` to use prototype design tokens. Key changes:
- Use `StatusBadge` component
- Prototype card styling (bg-card, shadow-card, rounded-[var(--radius)])
- Machine meta row with model + channels + date
- Action buttons: Workspace, Chat, Stop/Start
- Card body click navigates to `/machines/:id`
- Remove the old model selector from the card (that moves to the detail view)

The component should keep the same props interface (`machine`, `accountId`, `accountSlug`, `onStatusChange`) and use the same API calls for start/stop transitions. Use `useNavigate` for card click navigation.

Style reference from prototype:
```
.machine-card { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 16px 18px; }
.machine-card:hover { background: var(--card-hover); border-color: var(--border-hover); transform: translateY(-1px); }
.machine-card-actions { border-top: 1px solid var(--border-subtle); padding-top: 10px; }
```

- [ ] **Step 2: Update MachineCard test**

Update `frontend/src/components/MachineCard.test.tsx` to account for changed button labels (Workspace instead of Gateway) and removed model selector from card. Keep existing test patterns.

- [ ] **Step 3: Run tests**

Run: `cd frontend && npx vitest run src/components/MachineCard.test.tsx`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/MachineCard.tsx frontend/src/components/MachineCard.test.tsx
git commit -m "feat(ui): rewrite MachineCard with prototype design

New card styling matching prototype: status badge with animated dot,
meta row, Workspace/Chat/Stop actions. Model selector moved to detail view."
```

---

## Task 7: Rewrite Dashboard

**Files:**
- Modify: `frontend/src/pages/Dashboard.tsx`
- Modify: `frontend/src/components/CreateMachineModal.tsx`

- [ ] **Step 1: Rewrite Dashboard**

Rewrite `frontend/src/pages/Dashboard.tsx`:
- Vertical list layout (not grid) matching prototype
- Use SkeletonCard for loading state (3 cards)
- Page header: "Machines" + count + "New Machine" button
- Keep existing data fetching logic (listMachines, 5s polling)

Style reference from prototype:
```
.machines-grid { display: flex; flex-direction: column; gap: 8px; }
.page-title { font-size: 20px; font-weight: 600; letter-spacing: -0.035em; }
```

- [ ] **Step 2: Simplify CreateMachineModal**

Rewrite `frontend/src/components/CreateMachineModal.tsx`:
- Name input + size picker (Small/Standard/Pro)
- Use prototype modal styling (bg-card, shadow-modal, animate-modal-in)
- Size options as 3-column grid with name + spec
- Keep existing `createMachine` API call

- [ ] **Step 3: Verify build and manually review**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/Dashboard.tsx frontend/src/components/CreateMachineModal.tsx
git commit -m "feat(ui): rewrite Dashboard with vertical card list and simplified create modal

Dashboard now shows machine cards in vertical list with skeleton loading.
Create modal simplified to name + size (Small/Standard/Pro)."
```

---

## Task 8: Machine Detail Tabs — Overview & Model

**Files:**
- Create: `frontend/src/pages/machine-tabs/OverviewTab.tsx`
- Create: `frontend/src/pages/machine-tabs/ModelTab.tsx`
- Create: `frontend/src/components/SetupCard.tsx`

- [ ] **Step 1: Create SetupCard component**

Create `frontend/src/components/SetupCard.tsx` — clickable card with icon, title, description, and completion state. Matches prototype `.setup-card` styling.

- [ ] **Step 2: Create OverviewTab**

Create `frontend/src/pages/machine-tabs/OverviewTab.tsx`:
- Setup cards grid (2x2 desktop, 1-col mobile): Model, Channels, Integrations, Browser
- Each card shows completion state (green check if configured)
- Click navigates to the relevant tab
- Credit warning banner: yellow at 80%+ usage, red at 95%+ (with link to add credits)
- Usage card showing messages, tokens, cost (read from machine data)
- Machine info fields (ID, size, created, uptime)

Props: `machine: Machine, accountId: number, onTabChange: (tab: string) => void`

- [ ] **Step 3: Create ModelTab**

Create `frontend/src/pages/machine-tabs/ModelTab.tsx`:
- Primary model selector (reuse existing ModelPicker, restyle)
- Provider cards (Anthropic, OpenAI, Google, OpenRouter)
- Connected status + key mask for configured providers
- Uses existing API: `listMachineCredentials`, `listModels`, `setMachineModel`

Props: `machine: Machine, accountId: number`

- [ ] **Step 4: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/SetupCard.tsx frontend/src/pages/machine-tabs/OverviewTab.tsx frontend/src/pages/machine-tabs/ModelTab.tsx
git commit -m "feat(ui): add OverviewTab and ModelTab for machine detail

OverviewTab: setup cards grid, usage summary, machine info.
ModelTab: model picker, provider cards with connection status."
```

---

## Task 9: Machine Detail Tabs — Channels, Integrations, Browser, Backups

**Files:**
- Create: `frontend/src/pages/machine-tabs/ChannelsTab.tsx`
- Create: `frontend/src/pages/machine-tabs/IntegrationsTab.tsx`
- Create: `frontend/src/pages/machine-tabs/BrowserTab.tsx`
- Create: `frontend/src/pages/machine-tabs/BackupsTab.tsx`

- [ ] **Step 1: Create ChannelsTab**

Channel cards grid (2-col desktop, 1-col mobile): Webchat, Telegram, WhatsApp, Discord, Slack. Each shows connected status, configure button. Webchat always shows as available.

Props: `machine: Machine, accountId: number`

Style from prototype: `.channel-card`, `.channel-icon`, `.channel-status`

- [ ] **Step 2: Create IntegrationsTab**

Connected services list. Empty state with app icons (Gmail, Slack, GitHub, Sheets, Notion) and "Connect via chat" prompt pills.

Props: `machine: Machine`

- [ ] **Step 3: Create BrowserTab**

Browser enable/disable toggle. Provider options (Built-in). Shows existing noVNC viewer when enabled.

Uses existing API: `enableMachineCapability`, `disableMachineCapability`, `listMachineCapabilities`.

Props: `machine: Machine, accountId: number`

- [ ] **Step 4: Create BackupsTab**

Thin wrapper that imports and renders existing `BackupsTab` component (from `frontend/src/components/BackupsTab.tsx`), restyled with new design tokens. If the existing component is self-contained enough, just re-export it.

Props: `machine: Machine, accountId: number`

- [ ] **Step 5: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/pages/machine-tabs/
git commit -m "feat(ui): add Channels, Integrations, Browser, and Backups tabs

ChannelsTab: channel cards grid with status indicators.
IntegrationsTab: empty state with app icons and prompt pills.
BrowserTab: toggle + provider options.
BackupsTab: restyled wrapper of existing backup UI."
```

---

## Task 10: Rewrite MachineView (Machine Detail Page)

**Files:**
- Modify: `frontend/src/pages/MachineView.tsx`

- [ ] **Step 1: Rewrite MachineView**

Rewrite `frontend/src/pages/MachineView.tsx` as the machine detail page:
- Back link: `← Dashboard`
- Detail header: machine name + StatusBadge + action buttons
- Top actions: Start/Stop, Chat, Delete (with ConfirmDialog)
- Tab bar: Overview, Model, Channels, Integrations, Browser, Backups
- Read `:tab` param from URL, default to "overview"
- Tab switching updates URL via `navigate`
- Tab content renders the appropriate tab component

Uses existing data fetching (getMachine, polling). Passes machine + accountId to tab components.

Style from prototype:
```
.tab-bar { border-bottom: 1px solid var(--border); }
.tab-btn.active { border-bottom-color: var(--brand-500); color: var(--text-primary); }
```

- [ ] **Step 2: Add error banner for error state machines**

If machine status is "error", show a red warning banner at top of detail view with error message.

- [ ] **Step 3: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/MachineView.tsx
git commit -m "feat(ui): rewrite MachineView with 6-tab detail layout

New machine detail page: back link, status header, action buttons,
6 tabs (Overview/Model/Channels/Integrations/Browser/Backups).
URL-driven tab selection, confirmation dialogs for destructive actions."
```

---

## Task 11: Rewrite Chat Page

**Files:**
- Create: `frontend/src/components/MachinePicker.tsx`
- Modify: `frontend/src/pages/ChatPage.tsx`

- [ ] **Step 1: Read existing ChatContainer implementation**

Read `frontend/src/components/chat/ChatContainer.tsx` to understand how webchat is currently embedded (iframe vs WebSocket vs other). This determines the rewrite strategy.

- [ ] **Step 2: Create MachinePicker component**

Create `frontend/src/components/MachinePicker.tsx`:
- Compact list of machine rows: status dot, machine name, model name
- Clicking a row calls `onSelect(machineId)`
- Empty state: "No running machines" + link to Dashboard
- Props: `machines: Machine[], onSelect: (id: string) => void, loading: boolean`

- [ ] **Step 3: Rewrite ChatPage**

Rewrite `frontend/src/pages/ChatPage.tsx`:
- If `:machineId` param present → go straight to chat for that machine
- Otherwise → show MachinePicker (list of running machines)
- If only one running machine → skip picker, go to chat
- Chat view: iframe embedding ControlUI webchat URL (`https://{machine.name}.openclawmachines.com`)
- Desktop: full content area with iframe
- Mobile: full-screen iframe with back button header

Keep existing machine fetching logic. Adapt based on what `ChatContainer` currently does (from Step 1).

- [ ] **Step 3: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/ChatPage.tsx
git commit -m "feat(ui): rewrite Chat page with machine picker and webchat iframe

Machine picker for selecting which machine to chat with.
Full-screen iframe embedding ControlUI webchat. Mobile-optimized layout."
```

---

## Task 12: Restyle Settings Page

**Files:**
- Modify: `frontend/src/pages/Settings.tsx`

- [ ] **Step 1: Restyle Settings**

Update `frontend/src/pages/Settings.tsx`:
- Change tabs to: Profile, Members, Usage, Billing
- Use horizontal underline tab bar (matching prototype `.settings-tabs` style)
- Card-based layout with settings rows
- Add theme preference toggle in Profile tab (use `ThemeToggle` component from Task 2)
- Reuse existing tab content components (MembersTab, UsageDashboard, etc.)
- Restyle with new design tokens

Keep existing functionality, just update the visual presentation and tab structure.

- [ ] **Step 2: Verify build**

Run: `make typecheck`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/pages/Settings.tsx
git commit -m "feat(ui): restyle Settings with new tab structure and design tokens

Tabs: Profile, Members, Usage, Billing. Horizontal underline style.
Card-based layout matching prototype design system."
```

---

## Task 13: Cleanup & Final Verification

**Files:**
- Remove or deprecate: old imports in App.tsx, unused components
- Verify: all routes work, theme toggle works, responsive layout

- [ ] **Step 1: Remove unused imports and dead code**

Check `App.tsx` for unused imports (MachineCreate, old Layout). Remove them.
Check if any old components are no longer imported anywhere and can be safely removed.

- [ ] **Step 2: Run full test suite**

Run: `make test-frontend`
Expected: All tests pass. Fix any broken tests from renamed components or changed routes.

- [ ] **Step 3: Run typecheck**

Run: `make typecheck`
Expected: No type errors.

- [ ] **Step 4: Run build**

Run: `cd frontend && npx vite build`
Expected: Clean build, no errors.

- [ ] **Step 5: Manual verification checklist**

Start dev server: `make frontend`

Verify:
- [ ] Dashboard loads with machine cards
- [ ] Theme toggle switches between light and dark
- [ ] Machine card click navigates to detail view
- [ ] All 6 tabs render on machine detail
- [ ] Chat page shows machine picker or direct chat
- [ ] Settings page shows 4 tabs
- [ ] Mobile view: bottom tab bar visible
- [ ] Mobile view: chat is full-screen
- [ ] Create machine modal opens and works

- [ ] **Step 6: Final commit**

Run `git status` first to review what changed, then add specific files:

```bash
git status
# Review output, then add only the relevant changed files
git add frontend/src/App.tsx frontend/src/pages/ frontend/src/components/
git commit -m "chore(ui): cleanup unused imports and verify simplified UI

Remove dead code from old layout. All tests pass, typecheck clean, build succeeds."
```

---

## Summary

| Task | Component | Estimated Complexity |
|------|-----------|---------------------|
| 1 | Design Tokens & Theme CSS | Mechanical — config files |
| 2 | StatusBadge & SkeletonCard | Mechanical — small components |
| 3 | ConfirmDialog | Mechanical — Radix wrapper |
| 4 | UserMenu | Mechanical — Radix dropdown |
| 5 | AppShell + Routes | Integration — layout + routing |
| 6 | MachineCard Rewrite | Standard — visual + logic |
| 7 | Dashboard + CreateModal | Standard — visual + logic |
| 8 | OverviewTab + ModelTab | Standard — tab components |
| 9 | Channels/Integrations/Browser/Backups | Standard — tab components |
| 10 | MachineView Rewrite | Integration — orchestrates tabs |
| 11 | Chat Page Rewrite | Integration — iframe + picker |
| 12 | Settings Restyle | Standard — visual restyle |
| 13 | Cleanup & Verification | Mechanical — cleanup + test |

**Dependencies:** Task 1 must be done first (everything depends on tokens). Tasks 2-4 are independent. Task 5 depends on Task 4. Tasks 6-7 depend on Tasks 1-2. Tasks 8-9 are independent. Task 10 depends on Tasks 8-9. Task 11 is independent. Task 12 is independent. Task 13 is last.
