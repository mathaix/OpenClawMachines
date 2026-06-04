# Multi-Provider Regional Placement and UX Design

Date: 2026-03-31
Status: Proposed
Owners: Control Plane + Frontend

## 1. Goal

Define a provider-neutral placement architecture and user-facing region experience that:

- keeps **region-defaulted placement** as the baseline behavior
- allows users to select their preferred region at machine creation and later updates
- works across current and future providers (GCP, OVH, Hetzner, AWS, Azure, customer-owned)
- supports new region expansion (US, Europe, Asia) without reworking core placement logic

## 2. Current State and Gaps

Current code path:

- Placement defaults region from `PlacementService` constructor if request region is empty
- Machine create/start flow does not persist or pass a machine region preference
- Registered hosts are enrolled with `region="external"`, which conflicts with default regional filters

Relevant files:

- [`backend/internal/fleet/placement.go`](/Users/mantiz/openclawmachines/backend/internal/fleet/placement.go)
- [`backend/internal/machines/runtime.go`](/Users/mantiz/openclawmachines/backend/internal/machines/runtime.go)
- [`backend/internal/api/machines.go`](/Users/mantiz/openclawmachines/backend/internal/api/machines.go)
- [`backend/internal/api/enrollment.go`](/Users/mantiz/openclawmachines/backend/internal/api/enrollment.go)
- [`backend/internal/store/postgres.go`](/Users/mantiz/openclawmachines/backend/internal/store/postgres.go)

Main gap:

- Region defaults exist, but region taxonomy is not provider-neutral and user intent is not modeled.

## 3. Design Principles

1. Region remains explicit and defaulted, never implicit global-any placement.
2. User intent is persisted at machine level.
3. Geography model is provider-neutral.
4. Provider metadata is preserved, but placement uses canonical regions.
5. Affinity and data-locality safety rules remain intact.
6. Fallback behavior is policy-driven and transparent.

## 4. Canonical Geography Model

### 4.1 Canonical placement regions

Introduce a stable set of platform region codes used by placement and UI:

- `us-east`
- `us-central`
- `us-west`
- `eu-west`
- `eu-central`
- `eu-north`
- `asia-east`
- `asia-south`
- `asia-southeast`

These are platform-level routing regions, not raw provider region strings.

### 4.2 Provider location fields

For each host, keep both canonical and provider-native location values:

- `placement_region` (canonical; required for scheduling)
- `provider_region` (native provider region code, optional)
- `provider_zone` (native zone/az code, optional)
- `metro` (optional operator label, e.g. `ord`, `fra`, `sin`)

Examples:

- GCP `us-central1-b` -> `placement_region=us-central`, `provider_region=us-central1`, `provider_zone=us-central1-b`
- OVH `HIL1` -> `placement_region=us-west` (or `us-central` based on platform policy), `provider_region=HIL1`
- Hetzner `nbg1` -> `placement_region=eu-central`, `provider_region=nbg1`

### 4.3 Region catalog

Add a region catalog source (table or static config + API projection) to power UI and fallback ordering.

Fields:

- `code` (canonical code)
- `display_name`
- `continent`
- `priority`
- `enabled`
- `fallback_order` (ordered list)

## 5. Data Model Changes

### 5.1 Hosts

Add/normalize in `hosts`:

- `placement_region TEXT NOT NULL`
- `provider_region TEXT`
- `provider_zone TEXT`
- `metro TEXT`

Keep existing `region` temporarily as projection/backward-compat.

### 5.2 Machines

Add in `machines`:

- `preferred_region TEXT`
- `placement_policy TEXT NOT NULL DEFAULT 'strict'`

`placement_policy` values:

- `strict` (fail if preferred/default region unavailable)
- `prefer_local` (try preferred, then nearest fallback)

### 5.3 Accounts

Add in `accounts`:

- `default_region TEXT`

This provides team-level default behavior.

### 5.4 Optional: Region catalog table

