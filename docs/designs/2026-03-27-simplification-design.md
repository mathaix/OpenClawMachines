# Simplification Design (Pre-Onboarding)

Focus: finish structural simplifications with **OVHcloud/Hetzner-first** support. GCP remains compatible but is not the primary path.

## Objectives
- One owner per domain concept; DB as system of record.
- Provider-neutral fleet with first-class registered hosts (OVH, Hetzner) before managed GCP.
- Routing/tunnel state as projections; artifacts immutable; runtime surface reduced.

## Scope & PR Map
1. Store split + handler delegation — see `../plans/2026-03-27-pr1-store-split.md`.
2. RouteService + projector (DB truth) — see `../plans/2026-03-27-pr2-routing-projector.md`.
3. Provider-neutral fleet + placement records; OVH/Hetzner enrollment — see `../plans/2026-03-27-pr3-provider-neutral-fleet.md`.
4. Immutable artifact releases (rootfs/browser/agent) — see `../plans/2026-03-27-pr4-artifact-releases.md`.
5. Runtime surface tightening + config typing — see `../plans/2026-03-27-pr5-runtime-surface.md`.

## Priorities (OVH/Hetzner bias)
- Provider work (PR3) must support `registered_host` class first (OVH/Hetzner); GCP stays behind an adapter.
- Placement/heartbeat rules must not assume GCP metadata or public IP.
- Enrollment UX and CLI should default to registered-host paths.

## Rollout Principles
- Test-first; dual-write/dual-read where needed; feature gates for new paths.
- Shadow then cutover: run new services in parallel, compare outputs, then flip.
- Observability: metrics for placement success, route drift, reconcile lag, release promotion, enrollment failures.

## Non-Goals
- No product UX rework beyond provider-neutral field exposure.
- No new scheduling heuristics until fleet/domain boundaries are clean.

## Open Source & White-Label Alignment
- Define which components are publishable (routing service, fleet provider interface, agent/runtime) and ensure LICENSE headers/build profile support OSS bundles without proprietary pieces.
- Formalize `HostProvider` as a stable plug-in surface; ship OVH/Hetzner registered-host driver as the default example.
- Add release signing/verification to artifact flow so downstream operators can trust binaries/images.
- Externalize branding/theme (logos/colors/strings) so white-label builds avoid code edits.
- Keep auth pluggable (Firebase vs self-hosted) via adapters; document swap steps.
- Publish a “self-host/white-label” guide aligned to PR3/PR4/PR5 boundaries.

## Deferred / Callouts
- Backup/restore workflow migration: out of scope for this wave; revisit after fleet/routing cutovers.
- Auth consolidation: tracked separately; keep adapters in place during simplification.
- Event table cleanup: bundle into PR1 while touching repos to delete dead tables and EventRepo methods.
