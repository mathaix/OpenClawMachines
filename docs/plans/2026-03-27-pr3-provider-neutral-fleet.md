# PR3 — Provider-Neutral Fleet (OVH/Hetzner First) & Placement Records

Goal: support registered hosts (OVH/Hetzner) as first-class; make placement records authoritative; GCP behind adapter.

Scope
- Normalize host fields: provider, provider_class, lifecycle_mode, provider_host_id, agent_endpoint, labels/capabilities JSON.
- Introduce `HostProvider` interface with `registered_host` implementation (OVH/Hetzner) and GCP adapter behind it.
- Enrollment flow for registered hosts; remove reliance on GCP metadata/public IP for identity.
- Require placement records on start/restart; machines.host_id treated as projection during migration.
- Heartbeat/placement logic updated to not assume GCP-specific signals.
- Publish HostProvider SDK surface (docs/examples) for open-source/white-label consumers; OVH/Hetzner driver as reference.

Checklist
- [ ] DB migration adds/normalizes provider-neutral host fields; backfilled for existing hosts.
- [ ] HostProvider interface + drivers: `registered_host` (OVH/Hetzner), `gcp_managed` (compat).
- [ ] Enrollment API/CLI defaults to registered-host; GCP path still available.
- [ ] Placement dual-write/backfill; read path switched to placement records after backfill done.
- [ ] Heartbeat processing works without public IP; uses enrollment token + provider metadata.
- [ ] Tests: enroll OVH/Hetzner fixture, heartbeat, placement, restart with affinity, host drain/decommission.
- [ ] HostProvider SDK doc + sample drivers published for OSS/white-label use.

Cutover
- Feature flags: `fleet.provider_neutral`, `placement.read_from_records`.
- Sequence: dual-write placements → backfill → enable read_from_records → enable provider_neutral → deprecate GCP-only fields.

Verification
- Load/chaos: kill a registered host; ensure reconcile/placement handles it; drift=0.
- Route/placement metrics stable; OVH/Hetzner enroll succeeds end-to-end.
