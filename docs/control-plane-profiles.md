# Control Plane Deployment Profiles

`CONTROL_PLANE_PROFILE` makes the public control-plane startup boundary explicit.
Valid values are `local`, `operator`, and `hosted`.

For a production-like self-hosted control plane, use `operator` and configure
the same Cloudflare Tunnel, Worker/KV data plane, and Firebase or Cloudflare
Access auth shape used by the hosted control plane. The `local` profile is for
trusted localhost development, not public access.

## `local`

Default profile for the public core.

- Defaults `AUTH_MODE=dev`.
- Allows dev auth at server startup without `OCM_ALLOW_DEV_AUTH=1`.
- Does not default the control-plane config to OpenClaw-hosted GCP projects,
  domains, backup buckets, or browser/Hermes GCS manifest env values.
- Cloudflare Tunnel and Cloudflare KV are optional. If unset, host enrollment
  and routing paths that can run without them skip those integrations.
- `SECRET_ENCRYPTION_KEY` is optional at startup. Credential and secret APIs
  still require it before storing encrypted values.

Minimum:

```bash
CONTROL_PLANE_PROFILE=local
DATABASE_URL=postgres://...
```

## `operator`

For an operator-hosted control plane on infrastructure they manage.

- Requires an explicit `AUTH_MODE`; there is no implicit auth provider default.
- Does not default the control-plane config to OpenClaw-hosted GCP projects,
  domains, backup buckets, or browser/Hermes GCS manifest env values.
- Cloudflare Tunnel and Cloudflare KV are optional. Set the Cloudflare env vars
  only when the operator chooses Cloudflare-backed routing.
- GCE host provisioning is available only when both `GCP_PROJECT` and complete
  Cloudflare tunnel configuration are present. BYO/registered hosts do not
  require Cloudflare at registration time.
- `SECRET_ENCRYPTION_KEY` is optional at startup but required before using
  credential, OAuth, channel-token, or machine-secret storage.
- Operators should set artifact source env vars such as `ROOTFS_GCS_MANIFEST`,
  `AGENT_GCS_MANIFEST`, `BROWSER_ROOTFS_GCS_MANIFEST`,
  `HERMES_GCS_MANIFEST`, and `HERMES_ROOTFS_GCS_MANIFEST` when they want
  control-plane responses to advertise remote artifacts.
- When the operator wants hosted-parity behavior, Cloudflare Tunnel, Worker/KV,
  `DATA_PLANE_DOMAIN`, and a real auth provider become deployment
  prerequisites, even though they are not required merely to start the process.

Minimum:

```bash
CONTROL_PLANE_PROFILE=operator
DATABASE_URL=postgres://...
AUTH_MODE=firebase # or another supported auth mode
```

Optional Cloudflare routing:

```bash
CLOUDFLARE_API_TOKEN=...
CLOUDFLARE_ACCOUNT_ID=...
CLOUDFLARE_ZONE_ID=...
CLOUDFLARE_KV_NAMESPACE_ID=...
```

## `hosted`

For a hosted deployment operated by the repository owner or another operator.

- Defaults `AUTH_MODE=cfaccess`.
- Does not default GCP project, data-plane domain, backup bucket, or artifact
  manifests. Operators must set those values explicitly.
- Requires complete Cloudflare Tunnel and KV configuration.
- Requires either `SECRET_ENCRYPTION_KEY` or `GCP_SECRET_NAME`.

Minimum hosted-only requirements:

```bash
CONTROL_PLANE_PROFILE=hosted
DATABASE_URL=postgres://...
CLOUDFLARE_API_TOKEN=...
CLOUDFLARE_ACCOUNT_ID=...
CLOUDFLARE_ZONE_ID=...
CLOUDFLARE_KV_NAMESPACE_ID=...
CF_ACCESS_TEAM_DOMAIN=...
CF_ACCESS_AUD=...
SECRET_ENCRYPTION_KEY=32-byte-key
```
