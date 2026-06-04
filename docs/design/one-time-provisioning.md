# One-Time Provisioning & Post-Provision Lifecycle

## Problem
After first boot, machines should never re-enter the “provisioning” state. Today, if the previous host is marked unreachable/terminated (often during agent updates), the start path clears `host_id`/`vm_ip`, performs a fresh placement, and sets status to `provisioning`. Admin reset (`POST /admin/machines/{id}/reset`) also uses ReleaseHard + Unassign, so the next start re-provisions. This breaks affinity, can rebuild on a new host without migration, and violates “provisioning is first-boot only”.

## Principles
- Provisioning is **one-time**. After a machine completes its first boot, it may be stopped, started, or migrated, but not implicitly re-provisioned.
- Restarts use the existing data volume and show status “starting”.
- If the home host is unavailable, an explicit migration is required; start must fail with a migration-needed error, not fall back to provisioning.
- Admin reset must not allow re-provisioning; it should funnel operators into migration.

## Proposed Behavior Changes
1) **Provisioning gate**
   - Add an immutable flag (use `ProvisioningCompletedAt` on the machine row) after first successful boot.
   - Start path: if `ProvisioningCompletedAt != nil`, disallow any fresh placement. Only allow restart on the same volume/host; otherwise return `migration_required`.

2) **Host checks on restart**
   - Restart path retains host affinity. If host status is `terminated/unreachable/error/draining`, return `migration_required`; do not clear `host_id`/`vm_ip`.

3) **Admin reset**
   - For provisioned machines, do **not** run ReleaseHard/Unassign. Instead, set status to `error` with message “migration required; host unavailable”, or invoke the migration workflow. Force-reset that enables reprovision should be removed/guarded.

4) **Reconciler**
   - When marking a host unreachable/terminated, do not clear host_id for provisioned machines. Mark affected machines `error` + `migration_required` (status message) and keep host_id for migration context.

5) **API / UI**
   - Starting a provisioned machine with an unavailable host returns 409/422 with code `MIGRATION_REQUIRED`.
   - Admin reset endpoint should indicate migration is needed, not silently reset to stopped.

## Migration Workflow (unchanged, but enforced)
   - Explicit workflow to move the data volume: snapshot/copy volume, ReleaseHard, attach to new host, then set status “starting” on the new host.

## Success Criteria
- A provisioned machine never transitions to “provisioning” again.
- Restart on healthy host → “starting”.
- Host unavailable → start returns `migration_required`; status remains non-provisioning; host_id retained for migration.
- Admin reset no longer enables implicit reprovisioning.
- Reconciler doesn’t drop affinity for provisioned machines; it surfaces migration-required state.
- Host heartbeat gaps during planned maintenance do not trigger `unreachable/terminated` or migration-required; maintenance window skips staleness actions.

## Open Decisions
- Exact error code (409 vs 422) and error payload shape (`code`, `message`, `host_id`).
- Whether to feature-flag the guard (e.g., `MACHINE_REQUIRE_MIGRATION_ON_HOST_LOSS`).
- Minimum grace period for host heartbeat to avoid false unreachable during agent updates.
- How to mark maintenance: `hosts.maintenance_mode` boolean, `status=maintenance/draining`, or a schedule table; default duration when UI “Update” is clicked.
- API to clear a `migration_required` flag after successful migration or admin override.

## Rollout Plan
1) Implement start-path guard + migration_required error.
2) Update admin reset to refuse reprovision for provisioned machines.
3) Adjust reconciler to mark migration_required instead of clearing host_id.
4) Add tests: start on terminated host (provisioned) → migration_required; admin reset keeps host_id; reconciler on terminated host sets error/migration_required.
5) Ship behind a feature flag if needed; then enable globally.

## Maintenance Awareness
- When an operator clicks “Update” on a host, set a maintenance window (e.g., `maintenance_mode=true` with a TTL or explicit start/end).
- The host reconciler skips heartbeat staleness/termination while maintenance is active.
- After maintenance ends, normal heartbeat checks resume.

## Migration Flag Reset
- Introduce a `migration_required` flag/status message on machines.
- Provide an admin API to clear this flag after a successful migration or override, without triggering reprovision.