`region_catalog` (or config-backed API if DB table deferred):

- `code`, `display_name`, `continent`, `enabled`, `priority`, `fallback_order_json`

## 6. Placement Input Resolution

For first placement, resolve effective requested region in this order:

1. explicit request override (future optional start override)
2. machine `preferred_region`
3. account `default_region`
4. geolocated nearest region recommendation (from trusted edge header mapping)
5. platform fallback default (config, e.g. `us-central`)

For restarts with host affinity:

- if machine has viable home host, restart on home host regardless of `preferred_region`
- `preferred_region` affects **new placement / migration targets**, not unsafe cross-host restarts

## 7. Placement Selection Algorithm

### 7.1 Candidate filtering

Filter hosts by:

- `status = ready`
- heartbeat freshness
- sufficient capacity
- `placement_region = effective_region` (phase 1)
- provider/artifact compatibility policy

### 7.2 Compatibility model (provider-neutral)

Replace hard coupling to `source_image` as global default filter with compatibility checks:

- host artifact release compatibility (rootfs release ID or channel)
- required capabilities (e.g. `kvm`, `browser_vm`)
- optional provider constraints

### 7.3 Fallback policy

If no host in effective region:

- `strict`: return capacity-unavailable in region
- `prefer_local`: iterate region fallback order from region catalog and retry

### 7.4 Ordering inside a region

Use policy-configurable strategy:

- `binpack` for cost-optimized pools
- `spread` for always-on dedicated fleets

Expose this as a fleet policy knob, not hard-coded globally.

## 8. API Contract Changes

### 8.1 Region discovery endpoint

Add:

- `GET /api/regions`

Response includes:

- region code + display name
- availability state (`available`, `limited`, `unavailable`)
- recommended flag for current user/account
- optional estimated latency bands

### 8.2 Machine create

Extend create payload:

```json
{
  "name": "agent-1",
  "size": "standard",
  "auto_start": true,
  "preferred_region": "eu-west",
  "placement_policy": "strict"
}
```

### 8.3 Machine update

Allow updating:

- `preferred_region`
- `placement_policy`

Clarify behavior:

- takes effect on next fresh placement or migration
- does not force-migrate a running machine

### 8.4 Account settings

Add:

- `GET/PUT /api/accounts/{id}/settings` fields: `default_region`

### 8.5 Enrollment and host provisioning

Enrollment and managed provisioning must set canonical location fields:

- no host should end up with scheduler-opaque values like `external`
- enrollment request should optionally include location hints
- admin can patch host location metadata

## 9. Host Enrollment and Provider Onboarding

### 9.1 Enrollment payload extension

Extend `POST /api/agent/register` request with optional location:

```json
{
  "location": {
    "placement_region": "eu-central",
    "provider_region": "gra",
    "provider_zone": "gra-1",
    "metro": "par"
  }
}
```

If omitted:

- derive from token labels or provider mapping rules
- if unresolved, keep host in `provisioning/error` with explicit message until location is set

### 9.2 Provider mapping registry

Add a mapping layer:

- input: provider, provider_region/provider_zone
- output: canonical `placement_region`

Implemented as config or code map so new providers/regions can be added without SQL hacks.

### 9.3 Unknown region handling

Do not place on hosts with unknown canonical placement region.

- host remains `ready=false` for scheduler until mapped
- admin UI shows actionable remediation

## 10. UI Changes

## 10.1 Machine create surfaces

Update both:

- [`frontend/src/pages/MachineCreate.tsx`](/Users/mantiz/openclawmachines/frontend/src/pages/MachineCreate.tsx)
- [`frontend/src/components/CreateMachineModal.tsx`](/Users/mantiz/openclawmachines/frontend/src/components/CreateMachineModal.tsx)

Add region selector:

- default selection = recommended nearest or account default
- show badges: `Recommended`, `Low capacity`, `Unavailable`
- policy selector: `Strict` vs `Nearest fallback`

