# Admin Panel: Fleet Overview, Machines, and Logs

## Problem

The admin panel (`/dashboard/admin`) only implements the Hosts view. The HTML prototype (`frontend/prototype-admin.html`) defines six views; we need to implement four of them (excluding Telemetry and Upgrades) and add superuser access control so only `mathewma@gmail.com` can see or use the admin section.

## Scope

Build these views from the prototype:
1. **Fleet Overview** — stat cards (hosts, machines, vCPUs, memory, errors), host health grid, recent events feed
2. **Hosts** — restyle existing `Admin.tsx` to match prototype's dark design system (expandable rows, capacity bars, sparkline hints)
3. **Machines** — filterable table across all accounts/hosts, slide-out detail panel, bulk select
4. **Logs** — structured log viewer with level toggles (info/warn/error/debug), host/machine filters, search

NOT building: Telemetry, Upgrades.

## Access Control

### Backend: `requireSuperuser` middleware
- New middleware on the `/api/admin` route group in `server.go`
- Checks the authenticated user's email against a hardcoded `mathewma@gmail.com`
- Returns `403 Forbidden` for all other users
- All existing `/api/admin/*` routes are protected by this middleware

### Frontend: `isAdmin` flag
- `useAuth()` exposes `isAdmin: boolean` derived from `user.email === "mathewma@gmail.com"`
- `Layout.tsx` only renders the Admin nav link when `isAdmin` is true
- Admin route in the router returns 404/redirect for non-admin users

## Architecture

### Tab-based layout

Replace current `Admin.tsx` with a tab container that renders four sub-views:

```
Admin.tsx (tab router)
├── AdminFleetOverview.tsx
├── AdminHosts.tsx        (current Admin.tsx logic, restyled)
├── AdminMachines.tsx
└── AdminLogs.tsx
```

Tab state stored in URL hash (`#fleet`, `#hosts`, `#machines`, `#logs`) so links are shareable.

### New backend endpoint

`GET /api/admin/machines` — returns all machines across all accounts. Reuses existing store query but without account scoping. Protected by superuser middleware.

### Data flow

- **Fleet Overview**: Aggregates from existing `GET /api/admin/hosts` (host list with capacity stats) plus the new machines endpoint. Client-side computation for fleet totals.
- **Hosts**: Same as current — `GET /api/admin/hosts` with 5s polling. Expandable rows load `GET /api/admin/hosts/{id}/machines` and `GET /api/admin/hosts/{id}/vm-stats` on expand.
- **Machines**: `GET /api/admin/machines` with client-side filtering (status, host, account, search text). Slide-out detail fetches additional data per machine.
- **Logs**: `GET /api/admin/hosts/{id}/logs` per host, aggregated and parsed client-side. Level toggles, host/machine filters, search.

## Design system

Match the prototype's dark design system using existing Tailwind dark mode classes. Key patterns:
- Card backgrounds: `dark:bg-gray-900`, borders: `dark:border-gray-700`
- Status badges with pulse dots for running/error states
- Capacity bars (green < 50%, yellow 50-80%, red > 80%)
- Monospace font for IDs, versions, timestamps (`font-mono`)

## What doesn't change

- Existing API endpoints and their behavior
- Dashboard layout/routing structure
- Non-admin user experience
- Toast notification system
- Auth flow (CF Access)

## Files

### Backend
- `backend/internal/api/server.go` — add `requireSuperuser` middleware, mount on admin route group, add `GET /api/admin/machines` handler
- `backend/internal/api/admin_machines.go` — new file for admin machines handler (list all machines)
- `backend/internal/store/queries.go` (or equivalent) — add `ListAllMachines` query if not present

### Frontend
- `frontend/src/lib/auth.tsx` — add `isAdmin` to AuthState and AuthProvider
- `frontend/src/components/Layout.tsx` — conditional Admin nav link
- `frontend/src/pages/Admin.tsx` — rewrite as tab container
- `frontend/src/pages/admin/AdminFleetOverview.tsx` — new
- `frontend/src/pages/admin/AdminHosts.tsx` — extracted from current Admin.tsx, restyled
- `frontend/src/pages/admin/AdminMachines.tsx` — new
- `frontend/src/pages/admin/AdminLogs.tsx` — new
- `frontend/src/lib/api.ts` — add `listAllMachines` API call
