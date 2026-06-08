# Release Notes

This file indexes notable fixes and features in the OpenClaw Machines public
core. Each entry links to a short document under [`docs/releases/`](docs/releases/)
that describes the change, its root cause or rationale, and how it was verified.

Per [`AGENT.md`](AGENT.md) every change ships with a tracking issue and a PR; the
linked documents capture the change-level detail that doesn't belong in commit
messages.

Dates use ISO 8601 (`YYYY-MM-DD`). Change documents are named
`docs/releases/YYYY-MM-DD-short-slug.md` so they sort chronologically, and each
carries a matching `**Date:**` field in its header.

## Unreleased

| Date | Change | Type | Issue | PR |
| --- | --- | --- | --- | --- |
| 2026-06-08 | [Spot E2E lane: fix first-run abort in the tunnel/host poll loops](docs/releases/2026-06-08-spot-e2e-tunnel-fix.md) | fix | [#17](https://github.com/mathaix/OpenClawMachines/issues/17) | [#18](https://github.com/mathaix/OpenClawMachines/pull/18) |
