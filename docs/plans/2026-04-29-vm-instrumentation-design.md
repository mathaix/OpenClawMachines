# VM Resource Instrumentation — Design (rev. 3)

**Date:** 2026-04-29
**Status:** Design — revised after second Codex review (2026-04-29). Approved for implementation.
**Branch:** `instrumentation`
**Goal:** Give end users live and historical CPU + memory charts for every VM they own.
**Phase 1 scope:** OpenClaw machines only (UI). Browser VM data is collected and stored from day 1, but the browser VM detail page + tab is deferred to Phase 2.

## Why this matters to users

When something feels slow inside their VM, the user has no way today to see whether it's CPU-bound, memory-pressured, or just network. They open `/machines/:id` and see static metadata — name, slug, tunnel URL, status — but no signal about how the VM is actually using its 2 vCPU / 2 GB. Rolling out a feature, debugging a hang, or sizing the next VM all benefit from a simple "what's it doing right now" view.

This design adds a Resources tab on the machine detail page with two sub-views:

1. **Live** — sliding 5-minute window, refreshing every second, with current-value big numbers above two line charts.
2. **History** — pick a date range, see the full timeline. Available even after a VM is deleted, for the retention window.

The existing `UsageTab.tsx` already covers token/billing usage. The new tab is **Resources**, not Usage.

## What gets measured

For each VM (OpenClaw or Browser):

- **CPU %** — derived from cgroup `cpu.stat` `usage_usec`, sampled at 1 Hz, computed as `(delta_usage_usec / delta_wall_usec) * 100`. May exceed 100 % for multi-core VMs (correct: a 2-vCPU VM at full load reads 200 %).
- **Memory bytes** — cgroup `memory.current`, sampled at 1 Hz, instantaneous resident.

Not in this phase: disk I/O, network bytes, page faults, GPU. Easy to add once the pipeline exists.

## Architecture

```
                                                              (every 1 s)
[ host: ocm-agent ] ── 1 Hz sample ──▶ in-mem ring (30 s) ──┐
                                                            │ POST every 10 s, batched
                                                            ▼
[ Cloud Run: backend ] ── insert ──▶ vm_metrics_1s (Postgres, daily partitions, ~24 h)
                          │
                          │ 1-min cron downsamples 1 s → 1 m (with watermark + sample_count)
                          ▼
                     vm_metrics_1m (Postgres, daily partitions, 7 d)
                          ▲
[ user browser ] ─────────┴── GET /metrics?from=…&to=…   (history, fetched on view)
                ─────────────  GET /metrics?from=now-5m  (live, polled every 1 s)
```

**No SSE; delta polling.** The first revision used Server-Sent Events for the live view. Codex called this out: SSE behind Cloud Run scales with viewers, not VMs (100 open tabs = 100 long-lived handlers + 100 polls/s on Postgres). The first replacement (full 5-minute window every second) preserved the per-tab read load (Codex rev. 2). Final design uses **delta polling**: each request includes `?since=<lastSeenTs>` and the server returns only points newer than that. The `(account_id, vm_id, kind, ts)` index covers the query, and each tick scans ~0–10 rows. 100 open live tabs ≈ 100 tiny indexed reads/s, well under existing API headroom. The client animates the chart frame-by-frame between fetches so it feels live even though new data only lands every ~10 s (agent batch cadence).

### Source of truth: cgroup v2 on the host

Every VM already runs in its own systemd unit:

- `ocm-vm-<machine-id>.service` for OpenClaw VMs
- `ocm-browser-vm-<vm-id>.service` for Browser VMs

These map to cgroup v2 paths under `/sys/fs/cgroup/system.slice/`. The two files we read are kernel-owned and atomic-read:

- `cpu.stat` — line-oriented; `usage_usec <N>` is the cumulative CPU time charged to the cgroup (microseconds, monotonic *while the cgroup is alive*).
- `memory.current` — single integer, current bytes.

CPU% formula:

```
cpu_pct = (cur.usage_usec - prev.usage_usec) / (cur.wall_us - prev.wall_us) * 100
```

Wall time captured at sample time on the host clock (`time.Now()`), not from any kernel field. This handles the case where two consecutive reads happen 0.97 s and 1.04 s apart — the divisor uses the actual elapsed micros.

