# Admin Panel Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Extend the admin panel from Hosts-only to four views (Fleet Overview, Hosts, Machines, Logs) with superuser-only access control.

**Architecture:** Tab-based SPA at `/dashboard/admin` with URL hash routing (`#fleet`, `#hosts`, `#machines`, `#logs`). Backend gets a `requireSuperuser` middleware on all `/api/admin/*` routes and a new `GET /api/admin/machines` endpoint. Frontend gets `isAdmin` from `useAuth()` to hide the nav link and guard the route.

**Tech Stack:** Go (Chi router, pgx), React 18, TypeScript, Tailwind CSS, Radix UI

**Design doc:** `docs/plans/2026-03-02-admin-panel-design.md`

---

### Task 1: Backend — `ListAllMachines` store query

**Files:**
- Modify: `backend/internal/store/store.go:270` (Store interface)
- Modify: `backend/internal/store/postgres.go:279` (after ListMachinesByHost)

**Step 1: Add `ListAllMachines` to the Store interface**

In `backend/internal/store/store.go`, add after `ListMachinesByHost` (line 271):

```go
ListAllMachines(ctx context.Context) ([]Machine, error)
```

**Step 2: Implement in PostgresStore**

In `backend/internal/store/postgres.go`, add after the `ListMachinesByHost` function (after line 296):

