# Spot E2E lane: fix first-run abort in the tunnel/host poll loops

- **Type:** fix
- **Date:** 2026-06-08
- **Issue:** [#17](https://github.com/mathaix/OpenClawMachines/issues/17)
- **PR:** [#18](https://github.com/mathaix/OpenClawMachines/pull/18)
- **Area:** CI — Spot E2E lane (`.github/workflows/spot-e2e.yml`, `ci/spot-e2e.sh`)
- **Tracking:** part of [#10](https://github.com/mathaix/OpenClawMachines/issues/10) (Firecracker E2E on GCP spot)

## Summary

The Spot E2E lane aborted within seconds the first time it ever ran, before it
could provision a host or boot a Firecracker VM. The cause was a shell
control-flow bug, not a problem with the GCP/Cloudflare integration.

## Symptom

First-ever dispatch (run `27159781675`) ended in `failure` ~60s in. The job log
showed the script dying 4 milliseconds after `==> starting cloudflared tunnel`:

```
18:53:47.2417980Z ==> starting cloudflared tunnel
18:53:47.2457898Z ==> TEARDOWN
18:53:49.0991469Z ##[error]Process completed with exit code 1.
```

## Root cause

`ci/spot-e2e.sh` runs under `set -euo pipefail`. The tunnel-URL wait loop did:

```bash
BACKEND_URL="$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' /tmp/cf.log | head -1)"
```

On the first iteration `/tmp/cf.log` is still empty, so `grep` exits non-zero;
`pipefail` propagates that, and `errexit` aborts the entire script before the
loop can retry or the `no tunnel URL` guard can run. The host-status `read`
loop had the same latent failure mode for a transient empty/non-JSON response.

## Fix

Make the polling substitutions non-fatal so the loops behave as designed:

- Tunnel-URL grep substitution now ends with `|| true` — the loop polls for up
  to 40s for cloudflared to print the URL.
- The host-status `read` loop is guarded the same way — a transient empty
  response polls to the deadline instead of aborting.

Real failures still surface: the `no tunnel URL` and `timeout waiting for host
ready` guards are unchanged.

## Verification

- Reproduced the abort and confirmed the fix in isolation (red → green).
- `bash -n ci/spot-e2e.sh` parses; `shellcheck` clean.

## Known limitations

This fix only gets the lane *past the tunnel step*. The downstream stages — GCE
spot host provisioning, Firecracker boot, and the Playwright smoke
(`frontend/e2e/spot-smoke.spec.ts`) — had never executed at the time of this
change, so a full green run still needs to be confirmed by a (billable)
re-dispatch against an open PR head SHA.