### Discovery and sampler reset semantics

`cgroup.Discover(root string) []VMRef` scans `system.slice/` every 10 s for entries matching `^ocm-(browser-)?vm-([0-9a-f-]+)\.service$`. Cgroups that disappear between discovery and sample are dropped silently.

The sampler maintains `prev map[unitName]Sample`. **Reset rules**, all with tests:

1. On each discovery cycle, prune `prev` entries whose unit no longer exists.
2. On sample, if `cur.usage_usec < prev.usage_usec` (cgroup was destroyed and the unit was reused for a new VM with the same UUID — extremely rare, but possible after stop/start within the discovery window), treat it as a fresh first sample: replace `prev` and emit no point this tick.
3. First sample for a freshly-seen cgroup has no delta → emit a memory-only point with `cpu_pct = NULL`. Schema permits this (see below).

### Sampling loop (agent)

New package `backend/internal/vmmetrics/sampler`, single goroutine:

```go
type Sampler struct {
    Root         string                  // /sys/fs/cgroup/system.slice
    Tick         time.Duration           // 1 s
    DiscoverTick time.Duration           // 10 s
    BatchEvery   time.Duration           // 10 s
    HostID       int                     // integer, matches hosts.id
    Pusher       Pusher                  // POST batch to backend
    prev         map[string]Sample
    buffer       []Point                 // ring, capacity ~30 s of samples
}
```

- 1 Hz tick: for each cgroup, read both files, compute deltas vs `prev`, append a `Point{vm_id, kind, ts, cpu_pct, mem_bytes}` to the buffer (cpu_pct may be null per rule 3).
- Every 10 s: drain buffer into a batch, POST to backend. On non-2xx, requeue with exponential backoff (cap 60 s). On buffer full (>30 s of un-sent data), drop oldest with a counter (`vm_metrics_dropped_total`).

### Wire format (agent → backend)

`POST /api/agent/vm-metrics`

```json
{
  "host_id": 7,
  "points": [
    {
      "vm_id": "9f3c…",
      "kind": "machine",
      "ts": "2026-04-29T18:33:01.000Z",
      "cpu_pct": 12.4,
      "mem_bytes": 524288000
    }
  ]
}
```

`host_id` is the integer host row id (matches `hosts.id`), encoded as JSON number — same format as heartbeat. `cpu_pct` may be `null` for first samples.

**Auth (mirrors heartbeat exactly).** The metrics handler uses the existing `authenticateAgent(ctx, r, hostID)` helper at [`backend/internal/api/agent_heartbeat.go:142`](../../backend/internal/api/agent_heartbeat.go) without modification. Per-host token is preferred; fleet-token fallback works (Codex rev. 2 NEW CRITICAL — every existing host today auths with the fleet token because `hosts.agent_token` is NULL on all of them at provisioning, [`provisioner.go:210`](../../backend/internal/provisioner/provisioner.go)). Per-host token rotation is a separate hardening epic; once it lands, this endpoint tightens automatically.

The handler:

1. Parses `host_id` as int (mirrors heartbeat).
2. Calls `authenticateAgent(ctx, r, hostID)` — accepts either token form.
3. For each point, validates `vm_id` is currently placed on this `host_id`:
   - `kind == "machine"`: `SELECT account_id FROM machines WHERE id = $1 AND host_id = $2`
   - `kind == "browser"`: `SELECT account_id FROM browser_vms WHERE id = $1 AND host_id = $2`
   - Hard delete is the actual delete model (`postgres.go:604`), so missing row = "this host has no claim on this vm_id" → drop.
4. Stores the resolved `account_id` on the metrics row. Reads never join the live VM row, so deleted-VM history still authorizes.
5. Drops points with `vm_id` that fail validation, returns count in the response (so the agent can log).
6. Defense-in-depth: also rate-limit by `host_id` (1 req/s) so a stolen fleet token can't flood arbitrary metrics.

**Ingest validation, all enforced server-side, all tested:**

- `Content-Length` ≤ 64 KiB.
- `points` length ≤ 1000.
- For each point: `ts` within ±5 min of server time; `mem_bytes ≥ 0`; `cpu_pct` either null or in `[0, 100 * vcpus * 2]` (vcpus from VM row; the *2 is slack for noisy kernel accounting).
- All `ts` rounded to second-precision (nanos discarded).
- Future-dated points (which would auto-create future partitions on insert) are rejected before reaching the DB.