```go
func (s *PostgresStore) ListAllMachines(ctx context.Context) ([]Machine, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+machineColumns+` FROM machines ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	machines := []Machine{}
	for rows.Next() {
		m, err := scanMachine(rows.Scan)
		if err != nil {
			return nil, err
		}
		machines = append(machines, *m)
	}
	return machines, nil
}
```

**Step 3: Run tests**

Run: `make test-go`
Expected: All existing tests pass (new method added but nothing calls it yet).

**Step 4: Commit**

```bash
git add backend/internal/store/store.go backend/internal/store/postgres.go
git commit -m "feat(store): add ListAllMachines query for admin panel"
```

---

### Task 2: Backend — `requireSuperuser` middleware + admin machines endpoint

**Files:**
- Modify: `backend/internal/api/server.go:238-246` (admin route group)

**Step 1: Add `requireSuperuser` middleware**

In `backend/internal/api/server.go`, add a new method after the `AccountMiddleware` function (around line 373):

```go
// requireSuperuser blocks requests from non-superuser accounts.
// Currently hardcoded to a single email — replace with a DB flag if needed.
func (s *Server) requireSuperuser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := auth.UserFromContext(r.Context())
		if claims == nil || claims.Email != "mathewma@gmail.com" {
			writeError(w, http.StatusForbidden, "admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

**Step 2: Add `handleAdminListMachines` handler**

Add after `handleAdminResetMachine` (around line 1460):

```go
func (s *Server) handleAdminListMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := s.store.ListAllMachines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, machines)
}
```

**Step 3: Wire middleware + new route**

Replace the admin route block (lines 238-246) with:

```go
		// Admin — host management (superuser only)
		r.Route("/api/admin", func(r chi.Router) {
			r.Use(srv.requireSuperuser)
			r.Get("/hosts", srv.handleListHosts)
			r.Get("/hosts/{hostId}/machines", srv.handleListHostMachines)
			r.Get("/hosts/{hostId}/logs", srv.handleHostLogs)
			r.Get("/hosts/{hostId}/vm-stats", srv.handleHostVMStats)
			r.Post("/hosts", srv.handleProvisionHost)
			r.Delete("/hosts/{hostId}", srv.handleDestroyHost)
			r.Post("/hosts/{hostId}/refresh-rootfs", srv.handleRefreshRootfs)
			r.Post("/machines/{machineId}/reset", srv.handleAdminResetMachine)
			r.Get("/machines", srv.handleAdminListMachines)
		})
```

Note: This moves from flat routes (`r.Get("/api/admin/hosts", ...)`) to a `r.Route("/api/admin", ...)` group. URL paths are unchanged. The `r.Use(srv.requireSuperuser)` applies to all routes in the group.

**Step 4: Run tests**

Run: `make test-go && make test-gateway-e2e`
Expected: All pass. Gateway E2E tests use service token auth on internal routes, not admin routes, so no impact.

**Step 5: Commit**

```bash
git add backend/internal/api/server.go
git commit -m "feat(api): superuser middleware + admin machines endpoint"
```

---

### Task 3: Frontend — `isAdmin` flag in auth context

**Files:**
- Modify: `frontend/src/lib/auth.tsx`

**Step 1: Add `isAdmin` to AuthState interface**

In `frontend/src/lib/auth.tsx`, change the `AuthState` interface (line 5-11):

```typescript
interface AuthState {
  user: User | null;
  account: Account | null;
  loading: boolean;
  accountError: boolean;
  isAdmin: boolean;
  logout: () => void;
}
```

**Step 2: Update context default**

Update the `createContext` default (line 13-19):

```typescript
const AuthContext = createContext<AuthState>({
  user: null,
  account: null,
  loading: true,
  accountError: false,
  isAdmin: false,
  logout: () => {},
});
```

**Step 3: Compute `isAdmin` and pass to provider**

In the `AuthProvider` component, add a computed value and update the Provider value (line 83-87):

```typescript
  const isAdmin = user?.email === "mathewma@gmail.com";

  return (
    <AuthContext.Provider value={{ user, account, loading, accountError, isAdmin, logout }}>
      {children}
    </AuthContext.Provider>
  );
```

**Step 4: Run typecheck**

Run: `make typecheck`
Expected: Pass. No consumers use `isAdmin` yet.

**Step 5: Commit**

```bash
git add frontend/src/lib/auth.tsx
git commit -m "feat(auth): add isAdmin flag to auth context"
```

---

### Task 4: Frontend — Conditional admin nav link

**Files:**
- Modify: `frontend/src/components/Layout.tsx`

**Step 1: Filter nav items by admin status**

In `Layout.tsx`, change the static `nav` array to be filtered inside the component. Replace lines 7-11:

```typescript
const baseNav = [
  { label: "Dashboard", path: "/dashboard" },
  { label: "Admin", path: "/dashboard/admin", adminOnly: true },
  { label: "Settings", path: "/dashboard/settings" },
];
```

**Step 2: Filter in the component**

In the `Layout` function, after `const { user, logout } = useAuth();` (line 20), add:

```typescript
  const { user, logout, isAdmin } = useAuth();
```

Then change the `nav.map` in the JSX (line 47) to filter:

```typescript
            {baseNav
              .filter((item) => !item.adminOnly || isAdmin)
              .map((item) => (
```

**Step 3: Run typecheck**

Run: `make typecheck`
Expected: Pass.

**Step 4: Commit**

```bash
git add frontend/src/components/Layout.tsx
git commit -m "feat(layout): hide admin nav link for non-admin users"
```

---

### Task 5: Frontend — `listAllMachines` API call

**Files:**
- Modify: `frontend/src/lib/api.ts`

**Step 1: Add the API function**

After the existing admin API functions (around line 330), add:

```typescript
export const listAllMachines = () =>
  request<Machine[]>("/admin/machines");
```

Note: `Machine` type is already used in the codebase. Check the existing types — if the admin endpoint returns `store.Machine` (which maps to the same fields), the existing `Machine` type from `types.ts` should work. If there's a type mismatch (e.g. `account_id` not in the frontend type), add the missing fields.

**Step 2: Verify Machine type has needed fields**

Check `frontend/src/lib/types.ts` for the Machine type. The admin machines view needs: `id`, `name`, `slug`, `status`, `status_message`, `vcpus`, `memory_mb`, `host_id`, `account_id`, `custom_domain`, `openclaw_version`, `created_at`, `started_at`, `stopped_at`. Add any missing fields.

**Step 3: Run typecheck**

Run: `make typecheck`
Expected: Pass.

**Step 4: Commit**

```bash
git add frontend/src/lib/api.ts frontend/src/lib/types.ts
git commit -m "feat(api): add listAllMachines admin API call"
```

---

### Task 6: Frontend — Admin tab container (rewrite Admin.tsx)

**Files:**
- Modify: `frontend/src/pages/Admin.tsx` (full rewrite as tab container)
- Create: `frontend/src/pages/admin/` directory

**Step 1: Create the admin directory**

```bash
mkdir -p frontend/src/pages/admin
```

**Step 2: Rewrite Admin.tsx as a tab container**

Replace the entire contents of `frontend/src/pages/Admin.tsx` with:

```tsx
import { useState, useEffect } from "react";
import { useAuth } from "../lib/auth";
import { AdminFleetOverview } from "./admin/AdminFleetOverview";
import { AdminHosts } from "./admin/AdminHosts";
import { AdminMachines } from "./admin/AdminMachines";
import { AdminLogs } from "./admin/AdminLogs";

const tabs = [
  { id: "fleet", label: "Fleet Overview" },
  { id: "hosts", label: "Hosts" },
  { id: "machines", label: "Machines" },
  { id: "logs", label: "Logs" },
] as const;

type TabId = (typeof tabs)[number]["id"];

function getTabFromHash(): TabId {
  const hash = window.location.hash.slice(1);
  if (tabs.some((t) => t.id === hash)) return hash as TabId;
  return "fleet";
}

export function Admin() {
  const { isAdmin } = useAuth();
  const [activeTab, setActiveTab] = useState<TabId>(getTabFromHash);

  useEffect(() => {
    const onHashChange = () => setActiveTab(getTabFromHash());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  const switchTab = (id: TabId) => {
    window.location.hash = id;
    setActiveTab(id);
  };

  if (!isAdmin) {
    return (
      <p className="text-gray-500 dark:text-gray-400 py-16 text-center">
        You do not have access to this page.
      </p>
    );
  }

  return (
    <div>
      <nav className="flex gap-1 border-b border-gray-200 dark:border-gray-700 mb-6">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => switchTab(tab.id)}
            className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors ${
              activeTab === tab.id
                ? "border-brand-600 text-brand-600 dark:text-brand-400"
                : "border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300"
            }`}
          >
            {tab.label}
          </button>
        ))}
      </nav>
      {activeTab === "fleet" && <AdminFleetOverview />}
      {activeTab === "hosts" && <AdminHosts />}
      {activeTab === "machines" && <AdminMachines />}
      {activeTab === "logs" && <AdminLogs />}
    </div>
  );
}
```

**Step 3: Create placeholder components**

Create minimal placeholder files so the app compiles. Each will be fleshed out in subsequent tasks.

`frontend/src/pages/admin/AdminFleetOverview.tsx`:
```tsx
export function AdminFleetOverview() {
  return <div>Fleet Overview — coming soon</div>;
}
```

`frontend/src/pages/admin/AdminHosts.tsx`:
```tsx
export function AdminHosts() {
  return <div>Hosts — coming soon</div>;
}
```

`frontend/src/pages/admin/AdminMachines.tsx`:
```tsx
export function AdminMachines() {
  return <div>Machines — coming soon</div>;
}
```

`frontend/src/pages/admin/AdminLogs.tsx`:
```tsx
export function AdminLogs() {
  return <div>Logs — coming soon</div>;
}
```

**Step 4: Run typecheck**

Run: `make typecheck`
Expected: Pass.

**Step 5: Commit**

```bash
git add frontend/src/pages/Admin.tsx frontend/src/pages/admin/
git commit -m "feat(admin): tab container with placeholder views"
```

---

### Task 7: Frontend — AdminHosts (extract + restyle)

**Files:**
- Modify: `frontend/src/pages/admin/AdminHosts.tsx`

**Step 1: Extract current Admin.tsx logic**

Move the entire body of the old `Admin` component (all hooks, handlers, JSX) into `AdminHosts.tsx`. Rename the function to `AdminHosts`. Update imports — the component uses `useCallback`, `useState`, `useEffect`, `Fragment` from React, the API functions from `../../lib/api`, and `useToast` from `../../components/Toast`.

This is a straight copy-paste with import path adjustments (`../lib/api` → `../../lib/api`, `../components/Toast` → `../../components/Toast`).

**Step 2: Restyle to match prototype**

Key styling changes:
- Replace `bg-white dark:bg-gray-800` table wrapper with `bg-gray-900 border-gray-700` for dark mode match
- Add capacity bars inline in vCPU and Memory columns (like prototype shows `6/8` with a progress bar)
- Add heartbeat indicator in the last column using `last_heartbeat` from host data
- Use `font-mono text-xs` for IPs, versions, timestamps

**Step 3: Run typecheck + visual check**

Run: `make typecheck`
Then visually verify at `http://localhost:5173/dashboard/admin#hosts`.

