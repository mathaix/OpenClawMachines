# Placement Logic Analysis (Superseded)

**Date:** 2026-03-31
**Status:** Superseded by multi-provider regional placement design

## Superseded Notice

This analysis captured the immediate issue but recommended removing default regional fallbacks.
That recommendation is not aligned with the product direction.

Use this design instead:

- [`docs/plans/2026-03-31-multi-provider-region-placement-and-ui-design.md`](/Users/mantiz/openclawmachines/docs/plans/2026-03-31-multi-provider-region-placement-and-ui-design.md)

Current direction:

- Keep default regional placement behavior
- Persist machine/account region intent
- Use canonical provider-neutral placement regions
- Add explicit user region selection + fallback policy in UI/API

## Current Behavior

When a machine starts, `PlacementService.Reserve()` finds an eligible host via SQL:

```sql
SELECT ... FROM hosts
 WHERE status = 'ready'
   AND last_heartbeat > now() - interval '180 seconds'
   AND (capacity_vcpus - used_vcpus) >= $1
   AND (capacity_memory_mb - used_memory_mb) >= $2
   AND region = $3              -- only if region is non-empty
   AND source_image = $4        -- only if expectedImage is non-empty
 ORDER BY used_memory_mb DESC   -- bin-pack: fill fullest host first
 LIMIT 1
```

The region and image filters are **conditional** — only applied if non-empty. But the `PlacementService` applies defaults when the caller doesn't specify them:

```go
// placement.go — Reserve()
region := req.Region
if region == "" {
    region = ps.region           // defaults to cfg.GCPRegion ("us-central1")
}
expectedImage := req.ExpectedImage
if expectedImage == "" {
    expectedImage = ps.expectedImage  // defaults to cfg.SnapshotName (GCP snapshot)
}
```

`machines.Start()` never sets Region or ExpectedImage on the request — so the defaults always kick in.

## The Problem

All three hosts are OVH bare metal, but only one has been manually set to `region=us-central1` to work around this:

| Host | Name | Region | source_image | Gets placements? |
|------|------|--------|-------------|-----------------|
| 98 | east | `us-central1` | `ocm-20260225-064227` | Yes (matches default) |
| 104 | west | `external` | NULL | No (excluded by region filter) |
| 105 | west2 | `external` | NULL | No (excluded by region filter) |

The `region` and `source_image` defaults are GCP-era concepts:
- **`region`** was needed because GCP snapshots are regional — hosts in `us-central1` need snapshots from `us-central1`
- **`source_image`** matched the GCP disk snapshot version used to create the host VM

Neither concept applies to OVH hosts, which get rootfs from GCS (not regional snapshots).

## Root Cause

The placement service was designed for a single-provider (GCP) fleet where all hosts shared a region and snapshot. The defaults made sense then — they ensured machines only landed on hosts with the right rootfs version.

With a mixed fleet (GCP + OVH, or all-OVH across regions), the defaults act as unintended filters that exclude non-GCP hosts.

## Fix

**Remove the default fallbacks.** If the caller doesn't specify a region or image, don't filter — let all ready hosts compete based purely on capacity.

```go
// Before (applies GCP defaults)
region := req.Region
if region == "" {
    region = ps.region
}

// After (empty = any region)
region := req.Region  // empty = any region
```

Same for `expectedImage` — only filter when the caller explicitly requests a specific image version.

The conditional SQL already handles this correctly:
- `region=""` → no `AND region = $N` clause → matches all hosts
- `expectedImage=""` → no `AND source_image = $N` clause → matches all hosts

**Files changed:** `backend/internal/fleet/placement.go` (Reserve method)

## Ordering: Bin-Pack vs Spread

Current ordering is bin-pack (`ORDER BY used_memory_mb DESC`) — fills the fullest host first. This made sense for GCP where empty hosts could be shut down to save cost.

For always-on OVH bare metal, **spread** (`ORDER BY machine_count ASC` or `ORDER BY used_memory_mb ASC`) makes more sense — distribute load evenly across hosts you're paying for regardless.

This is a separate change and can be done independently.

## Future: User-Facing Region Selection

To let users choose a region at machine creation:

1. Give hosts meaningful region labels (`us-east`, `us-west`) instead of GCP zones or `external`
2. Add `region` field to the Machine model and create API
3. Pass user's region choice through to `PlacementRequest.Region`
4. Add `GET /api/regions` endpoint listing available regions (derived from active hosts)
5. Add region picker to frontend create machine form

This is a separate feature — the immediate fix just removes the default filter so all hosts are eligible.

## Key Code Paths

| File | What it does |
|------|-------------|
| `backend/internal/fleet/placement.go` | `Reserve()` — applies region/image defaults, calls store |
| `backend/internal/store/postgres.go` | `PlaceMachineOnHost()` — SQL with conditional WHERE clauses |
| `backend/internal/machines/runtime.go` | `Start()` — builds PlacementRequest (never sets Region/ExpectedImage) |
| `backend/cmd/server/main.go` | `NewPlacementService(db, cfg.GCPRegion, cfg.SnapshotName)` — injects defaults |
| `backend/internal/config/config.go` | `GCPRegion` derived from `GCP_ZONE` env var |