### Storage schema

Migration `084_vm_metrics.sql`. Two tables, partitioned by day on `ts`:

```sql
CREATE TABLE vm_metrics_1s (
    account_id INT          NOT NULL,
    host_id    INT          NOT NULL,
    vm_id      UUID         NOT NULL,
    kind       TEXT         NOT NULL CHECK (kind IN ('machine', 'browser')),
    ts         TIMESTAMPTZ  NOT NULL,
    cpu_pct    REAL,                                -- NULL allowed for first sample
    mem_bytes  BIGINT       NOT NULL CHECK (mem_bytes >= 0),
    PRIMARY KEY (vm_id, kind, ts)
) PARTITION BY RANGE (ts);

-- Access path is "give me points for one VM in this account in this time range",
-- and "list all my account's VMs over a range" for any future fleet view.
CREATE INDEX vm_metrics_1s_account_vm_ts ON vm_metrics_1s (account_id, vm_id, kind, ts);

CREATE TABLE vm_metrics_1m (
    account_id   INT          NOT NULL,
    host_id      INT          NOT NULL,           -- last host the bucket saw; informational
    vm_id        UUID         NOT NULL,
    kind         TEXT         NOT NULL CHECK (kind IN ('machine', 'browser')),
    ts           TIMESTAMPTZ  NOT NULL,           -- minute-aligned bucket start
    cpu_pct      REAL,                            -- avg over the minute, NULL if no samples had cpu_pct
    mem_bytes    BIGINT       NOT NULL CHECK (mem_bytes >= 0),
    sample_count INT          NOT NULL,           -- how many 1s rows fed this bucket
    finalized    BOOLEAN      NOT NULL DEFAULT FALSE,
    PRIMARY KEY (vm_id, kind, ts)
) PARTITION BY RANGE (ts);

CREATE INDEX vm_metrics_1m_account_vm_ts ON vm_metrics_1m (account_id, vm_id, kind, ts);
```

No FK to `machines.id` or `browser_vms.id` — metrics outlive the VM row. `account_id` is denormalized so deleted-VM history reads still authorize cleanly. (Codex CRITICAL.)

PK includes `kind` because a UUID collision across tables, while astronomically unlikely, isn't structurally impossible; a check constraint costs nothing and keeps the model honest.

Daily partitions named `vm_metrics_1s_y2026m04d29`. **Retention is daily-granular**, not exactly 24 h or 7 d — a partition can survive up to 24 h past the nominal cutoff before the cron drops it. Acceptable for Phase 1 and explicitly documented.

### Maintenance crons (singleton via pg advisory locks)

A new package `backend/internal/vmmetrics/maint` exposes three jobs. Cloud Run runs N instances; each starts the loops, but each job acquires a transaction-scoped advisory lock before running. Constants live in `maint/locks.go`:

```go
const (
    lockEnsurePartitions int64 = 0x564D5F455053 // "VM_EPS"
    lockDownsample1s1m   int64 = 0x564D5F4453   // "VM_DS"
    lockDropPartitions   int64 = 0x564D5F4452   // "VM_DR"
)
```

Pattern (Codex rev. 2 — session-scoped advisory locks don't compose with `pgxpool` because acquire and release can land on different connections; transaction-scoped locks auto-release on commit/rollback and don't have that problem):

```go
func runWithLock(ctx context.Context, db *pgxpool.Pool, key int64, job func(pgx.Tx) error) error {
    return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
        var got bool
        if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", key).Scan(&got); err != nil {
            return err
        }
        if !got { return nil } // another instance is running this job
        return job(tx)
    })
}
```

The job runs inside the lock-holding transaction, so all DDL/DML it performs is also rolled back if the job fails — important for `EnsurePartitions` and `DropOldPartitions` which do schema changes.