**Step 4: Commit**

```bash
git add frontend/src/pages/admin/AdminHosts.tsx
git commit -m "feat(admin): extract and restyle hosts view"
```

---

### Task 8: Frontend — AdminFleetOverview

**Files:**
- Modify: `frontend/src/pages/admin/AdminFleetOverview.tsx`

**Step 1: Implement fleet overview**

The component fetches:
- `listHosts()` for host data (5s polling, same as hosts view)
- `listAllMachines()` for machine data (5s polling)

Then computes:
- Total hosts count
- Active machines / total machines
- Fleet vCPUs used/total, memory used/total
- Machines in error count

Renders:
1. **Stat cards** — 6 cards in a grid: Total Hosts, Active Machines, Fleet vCPUs (with capacity bar), Fleet Memory (with capacity bar), Machines in Error (red tint), and a placeholder for LLM Spend (hardcoded "—" for now since telemetry is out of scope)
2. **Host Health grid** — one card per host showing name, zone, machine type, vCPU/memory stats, VM count, agent version, heartbeat age
3. **Recent Events feed** — placeholder with a note "Events feed requires backend event streaming endpoint" (the prototype shows events but there's no real-time event API yet)

**Step 2: Capacity bar helper**

Create a small inline helper (not a separate file — YAGNI):

```tsx
function CapacityBar({ used, total }: { used: number; total: number }) {
  const pct = total > 0 ? Math.round((used / total) * 100) : 0;
  const color = pct > 80 ? "bg-red-500" : pct > 50 ? "bg-yellow-500" : "bg-green-500";
  return (
    <div className="h-1.5 bg-gray-200 dark:bg-gray-700 rounded-full overflow-hidden">
      <div className={`h-full rounded-full ${color}`} style={{ width: `${pct}%` }} />
    </div>
  );
}
```

**Step 3: Run typecheck + visual check**

Run: `make typecheck`

**Step 4: Commit**

```bash
git add frontend/src/pages/admin/AdminFleetOverview.tsx
git commit -m "feat(admin): fleet overview with stat cards and host health grid"
```

---

### Task 9: Frontend — AdminMachines

**Files:**
- Modify: `frontend/src/pages/admin/AdminMachines.tsx`

**Step 1: Implement machine table with filters**

The component:
- Fetches `listAllMachines()` with 5s polling
- Fetches `listHosts()` once (for host name lookup by `host_id`)
- Implements client-side filtering: search text, status dropdown, host dropdown
- Renders a table matching the prototype: checkbox, Status (badge), Name (clickable), Domain, Account, Host, vCPU/RAM, OpenClaw version, Uptime/Status, Actions

Filter state stored in component state, not URL params (keep it simple).

**Step 2: Slide-out detail panel**

When a machine name is clicked, a slide-out panel opens from the right showing:
- Status badge
- Machine metadata (ID, account, host, vCPU/RAM, domain, created, started)
- Action buttons (Stop/Start/Restart depending on status)

Use a simple `position: fixed` overlay + panel with Tailwind. No need for Radix Dialog — the prototype uses a custom slide-out.

Action handlers call existing API functions (`startMachine`, `stopMachine` from `../../lib/api`). Check if these exist; if not, they can call the machine-specific endpoints.

**Step 3: Bulk select**

- Checkbox column with select-all header checkbox
- When any boxes are checked, show a floating action bar at the bottom: "N selected — Start / Stop"
- Bulk actions call existing endpoints in sequence

**Step 4: Run typecheck + visual check**

Run: `make typecheck`

**Step 5: Commit**

```bash
git add frontend/src/pages/admin/AdminMachines.tsx
git commit -m "feat(admin): machines view with filters, slide-out detail, bulk select"
```

---

### Task 10: Frontend — AdminLogs

**Files:**
- Modify: `frontend/src/pages/admin/AdminLogs.tsx`

**Step 1: Implement log viewer**

The component:
- Fetches logs from all hosts via `getHostLogs(hostId, lines)` (existing endpoint)
- Fetches host list via `listHosts()` for the host filter dropdown
- Parses log lines into structured entries: timestamp, level (INFO/WARN/ERROR/DEBUG), source, message
- Renders in a monospace log viewer with color-coded level indicators

**Step 2: Filters and controls**

- Host dropdown filter
- Level toggle buttons (Info, Warn, Error, Debug) — all except Debug on by default
- Search input (client-side text filter on parsed log lines)
- Line count indicator
- Auto-refresh toggle (5s polling when on)

**Step 3: Log sidebar**

- Log volume summary (count by level)
- Count by host

**Step 4: Log line parser**

Parse structured slog output. Example line:
```
Feb 28 14:33:00 selfupdate: check current=v0.42.1 latest=v0.42.1
```

Pattern: `<date> <time> <source>: <message with key=value pairs>`

```typescript
interface LogEntry {
  timestamp: string;
  level: "info" | "warn" | "error" | "debug";
  source: string;
  message: string;
  hostId: number;
  hostName: string;
}
```

Level detection: check for known prefixes or keywords — "ERROR" in line → error, "WARN" → warn, etc. Default to info.

**Step 5: Run typecheck + visual check**

Run: `make typecheck`

**Step 6: Commit**

```bash
git add frontend/src/pages/admin/AdminLogs.tsx
git commit -m "feat(admin): logs explorer with level toggles and filters"
```

---

### Task 11: Final integration + push

**Step 1: Run full quality checks**

```bash
make typecheck && make test-go && make test-gateway-e2e
```

**Step 2: Visual smoke test**

Start dev servers (`make frontend` + `make backend`) and verify:
- Non-admin user does NOT see Admin link in nav
- Admin user sees all 4 tabs
- Fleet Overview shows real host data
- Hosts tab has same functionality as before
- Machines tab loads and filters work
- Logs tab loads logs from hosts

**Step 3: Push**

```bash
git push
```