### 10.2 Machine details/settings

In machine settings:

- display current preferred region and active host region
- allow editing preferred region and placement policy
- show note: changes apply on next placement/migration

### 10.3 Dashboard and cards

Show:

- preferred region (machine intent)
- active host region (runtime location)
- mismatch indicator when running outside preferred region due to fallback

### 10.4 Start-time UX when region unavailable

If start fails under `strict`:

- show inline error with suggested nearby regions
- one-click retry with selected fallback region

### 10.5 Account settings UI

Add account-level default region selector:

- applies to new machines when no explicit machine region selected

### 10.6 Admin host UI

In host admin views:

- show canonical and provider-native location side by side
- highlight hosts with missing or invalid mapping
- bulk edit tool for remapping regions during provider onboarding

## 11. Migration Plan

### Phase 0: Schema + compatibility

- add columns (`placement_region`, `preferred_region`, `placement_policy`, `default_region`)
- keep old fields and old behavior running

### Phase 1: Dual-write and projection

- write canonical region fields on new host enroll/provision
- mirror `hosts.region` from `placement_region` for temporary compatibility

### Phase 2: API + frontend

- ship `/api/regions`
- ship machine create/update region fields
- update create UI + settings UI

### Phase 3: Placement switch

- resolve effective region from machine/account/user recommendation path
- replace old global region-only default logic with resolved intent
- keep region defaulting behavior enabled

### Phase 4: Cleanup

- deprecate direct reliance on legacy `hosts.region` semantics
- remove `external` as scheduler-eligible value

## 12. Backfill and Data Repair

### 12.1 Hosts

Backfill `placement_region` for existing hosts:

- use known mapping from provider metadata and labels
- manually map unresolved rows
- block placement on unresolved rows until fixed

### 12.2 Machines

Set `preferred_region`:

- keep null for existing machines by default
- optional backfill to home host region for currently provisioned machines

### 12.3 Accounts

Optional backfill:

- set `default_region` from majority running machine region per account

## 13. Observability and SLOs

Add metrics:

- placement attempts by requested region
- placement success rate by region/provider
- fallback rate (`strict` misses vs `prefer_local` fallback hits)
- unresolved host mapping count
- cross-region placement count (outside preferred)

Add structured logs fields:

- `requested_region`
- `effective_region`
- `selected_region`
- `fallback_used`
- `provider`

## 14. Security and Compliance Notes

- Region intent should be auditable in machine events.
- If compliance constraints are introduced later, enforce via policy layer (allowed region set per account/plan).
- Never silently place into disallowed regions.

## 15. Open Questions

1. Do we want per-machine hard compliance region constraints now, or only preferred-region UX first?
2. Should fallback policy default be `strict` for paid tiers and `prefer_local` for free tiers?
3. Should geolocation recommendation be based on Cloudflare country header only, or latency telemetry once available?

## 16. Implementation Checklist

Backend:

- migration files for new fields
- store structs and scan/insert/update changes
- `/api/regions` endpoint
- machine create/update request + validation
- placement effective-region resolver
- enrollment/provisioning canonical region mapping

Frontend:

- types/api client updates for region fields
- create page + modal region/policy controls
- machine settings region/policy editor
- dashboard region display
- account settings default region selector

Operations:

- host mapping runbook for each provider
- region catalog ownership and change process
- rollout dashboards + alert thresholds

## 17. Non-Goals

- Automatic live migration of running machines between regions
- Per-request global traffic steering across machine regions
- Replacing host-affinity safety model for data-local volumes

## 18. Summary

The correct long-term direction is not removing region defaults. The correct direction is:

- preserve default regional placement
- make region intent explicit and persisted
- normalize provider-native locations into canonical placement regions
- expose region choice and fallback policy in UX
- keep restart affinity guarantees intact

This gives us safe behavior today and a stable path for multi-provider, multi-region expansion across US, Europe, and Asia.