| Job | Frequency | What it does |
|-----|-----------|--------------|
| `EnsurePartitions` | every 1 h | Create partitions for `today + 0..3` days for both tables. Idempotent (`IF NOT EXISTS`). |
| `Downsample1s1m` | every 1 min | For each `(vm_id, kind, minute_bucket)` whose bucket end is older than the **watermark** (currently `now() - 90s`, > max retry window) and not yet `finalized`: aggregate matching 1 s rows, upsert into `vm_metrics_1m` with `sample_count = N`, set `finalized = true` only if `N >= 60` *or* `bucket_end < now() - 5min` (give very-late points a few minutes to land). |
| `DropOldPartitions` | every 1 h | `DROP TABLE IF EXISTS` 1 s partitions whose nominal date is more than 1 day before today (UTC), 1 m partitions more than 7 days before. |

**Late-arrival handling.** The first revision used `INSERT … ON CONFLICT DO NOTHING`, which freezes a partial bucket if more samples land later. Now (Codex rev. 2 — must include `finalized = EXCLUDED.finalized` so a 30-sample bucket can update to 60-sample finalized on the next pass):

```sql
INSERT INTO vm_metrics_1m (account_id, host_id, vm_id, kind, ts, cpu_pct, mem_bytes, sample_count, finalized)
VALUES (...)
ON CONFLICT (vm_id, kind, ts) DO UPDATE SET
  cpu_pct      = EXCLUDED.cpu_pct,
  mem_bytes    = EXCLUDED.mem_bytes,
  sample_count = EXCLUDED.sample_count,
  host_id      = EXCLUDED.host_id,
  finalized    = EXCLUDED.finalized
WHERE NOT vm_metrics_1m.finalized;
```

`finalized` in `EXCLUDED` is computed per-row by the downsample job: `finalized = (sample_count >= 60 OR bucket_end < now() - INTERVAL '5 min')`. Once a bucket is finalized, the `WHERE NOT vm_metrics_1m.finalized` predicate drops further updates — late points are silently discarded.

Insert path failure if today's partition is missing: `EnsurePartitions` runs at `t-0` boot and every hour. If a point lands and the partition isn't there yet, the insert fails with a clear error → the agent batch retries → typically next batch (10 s later) succeeds because the next maintenance tick (or boot run) created it. Tested.

### User-facing endpoints (account-scoped, matches existing convention)

All under `/api/accounts/{accountId}/...`, all pass through `srv.AccountMiddleware` for membership auth (matches `backend/internal/api/server.go:362-405`).

#### Phase 1 — machines only

```
GET /api/accounts/{accountId}/machines/{id}/metrics?from=<ISO>&to=<ISO>
GET /api/accounts/{accountId}/machines/{id}/metrics?since=<ISO>
```

The two query forms:

- **Range** (`from`/`to`): used by the History view. Server picks resolution by range:
  - `to - from <= 1h` → query `vm_metrics_1s`
  - `to - from > 1h` → query `vm_metrics_1m`
- **Cursor** (`since`): used by the Live view. Server returns up to 1000 points from `vm_metrics_1s` with `ts > since`, ordered by `ts`. Always 1-second resolution. The first call (no `since`) returns the last 5 minutes; subsequent calls pass the `ts` of the last received point.

If a range straddles the 1 s retention boundary (e.g., `from = -25h, to = now`), the server splits the query: 1 m for the older portion, 1 s for the last 24 h. The response carries `resolution: "1s" | "1m" | "mixed"`.

Hard cap: `to - from <= 7d`. Larger ranges → 400.

Response:

```json
{
  "vm_id": "…",
  "resolution": "1s",
  "from": "…",
  "to": "…",
  "points": [{"ts": "…", "cpu_pct": 12.4, "mem_bytes": 524288000}]
}
```

Live view: frontend polls the cursor form every 1 s with `?since=<lastTs>`, appending the (usually 0–10) returned points to its in-memory buffer.

Authorization: the handler verifies the machine belongs to `accountId`. For deleted machines, the metrics rows still carry `account_id`, and the same handler returns history if the caller's account matches that stored `account_id`. Tested.

#### Phase 1 — browser VMs (data only, no UI)

```
GET /api/accounts/{accountId}/browser-vms/{id}/metrics?from=<ISO>&to=<ISO>
```

Same shape as machines, gated on browser-VM ownership. No frontend in Phase 1 — Codex correctly noted that `BrowserVMLivePage` is an iframe proxy, not a tabbed detail page, so a browser-VM detail UI is its own design (Phase 2).

### Frontend / UX (Phase 1)

New tab `ResourcesTab.tsx` in `MachineView.tsx`, placed between **Overview** and **Channels**. Tab icon: `Activity` from lucide-react. Inside the tab, two sub-tabs via Radix `<Tabs>`: `live` (default) and `history`. URL state via `?view=live|history` so deep-links survive refresh.

#### Live view layout

```
┌────────────────────────────────────────────────────┐
│  Live ●        History                             │  sub-tabs (● = active)
├────────────────────────────────────────────────────┤
│  ┌──────────────────────┐  ┌────────────────────┐  │
│  │ CPU                  │  │ Memory             │  │
│  │ 142%                 │  │ 512 MB             │  │
│  │ of 200% allocated    │  │ of 2,048 MB        │  │
│  └──────────────────────┘  └────────────────────┘  │
│                                                    │
│  CPU usage  ·  last 5 minutes                      │
│  ┌──────────────────────────────────────────────┐  │
│  │              ▁▂▃▅▇█▇▅▃▂▁▂▃▅▇█▇               │  │
│  └──────────────────────────────────────────────┘  │
│                                                    │
│  Memory usage  ·  last 5 minutes                   │
│  ┌──────────────────────────────────────────────┐  │
│  │   ─────────╱──╲────────╱──╲──────────         │  │
│  └──────────────────────────────────────────────┘  │
│                                                    │
│  Updated 1s ago                                    │
└────────────────────────────────────────────────────┘
```

UX decisions:

- **Big-number cards include a denominator**: "of 200% allocated" (`vcpus * 100`), "of 2,048 MB" (`machine.memory_mb`). Reads like "how much of what I'm paying for am I using" without forcing the user to remember sizing.
- **CPU y-axis**: 0 to `vcpus * 100`. A 2-vCPU VM's chart goes 0–200, so saturation is visually obvious — not just "the line is high."
- **Memory y-axis**: 0 to `machine.memory_mb` (the allocated amount).
- **No threshold zones** (no red bars at 90%) in Phase 1 — bias toward neutrality. Easy to add when we have real data on what users care about.
- **Time axis**: relative for live ("now", "-1m", "-5m"), absolute for history.
- **Tooltip on hover**: shows exact value + absolute timestamp.
- **"Updated Ns ago" indicator**: derived from the polling cursor. Goes amber after 30 s without a new point (genuine "your VM may have stalled" signal — not a network UI failure).
- **Stopped/missing data**: charts render existing data; right edge fades to a thin gray "stopped" band if `last_point.ts < now() - 30s`. Big-number cards show "—" with a "VM stopped" caption.
- **Empty state** (brand-new VM, no data yet): skeleton bars where charts go; copy reads "Waiting for first sample…"

#### History view layout

```
┌────────────────────────────────────────────────────┐
│  Live           History ●                          │
├────────────────────────────────────────────────────┤
│  Range: [Last 24 h ▼]   [Custom…]   1-min res      │
│                                                    │
│  CPU usage                                         │
│  ┌──────────────────────────────────────────────┐  │
│  │ ┄┄╱╲┄╱╲╱╲┄╱╲╱╲╱╲╱╲┄┄╱╲╱╲┄┄                  │  │
│  └──────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────┐  │
│  │ ░░░░░░░░░░░░░░░░░░░░░░ (brush thumb)         │  │
│  └──────────────────────────────────────────────┘  │
│                                                    │
│  Memory usage  (same shape)                        │
└────────────────────────────────────────────────────┘
```

- **Range presets**: Last 5 m / 1 h / 24 h / 7 d, plus Custom (date+time pickers).
- **Resolution badge**: "1-second", "1-minute", or "mixed".
- **Brush** under each chart for zoom/pan; touch-friendly.
- **No big-number cards in history view** — current values aren't meaningful at a historical timestamp.
- **Empty state**: "No data in this range" — covers retention drop, VM-not-yet-existed, and account-just-disabled cases without claiming which.

#### Polling, error, loading

- **First mount**: skeleton bars; range request fires immediately.
- **Live polling**: `setInterval(1000)` calls `GET …/metrics?since=<lastTs>` with cursor advancement. Network failure: keep last data, show "reconnecting…" badge top-right; auto-recovers on next successful tick.
- **History view**: single fetch on range change; no polling.
- **403** (rare — would mean account membership broke mid-session): redirect to dashboard with toast.

#### Theming, mobile, a11y

- Uses existing Tailwind tokens (`--color-primary`, `--color-muted`); dark mode works without per-chart hacks.
- Below 640 px: big-number cards stack to 1 column; charts already stacked.
- Recharts is keyboard-navigable for the time axis; tooltip is announced via `aria-live="polite"`.

#### Dependencies

Add `recharts` to `frontend/package.json`. Bundle impact: ~80 KiB gzipped (recharts + d3-shape transitively). Lockfile change goes in the same commit. No other frontend deps.

### Existing VMs and hosts (rollout behavior)

This section is explicit because Codex flagged the gap (rev. 2 NEW CRITICAL).

**Existing OpenClaw + Browser VMs running under per-VM systemd units** (post runtime-ownership-split — most/all of the live fleet today):

- New agent rolls out per host on admin click ("Update" button on AdminHosts).
- After agent restart, sampler scans `system.slice/`, finds all current `ocm-(browser-)vm-*.service` units, and starts sampling on the next 1 Hz tick.
- Memory chart begins populating immediately (first sample is a valid memory read).
- CPU chart begins on the second tick (first sample emits `cpu_pct = NULL`).
- **No backfill.** History for an existing VM starts at agent-rollout time on its host. The first 1-minute bucket may have <60 samples; downsample finalizes it on the watermark expiry, not on 60-sample completeness — so partial-minute buckets are still visible in the History view.

**Legacy "direct-owner" VMs** (pre-split — agent's own cgroup, no per-VM unit, kept alive by `KillMode=process`): cgroup path doesn't match the discovery regex. Sampler skips them silently. These are being migrated out anyway.

**Existing hosts authenticate with the fleet token.** Every host today has `hosts.agent_token IS NULL` (provisioned without one). Phase 1 metrics auth therefore mirrors heartbeat exactly — fleet token works. A separate hardening pass will generate per-host tokens at provisioning and rotate existing hosts; metrics auth tightens automatically when that lands. No host-side change needed for this feature.

### Naming and FK awareness

`kind` discriminator avoids ambiguity. UUIDs are unique-per-table in practice but the discriminator costs ~1 byte and keeps queries explicit. The user-facing endpoints take a path-typed id and *know* the kind, so they always filter on `(account_id, kind, vm_id)` together.

## Test plan (additions in **bold** are from the Codex review)

### Backend unit (Go)

- `cgroup.ParseCPUStat` — fixture text parses; missing `usage_usec` field returns error.
- `cgroup.ParseMemoryCurrent` — single-integer file parses; whitespace tolerated.
- `cgroup.CPUPercent(prev, curr)` — known deltas → known %, including >100 %, divide-by-zero → 0, **and the `cur < prev` reset case → returns "first sample" sentinel**.
- `cgroup.Discover(root)` — fixture dir with mixed `ocm-vm-*`, `ocm-browser-vm-*`, `unrelated.service` → only VM units returned, kind classified correctly.
- `sampler.Tick` — fed two synthetic samples → one point with the expected fields. **First sample emits null cpu_pct.** **Cgroup-disappear pruning: stop the cgroup, run discover, ensure prev entry is removed.**
- `store.InsertVMMetrics` (integration, real Postgres via testcontainers) — round-trips a 200-point batch. **Null cpu_pct round-trips correctly.**
- `store.QueryVMMetrics` — given seeded data, returns the right slice for a range; resolution selection picks `_1s` for ≤1h, `_1m` for >1h, `mixed` for straddle ranges.
- **`store.QueryVMMetricsForDeletedMachine`** — seed metrics with `account_id`, delete the `machines` row, query as the owning account → still returns history.
- **`store.AccountIDFromMetrics`** — denormalized `account_id` enforces ownership without joining the live VM row.
- `maint.Downsample1s1m` — seed 60 1 s points across one minute, run, expect one 1 m row with correct avg + `sample_count = 60` + `finalized = true`. Run again → no-op.
- **`maint.Downsample1s1m` late-arrival** — seed 30 of 60 points, run after 90 s → unfinalized 1 m row with `sample_count = 30`. Add the other 30 within the finalization window → re-run → updated row with `sample_count = 60`, `finalized = true`. Add a point after finalization → silently dropped.
- `maint.DropOldPartitions` — seed daily partitions for the last 30 days → 1 s partitions older than 1 day and 1 m partitions older than 7 days dropped. **UTC midnight rollover: simulate two runs straddling 23:59:30 → 00:00:30, no partition is dropped that the next minute would still write to.**
- `maint.EnsurePartitions` — fresh DB → today + 3 future partitions exist on both tables.
- **`maint.AdvisoryLockContention`** — start two `Downsample1s1m` jobs concurrently against the same DB; only one runs at a time, second exits cleanly. Same test for `EnsurePartitions` and `DropOldPartitions` (one job per lock key).
- **`maint.LockReleaseOnTxRollback`** — start a job that fails inside its transaction; verify the advisory lock is released (next caller acquires it immediately) and partial schema changes are rolled back.
- **`Insert with no partition`** — drop today's partition, insert a point → fails with the expected SQLSTATE; agent retry behavior tested in the HTTP layer.

### Backend HTTP (Go)

- `POST /api/agent/vm-metrics` — bearer-auth: missing token 401, wrong token 401, **fleet-token 401 (must be per-host)**, valid → 204 + rows in DB.
- `POST` — point with wrong `host_id` for the `vm_id` is rejected (count returned in response).
- **`POST` validation**: oversize body 413; >1000 points 400; ts older than 5 min skew 400; ts in the future 400; negative `mem_bytes` 400; `cpu_pct` outside `[0, 100*vcpus*2]` 400.
- **`POST` duplicate semantics**: same (vm_id, kind, ts) twice in one batch → second is dropped, response counts mismatch.
- `GET …/machines/:id/metrics` — owner gets points; non-owner gets 403; **deleted-VM range still returns historical points to the owning account; cross-account access still 403.**
- `GET …/machines/:id/metrics?from=…&to=…` — straddle range returns `resolution: "mixed"` and merged points.

### Frontend (Vitest + Testing Library)

- `ResourcesTab` renders the Live sub-tab by default; mocked `getMachineMetrics` returning 5 points → both charts render.
- `ResourcesTab` History sub-tab — mocked `getMachineMetrics` returning 60 points → line chart renders with correct domain and resolution badge.
- Range picker switches resolution badge from "1-second" to "1-minute" when the range exceeds 1 h.
- **Polling lifecycle**: `setInterval` fires every 1 s while the Live tab is mounted; clearing on unmount stops further fetches.
- **Polling failure**: mocked failure → "reconnecting…" indicator visible; recovery on next successful tick clears it.

### End-to-end smoke (manual, dev host)

1. Provision a fresh machine, open Resources tab → live values appear within ~10 s.
2. `stress-ng --cpu 2 --timeout 30s` inside the VM → CPU chart spikes to ~200 %.
3. Allocate 500 MB inside the VM (`head -c 500M /dev/zero | tail`) → Memory chart steps up.
4. Stop the VM → live view shows "VM stopped"; history view still loads the last 30 minutes.
5. Delete the machine → history endpoint still serves the retention window for the owning account; another account gets 403.
6. Wait 1 day → history at "last 7 days" shows 1-min resolution data for the day-old portion; data older than retention is gone (no errors, just empty).

## Build sequence

Each step is a logical commit.

1. **Migration** — `084_vm_metrics.sql` + initial partitions + advisory-lock helpers. Tests for `Store.InsertVMMetrics`/`QueryVMMetrics` including null cpu_pct.
2. **cgroup library** — `vmmetrics/cgroup` with parse + discover + percent including reset case. Pure, fixture-driven tests.
3. **Sampler** — `vmmetrics/sampler` with the goroutine, ring, batch, prune-on-discover. Tests use a fake `Pusher`.
4. **Agent integration** — wire sampler into `cmd/agent/main.go`, gated on `cfg.BackendURL && cfg.HostID && cfg.AgentToken` like heartbeat.
5. **Backend ingest endpoint** — `POST /api/agent/vm-metrics` with per-host-only auth, placement check, validation, drop-and-count.
6. **Maintenance crons** — `EnsurePartitions`, `Downsample1s1m`, `DropOldPartitions` wired into the API server background loops, all with advisory locks.
7. **User history endpoint** — `GET /api/accounts/{accountId}/machines/{id}/metrics`. Includes browser-VM endpoint at the same path shape (data-only — no UI yet).
8. **Frontend** — add Recharts, build `ResourcesTab` (machines only), register in `MachineView.tsx`. 1 Hz polling for live.
9. **End-to-end smoke** on east host; commit screenshots in PR.

## Risks and open questions

- **Cardinality at 1 Hz**: 100 concurrent VMs → 8.6 M rows/day in `vm_metrics_1s`, ~430 MB before pg overhead. Daily partitions cap query scan to one day. If write rate becomes a problem we can drop tick to 2 s or push aggregates from agent.
- **Polling at 1 Hz with `since=` cursor**: 100 simultaneously-open Resources tabs = 100 indexed reads/s, each scanning 0–10 fresh rows (everything older than `lastTs` is skipped by the index). Trivial DB load. We accept up to 10 s data freshness because the agent batches every 10 s — the live chart visibly lags real-time by that much, which is honest and matches the data we actually have.
- **VM moves between hosts**: each batch is host-scoped. If a VM moves, the new host's agent picks up its cgroup and continues sampling. The 1 s history just has a few-second gap during the move.
- **Legacy direct-owner VMs (pre runtime-ownership-split)**: their cgroup isn't `system.slice/ocm-vm-*.service`. Sampler won't see them. Acceptable — those are being migrated out anyway.
- **Privacy**: CPU/memory of one user's VM is not visible to others — strictly account-scoped at the API layer. The agent → backend ingest doesn't carry user identity at all; ownership is resolved server-side via host placement and stored on the metric row.
- **Browser VM UI**: deferred to Phase 2 along with the broader question of what a browser-VM detail page should look like. Phase 1 collects and stores browser-VM metrics from day 1 so Phase 2 starts with data, not a backfill.
- **Storage growth past Phase 1**: at 1 m × 7 d × 100 VMs = 1 M rows in `vm_metrics_1m`. Fine. Disk and network would add columns or sibling tables; design accommodates either.
- **Retention is daily-granular, not exact**: dropping whole partitions means a 1 s row can live up to ~48 h before its partition is dropped. Acceptable Phase 1; if exact retention matters later, switch to row-level deletes or hourly partitions.

## Out of scope (Phase 2+)

- Browser VM detail page + Resources tab.
- Disk I/O, network counters, page faults — same pipeline, more cgroup fields.
- Alerting ("ping me if my VM is at 95 % CPU for 5 min").
- Aggregated views (account-wide rollup, host-wide rollup).
- Export to Prometheus / OpenTelemetry — straightforward once the schema is stable.

## Changelog

- **rev. 3 (2026-04-29)** — addressed Codex rev. 2 findings + added concrete UX spec + Existing-VMs section. Auth changed back to mirror-heartbeat-exactly (fleet-token fallback works) because every existing host has `agent_token IS NULL` — strict per-host-only would brick the fleet; per-host token rotation is now a separate hardening epic. Advisory locks moved to `pg_try_advisory_xact_lock` inside a transaction so `pgxpool` can't lose the release. Downsample upsert now includes `finalized = EXCLUDED.finalized`. Removed nonexistent `deleted_at IS NULL` from placement SQL. Live polling switched to delta-cursor (`?since=lastTs`) so per-tab DB load stays bounded. Added rate-limit-by-host on ingest as defense-in-depth. Lock-contention tests extended to all three crons + tx-rollback case. New section: full UX layout for live and history views, including big-number cards, axis policy, stopped-VM affordance, mobile/a11y/theming notes.
- **rev. 2 (2026-04-29)** — addressed first Codex review: `account_id`/`host_id` denormalized on metrics rows; integer `host_id`; `cpu_pct` nullable; `kind` CHECK constraint; PK + index include `kind` and align with access path; SSE replaced with polling; routes moved under `/api/accounts/{accountId}`; advisory locks on maintenance jobs; downsample uses watermark + `sample_count` + `finalized`; sampler reset rules and discover-prune; ingest validation specified; daily-granular retention documented; browser-VM UI moved to Phase 2 (data still collected). Added tests: deleted-VM auth, null cpu_pct round-trip, late-downsample, advisory-lock contention, UTC midnight rollover, no-partition insert, oversize/skew/duplicate validation, polling lifecycle.
- **rev. 1 (2026-04-29)** — initial design.
